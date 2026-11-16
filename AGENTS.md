# Working on seekout

Listing alerts: sources (RSS, utahrealestate.com `ure`, KSL classifieds/cars
`ksl`, flight deals `farewatch`) → seekers → alerters (ntfy, webhook), plus price tracking and an MCP
server. One Go binary with embedded PocketBase. Live at
https://seekout.mau.guru, namespace `seekout`, ArgoCD app `seekout`, manifests
`~/Projects/home-k8s/apps/seekout` (see that repo's `AGENTS.md` for deploy
rules). Remote is GitHub `mauza/seekout`, default branch `main`, no CI.

Read README.md first: "How it works" and "Extending" cover the architecture
and how to add source types, alerter types, listing kinds and condition keys.

## Checks

- `go vet ./... && go test ./...`; `node --check web/app/app.js` after UI
  edits. After touching routes, run the real binary and
  `curl -X POST localhost:8090/mcp` (expect 401): the router panic it guards
  against (method-less `/mcp` vs static `GET /{path...}`) is invisible to the
  httptest MCP test.
- UI: check desktop and ~390px mobile widths. Inputs need an explicit
  `type=` to pick up the shared styles.
- `scripts/prod-test.sh` is the end-to-end pass (README "Test"); it writes
  `test-*` records and deletes the pod once, so it is not read-only.

## Local QA on real data

Run against a copy of prod, never against prod itself:

1. As superuser, `POST /api/backups`, then download the zip; unpack it to a
   scratch `pb_data`.
2. Before first boot: `sqlite3 pb_data/data.db "update sources set enabled=0;
   update alerters set enabled=0; update users set tokenKey=lower(hex(randomblob(25)))"`.
   Otherwise the copy polls URE/KSL and pings the user's phone, and tokens
   minted in the copy are valid on prod.
3. `go run . serve --http=127.0.0.1:8090 --dir=<copy>`; browser session via
   `go run . mcp-token <email> --dir=<copy>`.
4. Pricing prompt changes: `seekout pricing backfill --days 30` on the copy
   (needs `SEEKOUT_LLM_URL`/`SEEKOUT_LLM_KEY`; the gateway key is the LiteLLM
   master key in `~/.config/opencode/litellm-master-key`), then read the
   verdicts.

## Rules and traps

- A new source/alerter type or kind needs no migration and no UI change
  (see `internal/plugin`, `internal/hooks.SyncSelects`). Don't add per-type
  branches to `web/app/app.js`. A new condition key must be added to
  `conditionFromRow` in `web/app/app.js`, or UI saves silently drop it.
- Server-side saves (`app.Save`) skip the collection API rules but run the
  validate hooks in `internal/hooks`. Per-user checks that must hold
  everywhere go there, not only in the MCP tools.
- Sources and invites are admin-only (`users.admin`, set only by the
  `seekout admin` CLI); signup needs a one-time invite. Server-side code
  that creates sources on a user's behalf must check `admin` itself, as MCP
  `create_source` does.
- Requests to user-supplied URLs (alerters) go through `netguard.Client`,
  never `http.DefaultClient`: it refuses internal addresses.
- Poll bookkeeping saves with `SaveNoValidate` on a freshly loaded record on
  purpose (`pipeline.finishSource`); keep it that way.
- Pricing (`internal/pricing`) gets structured LLM output from a forced tool
  call: the LiteLLM gateway drops `response_format` for the ChatGPT models.
  Tests use httptest fakes for the gateway and SearXNG.
- URE needs the page's CSRF token on every XHR (else 200 + empty body); KSL
  needs a browser User-Agent (PerimeterX) and only page 1 is reachable.
- Stored listings are never updated after first sight (status/price stale);
  `hoa_fee` is a string. Known gaps, not bugs to "fix" silently.
- Boot upserts the superuser from the secret, which invalidates existing
  superuser tokens: re-authenticate after a rollout.

## Release and deploy

Verified changes ship without asking: commit, push `main`, release, deploy,
check live.

1. Bump `version` in `main.go` to the new image tag; commit and push `main`.
2. Build multi-arch (arm64 workers; amd64-only crash-loops):
   `docker buildx build --builder multiarch-hostnet --platform linux/amd64,linux/arm64 -t docker.mau.guru/library/seekout:X.Y.Z --push .`
3. Read the current pin, then from `~/Projects/home-k8s`:
   `grep -n 'seekout:' apps/seekout/deployment.yaml` and
   `ship-pin -a seekout -m "seekout X.Y.Z: <why>" docker.mau.guru/library/seekout:<old> docker.mau.guru/library/seekout:X.Y.Z apps/seekout/deployment.yaml`.
   Replicas stay 1 with `Recreate` (embedded SQLite).

## Verify live

- `curl -fsS https://seekout.mau.guru/api/health`; `k -n seekout get pods`.
- Logs are in PocketBase's `_logs` table, not stdout (stdout is empty). Read
  them as superuser via `GET /api/logs` (creds: `~/.secrets/seekout.txt`, the
  prod-test account), or with `sqlite3` on a backup copy (`auxiliary.db`).
- A source's failures land in its `last_error`; check it after source edits.
