# Testing

The suite is organized as a ladder — **unit → integration → adversarial** —
across both the Go backend and the TypeScript frontend. Each rung is
independently runnable.

## Prerequisites

- Go ≥ 1.24, Node ≥ 20, and PostgreSQL with the `ltree` extension.
- A scratch database for the DB-backed tests (override the DSN with
  `ORIGOA_TEST_DSN`):
  ```bash
  createuser origoa; createdb -O origoa origoa_test
  psql -d origoa_test -c 'CREATE EXTENSION IF NOT EXISTS ltree'
  ```
- For the browser tests: `cd frontend && npm install`. Playwright uses the
  system Chromium; set `CHROMIUM_PATH` to override the binary.

## Backend (Go)

```bash
cd backend
go test -p 1 ./...          # unit + integration, everything
go test -race -p 1 ./...    # the same, under the race detector
```

`-p 1` runs the package test binaries serially. It is required because the
DB-backed packages share one scratch database; parallel packages would
truncate each other's tables. Tests that cannot reach Postgres skip rather
than fail.

| Rung | Where | What it covers |
|---|---|---|
| **Unit** | `ojson`, `schemamodel`, `artifact`, `config`, `scanner`, `projection` (`encode_test.go`) | Order-preserving JSON, schema composition & workflow rules, artifact parsing/search-text, config loading, path classification, ltree encoding and NUL/oversize sanitization — pure, no DB |
| **Integration** | `gitstore`, `resolve`, `repo`, `api` | Git plumbing against a real repo; lexical schema/workflow resolution, the full repository update transaction, CRUD, overlays, HID history and reindex against Postgres; the REST API over HTTP |
| **Adversarial** | `repo/torture_test.go`, `*/fuzz_test.go` | Concurrent mixed-operation torture under a live reindex, two processes sharing one repository, foreign-commit drift, hostile payloads, projection-never-wedges; fuzzers for JSON round-trip, path classification, folder cleaning and input sanitization |

Run one rung or target explicitly:

```bash
go test ./internal/artifact/ ./internal/config/ ./internal/scanner/   # some unit packages
go test -run 'Torture|MultiWriter|Adversarial' ./internal/repo/        # concurrency torture
go test -run '^$' -fuzz FuzzRoundTripStable ./internal/ojson/          # a fuzzer
```

## Frontend (TypeScript)

```bash
cd frontend
npm run test:unit           # pure logic, jsdom, no server (vitest)
```

The browser rungs drive the real SPA and need a **running, freshly seeded
server**:

```bash
# from backend/, once per browser run:
go run ./cmd/origoa-seed
ORIGOA_STATIC=../frontend/dist go run ./cmd/origoad &

# from frontend/:
npm run test:integration    # happy-path end-to-end (expects the demo dataset)
npm run test:adversarial     # stored-XSS, malformed URLs, hostile input, conflict, ...
npm run test:chaos           # whole-system: live UI + concurrent writers + reindex churn
```

Reseed (`origoa-seed` against an empty repo) before each browser run — the
integration and chaos assertions expect the demo dataset's exact shape.

| Rung | File | What it covers |
|---|---|---|
| **Unit** | `test/unit/*.test.ts` | The HTML sanitizer (XSS stripping), the central store (observe/update/change-detection), the URL⇄state router mapping — pure functions under jsdom |
| **Integration** | `test/integration.mjs` | The MVP success path end to end: schema-driven navigation and detail, workflow transitions, field editing, overlay resolution, the document editor, search, and creating an artifact through the UI |
| **Adversarial** | `test/adversarial.mjs` | Stored-XSS on every content surface, malformed/oversized deep links, missing artifacts, hostile field input, an optimistic-concurrency conflict, reindex-while-browsing, rapid navigation |
| **Whole-system** | `test/chaos.mjs` | A live browser session while concurrent API clients hammer writes and repeated reindexing churns the projection; asserts no data lost, the WebSocket drives live UI updates, and the live projection equals a from-scratch Git rebuild |

## What each layer proves

- **Unit** — the pure logic is correct in isolation and fast to check.
- **Integration** — the components work wired to their real dependencies
  (Git, Postgres, HTTP, the browser).
- **Adversarial / whole-system** — the guarantees hold under hostile input
  and concurrency: Git stays the single source of truth, the PostgreSQL
  projection always equals a from-scratch rebuild, and neither layer can be
  broken into losing data or executing injected script.
