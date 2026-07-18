# Setup

Two ways to run Origoa: **Docker** (one stack, nothing to install but Docker)
or **manual** (Go + Node + PostgreSQL on your machine). Both end at the same
place — the API and the web UI on <http://localhost:8000>.

- [1. Run with Docker](#1-run-with-docker)
- [2. Run manually](#2-run-manually)
- [3. Development mode (hot reload)](#3-development-mode-hot-reload)
- [4. Configuration reference](#4-configuration-reference)
- [5. Running the tests](#5-running-the-tests)
- [6. Deploying to production](#6-deploying-to-production)
- [7. Security posture](#7-security-posture-read-before-exposing-it)

---

## 1. Run with Docker

Requires Docker with the Compose plugin. From this directory (`origoa/`):

```bash
docker compose up -d db                       # 1. start PostgreSQL
docker compose run --rm backend origoa-seed   # 2. (optional) load the demo data
docker compose up -d --build backend          # 3. build & start the API + UI
```

Open <http://localhost:8000>. That's it.

- Step 2 is optional — skip it for an empty repository. It seeds a small demo
  domain (requirements, test cases, products with an overlay variant, a
  document, links, comments and a workflow) so the UI has something to show.
- If you seed **after** the backend is already running, run
  `docker compose restart backend` so it picks up the seeded commits.
- Data persists in two named volumes: `db-data` (PostgreSQL) and `repo-data`
  (the Git repository, the source of truth).

Handy shortcuts (see `make help`):

```bash
make docker-up      # build + start the whole stack
make docker-seed    # seed the demo data and restart the backend
make docker-down    # stop (volumes are kept)
```

Stop and wipe everything: `docker compose down -v`.

---

## 2. Run manually

**Prerequisites:** Go ≥ 1.24, Node ≥ 20, and PostgreSQL ≥ 14 with the `ltree`
extension (bundled with the standard distribution).

### 2.1 Create the database

```bash
createuser origoa --pwprompt          # choose a password (the examples use "origoa")
createdb -O origoa origoa
psql -d origoa -c 'CREATE EXTENSION IF NOT EXISTS ltree'
```

The backend applies its own tables and indexes on startup; you only create the
database and the extension.

### 2.2 Build the frontend

```bash
cd frontend
npm install
npm run build          # outputs frontend/dist
cd ..
```

### 2.3 Seed and run the backend

```bash
export ORIGOA_DSN='postgres://origoa:origoa@localhost:5432/origoa?sslmode=disable'
export ORIGOA_GIT_DIR="$PWD/data/repo.git"
export ORIGOA_STATIC="$PWD/frontend/dist"

cd backend
go run ./cmd/origoa-seed        # optional: load the demo data
go run ./cmd/origoad            # serves the API and the UI on :8000
```

Open <http://localhost:8000>.

The same flow is wrapped in the Makefile:

```bash
make seed        # DSN / GIT_DIR overridable: make seed DSN=... GIT_DIR=...
make run         # builds the SPA and runs the server
```

---

## 3. Development mode (hot reload)

Run the backend on `:8000` (as above) and the Vite dev server separately:

```bash
cd frontend && npm run dev      # or: make dev
```

Vite serves the UI on <http://localhost:5173> with hot module reload and
proxies `/api` (REST and WebSocket) to the backend on `:8000`. Because the
browser talks only to the Vite origin, no CORS configuration is needed.

If instead you point the browser directly at a frontend on a **different
origin** than the API (no proxy), set `ORIGOA_CORS_ORIGIN` on the backend to
that origin (see below).

---

## 4. Configuration reference

Configure with environment variables, a JSON file (`-config origoa.json`), or
both — environment variables win. See `backend/origoa.example.json`.

| Env var | Config key | Default | Purpose |
|---|---|---|---|
| `ORIGOA_LISTEN` | `listen` | `:8000` | Listen address |
| `ORIGOA_DSN` | `database` | `postgres://origoa:origoa@localhost:5432/origoa` | PostgreSQL connection URL |
| `ORIGOA_GIT_DIR` | `gitDir` | `./data/repo.git` | Bare Git repository (the source of truth) |
| `ORIGOA_STATIC` | `staticDir` | *(none)* | Directory of the built SPA to serve; unset = API only |
| `ORIGOA_CORS_ORIGIN` | `corsOrigin` | *(empty)* | Allowed cross-origin (`*` for any); empty = same-origin only |
| — | `author.name` / `author.email` | `origoa` | Identity stamped on commits |
| — | `scanner` | foundation defaults | GUID files, config folders and indexers |

The backend logs its effective configuration at startup with the database
password redacted.

---

## 5. Running the tests

The full ladder — unit → integration → adversarial → whole-system — is
documented in [TESTING.md](TESTING.md). In short:

```bash
# backend (needs a scratch database; see TESTING.md)
cd backend && go test -p 1 ./...

# frontend unit tests (no server)
cd frontend && npm run test:unit

# browser tiers (need a running, freshly seeded server)
cd frontend && npm run test:integration    # happy-path end-to-end
              npm run test:adversarial     # XSS, malformed input, conflicts
              npm run test:chaos           # live UI + concurrent writers + reindex
```

---

## 6. Deploying to production

- **Serve the SPA same-origin.** Point `ORIGOA_STATIC` at the built `dist`
  and let `origoad` serve both the API and the UI from one origin. This is the
  default topology; it needs no CORS and keeps the security headers effective.
- **Terminate TLS at a reverse proxy** (nginx, Caddy, a cloud load balancer)
  in front of `origoad`, which speaks plain HTTP. Forward WebSocket upgrades
  for `/api/ws`.
- **Persist two things:** the Git repository (`ORIGOA_GIT_DIR`) and the
  PostgreSQL database. Git is authoritative — the database is a projection and
  can always be rebuilt from Git (`POST /api/reindex`), so back up the Git
  repository above all.
- **Set a real database password** and pass the DSN via a secret, not a
  committed file.
- The server ships graceful shutdown, request timeouts (Slowloris-safe,
  WebSocket-friendly), a bounded database-connect retry on startup, panic
  recovery, and conservative security response headers (a Content-Security-
  Policy, `nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy`).

---

## 7. Security posture — read before exposing it

Origoa implements the design guide's MVP, which **deliberately excludes
authentication and authorization**. Everyone who can reach the API can read
and write everything. That is fine for a trusted network or a single-user
deployment, but the service must **not** be exposed to the public internet on
its own.

To run it somewhere untrusted, put it behind a gateway that provides:

- **Authentication** (SSO / OAuth proxy / mTLS) in front of `origoad`.
- **TLS** termination.
- **Rate limiting** and request-size limits at the edge (the backend caps
  request bodies and search results, but has no per-client rate limiting).

What the application *does* harden, out of the box:

- **No stored XSS** — untrusted rich-text and document HTML is sanitized
  before rendering, and a Content-Security-Policy is applied as defense in
  depth (both covered by the adversarial test suite).
- **No cross-site WebSocket hijacking** — the session WebSocket rejects
  connections whose `Origin` is neither same-origin nor the configured
  `ORIGOA_CORS_ORIGIN`.
- **No SQL injection** — every query is parameterized; repository paths and
  identifiers are validated, and hostile input (NUL bytes, multi-megabyte
  values) is sanitized so it cannot corrupt or wedge the projection.
- **No CORS by default** — cross-origin access is opt-in via
  `ORIGOA_CORS_ORIGIN`.

Also outside the MVP (per the design guide), and therefore not to be relied on
in production yet: branching/merging, multi-server deployment, server-side
extension execution, and anchored document comments.
