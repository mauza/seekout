# Working on seekout

Read README.md first: "How it works" and "Extending" cover the architecture
and how to add source types, alerter types, listing kinds and condition keys.

- Checks: `go vet ./... && go test ./...`; `node --check web/app/app.js`
  after UI edits. After touching routes, run the real binary and
  `curl -X POST localhost:8090/mcp` (expect 401): the router panic it guards
  against is invisible to the httptest MCP test.
- A new source/alerter type or kind needs no migration and no UI change
  (see `internal/plugin`, `internal/hooks.SyncSelects`). Don't add per-type
  branches to `web/app/app.js`.
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
- UI: check desktop and ~390px mobile widths. Inputs need an explicit
  `type=` to pick up the shared styles.
- Release: bump `version` in `main.go`, then follow README "Deploy". Read
  the current image pin in home-k8s before bumping it.
