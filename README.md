# seekout

Listing alerts: poll sources (RSS feeds, the Utah MLS via utahrealestate.com),
match new listings against per-user seekers, fire alerters (ntfy, webhook/Slack).

Single Go binary with PocketBase embedded: DB, auth, REST + realtime API,
admin UI at `/_/`, the webapp, and an MCP server for agents at `/mcp`.

## How it works

The record types, all PocketBase collections (`migrations/`):

| collection | owner | what it is |
|---|---|---|
| `sources` | shared, admins change | a configured poller: a type (`rss`, `ure`), its `config`, a listing `kind`, an interval |
| `listings` | shared | one normalized item a source produced (`internal/listing`): typed columns plus free-form `attrs` |
| `seekers` | per user | a saved search: optional kind and source allow-list, conditions, the alerters to notify |
| `alerters` | per user | a notification target: a type (`ntfy`, `webhook`) and its `config` |
| `alerts` | per user | one record per (seeker, listing) match, with per-alerter delivery results |
| `invites` | admins | one-time signup codes |

One data path, `internal/pipeline.PollSource`, run per source by the minute
cron (when its interval is due), the UI's "poll now" and MCP `poll_source`:

```
source.Poll → dedupe on (source, external_id) → Enrich (new listings only)
  → store → evaluate every enabled seeker → alert record + alerter fan-out
```

- A source's first poll only stores its backlog (`seeded`); alerts start with
  listings that appear after that, and a seeker only alerts on listings
  stored after it exists. `search_listings` (MCP) looks at what is stored.
- A failed poll lands in the source's `last_error`; it never stops the other
  sources. Only one poll of a source runs at a time.
- Alerts are delivered once: a failed delivery is recorded on the alert, not
  retried.

## Source layout

- `main.go` — bootstrap, cron, custom routes, embedded static UI
- `internal/listing` — the normalized `Listing` and the listing `Kinds`
- `internal/plugin` — `Info`/`Field`: how a source or alerter type describes
  its config (drives validation, the UI's forms and the MCP docs)
- `internal/sources` — `Source` (+ optional `Enricher`) interface and
  registry; `rss.go`, `ure.go`
- `internal/alerters` — `Alerter` interface and registry; `ntfy.go`,
  `webhook.go`
- `internal/match` — seeker conditions: validation and evaluation
- `internal/pipeline` — the data path above
- `internal/hooks` — record rules the API rules can't express (conditions,
  alerter ownership, per-type config, invite-only signup) and the boot-time
  select sync
- `internal/mcpserver` — the `/mcp` tools
- `internal/appsettings` — mail settings from the environment
- `migrations/` — collections and API rules
- `web/` — framework-free UI, no build step: `index.html` + `landing.*` are
  the public landing page and signup, `web/app/` is the signed-in app (hash
  routes for Alerts, Seekers, Sources, Listings, Alerters), `styles.css` is
  the shared design system, `vendor/` holds the PocketBase JS SDK

## Extending

### Add a source type

One file in `internal/sources/`, nothing else:

```go
type carsSource struct{}

func (carsSource) Poll(ctx context.Context, src *core.Record) ([]listing.Listing, error) {
	var cfg struct{ Make string `json:"make"` }
	if err := src.UnmarshalJSONField("config", &cfg); err != nil {
		return nil, err
	}
	// fetch, then return every listing the site shows; the pipeline dedupes
	return []listing.Listing{{ExternalID: "123", Title: "2019 Tacoma", Price: 31000,
		URL: "https://...", Attrs: map[string]any{"miles": 48000, "year": 2019}}}, nil
}

func init() {
	Register(plugin.Info{
		Type: "cars", Label: "Cars.example", Description: "One line for the UI and agents.",
		Fields: []plugin.Field{{Key: "make", Label: "Make", Type: plugin.Text, Required: true}},
	}, carsSource{})
}
```

Registration does the rest: boot adds `cars` to the `sources.type` select
(no migration), saves validate `config` against `Fields`, the web UI renders
the form from `/api/seekout/plugins`, and MCP `create_source` documents it.

Rules a source must follow:

- `ExternalID` is stable for the life of a listing; it is the dedupe key.
- Put site-specific facts in `Attrs` with snake_case keys and plain JSON
  values; seeker conditions address them by key (`miles`) or dot path.
  Leave a key out when the site doesn't say — don't store a guess, since
  every attr is matchable.
- Bound every request with the poll's `ctx` and a timeout, and reuse one
  `http.Transport` across polls.
- Fail loudly when the page shape changes (no parseable items, missing
  token): the error lands in `last_error`, whereas an empty result looks
  like "nothing new" forever.
- If list results are thin, implement `Enricher`: `Enrich` runs once per new
  listing before it is stored, and an error keeps the listing out until a
  later poll succeeds.

### Add an alerter type

One file in `internal/alerters/`: implement `Send(ctx, rec, alert)`, bound it
with its own timeout, and `Register(plugin.Info{...}, impl)` from `init()`.
The same automatic select sync, validation and UI form apply. Config keys not
declared in `Fields` are allowed and kept (the webhook's `headers`), for
settings too advanced for the form.

`plugin.Field` types: `text`, `url` (must be http/https), `number` (with
optional `Min`/`Max`), `select` (`Options`), `secret` (left out of card
summaries).

### Add a listing kind

Append to `listing.Kinds`. Boot adds it to the `kind` selects of sources,
listings and seekers, and the UI and MCP instructions read the list.

### Change the schema otherwise

Add a numbered Go migration in `migrations/`. Every pod migrates on start;
there is one replica.

## Admins and invites

`users.admin` marks the accounts that manage what everyone shares: they can
create, edit and delete sources (REST, UI and MCP `create_source`) and manage
invites. Everyone signed in can read sources and listings and "poll now".
No API path can set `admin`, including signup and profile updates; grant it
from the CLI:

```sh
kubectl -n seekout exec deploy/seekout -- /seekout admin you@example.com --dir=/pb/pb_data
# --revoke to take it away
```

Signup is invite-only. An admin creates an invite in the app (Invites, in
the sidebar), which copies a link like `https://seekout.mau.guru/?invite=<code>`;
the landing page fills the code in and opens signup. The code is generated
server-side and works once: `internal/hooks/invites.go` claims it in the same
transaction that creates the account, so a failed signup doesn't spend it
and two signups can't share it. Superusers (the dashboard, prod-test) create
accounts without one.

## Seekers and conditions

A condition is `{field, op, value}`. `field` names a typed listing column
(`title`, `price`, `url`, `image`, `kind`, `description`, `external_id`,
`published`) or a dot path into `attrs` — `sqft`, `address.city`. A leading
`attrs.` is accepted and ignored. `op` is one of `= != > < >= <= in contains`:

- comparisons are numeric when both sides parse as numbers, string otherwise
- `in` takes a JSON array or a comma-separated string
- `contains` is substring on strings, membership on lists
- a missing attr fails its condition; typed columns are never missing, and an
  unknown `price` is `0` (add `price > 0` where that matters)

Conditions are hard by default: every one must hold. A condition with an
ordering op (`> >= < <=`) can instead be soft, with `floor` (worst
acceptable), `best` (full credit) and optional `weight` (default 1):
`{"field":"garage_capacity","op":">=","value":3,"soft":true,"floor":2,"best":4,"weight":2}`.
Each soft condition scores 0 at its value, rises linearly to +1 at `best`
(capped), falls to -1 at `floor`, and fails the listing outright past
`floor`; a missing value scores -1. A listing matches when every hard
condition holds and the weighted soft scores sum to >= 0, so a shortfall on
one has to be paid for by others. Alerts from a soft seeker carry the
breakdown (`score +0.15: garage_capacity 2 (-1.00 x2), lot_acres 0.43
(+1.00), ...`), and `search_listings` returns it per listing.

Seekers are validated on every save (`internal/hooks`): a malformed
condition, or an alerter that isn't the seeker owner's, is a 400.

When adding a key to the condition shape, add it to `conditionFromRow` in
`web/app/app.js` too: the editor rebuilds conditions from its rows, so a key
it doesn't know is silently dropped when a seeker is saved from the UI.

## Sources

- `rss` covers any RSS/Atom/RDF feed. Price is read from a `$` amount in the
  title. realtor.com's old `mapi-ng` API is gone (NXDOMAIN) and Craigslist
  blocks some IPs even for RSS — config concerns, visible in `last_error`.
- `ure` (utahrealestate.com) has no public API: it opens a session with
  `GET /search/map.search/type/{n}`, then reads server-rendered cards from the
  `map.inline.results` XHR and regex-parses them. Every XHR must send the
  `X-CSRF-TOKEN` from the page's `<meta name="csrf-token">` (since 2026-10);
  without it the site answers 200 with an empty body. Unparseable cards, a
  missing token and an empty response all land in `last_error`; check the
  selectors at the top of `internal/sources/ure.go` when they do.
  - Listings carry `beds`, `baths`, `sqft`, `status`, `mls`, `listing_type`,
    `days_on_market`, `zip`, usually `city` (left out when the address slug
    is ambiguous) and sometimes `open_house`.
  - Detail-page enrichment adds `garage_capacity` (absent when the listing
    names none), `lot_acres`, `hoa` (true/false) and `hoa_fee`. `hoa=false`
    is only recorded on a page whose `og:url` names the same MLS number, so a
    block page or a redesign errors instead of reading as "no HOA". A listing
    pulled before its detail fetch is stored with `status` `OFF MARKET` and
    no detail fields. Listings stored before 0.5.2 were never enriched.
  - Newest-first results include a few `SOLD` / `UNDER CONTRACT` rows, so
    home seekers want `status in ACTIVE,COMING SOON`.

## MCP

`/mcp` is a stateless streamable-HTTP MCP server (`internal/mcpserver`) for
agents such as Hermes. Its tools cover sources (list, create, poll),
listings (`search_listings` takes seeker-style conditions and is a good way
to try conditions out before saving them), seekers (CRUD), alerters (list,
test) and alerts. It runs alongside the cron poller and the web UI, not
instead of them: MCP is request/response, so alert delivery stays here.

Auth is a PocketBase `users` token sent as `Authorization: Bearer <token>`,
and every tool acts as that user. The tools run server-side, so the
collection API rules don't apply to them: each tool scopes to the caller
itself, and saves go through the same validate hooks as the REST API. Mint a
long-lived token (the default is 365 days; changing the user's password
revokes it):

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

## Run locally

```sh
go run . serve --http=127.0.0.1:8090
# first time only:
go run . superuser upsert you@example.com <password>
```

Signup needs an invite, so create the first account in the dashboard at
`/_/` (`users` → new record, with `verified` on), then make it an admin with
`go run . admin you@example.com`; it can invite everyone else. Accounts must
confirm their email address before they can sign in, so a local instance
needs SMTP or invited accounts have to be flipped by hand (`verified` in the
dashboard, or `sqlite3 pb_data/data.db "update users set verified=1"`).
`superuser upsert` is only for the PocketBase dashboard at `/_/`, which is a
separate collection.

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

## Test

```sh
go test ./...
```

Unit and in-process tests (PocketBase test app, no network): condition
validation and scoring (`match`), plugin config validation (`plugin`), the
validate hooks and select sync (`hooks`), the poll path — enrichment
retries, alert text, concurrent polls, edits made mid-poll, alerter
ownership (`pipeline`), the `ure` parsers against fixtures trimmed from live
pages (`sources`), and the MCP tools end to end over HTTP (`mcpserver`).

The httptest MCP test can't see router conflicts (`/mcp` must be registered
per method, or it panics against the static `GET /{path...}` route), so smoke
the real binary after touching routes: `go run . serve` and
`curl -X POST localhost:8090/mcp` should answer 401.

`scripts/prod-test.sh` is an end-to-end pass against a running instance:
transport and TLS, auth and anonymous access, ntfy/webhook delivery (verified
by reading the messages back off the ntfy server), invite-only signup and
admin-only sources, config validation, the
condition-operator matrix, source seeding, dedup, `last_error` handling,
per-user isolation, cron, pod-restart persistence and cascade deletes.

```sh
scripts/prod-test.sh                    # https://seekout.mau.guru, cluster checks on
SEEKOUT_URL=http://127.0.0.1:8090 \
  SEEKOUT_EMAIL=you@example.test \
  SEEKOUT_PASSWORD=... \
  SEEKOUT_SKIP_CLUSTER=1 scripts/prod-test.sh
```

Credentials default to the first bare email line in `~/.secrets/seekout.txt`
and the line after it; the account must exist in both the `users` collection
(as an admin) and `_superusers`. It is not read-only: it creates and removes `test-*`
records plus a second user, and with cluster access it deletes the pod once to
prove the data survives (`SEEKOUT_SKIP_RESTART=1` to skip).

## Deploy

Bump `version` in `main.go` (MCP clients see it) to match the image tag,
build multi-arch and push to Harbor, then bump the pin in
`~/Projects/home-k8s/apps/seekout/deployment.yaml` and let ArgoCD sync:

```sh
# The shared "multiarch" builder times out pushing to Harbor; use a
# throwaway host-network builder (BuildKit carries its own arm64 qemu).
docker buildx create --name seekout-build --driver-opt network=host
docker buildx build --builder seekout-build --platform linux/amd64,linux/arm64 \
  -t docker.mau.guru/library/seekout:X.Y.Z --push .
docker buildx rm seekout-build
```

The cluster has arm64 workers, so images must be multi-arch — an amd64-only
image crash-loops with `exec format error`. Replicas must stay 1 with the
`Recreate` strategy (embedded SQLite).

The superuser comes from the `seekout-superuser` secret (`entrypoint.sh`
upserts it on boot); sealed example:

```sh
kubectl create secret generic seekout-superuser -n seekout \
  --from-literal=email=you@example.com \
  --from-literal=password='<password>' \
  --dry-run=client -o yaml | kubeseal -o yaml > apps/seekout/sealed-secret.yaml
```

The webapp login lives in the `users` collection; superusers can only use the
dashboard at `/_/`. Both accounts are needed.
