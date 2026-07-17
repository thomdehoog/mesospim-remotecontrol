# Installing and running Origoa

This tutorial takes you from a clean machine to a running Origoa server
with a configured domain, in three stages: install, run, and define your
first artifact types. Every command was taken from a working session — if
a step fails, see [Troubleshooting](#troubleshooting).

## 1. Prerequisites

| Requirement | Version | Why |
|---|---|---|
| Go | ≥ 1.24 | builds the server |
| git | ≥ 2.38 | Origoa's storage engine — the server shells out to git plumbing |
| Node.js + npm | ≥ 20 | builds the web frontend (not needed for API-only use) |
| PostgreSQL | ≥ 14, optional | persistent projection database; without it Origoa runs fully in-memory |

Check what you have:

```sh
go version && git --version && node --version
```

## 2. Build

```sh
git clone https://github.com/thomdehoog/origoa.git
cd origoa
make build          # builds web/dist (npm install + typecheck + bundle) and bin/origoad
```

API-only build without Node:

```sh
go build -o bin/origoad ./cmd/origoad
```

Verify the build by running the test suite:

```sh
make test           # go vet, gofmt gate, full suite with race detector
```

## 3. Run

### In-memory mode (simplest — start here)

```sh
./bin/origoad -repo data/origoa.git -addr 127.0.0.1:8080 -web web/dist
```

- `-repo` — path to a **bare Git repository**. Created automatically if it
  doesn't exist. This is your data; everything else is derived from it.
- `-web` — serves the frontend at `/`. Omit for API-only.

The projection (indexes, search) lives in memory and is rebuilt from the
Git repository on every start. Nothing besides the `-repo` directory needs
to survive a restart.

### PostgreSQL mode (persistent projection)

Create a database, then pass a DSN:

```sh
createdb origoa
./bin/origoad -repo data/origoa.git -web web/dist \
  -db "postgres://user:password@localhost:5432/origoa?sslmode=disable"
```

Tables are created automatically on first start. On subsequent starts the
stored projection is reused when its recorded revision matches the Git
HEAD; any divergence (crash, direct `git push` into the repo) triggers an
automatic rebuild from Git. The database is disposable by design — you can
drop it at any time and the next start reconstructs it.

### Verify it's alive

```sh
curl -s http://127.0.0.1:8080/api/tree
# {"artifacts":null,"folders":[],"rev":""}
```

Or run the end-to-end suite against your instance:

```sh
./scripts/e2e.sh http://127.0.0.1:8080
```

## 4. First steps: define a domain

Origoa has no built-in artifact types — schemas define them. This
walkthrough builds a tiny requirements domain (the same one
`examples/seed.sh` creates; run that instead if you just want data).

**Define a workflow** (a reusable state machine):

```sh
curl -X PUT http://127.0.0.1:8080/api/workflows/dev \
  -H 'Content-Type: application/json' -d '{
  "id": "dev", "initial": "open",
  "states": ["open", "review", "done"],
  "transitions": [
    {"from": "open", "to": "review"},
    {"from": "review", "to": "done"},
    {"from": "review", "to": "open"}
  ]}'
```

**Define an artifact type** (fields, HID prefix, workflow assignment):

```sh
curl -X PUT http://127.0.0.1:8080/api/schemas/requirement \
  -H 'Content-Type: application/json' -d '{
  "artifactType": "requirement", "kind": "entry",
  "hidPrefix": "REQ", "workflows": ["dev"],
  "fields": [
    {"id": "priority", "name": "Priority", "type": "enum",
     "options": ["low", "medium", "high"]},
    {"id": "rationale", "name": "Rationale", "type": "text"}
  ]}'
```

The file name in the URL must equal the `artifactType`. Add `?scope=some/
folder` to define the schema only for a repository subtree — schemas
compose lexically from root to artifact, nearest definition wins.

**Create an entry:**

```sh
curl -X POST http://127.0.0.1:8080/api/entries \
  -H 'Content-Type: application/json' -d '{
  "path": "specs/boot", "type": "requirement",
  "title": "System boots in under 2 seconds",
  "fields": {"priority": "high", "rationale": "Startup latency sells."}}'
```

The response contains the permanent `guid`, the auto-generated HID
(`REQ-1`), and the initial workflow state (`dev: open`). Every write you
just made is one Git commit — inspect your data at any time:

```sh
git --git-dir=data/origoa.git log --oneline
```

**Explore further:** transition workflows
(`POST /api/artifacts/{guid}/transition`), link artifacts
(`POST /api/links`), comment (`POST /api/comments`), search
(`GET /api/search?q=boot`), and open the frontend at
http://127.0.0.1:8080/. The full endpoint list is in the
[README](../README.md#rest-api).

## 5. Production deployment notes

- **Put a reverse proxy with TLS and authentication in front.** Origoa
  intentionally ships without auth (MVP scope); it must not face untrusted
  networks directly.
- **One server process per repository is the normal topology.** Running
  two servers against the same repo + database is safe (writes are
  CAS-protected end to end and tested), but conflicting concurrent edits
  resolve last-writer-wins at file level.
- **Back up the bare Git repository** (`-repo` directory) — it is the only
  state that matters. `git clone --mirror` of it is a complete backup.
- **Direct Git access is supported**: clone the bare repo, edit artifact
  files, push. The server tolerates malformed files and can always be
  resynchronized (`POST /api/admin/reindex`).
- A minimal systemd unit:

  ```ini
  [Unit]
  Description=Origoa Foundation
  After=network.target postgresql.service

  [Service]
  ExecStart=/opt/origoa/bin/origoad -repo /var/lib/origoa/origoa.git \
    -addr 127.0.0.1:8080 -web /opt/origoa/web/dist \
    -db postgres://origoa:secret@localhost/origoa?sslmode=disable
  Restart=on-failure
  User=origoa

  [Install]
  WantedBy=multi-user.target
  ```

## Troubleshooting

**`git init: exec: "git": executable file not found`** — install git; the
server drives git plumbing as subprocesses.

**HTTP 503 `projection temporarily unavailable`** — the PostgreSQL
connection failed. Reads fail closed on purpose (a fabricated empty answer
could corrupt data, e.g. duplicate HIDs). Fix the database and requests
resume; no data is lost — Git is authoritative.

**Projection looks stale after a direct `git push`** — any API write or a
server restart resynchronizes automatically; `POST /api/admin/reindex`
forces it immediately.

**HTTP 409 `repository is being modified concurrently`** — heavy write
contention from multiple processes exhausted the retry loop (20 attempts
with backoff). Retry the request.

**HTTP 412 on PUT** — your `If-Match` ETag is stale; someone changed the
artifact since you read it. Re-fetch, merge, retry.

**Frontend loads but shows nothing** — you probably started API-only
(no `-web`) or `web/dist` wasn't built; run `make web`.
