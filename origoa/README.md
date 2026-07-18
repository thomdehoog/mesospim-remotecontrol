# Origoa Foundation

A generic storage platform for building information management applications,
implemented from the *Origoa Foundation* design guide. The Foundation stores,
organizes, versions, queries and relates structured information independently
of any business domain: artifact types such as requirements, products or
tickets are defined entirely through repository configuration, never in code.

**Git is the single source of truth.** Every repository modification is one
Git commit built through plumbing-level object construction (no working
directory). PostgreSQL holds only derived, rebuildable projections for fast
querying. The whole database can be reconstructed from Git at any time.

```
Git Repository  (source of truth)
      │
Origoa Backend  (Go — repository services, projections, REST + WebSocket)
      │
Frontend        (TypeScript + Lit web components, no framework)
```

## Layout

| Path | Contents |
|---|---|
| `backend/` | Go backend (`origoad` server, `origoa-seed` demo data) |
| `backend/internal/ojson` | Order/format-preserving JSON persistence (stable Git diffs) |
| `backend/internal/gitstore` | Bare-repo management: blobs → trees → commit, CAS branch publish |
| `backend/internal/projection` | PostgreSQL projection, plain SQL, `ltree` hierarchy index |
| `backend/internal/scanner` | Configurable repository scanner + foundation indexer |
| `backend/internal/schemamodel` | Schema/workflow definitions, lexical composition rules |
| `backend/internal/resolve` | Effective-schema and workflow resolution along the hierarchy |
| `backend/internal/repo` | Repository services: update transaction, CRUD, overlays, HIDs, reindex |
| `backend/internal/api` | REST API + WebSocket session service |
| `frontend/` | SPA: central store, URL router, REST/WS clients, schema-driven UI |

## Repository format

- Entries and documents live in directories named by their permanent GUID;
  the identity file `.origoa.json` carries kind, type, title, HID, fields,
  workflow states and (for documents) the hierarchical section tree.
- Every configuration scope may contain a hidden `.origoa/` directory with
  `schemas/`, `workflows/`, `links/` and `comments/`. Links and comments are
  independent GUID-identified artifacts stored near their source/subject
  (metadata locality).
- The scanner is configured through markers (`guid_files`, `config_folders`,
  `indexers`) and ignores everything else. Commit messages are documentation
  only — projection always derives from tree diffs.

## The repository update transaction

Every write follows the sequence from the Implementation Notes chapter:
check `processed_hash` against HEAD and replay missing commits → construct
the Git commit (branch untouched) → begin the PostgreSQL transaction →
update projections → acquire the repository mutex → publish via
compare-and-swap `update-ref` (on conflict: release, roll back, rebuild,
retry) → update `processed_hash` → release the mutex → commit. Direct Git
pushes from external tooling are picked up by replay; if incremental replay
is impossible the backend falls back to a full reindex (history
reconstruction of the complete HID history → GUID recognition → field
indexing → bulk full-text rebuild — parallel workers derive the search text,
the GIN index is dropped and recreated, and `maintenance_work_mem` is raised
for the build → history scan for deleted artifacts). Because the reindex walks Git history to
rebuild the identifier history, no derived data — including every HID ever
assigned and the commit that assigned it — is lost across a rebuild. A
background loop also continuously synchronizes the projection, so commits
pushed straight into the Git repository by external tooling are picked up
without waiting for the next API write.

Concurrency follows the spec's two mechanisms. Writers hold the Maintenance
Mode gate in shared mode and remain concurrent; a reindex or large structural
operation holds it exclusively, so the repository is read-only for the
duration while ordinary reads continue (writes get "Temporarily
Unavailable"). `processed_hash` is always read before HEAD, which — since the
hash is committed only after the branch reference advances — guarantees it is
never mistaken for a descendant of a momentarily-stale HEAD. The `gitstore`
package serializes access to the underlying (non-thread-safe) go-git library
so the store is a safe concurrent abstraction. These paths are covered by a
concurrent-torture suite (mixed operations under a live reindex, two
processes sharing one repository, foreign-commit drift) asserting that the
live projection always equals a from-scratch rebuild, plus fuzz tests for
JSON round-trip stability, path classification and NUL/oversize input
handling.

## Running it

The fastest path is Docker — from `origoa/`:

```bash
docker compose up -d db                       # PostgreSQL
docker compose run --rm backend origoa-seed   # optional demo data
docker compose up -d --build backend          # API + UI on http://localhost:8000
```

Or manually (Go ≥ 1.24, Node ≥ 20, PostgreSQL with `ltree`):

```bash
createdb -O origoa origoa && psql -d origoa -c 'CREATE EXTENSION IF NOT EXISTS ltree'
cd frontend && npm install && npm run build && cd ../backend
go run ./cmd/origoa-seed                                # optional demo data
ORIGOA_STATIC=../frontend/dist go run ./cmd/origoad     # http://localhost:8000
```

`make help` lists shortcuts for both. **[SETUP.md](SETUP.md)** is the full,
step-by-step tutorial — Docker and manual, development mode, the configuration
reference, and the production and security guidance. Read
[§7 Security posture](SETUP.md#7-security-posture-read-before-exposing-it)
before exposing the service: the MVP has no built-in authentication and must
sit behind an auth gateway and TLS when running anywhere untrusted.

## Tests

The suite is a ladder — unit → integration → adversarial — across both the
backend and the frontend, documented in full in [TESTING.md](TESTING.md):

```bash
cd backend  && go test -p 1 ./...          # backend unit + integration + torture/fuzz
cd frontend && npm run test:unit           # frontend pure logic (vitest + jsdom)
#   with a running, freshly seeded server:
cd frontend && npm run test:integration    # happy-path end-to-end
              npm run test:adversarial     # stored-XSS, malformed URLs, hostile input, conflicts
              npm run test:chaos           # whole-system: live UI + concurrent writers + reindex
```

Backend DB-backed tests use a scratch database (`ORIGOA_TEST_DSN`, default
`postgres://origoa:origoa@localhost:5432/origoa_test`) and skip when it is
unreachable; `-p 1` serializes packages that share it. The adversarial and
whole-system tiers prove the guarantees hold under hostile input and
concurrency — Git stays authoritative, the projection always equals a
from-scratch rebuild, and neither layer can be broken into losing data or
executing injected script. Untrusted rich-text and document HTML is passed
through a small sanitizer (`src/sanitize.ts`) before rendering.

## API sketch

Artifact APIs: `POST /api/entries|documents|links|comments`,
`GET|PATCH|DELETE /api/artifacts/{guid}`, `PATCH /api/links/{guid}`
(custom fields), `PATCH /api/comments/{guid}` (text),
`POST /api/artifacts/{guid}/move`,
`POST /api/artifacts/{guid}/workflows/{name}/transition`.

Service APIs: `GET /api/tree`, `GET /api/search` (full text, kind/type,
folder/subtree, `field.<id>=` filters), `GET /api/schemas`,
`GET /api/schemas/effective`, `GET /api/workflows/{name}`,
`GET /api/artifacts/{guid}/links|comments|history|overlay`,
`POST /api/folders`, `POST /api/folders/move`, `GET /api/folders/impact`,
`GET /api/status`, `POST /api/reindex`, and `GET /api/ws` (session service:
presence, repository events, maintenance and indexing progress).

Writes support optimistic concurrency via `ifRevision` (HTTP 409 when the
artifact changed since it was loaded). During maintenance mode writes return
503 and search returns "temporarily unavailable" while reindexing.

## Scope notes and deviations

- The MVP exclusions from the design guide apply: no permission system,
  no branching/merging, no extension execution, no anchored document
  comments, no referencing of historical revisions.
- The document editor is a purpose-built Lit component (hierarchical
  sections, rich-text blocks, images, entry references) rather than an
  embedded BlockSuite instance; BlockSuite integration is the intended
  follow-up and isolated behind the same component boundary.
- JSON stability is guaranteed verbatim for unchanged files; logically
  modified files are rewritten preserving property order and detected
  indentation/line-ending/trailing-newline style (exotic hand formatting
  inside a modified file may be normalized).
- The extension model chapter (server-side JavaScript hooks) is not part of
  the MVP; the scanner's indexer registry is the prepared extension point.
