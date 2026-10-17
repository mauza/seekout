# seekout

Listing alerts: poll sources (RSS feeds, the Utah MLS via utahrealestate.com),
match new listings against per-user seekers, fire alerters (ntfy, webhook/Slack).

Single Go binary with PocketBase embedded: DB, auth, REST + realtime API,
admin UI at `/_/`, and the webapp served from the binary.

## Source layout

- `main.go` — bootstrap, cron registration, custom routes (`POST
  /api/seekout/sources/{id}/poll`, `POST /api/seekout/alerters/{id}/test`),
  embedded static UI
- `migrations/` — PocketBase Go migrations (collections + rules)
- `internal/listing` — normalized listing shape
- `internal/sources` — `Source` interface + registry; add a site = one file +
  `Register("type", impl)`
- `internal/match` — seeker condition evaluation (`= != > < >= <= in
  contains`, dot-path into `attrs` JSON)
- `internal/alerters` — `Alerter` interface + registry (ntfy, webhook)
- `internal/pipeline` — poll → dedup (`(source, external_id)` unique) →
  match → alert fan-out
- `web/` — framework-free UI, no build step: `index.html` + `landing.*` are
  the public landing page and signup, `web/app/` is the signed-in app
  (hash routes for Alerts, Seekers, Sources, Listings, Alerters),
  `styles.css` is the shared design system, `vendor/` holds the PocketBase
  JS SDK

## Run locally

```sh
go run . serve --http=127.0.0.1:8090
# first time only:
go run . superuser upsert you@example.com <password>
```

Sign up at http://127.0.0.1:8090/. Accounts must confirm their email address
before they can sign in, so a local instance needs SMTP or the account has to
be flipped by hand in the dashboard (`users` → `verified`). `superuser upsert`
is only for the PocketBase dashboard at `/_/`, which is a separate collection.

## Email

Verification is enforced by the `users` collection's `authRule`
(`verified = true`), which PocketBase applies after the password check on
every auth path, answering 403 rather than the 400 of a wrong password. The
email links to `/verify#<token>`, a static page that confirms the token and
offers a resend.

Mail settings are read from the environment on every boot
(`internal/appsettings`) instead of being stored only in the settings row, so
a fresh volume comes up configured:

| var | note |
|---|---|
| `APP_URL` | what `{APP_URL}` resolves to in the email — wrong value, dead links |
| `APP_NAME` | subject line and body |
| `SMTP_HOST` / `SMTP_PORT` | unset host leaves stored settings alone |
| `SMTP_USERNAME` / `SMTP_PASSWORD` | password comes from the `seekout-mail` secret in k8s |
| `SMTP_AUTH_METHOD` | `PLAIN` or `LOGIN` |
| `SMTP_TLS` | `false` sends STARTTLS (port 587); `true` is implicit TLS (465) |
| `SMTP_FROM` / `SMTP_SENDER_NAME` | Zoho refuses any sender but the authenticated mailbox |

## MCP

`/mcp` is a stateless streamable-HTTP MCP server (`internal/mcpserver`) for
agents such as Hermes. Its tools cover sources (list, create, poll),
listings (`search_listings` takes seeker-style conditions and is a good way
to try conditions out before saving them), seekers (CRUD), alerters (list,
test) and alerts. It runs alongside the cron poller and the web UI, not
instead of them: MCP is request/response, so alert delivery stays here.

Auth is a PocketBase `users` token sent as `Authorization: Bearer <token>`,
and every tool acts as that user. The tools run server-side, so the
collection API rules don't apply to them; each tool scopes to the caller
itself. Mint a long-lived token (the default is 365 days; changing the
user's password revokes it):

```sh
kubectl -n seekout exec deploy/seekout -- /seekout mcp-token you@example.com --dir=/pb/pb_data
```

Hermes config (`mcp_servers`), with the token in Hermes's `/opt/data/.env`
as `SEEKOUT_MCP_TOKEN`:

```yaml
seekout:
  url: "http://seekout.seekout/mcp"
  headers:
    Authorization: "Bearer ${SEEKOUT_MCP_TOKEN}"
  tools:
    exclude: ["delete_*"]
```

## Test

`go test ./...` covers the pure logic that the HTTP suite can't reach: the
`ure` card parser (against a fixture trimmed from a live response) and seeker
condition resolution.

`scripts/prod-test.sh` is an end-to-end pass against a running instance:
transport and TLS, auth and anonymous access, ntfy/webhook delivery (verified
by reading the messages back off the ntfy server), the condition-operator
matrix, source seeding, dedup, `last_error` handling, per-user isolation,
cron, pod-restart persistence and cascade deletes.

```sh
scripts/prod-test.sh                    # https://seekout.mau.guru, cluster checks on
SEEKOUT_URL=http://127.0.0.1:8090 \
  SEEKOUT_EMAIL=you@example.test \
  SEEKOUT_PASSWORD=... \
  SEEKOUT_SKIP_CLUSTER=1 scripts/prod-test.sh
```

Credentials default to the first bare email line in `~/.secrets/seekout.txt`
and the line after it; the account must exist in both the `users` collection
and `_superusers`. It is not read-only: it creates and removes `test-*`
records plus a second user, and with cluster access it deletes the pod once to
prove the data survives (`SEEKOUT_SKIP_RESTART=1` to skip).

## Deploy

Build and push to harbor, then let ArgoCD sync `apps/seekout`:

```sh
docker buildx build --builder multiarch --platform linux/amd64,linux/arm64 \
  -t docker.mau.guru/library/seekout:0.1.1 --push .
```

Create the superuser secret (sealed-secret or plain; sealed example):

```sh
kubectl create secret generic seekout-superuser -n seekout \
  --from-literal=email=you@example.com \
  --from-literal=password='<password>' \
  --dry-run=client -o yaml | kubeseal -o yaml > apps/seekout/sealed-secret.yaml
```

App runs at https://seekout.mau.guru. Sources poll on their configured
interval via the in-process cron; "poll now" in the UI triggers an immediate
poll.

## Notes

- realtor.com's old `mapi-ng` API is decommissioned (NXDOMAIN); Craigslist
  blocks some IPs even for RSS. Both are source-config concerns, not code —
  `last_error` on the source record surfaces failures in the UI.
- the `ure` source (utahrealestate.com) has no public API: it opens a session
  with a `GET /search/map.search/type/{n}`, then reads server-rendered cards
  from the `map.inline.results` XHR and regex-parses the address/price/URL
  out of the returned HTML. If the site's markup changes, parsing degrades
  silently (fewer or zero listings, no `last_error`) — check the card
  selectors at the top of `internal/sources/ure.go`.
- seeker condition fields name a typed listing column (`title`, `price`,
  `url`, `kind`, `description`, `external_id`, `published`) or a dot path
  into the listing's `attrs` JSON — `sqft`, `address.city`. A leading
  `attrs.` is accepted and ignored, so `sqft` and `attrs.sqft` are the same
  field. `ure` listings carry `beds`, `baths`, `sqft`, `status`, `mls`,
  `listing_type`, `days_on_market` and sometimes `open_house`.
- replicas must stay 1 (embedded SQLite).
- the cluster has arm64 workers, so images must be multi-arch — an
  amd64-only image crash-loops with `exec format error`.
- the webapp login lives in the `users` collection; superusers can only use
  the dashboard at `/_/`. Both accounts are needed.
