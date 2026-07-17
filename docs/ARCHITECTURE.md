# Source structure

A guided tour of the repository: what each package does, why it exists,
and how a request flows through them. Layers only ever depend downward:

```
cmd/origoad          entry point: flags, HTTP server wiring
  └─ internal/httpapi   REST layer: routing, status codes, ETag/If-Match
       └─ internal/core    the Foundation: all domain logic
            ├─ internal/gitx    bare-repo Git plumbing
            └─ internal/ojson   order-preserving JSON codec
web/                 Lit + TypeScript frontend (talks only to /api)
```

## cmd/origoad — the server binary

One `main.go`: parses flags (`-repo`, `-addr`, `-web`, `-db`), opens the
Foundation with the chosen projection (in-memory by default, PostgreSQL
when `-db` is given), mounts the REST API under `/api/` and optionally a
static frontend at `/`, and serves with explicit HTTP timeouts (Slowloris
protection). No logic lives here — it is wiring only, so everything is
testable without a process.

## internal/gitx — Git as a storage engine

Drives a **bare** repository through plumbing subprocesses; there is never
a working tree, because a working tree is shared mutable state a
concurrent server must not have.

- `gitx.go` — everything:
  - `Init` / `Head` — open-or-create; `Head` distinguishes "unborn branch"
    (exit code 1) from real failures, so a transient git error can never
    masquerade as an empty repository.
  - `CommitOnce(parent, msg, ops)` — builds a commit from `Op`s (write or
    delete) against a private temp index (`hash-object`, one NUL-terminated
    `update-index -z --index-info` stream, `write-tree`, `commit-tree`)
    and publishes it with **one** compare-and-swap `update-ref`. If the
    branch moved it returns `ErrStale` and publishes nothing — retry
    policy belongs to the caller, which must re-validate first.
  - `Commit` — blind-retry wrapper for tests/tooling only; server writes
    go through the Foundation's validated loop instead.
  - `ListTree` / `ReadBlobs` — batched reads (`ls-tree`, `cat-file
    --batch`). Pathspecs are `:(literal)` and `core.quotepath=false` is
    forced globally: user-named folders must never be interpreted as
    pathspec magic or quoted on output (both caused real bugs — see
    [DESIGN_NOTES](../DESIGN_NOTES.md) §4).
  - `Log` — commit history for a caller-supplied pathspec.
  - `BlobSHA` — computes a blob's Git object name without touching the
    repo; this is the artifact ETag.

## internal/ojson — stable JSON

`Obj` is a JSON object that preserves key order through parse → edit →
encode. Guarantees: unchanged objects re-encode byte-identically, edited
keys keep their position, new keys append, unknown keys pass through
untouched. This is what makes Git history show *logical* changes instead
of serializer noise, and lets external tools add properties to artifact
files without the server destroying them. Fuzzed for canonical-form
stability.

## internal/core — the Foundation

All domain logic. One package, split by concern:

- `model.go` — artifact kinds (`entry`, `document`, `link`, `comment`),
  the projected `Meta` summary, GUID generation/validation, error
  sentinels (`ErrNotFound`, `ErrConflict`, `ErrValidation`,
  `ErrPrecondition`, `ErrUnavailable`), and `CleanFolder` — the single
  gate every user-supplied folder path passes (rejects traversal,
  `.origoa`, GUID segments, control characters, pathspec magic, unbounded
  depth/length).

- `schema.go` — `Schema`, `Field`, `Relationship`, `Workflow` types and
  `composeSchemas`: lexical composition root → artifact where deeper
  definitions win per property, fields merge by id, and
  `"inheritance": "off"` severs everything above.

- `projection.go` — the **Projection interface** and everything shared by
  its implementations:
  - `classify(path, sha, content)` — the repository scanner: recognizes
    artifact files (`<folder>/<guid>/.origoa.json`) and metadata
    (`.origoa/{links,comments,schemas,workflows}/`), tolerates anything
    malformed (direct Git pushes are a supported workflow), and sanitizes
    all projected text (NUL-stripped, size-capped) so no repository
    content can wedge a projection backend.
  - `searchText` — single linear token scan (never recursive re-parsing;
    the recursive version was a self-authored DoS).
  - `syncRecords` — shared full-rebuild loader; prefilters relevant paths
    so foreign blobs are never fetched.
  - The interface itself: `Sync` (full rebuild from HEAD), `Apply(parent,
    newHead, changes)` (project one commit, **CAS on parent** — stale
    projections refuse and fall back to Sync), and fail-closed reads
    (errors are errors, never fabricated empty results).

- `memory.go` — in-memory projection (default). Maps keyed by file path
  and GUID, incremental `Apply`, full rebuild on `Sync`/divergence. Zero
  dependencies; also what CI proves rebuildability with on every run.

- `postgres.go` — PostgreSQL projection: plain SQL, no ORM. Auto-created
  schema (`artifacts` with a generated tsvector + GIN index,
  `config_files`, `repo_state`), `processed_hash` advanced with a
  database-side CAS mirroring the Git CAS (so two servers sharing one
  database can never silently skip a commit).

- `foundation.go` — the operations layer and the heart of the design:
  - `write(prepare)` — the transactional write loop (design guide §10.1):
    sync projection to Git head → run `prepare` (all validation + ops
    built against that state) → `CommitOnce` → `Apply`. On CAS contention
    the loop re-syncs and re-runs `prepare` from scratch with jittered
    backoff; validations are therefore never applied to stale state.
  - Every operation (`CreateArtifact`, `UpdateArtifact`, `DeleteArtifact`,
    `MoveArtifact`, `CreateLink`, `CreateComment`, `Transition`,
    `PutSchema`, `PutWorkflow`) is one `prepare` closure producing one
    structured commit (`subject` + `Origoa-Op`/`Origoa-Guid` trailers).
  - Reads: `Artifact` (meta + full object), `List`/`Search` (limit-capped),
    `ResolveOverlay` (base-chain field merge with cycle detection),
    `Links`, `Comments` (chronological), `History` (follows the GUID
    across moves via a glob pathspec), `EffectiveSchema`, `WorkflowDef`.

- Tests, deliberately split by purpose:
  - `core_test.go` — behavioral unit tests + the `openTest` helper that
    runs the *same suite* against PostgreSQL when `ORIGOA_TEST_DSN` is
    set.
  - `fidelity_test.go` — spec-invariant guards (unknown properties survive
    updates, history survives moves, hostile folder names, threads,
    multi-file artifacts, no-op updates create no commit).
  - `stress_test.go` — concurrency torture: mixed operations under
    concurrent reindexing with a live-projection-equals-fresh-rebuild
    invariant; two writer processes sharing one repo and one database;
    foreign-committer drift detection; hostile payloads (NUL, multi-MB,
    deep nesting).
  - `postgres_test.go` — `processed_hash` fast start and divergence
    recovery.
  - `fuzz_test.go`, `bench_test.go` — fuzz targets and benchmarks.

## internal/httpapi — the REST layer

Thin by design: routing (Go 1.22 method patterns), request decoding with a
4 MiB cap, and translation between domain errors and status codes
(400/404/409/412/503; internal details are logged server-side and never
leaked to clients). Artifact routes are kind-checked — an entry GUID is
404 on `/api/documents/...`. ETag/If-Match follow RFC 9110 (quoted tags,
`W/`, `*`). The comments endpoint batches its object reads into one git
call. `httpapi_test.go` is the black-box integration + adversarial suite.

## web — the frontend

A deliberately small Lit + TypeScript single-page app (`src/main.ts`,
~300 lines): folder tree, artifact table, and a **schema-driven** detail
view — fields, enum options, and workflow transitions are all rendered
from the effective schema fetched at selection time, so the client
contains no domain knowledge. Saving merges form values into the stored
fields (never clobbering properties it doesn't render) and coerces types.
Built with esbuild, typechecked with `tsc --noEmit`; no framework, no
runtime dependencies beyond Lit.

## scripts/ and examples/

- `examples/seed.sh` — populates a demo requirements domain through the
  public API (also the fixture for e2e).
- `scripts/e2e.sh` — black-box end-to-end assertions against a live
  server; used by `make e2e` and both CI e2e jobs (memory + PostgreSQL).

## What the data repository looks like

The `-repo` directory is a normal bare Git repo you can clone and inspect:

```
specs/boot/
  2f6c.../                    ← GUID directory = one entry/document
    .origoa.json              ← the artifact (guid, kind, type, hid, title,
                                 workflows, fields, content, …)
specs/
  .origoa/                    ← metadata scope (nearest to its subjects)
    links/<guid>.json         ← directed typed link (source, target)
    comments/<guid>.json      ← comment (subject, parent, text, created)
    schemas/requirement.json  ← schema definition for this subtree
    workflows/dev.json        ← workflow definition for this subtree
```

Folders organize; GUIDs identify. Moving a folder changes no reference
anywhere — that separation is the core of the design.
