# Design Notes — spec vs. implementation

This document records how the implementation relates to the *Origoa
Foundation* design guide: what was adopted, what was adapted, what was
deliberately not built, what adversarial testing taught us, and where gaps
remain. The focus is on **why** each call was made. The spec's section
numbers (§) refer to the design guide.

## 1. Adopted as specified — and why it earned its place

**Git as the single source of truth, everything else a disposable
projection (§3.8, §5.2).** This is the spec's strongest idea and it paid
for itself repeatedly: every consistency bug found during adversarial
testing had the same cheap, safe remedy — rebuild from HEAD. A design
where derived state can always be discarded turns whole classes of
corruption from "restore from backup" into "run reindex."

**Plumbing-only commits, no working directory (§5.3).** Implemented with
`hash-object` / `update-index --index-info` / `write-tree` / `commit-tree`
against a private temp index, published by compare-and-swap `update-ref`.
Why: a working tree is shared mutable state — the one thing a concurrent
server should not have. The CAS ref update is also what makes multi-writer
safety *provable* rather than hoped-for.

**The §10.1 transactional write procedure.** The final write path is
almost literally the spec's sequence: sync projection → validate & build
changeset → CAS publish → project → (on contention) restart from scratch.
Notably, we first implemented a *weaker* version (retry the CAS with
precomputed ops) and adversarial testing proved the spec right — see §4
below. The one reordering: we publish to Git *before* the projection
transaction commits, instead of inside it. Both orderings converge because
Git is authoritative; ours keeps the failure mode simple (projection
behind → resync) instead of requiring a cross-system mutex.

**Stable, order-preserving JSON serialization (§3.16).** A dedicated
order-preserving codec (`internal/ojson`) guarantees: unchanged artifacts
re-encode byte-identically, edits keep key positions, new keys append,
unknown properties survive untouched. Why it matters: the spec's goal is
that Git history shows *logical* changes, and external tools must be able
to extend artifact files without the server destroying their data. This is
tested end-to-end (a hand-committed `x-extension` property survives an API
update in place) and fuzzed for canonical-form stability.

**GUID identity separate from folder organization (§2.2.1–2.2.2), lexical
schema composition with `inheritance: off` (§4.3–4.4), lexically resolved
workflows, HIDs with prefix auto-generation, entry overlays, metadata
locality for links/comments (§3.4), structured commit messages, one commit
per logical operation (§5.4).** All implemented as specified. The
composition rules ("nearest definition wins, per property; fields merge by
id") were unambiguous enough to implement directly and are covered by
dedicated tests for the tricky cases (field replacement in place,
severing, root-scope isolation).

**Configurable-scanner spirit (§10.4).** The classifier recognizes content
by markers (`.origoa.json` in a GUID directory, `.origoa/{links,comments,
schemas,workflows}/`) and *tolerates everything else*, including malformed
files — required because the spec explicitly invites direct Git pushes.
The full pluggable-indexer registry was not built (see §3).

## 2. Adapted — same intent, different mechanics, and why

**PostgreSQL projection: yes, but behind a seam, with in-memory as the
default.** The spec mandates PostgreSQL (§3.10, §10). We first shipped
in-memory only and were (correctly) called back to the spec. The final
shape: a `Projection` interface with two implementations — plain-SQL
PostgreSQL with `processed_hash` tracking, and a zero-dependency in-memory
projection. Why keep both: the spec itself demands the database be fully
reconstructable from Git, which means the *interface* is the architecture
and the backend is an operational choice. In-memory makes development and
CI trivial and proves the rebuildability claim on every test run;
PostgreSQL provides persistence and scale. The seam also forced honest
abstractions (fail-closed errors, CAS'd `Apply`) that both backends share.

**Hierarchy representation: text folder column + prefix index, not ltree
(§3.11).** ltree restricts labels to `[A-Za-z0-9_]`, but folder names are
user-visible organization and legitimately contain unicode, spaces, and
punctuation. Encoding names into ltree labels would trade a real
requirement (arbitrary folder names) for a theoretical one (recursive
queries we don't run). A `text_pattern_ops` index over `folder LIKE
'prefix/%'` answers every hierarchy query the API exposes.

**`processed_hash` update is a CAS, not just a write (§5.13).** The spec
stores the synchronized revision; we additionally advance it with
`UPDATE ... WHERE processed_hash = <parent>`. Why: with two server
processes sharing one database, an unconditional write lets one process
mark commits as processed that were never projected — permanent, silent
divergence. The DB-side CAS mirrors the Git-side CAS, so the "projection
represents revision X" claim is enforced, not asserted. Adversarial
multi-writer tests fail without it.

**Recovery: straight to full rebuild instead of sequential commit replay
(§3.12, §5.13–5.15).** The spec prefers replaying missing commits and
keeps full rebuild as the fallback. We implemented only the fallback. Why:
replay is an optimization with real correctness risk (per-commit diffing,
rename handling, partial-failure states), while rebuild is the safe
superset — and with blob prefiltering it is fast at MVP scale. The
incremental path *does* exist for the common case: every commit a server
publishes itself is projected incrementally. Only divergence pays for a
rebuild.

**Reindex phases collapsed (§3.15, §10.3).** The spec's staged reindex
(GUID table → field index → full-text → history scan, with progressive
service restoration) matters for repositories large enough that a rebuild
takes minutes. Ours is a single transaction because at MVP scale it takes
milliseconds; the staged version is an optimization to add when a
deployment actually feels the pause. The deleted-artifact history scan is
a genuine gap (see §5).

**Validation boundary drawn slightly differently (§4.11).** The spec
assigns validation to the application layer. We enforce in the Foundation
exactly the invariants whose violation would corrupt the *repository
contract*: HID uniqueness, overlay acyclicity, workflow transition
legality, link type/endpoint allowlists when a schema declares
relationships, and config-file-name = declared-id (to prevent silent
same-scope shadowing). Cardinalities and field-level validation stay with
applications as specified. Why: "the application will validate" is not a
defense the storage layer can rely on — but over-validating would make the
Foundation domain-aware, which the spec rightly forbids.

**ETag/If-Match with RFC 9110 semantics.** The spec asks for optimistic
concurrency; we bound it to the artifact's Git blob SHA (an ETag that is
*derived from the source of truth*, not a counter) and honor standard
quoting, `W/`, and `*` so real HTTP clients and proxies work. Stale
preconditions are 412, conflicts (duplicate HID) 409.

## 3. Deliberately not built — and why that is the right call for now

Everything here is listed in the spec as outside or beyond MVP scope
(§9.8, §9.10), or is infrastructure that would be speculative today:

- **Authentication / permissions** — explicitly out of MVP scope. The
  server must sit behind a trusted boundary. Building a permission model
  before the information model has users would be invented requirements.
- **WebSocket session service / presence / conflict warnings (§7.16.3)** —
  the underlying guarantee (optimistic concurrency via If-Match) exists
  and is tested; the *advisory* layer on top is pure UX and needs real
  concurrent users to design against.
- **Extension hooks (§8)** — load/save/workflow hooks are the spec's
  chosen extensibility mechanism, but hooks are contracts: shipping them
  before a first consumer exists means freezing the wrong API.
- **BlockSuite WYSIWYG editor (§7.6)** — documents store hierarchical
  content and entry references today; the editor is a large dependency
  with its own architecture and deserves its own iteration. The frontend
  is otherwise the spec's thin, schema-driven client (Lit, no framework).
- **Branching/merging, distributed repos, historical-revision reads** —
  postponed by §9.8/§9.10. The storage layer (bare Git) already supports
  them physically, which is the point of choosing Git.
- **Pluggable indexer registry (§10.4.4)** — one built-in classifier
  covers the Foundation's four artifact kinds; a registry with one entry
  is indirection without information.
- **Sequential commit replay, staged reindex, deleted-artifact tracking**
  — see §2 and §5.

## 4. What adversarial testing taught us

These were real, reproduced failures — each shaped a design rule.

**1. "Retry the CAS" is not a concurrency strategy; "retry the
transaction" is.** Our first write path validated once, then retried the
ref CAS with precomputed ops. Torture tests with two writers and a foreign
committer proved every bad consequence the spec's §10.1 quietly prevents:
If-Match checked against dead state, duplicate auto-HIDs, direct pushes
silently rebased away, and a Postgres projection that skipped commits
*while its revision tracking claimed it was current*. The fix was to
implement §10.1 literally — re-validate everything against the new head on
every retry. Lesson: the spec's most boring-looking sequence diagram was
its most load-bearing.

**2. Anything user-influenced that reaches a subprocess is an injection
surface — including *file names into git pathspecs*.** A folder named
`:team` made `DELETE` return success while deleting nothing (git parsed
the pathspec magic and matched nothing); `:!x` made artifacts permanently
undeletable; a newline broke `--index-info`; unicode folders were
corrupted by git's path quoting on output. Fixes: `:(literal)` pathspecs
everywhere, `core.quotepath=false`, NUL-terminated index streams, and
folder validation that rejects control characters, leading `:`, and
unbounded depth/length. Lesson: allowlisting input shape is necessary but
not sufficient — the *encoding at each boundary* (here: git's CLI
conventions) must be handled explicitly.

**3. Quadratic behavior is a denial of service you wrote yourself.** The
original search-text extractor recursively re-parsed every nested JSON
subtree; a 5000-deep payload hung a write for nine minutes. Rewritten as a
single linear token scan with a byte cap. Lesson: on a write path, treat
any O(n²) on user input as a security bug, not a performance footnote.

**4. One hostile byte must never wedge a projection.** PostgreSQL rejects
NUL in `text`, `\u0000` in `jsonb`, and >1 MiB tsvectors. Before
sanitization, a single such artifact didn't just fail its own write — it
made every future `Sync` fail, permanently bricking the query layer while
Git stayed healthy. All projected text is now NUL-stripped and capped at
classification time, so the projection accepts anything Git can contain.
Lesson: a rebuildable projection is only rebuildable if *rebuild cannot
fail on data the source of truth permits*.

**5. Fail-open reads turn outages into corruption.** Projection reads
originally returned empty results on database errors. Consequence chain: a
DB blip → "HID not taken" → duplicate HID committed to Git — an *outage*
laundered into permanent damage to the source of truth. All reads now fail
closed (HTTP 503). Lesson: derived-state errors must be distinguishable
from derived-state absence, especially when writes consult reads.

**6. Distinguish "empty" from "broken".** `git rev-parse` failing was
treated as "unborn branch", so a transient git failure could rebuild the
projection as *empty* and serve it with confidence. Exit-code 1 (ref
missing) is now the only "empty"; everything else propagates. Same family
as lesson 5.

**7. Timestamps used for ordering need more resolution than your fastest
writer.** Comment ordering by RFC3339 seconds was nondeterministic the
moment two comments landed in one second; fixed-width nanosecond
timestamps made lexical order equal chronological order. Caught by the
first test that created two comments in a row.

**8. The typechecker and the fuzzer are cheap reviewers.** `tsc --noEmit`
caught a real type hole in the frontend fallback path; 1M+ fuzz executions
validated the JSON codec's canonical-form claim and the folder validator's
safety envelope. Neither found anything the hand-written tests had found —
and that is the point: they guard the space between the tests.

## 5. Remaining gaps (known, accepted, ordered by likely next need)

1. **Deleted-artifact tracking (§3.15 phase 4, §5.16).** Deleted GUIDs are
   recoverable from Git history but there is no projected index of them —
   "find deleted item and follow its references" requires a manual
   `git log`. Next real feature on the spec's list.
2. **Metadata relocation on move (§3.7).** Links/comments stay where they
   were created; GUID-based references keep everything correct (the spec
   classifies locality as a *preferred*, restorable invariant), but a
   maintenance operation to restore locality does not exist yet.
3. **HID history surface (§2.2.1).** Every HID change is a commit, so the
   history exists in Git, but there is no API to query "which artifact was
   REQ-42 in March" without walking history.
4. **Offset/keyset pagination.** Listings are limit-capped but not
   pageable; fine for MVP data volumes, wrong for 100k artifacts.
5. **Substring-search fallback is a table scan.** The GIN index serves
   word queries; the `LIKE '%…%'` fallback that gives in-memory/Postgres
   behavioral parity scans. Options when it hurts: `pg_trgm`, or dropping
   parity and documenting word-search-only semantics.
6. **Multi-writer file-level last-writer-wins.** Two servers editing the
   *same artifact* concurrently resolve at file granularity (with correct
   If-Match protection per client, and no lost commits — Git holds both).
   True merge semantics would require the spec's postponed
   branching/merging work.
7. **Restart-time reindex for the in-memory projection** is a full rebuild
   by design; a large repository on a slow disk will feel it. That is the
   moment to switch `-db` on, which is exactly why the seam exists.

## 6. One-line summary

The spec's architecture survived contact with adversarial testing
remarkably well — every deep fix made the implementation *more* like the
document, not less; the places we diverged are places where a concrete
failure mode (arbitrary folder names, shared-database races, hostile
bytes) demanded mechanics the spec left unspecified.
