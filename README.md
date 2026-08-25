# seekout

Listing alerts: poll sources (RSS feeds, later scrapers), match new listings
against per-user seekers, fire alerters (ntfy, webhook/Slack).

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
- `web/` — framework-free UI (PocketBase JS SDK, realtime alerts feed)

## Run locally

```sh
go run . serve --http=127.0.0.1:8090
# first time only:
go run . superuser upsert you@example.com <password>
```

Then create a login user at http://127.0.0.1:8090/_/ (Users collection) and
sign in at http://127.0.0.1:8090/.

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
- replicas must stay 1 (embedded SQLite).
- the cluster has arm64 workers, so images must be multi-arch — an
  amd64-only image crash-loops with `exec format error`.
- the webapp login lives in the `users` collection; superusers can only use
  the dashboard at `/_/`. Both accounts are needed.
