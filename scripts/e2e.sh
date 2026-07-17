#!/usr/bin/env bash
# End-to-end smoke test: builds nothing, drives a running origoad through the
# full artifact lifecycle and asserts on real responses.
#
#   ./bin/origoad -repo /tmp/e2e.git [-db ...] &   # start a fresh server
#   ./scripts/e2e.sh [http://127.0.0.1:8080]
set -euo pipefail
API=${1:-http://127.0.0.1:8080}/api

fail() { echo "E2E FAIL: $*" >&2; exit 1; }
jqpy() { python3 -c "import json,sys; $1"; }

# Seed a demo domain (schemas, workflow, entries, overlay, link, comment, doc).
"$(dirname "$0")/../examples/seed.sh" "${API%/api}" >/dev/null

R1=$(curl -sfS "$API/search?q=selling" | jqpy "print(json.load(sys.stdin)['artifacts'][0]['guid'])")
[ -n "$R1" ] || fail "seeded requirement not searchable"

# Overlay resolution merges base fields.
OV=$(curl -sfS "$API/search?q=Variant" | jqpy "print(json.load(sys.stdin)['artifacts'][0]['guid'])")
curl -sfS "$API/entries/$OV?resolve=1" | jqpy "
d=json.load(sys.stdin)['resolved']['fields']
assert d['priority']=='medium' and 'selling' in d['rationale'], d" || fail "overlay resolution"

# ETag / If-Match: stale precondition must 412, fresh must 200.
ETAG=$(curl -sfSI "$API/entries/$R1" | tr -d '\r' | awk -F': ' 'tolower($1)=="etag"{print $2}')
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X PUT "$API/entries/$R1" \
  -H "If-Match: $ETAG" -H 'Content-Type: application/json' -d '{"title":"e2e updated"}')
[ "$CODE" = 200 ] || fail "fresh If-Match rejected ($CODE)"
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X PUT "$API/entries/$R1" \
  -H "If-Match: $ETAG" -H 'Content-Type: application/json' -d '{"title":"clobber"}')
[ "$CODE" = 412 ] || fail "stale If-Match accepted ($CODE)"

# Kind-checked routes: an entry GUID is 404 on the documents collection.
CODE=$(curl -s -o /dev/null -w '%{http_code}' "$API/documents/$R1")
[ "$CODE" = 404 ] || fail "wrong-kind route returned $CODE"

# Move keeps identity and history.
curl -sfS -X POST "$API/artifacts/$R1/move" -H 'Content-Type: application/json' \
  -d '{"path":"archive/e2e"}' >/dev/null
curl -sfS "$API/artifacts/$R1/history" | jqpy "
h=json.load(sys.stdin)['history']
assert len(h) >= 3, h
assert any('created' in e['subject'] for e in h), h" || fail "history lost after move"

# Workflow state survived; comments are threaded and ordered.
curl -sfS "$API/entries/$R1" | jqpy "
m=json.load(sys.stdin)['meta']
assert m['workflows']['dev']=='review', m" || fail "workflow state"
curl -sfS "$API/artifacts/$R1/comments" | jqpy "
c=json.load(sys.stdin)['comments']
assert len(c)==1 and c[0]['data']['text'], c" || fail "comments"

# Reindex agrees with the live projection.
curl -sfS -X POST "$API/admin/reindex" >/dev/null
curl -sfS "$API/entries/$R1" | jqpy "
assert json.load(sys.stdin)['meta']['folder']=='archive/e2e'" || fail "state lost after reindex"

echo "E2E PASS"
