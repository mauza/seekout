#!/usr/bin/env bash
#
# End-to-end test pass against a running seekout instance.
#
# Usage:
#   scripts/prod-test.sh                          # test https://seekout.mau.guru
#   SEEKOUT_URL=http://127.0.0.1:8090 \
#     SEEKOUT_EMAIL=me@example.test \
#     SEEKOUT_PASSWORD=secret scripts/prod-test.sh
#
# Configuration (all optional):
#   SEEKOUT_URL          base URL              (default https://seekout.mau.guru)
#   SEEKOUT_EMAIL        login email           (default: parsed from secrets file)
#   SEEKOUT_PASSWORD     login password        (default: parsed from secrets file)
#   SEEKOUT_SECRETS      credentials file      (default ~/.secrets/seekout.txt)
#   SEEKOUT_NAMESPACE    kubernetes namespace  (default seekout)
#   NTFY_URL             ntfy server           (default https://ntfy.sh)
#   SEEKOUT_SKIP_CLUSTER=1   skip kubectl-based checks
#   SEEKOUT_SKIP_RESTART=1   skip the pod-restart persistence check
#
# The account needs to exist in BOTH the users collection (webapp login) and
# _superusers (dashboard); `superuser upsert` plus a users record, as the
# deployment does.
#
# NOT read-only. It creates and deletes records named test-*, a second user
# (tester-b@seekout.local), and — when it can reach the cluster and
# SEEKOUT_SKIP_RESTART is unset — deletes the pod once to prove the data
# survives. Alerter delivery is verified by publishing to random ntfy topics
# and reading the messages back, so it needs outbound access to NTFY_URL from
# both this machine and the instance under test.
#
set -uo pipefail

BASE="${SEEKOUT_URL:-https://seekout.mau.guru}"
SECRETS="${SEEKOUT_SECRETS:-$HOME/.secrets/seekout.txt}"
NS="${SEEKOUT_NAMESPACE:-seekout}"
NTFY="${NTFY_URL:-https://ntfy.sh}"

SCHEME="${BASE%%://*}"
HOST="${BASE#*://}"
HOST="${HOST%%/*}"

EMAIL="${SEEKOUT_EMAIL:-}"
PASS="${SEEKOUT_PASSWORD:-}"
if [ -z "$EMAIL" ] || [ -z "$PASS" ]; then
  if [ ! -r "$SECRETS" ]; then
    echo "no credentials: set SEEKOUT_EMAIL and SEEKOUT_PASSWORD, or provide $SECRETS" >&2
    exit 2
  fi
  # Credentials file holds a bare email line followed by its password.
  [ -z "$EMAIL" ] && EMAIL=$(awk '/^[^ \t@]+@[^ \t@]+$/ {print; exit}' "$SECRETS")
  [ -z "$PASS" ] && PASS=$(awk 'found {print; exit} /^[^ \t@]+@[^ \t@]+$/ {found=1}' "$SECRETS")
fi
if [ -z "$EMAIL" ] || [ -z "$PASS" ]; then
  echo "could not determine credentials from $SECRETS" >&2
  exit 2
fi

for tool in curl jq openssl awk; do
  command -v "$tool" >/dev/null || { echo "missing required tool: $tool" >&2; exit 2; }
done

CLUSTER=0
if [ "${SEEKOUT_SKIP_CLUSTER:-0}" != "1" ] \
   && command -v kubectl >/dev/null 2>&1 \
   && kubectl get namespace "$NS" >/dev/null 2>&1; then
  CLUSTER=1
fi

PASSED=0
FAILED=0
SKIPPED=0
CREATED_SOURCES=()
CREATED_SEEKERS=()
CREATED_ALERTERS=()
USER_B_ID=""
B_EMAIL="tester-b@seekout.local"

ok()   { PASSED=$((PASSED+1));  printf 'PASS  %s\n' "$1"; }
bad()  { FAILED=$((FAILED+1));  printf 'FAIL  %s -- %s\n' "$1" "${2:-}"; }
skip() { SKIPPED=$((SKIPPED+1)); printf 'SKIP  %s -- %s\n' "$1" "${2:-}"; }
check(){ [ "$2" = "$3" ] && ok "$1" || bad "$1" "expected '$3', got '$2'"; }

jqr() { jq -r "$1" 2>/dev/null; }

api() { # api METHOD PATH [TOKEN] [DATA]
  local m="$1" p="$2" t="${3:-}" d="${4:-}"
  local args=(-sS -m 120 -X "$m" "$BASE$p" -H 'Content-Type: application/json')
  [ -n "$t" ] && args+=(-H "Authorization: Bearer $t")
  [ -n "$d" ] && args+=(-d "$d")
  curl "${args[@]}"
}

code() { # code METHOD PATH [TOKEN] [DATA] -> http status
  local m="$1" p="$2" t="${3:-}" d="${4:-}"
  local args=(-sS -m 120 -o /dev/null -w '%{http_code}' -X "$m" "$BASE$p" -H 'Content-Type: application/json')
  [ -n "$t" ] && args+=(-H "Authorization: Bearer $t")
  [ -n "$d" ] && args+=(-d "$d")
  curl "${args[@]}"
}

printf 'target %s (cluster checks: %s)\n\n' "$BASE" "$([ "$CLUSTER" = 1 ] && echo "on, namespace $NS" || echo off)"

echo "=============== 1. infrastructure ==============="

if [ "$CLUSTER" = 1 ]; then
  POD_JSON=$(kubectl get pods -n "$NS" -l app=seekout -o json)
  check "pod is Running" "$(echo "$POD_JSON" | jqr '.items[0].status.phase')" "Running"
  check "pod container ready" "$(echo "$POD_JSON" | jqr '.items[0].status.containerStatuses[0].ready')" "true"
  check "pod has no restarts" "$(echo "$POD_JSON" | jqr '.items[0].status.containerStatuses[0].restartCount')" "0"
  check "data PVC bound" "$(kubectl get pvc seekout-pvc -n "$NS" -o jsonpath='{.status.phase}')" "Bound"
  check "dumps PVC bound" "$(kubectl get pvc seekout-dumps -n "$NS" -o jsonpath='{.status.phase}')" "Bound"
  check "dump CronJob scheduled" "$(kubectl get cronjob seekout-dump -n "$NS" -o jsonpath='{.spec.schedule}')" "30 0 * * *"
  if kubectl get application seekout -n argocd >/dev/null 2>&1; then
    check "argocd sync state" "$(kubectl get application seekout -n argocd -o jsonpath='{.status.sync.status}')" "Synced"
    check "argocd health state" "$(kubectl get application seekout -n argocd -o jsonpath='{.status.health.status}')" "Healthy"
  else
    skip "argocd application state" "no seekout Application in argocd"
  fi
else
  skip "cluster checks" "kubectl unavailable, namespace $NS missing, or SEEKOUT_SKIP_CLUSTER=1"
fi

echo "=============== 2. transport ==============="

check "health endpoint" "$(code GET /api/health)" "200"
check "webapp index" "$(code GET /)" "200"
check "admin dashboard" "$(code GET /_/)" "200"
check "unknown api route 404" "$(code GET /api/seekout/does-not-exist)" "404"
check "unknown static path 404" "$(code GET /does-not-exist.html)" "404"

if [ "$SCHEME" = "https" ]; then
  REDIR=$(curl -sS -m 20 -o /dev/null -w '%{http_code}' "http://$HOST/")
  { [ "$REDIR" = "301" ] || [ "$REDIR" = "302" ] || [ "$REDIR" = "308" ]; } \
    && ok "http redirects to https ($REDIR)" || bad "http redirects to https" "got $REDIR"

  CERT=$(echo | openssl s_client -connect "$HOST:443" -servername "$HOST" 2>/dev/null \
         | openssl x509 -noout -issuer -enddate 2>/dev/null)
  [ -n "$CERT" ] && ok "TLS certificate served" || bad "TLS certificate" "no certificate returned"
  CERT_END=$(echo "$CERT" | sed -n 's/notAfter=//p')
  if [ -n "$CERT_END" ]; then
    CERT_DAYS=$(( ( $(date -d "$CERT_END" +%s) - $(date +%s) ) / 86400 ))
    [ "$CERT_DAYS" -gt 14 ] \
      && ok "TLS cert valid for ${CERT_DAYS}d" || bad "TLS cert expiry" "${CERT_DAYS}d left"
  else
    bad "TLS cert expiry" "could not parse notAfter"
  fi
else
  skip "TLS and redirect checks" "target is plain http"
fi

echo "=============== 3. auth ==============="

SU_TOKEN=$(api POST /api/collections/_superusers/auth-with-password "" "{\"identity\":\"$EMAIL\",\"password\":\"$PASS\"}" | jqr .token)
{ [ -n "$SU_TOKEN" ] && [ "$SU_TOKEN" != "null" ]; } && ok "superuser auth" || bad "superuser auth"

# A superuser is not a webapp account, and on a fresh instance the users
# record does not exist yet — provision it rather than failing every
# authenticated assertion that follows. Created pre-verified: the point here
# is to exercise the app, not a mailbox.
if [ "$(api GET "/api/collections/users/records?filter=(email='$EMAIL')" "$SU_TOKEN" | jqr .totalItems)" = "0" ]; then
  api POST /api/collections/users/records "$SU_TOKEN" \
    "{\"email\":\"$EMAIL\",\"password\":\"$PASS\",\"passwordConfirm\":\"$PASS\",\"verified\":true}" >/dev/null
  printf 'INFO  created the webapp account %s\n' "$EMAIL"
fi

AUTH=$(api POST /api/collections/users/auth-with-password "" "{\"identity\":\"$EMAIL\",\"password\":\"$PASS\"}")
TOKEN=$(echo "$AUTH" | jqr .token)
USER_ID=$(echo "$AUTH" | jqr .record.id)
{ [ -n "$TOKEN" ] && [ "$TOKEN" != "null" ]; } && ok "webapp user auth" || bad "webapp user auth" "$AUTH"
# Sources and invites are admin-only; grant with `seekout admin <email>`.
check "webapp user is admin" "$(echo "$AUTH" | jqr .record.admin)" "true"

check "wrong password rejected" "$(code POST /api/collections/users/auth-with-password "" "{\"identity\":\"$EMAIL\",\"password\":\"wrong-password\"}")" "400"
# PocketBase list rules filter rather than deny, so the contract to verify is
# "anonymous sees nothing", not a 403.
check "anonymous sees no seekers" "$(api GET /api/collections/seekers/records | jqr .totalItems)" "0"
check "anonymous sees no alerts" "$(api GET /api/collections/alerts/records | jqr .totalItems)" "0"
check "anonymous sees no alerters" "$(api GET /api/collections/alerters/records | jqr .totalItems)" "0"
check "anonymous cannot create a source" "$(code POST /api/collections/sources/records "" '{"name":"anon","type":"rss","kind":"other","config":{"url":"https://example.com"},"interval_minutes":30}')" "400"
check "anonymous poll denied" "$(code POST /api/seekout/sources/xxxxxxxxxxxxxxx/poll)" "401"
check "anonymous alerter test denied" "$(code POST /api/seekout/alerters/xxxxxxxxxxxxxxx/test)" "401"

# --- email verification gate ------------------------------------------------
# Accounts must confirm their address before any auth path issues a token.
# PocketBase enforces it via the users collection authRule, which answers 403
# ("doesn't satisfy the collection requirements") as opposed to 400 for bad
# credentials, so the two failures are distinguishable by the UI.
check "users collection requires verified" \
  "$(api GET /api/collections/users "$SU_TOKEN" | jqr .authRule)" "verified = true"

PENDING_EMAIL="test-pending-$(openssl rand -hex 4)@seekout.local"
PENDING_PASS="pending-$(openssl rand -hex 6)"
check "signup without an invite rejected" "$(code POST /api/collections/users/records "" \
  "{\"email\":\"$PENDING_EMAIL\",\"password\":\"$PENDING_PASS\",\"passwordConfirm\":\"$PENDING_PASS\"}")" "400"
INVITE_ID_CODE=$(api POST /api/collections/invites/records "$TOKEN" '{"note":"test-prod-test"}' | jqr '.id + " " + .code')
INVITE_ID=${INVITE_ID_CODE% *}
INVITE_CODE=${INVITE_ID_CODE#* }
PENDING_ID=$(api POST /api/collections/users/records "" \
  "{\"email\":\"$PENDING_EMAIL\",\"password\":\"$PENDING_PASS\",\"passwordConfirm\":\"$PENDING_PASS\",\"invite\":\"$INVITE_CODE\"}" | jqr .id)
{ [ -n "$PENDING_ID" ] && [ "$PENDING_ID" != "null" ]; } && ok "signup with an invite" || bad "signup with an invite"
check "invite works only once" "$(code POST /api/collections/users/records "" \
  "{\"email\":\"x-$PENDING_EMAIL\",\"password\":\"$PENDING_PASS\",\"passwordConfirm\":\"$PENDING_PASS\",\"invite\":\"$INVITE_CODE\"}")" "400"
check "signup cannot claim admin" "$(api GET "/api/collections/users/records/$PENDING_ID" "$SU_TOKEN" | jqr .admin)" "false"
api DELETE "/api/collections/invites/records/$INVITE_ID" "$TOKEN" >/dev/null

check "unverified account cannot authenticate" \
  "$(code POST /api/collections/users/auth-with-password "" "{\"identity\":\"$PENDING_EMAIL\",\"password\":\"$PENDING_PASS\"}")" "403"

# 204 regardless of whether the address exists: no account enumeration.
check "verification request accepted for known address" \
  "$(code POST /api/collections/users/request-verification "" "{\"email\":\"$PENDING_EMAIL\"}")" "204"
check "verification request leaks nothing for unknown address" \
  "$(code POST /api/collections/users/request-verification "" '{"email":"definitely-not-a-user@seekout.local"}')" "204"

api PATCH "/api/collections/users/records/$PENDING_ID" "$SU_TOKEN" '{"verified":true}' >/dev/null
PENDING_TOKEN=$(api POST /api/collections/users/auth-with-password "" \
  "{\"identity\":\"$PENDING_EMAIL\",\"password\":\"$PENDING_PASS\"}" | jqr .token)
{ [ -n "$PENDING_TOKEN" ] && [ "$PENDING_TOKEN" != "null" ]; } && ok "verified account authenticates" || bad "verified account authenticates"

api DELETE "/api/collections/users/records/$PENDING_ID" "$SU_TOKEN" >/dev/null

# The verification email links here, so the route must exist even though the
# static handler has no index fallback.
check "verify page served" "$(code GET /verify)" "200"
check "mail transport configured" \
  "$(api GET /api/settings "$SU_TOKEN" | jqr .smtp.enabled)" "true"
check "mail sender configured" \
  "$([ -n "$(api GET /api/settings "$SU_TOKEN" | jqr .meta.senderAddress)" ] && echo yes)" "yes"

# An interrupted run leaves test-* records behind, which would fail the
# leftover assertions at the end of the next one. Clear them first so the
# suite is repeatable.
PURGED=0
for col in seekers sources alerters; do
  for id in $(api GET "/api/collections/$col/records?perPage=100" "$SU_TOKEN" \
              | jqr '.items[] | select(.name | startswith("test-")) | .id'); do
    api DELETE "/api/collections/$col/records/$id" "$SU_TOKEN" >/dev/null
    PURGED=$((PURGED+1))
  done
done
# Same for the throwaway account the verification checks create.
for id in $(api GET "/api/collections/users/records?perPage=100&filter=(email~'test-pending-')" "$SU_TOKEN" | jqr '.items[].id'); do
  api DELETE "/api/collections/users/records/$id" "$SU_TOKEN" >/dev/null
  PURGED=$((PURGED+1))
done
[ "$PURGED" -gt 0 ] && printf 'INFO  purged %s leftover test record(s) from a previous run\n' "$PURGED"

echo "=============== 4. alerters ==============="

NTFY_TOPIC="seekout-test-$(openssl rand -hex 6)"
WEBHOOK_TOPIC="seekout-hook-$(openssl rand -hex 6)"

NTFY_ID=$(api POST /api/collections/alerters/records "$TOKEN" \
  "{\"user\":\"$USER_ID\",\"name\":\"test-ntfy\",\"type\":\"ntfy\",\"config\":{\"url\":\"$NTFY\",\"topic\":\"$NTFY_TOPIC\",\"tags\":\"robot\"},\"enabled\":true}" | jqr .id)
{ [ -n "$NTFY_ID" ] && [ "$NTFY_ID" != "null" ]; } && ok "create ntfy alerter" || bad "create ntfy alerter"
CREATED_ALERTERS+=("$NTFY_ID")

# The webhook alerter points at an ntfy topic too: ntfy stores the raw POST
# body as the message, so we can read back exactly what was sent.
HOOK_ID=$(api POST /api/collections/alerters/records "$TOKEN" \
  "{\"user\":\"$USER_ID\",\"name\":\"test-webhook\",\"type\":\"webhook\",\"config\":{\"url\":\"$NTFY/$WEBHOOK_TOPIC\"},\"enabled\":true}" | jqr .id)
{ [ -n "$HOOK_ID" ] && [ "$HOOK_ID" != "null" ]; } && ok "create webhook alerter" || bad "create webhook alerter"
CREATED_ALERTERS+=("$HOOK_ID")

check "ntfy test send" "$(code POST "/api/seekout/alerters/$NTFY_ID/test" "$TOKEN")" "200"
check "webhook test send" "$(code POST "/api/seekout/alerters/$HOOK_ID/test" "$TOKEN")" "200"

# Config is validated against the type's declared fields on save, so a
# misconfigured alerter never gets stored.
check "misconfigured alerter rejected" "$(code POST /api/collections/alerters/records "$TOKEN" \
  "{\"user\":\"$USER_ID\",\"name\":\"test-broken\",\"type\":\"ntfy\",\"config\":{},\"enabled\":true}")" "400"

sleep 3
NTFY_MSG=$(curl -sS -m 20 "$NTFY/$NTFY_TOPIC/json?poll=1" | jqr 'select(.event=="message") | .title' | head -1)
check "ntfy delivered test title" "$NTFY_MSG" "Seekout test"
HOOK_BODY=$(curl -sS -m 20 "$NTFY/$WEBHOOK_TOPIC/json?poll=1" | jqr 'select(.event=="message") | .message' | head -1)
case "$HOOK_BODY" in
  *'"title":"Seekout test"'*) ok "webhook delivered JSON payload" ;;
  *) bad "webhook payload" "$HOOK_BODY" ;;
esac

echo "=============== 5. matching matrix ==============="

# A source seeds on its first poll (ingest only, no alerts), so the matrix is
# evaluated on a second poll: drop the seeded listings and re-poll, which makes
# every item new again with the seekers now in place.
SRC=$(api POST /api/collections/sources/records "$TOKEN" \
  '{"name":"test-go-releases","type":"rss","kind":"other","config":{"url":"https://github.com/golang/go/releases.atom"},"interval_minutes":1440,"enabled":true}' | jqr .id)
{ [ -n "$SRC" ] && [ "$SRC" != "null" ]; } && ok "create source" || bad "create source"
CREATED_SOURCES+=("$SRC")

SEED=$(api POST "/api/seekout/sources/$SRC/poll" "$TOKEN")
SEED_FETCHED=$(echo "$SEED" | jqr .fetched)
check "first poll reports seeding" "$(echo "$SEED" | jqr .seeded)" "true"
[ "${SEED_FETCHED:-0}" -gt 0 ] && ok "seed ingested $SEED_FETCHED listings" || bad "seed ingest" "$SEED"
check "seeding stores every listing" "$(echo "$SEED" | jqr .new)" "$SEED_FETCHED"
check "seeding raises no alerts" "$(echo "$SEED" | jqr .matched)" "0"
check "seeded flag persisted" "$(api GET "/api/collections/sources/records/$SRC" "$TOKEN" | jqr .seeded)" "true"

mkseeker() { # mkseeker NAME KIND CONDITIONS_JSON [ALERTERS_JSON] -> id
  api POST /api/collections/seekers/records "$TOKEN" \
    "{\"user\":\"$USER_ID\",\"name\":\"$1\",\"kind\":\"$2\",\"sources\":[\"$SRC\"],\"alerters\":${4:-[]},\"conditions\":$3,\"enabled\":true}" | jqr .id
}

# One known external_id keeps the alerter-backed seeker to a single match
# instead of pushing the whole feed to ntfy.
ONE_ID=$(api GET "/api/collections/listings/records?perPage=1&filter=(source='$SRC')" "$TOKEN" | jqr '.items[0].external_id')

S_ALL=$(mkseeker      test-match-all       any '[]')
S_CONTAINS=$(mkseeker test-contains        any '[{"field":"title","op":"contains","value":"go1"}]')
S_NOMATCH=$(mkseeker  test-nomatch         any '[{"field":"title","op":"contains","value":"zzz-never-matches"}]')
S_IN=$(mkseeker       test-in-operator     any '[{"field":"kind","op":"in","value":"other,home"}]')
S_NEQ=$(mkseeker      test-not-equal       any '[{"field":"kind","op":"!=","value":"other"}]')
S_KIND=$(mkseeker     test-kind-mismatch   car '[]')
S_MISSING=$(mkseeker  test-missing-field   any '[{"field":"attrs.nonexistent.deep","op":"=","value":"x"}]')
S_TWO_COND=$(mkseeker test-two-conditions  any '[{"field":"kind","op":"=","value":"other"},{"field":"title","op":"contains","value":"zzz-never"}]')
S_ONE=$(mkseeker      test-single-listing  any "[{\"field\":\"external_id\",\"op\":\"=\",\"value\":\"$ONE_ID\"}]" "[\"$NTFY_ID\",\"$HOOK_ID\"]")

# mkseeker runs inside a command substitution, i.e. a subshell, so it cannot
# append to this array itself — collect the ids here in the parent shell.
CREATED_SEEKERS=("$S_ALL" "$S_CONTAINS" "$S_NOMATCH" "$S_IN" "$S_NEQ" "$S_KIND" "$S_MISSING" "$S_TWO_COND" "$S_ONE")

for lid in $(api GET "/api/collections/listings/records?perPage=200&filter=(source='$SRC')" "$TOKEN" | jqr '.items[].id'); do
  api DELETE "/api/collections/listings/records/$lid" "$SU_TOKEN" >/dev/null
done
check "listings cleared for re-poll" "$(api GET "/api/collections/listings/records?perPage=1&filter=(source='$SRC')" "$TOKEN" | jqr .totalItems)" "0"

POLL=$(api POST "/api/seekout/sources/$SRC/poll" "$TOKEN")
FETCHED=$(echo "$POLL" | jqr .fetched)
check "post-seed poll is not seeding" "$(echo "$POLL" | jqr .seeded)" "false"
[ "${FETCHED:-0}" -gt 0 ] && ok "poll fetched $FETCHED listings" || bad "poll fetched listings" "$POLL"
check "all fetched listings are new" "$(echo "$POLL" | jqr .new)" "$FETCHED"
[ "$(echo "$POLL" | jqr .matched)" -gt 0 ] 2>/dev/null \
  && ok "poll matched $(echo "$POLL" | jqr .matched) seeker/listing pairs" || bad "poll matched" "$POLL"

alerts_for() { api GET "/api/collections/alerts/records?perPage=1&filter=(seeker='$1')" "${2:-$TOKEN}" | jqr .totalItems; }

check "no-condition seeker matches all"    "$(alerts_for "$S_ALL")"      "$FETCHED"
check "'contains' seeker matches all"      "$(alerts_for "$S_CONTAINS")" "$FETCHED"
check "impossible 'contains' matches none" "$(alerts_for "$S_NOMATCH")"  "0"
check "'in' operator matches all"          "$(alerts_for "$S_IN")"       "$FETCHED"
check "'!=' operator excludes all"         "$(alerts_for "$S_NEQ")"      "0"
check "kind mismatch matches none"         "$(alerts_for "$S_KIND")"     "0"
check "missing field matches none"         "$(alerts_for "$S_MISSING")"  "0"
check "two conditions AND together"        "$(alerts_for "$S_TWO_COND")" "0"
check "'=' on external_id matches one"     "$(alerts_for "$S_ONE")"      "1"

DELIVERED=$(api GET "/api/collections/alerts/records?perPage=1&filter=(seeker='$S_ONE')" "$TOKEN" | jqr '.items[0].delivered')
check "alert delivered to 2 alerters" "$(echo "$DELIVERED" | jqr 'length')" "2"
check "all deliveries succeeded" "$(echo "$DELIVERED" | jqr '[.[] | select(.ok==true)] | length')" "2"

sleep 3
NTFY_ALERT=$(curl -sS -m 20 "$NTFY/$NTFY_TOPIC/json?poll=1" | jqr 'select(.event=="message") | .title' | tail -1)
check "ntfy received the listing alert" "$NTFY_ALERT" "test-single-listing"

LISTING=$(api GET "/api/collections/listings/records?perPage=1&filter=(source='$SRC')" "$TOKEN" | jqr '.items[0]')
check "listing has title" "$(echo "$LISTING" | jqr '.title | length > 0')" "true"
check "listing has url" "$(echo "$LISTING" | jqr '.url | startswith("https://")')" "true"
check "listing kind copied from source" "$(echo "$LISTING" | jqr .kind)" "other"
check "listing has published date" "$(echo "$LISTING" | jqr '.published | length > 0')" "true"

echo "=============== 6. dedup ==============="

POLL2=$(api POST "/api/seekout/sources/$SRC/poll" "$TOKEN")
check "re-poll fetches again" "$(echo "$POLL2" | jqr .fetched)" "$FETCHED"
check "re-poll finds nothing new" "$(echo "$POLL2" | jqr .new)" "0"
check "re-poll raises no alerts" "$(echo "$POLL2" | jqr .matched)" "0"
check "alert count unchanged after re-poll" "$(alerts_for "$S_ALL")" "$FETCHED"

DUP=$(echo "$LISTING" | jqr '{source, external_id, kind}')
check "duplicate listing insert rejected" "$(code POST /api/collections/listings/records "$SU_TOKEN" "$DUP")" "400"

echo "=============== 7. error handling ==============="

BAD_SRC=$(api POST /api/collections/sources/records "$TOKEN" \
  "{\"name\":\"test-bad-feed\",\"type\":\"rss\",\"kind\":\"other\",\"config\":{\"url\":\"$BASE/definitely-not-a-feed\"},\"interval_minutes\":1440,\"enabled\":true}" | jqr .id)
CREATED_SOURCES+=("$BAD_SRC")
check "unreachable feed poll fails" "$(code POST "/api/seekout/sources/$BAD_SRC/poll" "$TOKEN")" "400"
BAD_REC=$(api GET "/api/collections/sources/records/$BAD_SRC" "$TOKEN")
check "failure recorded in last_error" "$(echo "$BAD_REC" | jqr '.last_error | length > 0')" "true"
check "last_polled set despite failure" "$(echo "$BAD_REC" | jqr '.last_polled | length > 0')" "true"
check "successful source has empty last_error" "$(api GET "/api/collections/sources/records/$SRC" "$TOKEN" | jqr '.last_error')" ""
check "poll of unknown source 404s" "$(code POST "/api/seekout/sources/nonexistent1234/poll" "$TOKEN")" "404"
check "source requires name" "$(code POST /api/collections/sources/records "$TOKEN" '{"type":"rss","kind":"other","config":{"url":"https://example.com"},"interval_minutes":30}')" "400"
check "source rejects unknown type" "$(code POST /api/collections/sources/records "$TOKEN" '{"name":"x","type":"bogus","kind":"other","config":{},"interval_minutes":30}')" "400"

echo "=============== 8. per-user isolation ==============="

# A leftover tester-b would fail the unique-email check and leave every
# assertion below passing vacuously as an anonymous caller.
STALE_B=$(api GET "/api/collections/users/records?filter=(email='$B_EMAIL')" "$SU_TOKEN" | jqr '.items[0].id')
if [ -n "$STALE_B" ] && [ "$STALE_B" != "null" ]; then
  check "stale second user removed" "$(code DELETE "/api/collections/users/records/$STALE_B" "$SU_TOKEN")" "204"
fi

B_PASS="testuser-b-$(openssl rand -hex 6)"
USER_B_ID=$(api POST /api/collections/users/records "$SU_TOKEN" \
  "{\"email\":\"$B_EMAIL\",\"password\":\"$B_PASS\",\"passwordConfirm\":\"$B_PASS\",\"verified\":true}" | jqr .id)
{ [ -n "$USER_B_ID" ] && [ "$USER_B_ID" != "null" ]; } && ok "create second user" || bad "create second user"
B_TOKEN=$(api POST /api/collections/users/auth-with-password "" "{\"identity\":\"$B_EMAIL\",\"password\":\"$B_PASS\"}" | jqr .token)
{ [ -n "$B_TOKEN" ] && [ "$B_TOKEN" != "null" ]; } \
  && ok "second user can authenticate" \
  || bad "second user auth" "isolation checks below would be vacuous without a token"

check "user B sees no seekers of user A"  "$(api GET /api/collections/seekers/records "$B_TOKEN" | jqr .totalItems)"  "0"
check "user B sees no alerts of user A"   "$(api GET /api/collections/alerts/records "$B_TOKEN" | jqr .totalItems)"   "0"
check "user B sees no alerters of user A" "$(api GET /api/collections/alerters/records "$B_TOKEN" | jqr .totalItems)" "0"
check "user B cannot read A's seeker"     "$(code GET "/api/collections/seekers/records/$S_ALL" "$B_TOKEN")" "404"
check "user B cannot modify A's seeker"   "$(code PATCH "/api/collections/seekers/records/$S_ALL" "$B_TOKEN" '{"name":"hijacked"}')" "404"
check "user B cannot delete A's alerter"  "$(code DELETE "/api/collections/alerters/records/$NTFY_ID" "$B_TOKEN")" "404"
check "user B cannot test A's alerter"    "$(code POST "/api/seekout/alerters/$NTFY_ID/test" "$B_TOKEN")" "400"
check "user B cannot attach A's alerter"  "$(code POST /api/collections/seekers/records "$B_TOKEN" \
  "{\"user\":\"$USER_B_ID\",\"name\":\"test-sneaky\",\"kind\":\"any\",\"alerters\":[\"$NTFY_ID\"]}")" "400"
check "listings are shared across users"  "$(api GET /api/collections/listings/records "$B_TOKEN" | jqr '.totalItems > 0')" "true"
check "user B cannot create a source"     "$(code POST /api/collections/sources/records "$B_TOKEN" '{"name":"test-b","type":"rss","kind":"other","config":{"url":"https://example.com"},"interval_minutes":30}')" "400"
check "user B cannot change a source"     "$(code PATCH "/api/collections/sources/records/$SRC" "$B_TOKEN" '{"enabled":false}')" "404"
check "user B cannot make themself admin" "$(code PATCH "/api/collections/users/records/$USER_B_ID" "$B_TOKEN" '{"admin":true}')" "404"
check "user B cannot list invites"        "$(api GET /api/collections/invites/records "$B_TOKEN" | jqr .totalItems)" "0"
check "user B can see shared sources"     "$(code GET /api/collections/sources/records "$B_TOKEN")" "200"

echo "=============== 9. cron ==============="

api PATCH "/api/collections/sources/records/$SRC" "$TOKEN" '{"interval_minutes":1}' >/dev/null
BEFORE=$(api GET "/api/collections/sources/records/$SRC" "$TOKEN" | jqr .last_polled)
CRON_OK=0
for _ in $(seq 1 20); do
  sleep 6
  [ "$(api GET "/api/collections/sources/records/$SRC" "$TOKEN" | jqr .last_polled)" != "$BEFORE" ] && { CRON_OK=1; break; }
done
[ "$CRON_OK" = "1" ] && ok "cron polled a due source" || bad "cron poll" "last_polled never advanced from $BEFORE"
api PATCH "/api/collections/sources/records/$SRC" "$TOKEN" '{"interval_minutes":1440}' >/dev/null

echo "=============== 10. restart persistence ==============="

if [ "$CLUSTER" = 1 ] && [ "${SEEKOUT_SKIP_RESTART:-0}" != "1" ]; then
  LISTINGS_BEFORE=$(api GET "/api/collections/listings/records?perPage=1" "$TOKEN" | jqr .totalItems)
  ALERTS_BEFORE=$(api GET "/api/collections/alerts/records?perPage=1" "$TOKEN" | jqr .totalItems)
  OLD_POD=$(kubectl get pods -n "$NS" -l app=seekout -o jsonpath='{.items[0].metadata.name}')
  kubectl delete pod -n "$NS" "$OLD_POD" --wait=false >/dev/null 2>&1
  sleep 5
  kubectl wait --for=condition=ready pod -l app=seekout -n "$NS" --timeout=180s >/dev/null 2>&1
  NEW_POD=$(kubectl get pods -n "$NS" -l app=seekout -o jsonpath='{.items[0].metadata.name}')
  [ "$NEW_POD" != "$OLD_POD" ] && ok "pod replaced ($OLD_POD -> $NEW_POD)" || bad "pod not replaced"

  # Recreate strategy with a single replica (SQLite): expect a short window
  # where the ingress has no ready endpoint and answers 503. /api/health can
  # also still be answered by the terminating pod, so readiness is measured
  # from the first successful data read, and every post-restart assertion
  # retries rather than reporting a dropped connection as data loss.
  read_retry() { # read_retry PATH JQ_EXPR -> first non-empty result
    local out
    for _ in $(seq 1 30); do
      out=$(api GET "$1" "$TOKEN" | jqr "$2")
      { [ -n "$out" ] && [ "$out" != "null" ]; } && { echo "$out"; return; }
      sleep 2
    done
    echo ""
  }

  OUTAGE=0
  for _ in $(seq 1 60); do
    [ -n "$(api GET "/api/collections/listings/records?perPage=1" "$TOKEN" | jqr .totalItems)" ] && break
    OUTAGE=$((OUTAGE+2)); sleep 2
  done
  check "healthy after restart" "$(code GET /api/health)" "200"
  printf 'INFO  restart downtime observed: ~%ss\n' "$OUTAGE"
  check "listings survived restart" "$(read_retry "/api/collections/listings/records?perPage=1" .totalItems)" "$LISTINGS_BEFORE"
  check "alerts survived restart" "$(read_retry "/api/collections/alerts/records?perPage=1" .totalItems)" "$ALERTS_BEFORE"

  # The swap can drop a request aimed at the terminating pod, so retry rather
  # than reporting a transient connection reset as an auth failure.
  TOKEN=""
  for _ in $(seq 1 10); do
    TOKEN=$(api POST /api/collections/users/auth-with-password "" "{\"identity\":\"$EMAIL\",\"password\":\"$PASS\"}" | jqr .token)
    { [ -n "$TOKEN" ] && [ "$TOKEN" != "null" ]; } && break
    sleep 3
  done
  { [ -n "$TOKEN" ] && [ "$TOKEN" != "null" ]; } && ok "auth works after restart" || bad "auth after restart"
  check "no errors in fresh pod log" "$(kubectl logs -n "$NS" -l app=seekout --tail=50 2>/dev/null | grep -ciE 'error|panic')" "0"
else
  skip "restart persistence" "needs cluster access and SEEKOUT_SKIP_RESTART unset"
fi

echo "=============== 11. cleanup ==============="

# The entrypoint runs `superuser upsert` on every boot, which rotates the
# superuser tokenKey — any token minted before a restart is dead here. Without
# this re-auth the deletes below fail silently and the follow-up reads return
# 404 from rule denial, which looks like success.
SU_TOKEN=$(api POST /api/collections/_superusers/auth-with-password "" "{\"identity\":\"$EMAIL\",\"password\":\"$PASS\"}" | jqr .token)
{ [ -n "$SU_TOKEN" ] && [ "$SU_TOKEN" != "null" ]; } && ok "superuser re-auth" || bad "superuser re-auth"

for id in "${CREATED_SEEKERS[@]}";  do [ -n "$id" ] && api DELETE "/api/collections/seekers/records/$id" "$TOKEN" >/dev/null; done
for id in "${CREATED_SOURCES[@]}";  do [ -n "$id" ] && api DELETE "/api/collections/sources/records/$id" "$TOKEN" >/dev/null; done
for id in "${CREATED_ALERTERS[@]}"; do [ -n "$id" ] && api DELETE "/api/collections/alerters/records/$id" "$TOKEN" >/dev/null; done
check "second user deleted" "$(code DELETE "/api/collections/users/records/$USER_B_ID" "$SU_TOKEN")" "204"

# Verified with a fresh superuser token so a stale one cannot read as empty.
check "alerts of a deleted seeker cascade away" "$(alerts_for "$S_ALL" "$SU_TOKEN")" "0"
check "listings of a deleted source cascade away" "$(api GET "/api/collections/listings/records?perPage=1&filter=(source='$SRC')" "$SU_TOKEN" | jqr .totalItems)" "0"
check "second user gone from list" "$(api GET "/api/collections/users/records?filter=(email~'tester-b')" "$SU_TOKEN" | jqr .totalItems)" "0"
check "no test seekers anywhere"  "$(api GET "/api/collections/seekers/records?perPage=100" "$SU_TOKEN"  | jqr '[.items[] | select(.name | startswith("test-")) | .name] | join(",")')" ""
check "no test sources anywhere"  "$(api GET "/api/collections/sources/records?perPage=100" "$SU_TOKEN"  | jqr '[.items[] | select(.name | startswith("test-")) | .name] | join(",")')" ""
check "no test alerters anywhere" "$(api GET "/api/collections/alerters/records?perPage=100" "$SU_TOKEN" | jqr '[.items[] | select(.name | startswith("test-")) | .name] | join(",")')" ""
check "no test accounts anywhere" "$(api GET "/api/collections/users/records?perPage=100&filter=(email~'test-pending-')" "$SU_TOKEN" | jqr .totalItems)" "0"

printf '\n=============== RESULT: %d passed, %d failed, %d skipped ===============\n' "$PASSED" "$FAILED" "$SKIPPED"
[ "$FAILED" -eq 0 ]
