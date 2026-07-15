# mesoSPIM Remote Control cleanup refactor plan

## Objective

Make the existing, live-validated Remote Control contribution clean, organised, readable,
maintainable, and professional.

This is a cleanup, not an improvement project. Every structural change must be justifiable as
"the same behaviour, expressed better." New hardening, new protocol behaviour, and new features do
not belong in this change.

Target base: `mesoSPIM-control` commit `b3c9638` on `release/candidate-py312`.

This document is the single source of truth. `PLAN.md` points here.

## Scope boundary

### In scope

- Move the 201 lines of Remote Control GUI code out of `mesoSPIM_MainWindow.py` into a dedicated tab
  module.
- Replace four undeclared command/session attributes on Core with one eagerly initialized session
  container.
- Declare the existing `_remote_control_server` Core attribute instead of creating it dynamically.
- Collapse genuine duplication where the result is shorter and at least as readable.
- Delete dead or redundant code.
- Correct misleading comments.
- Keep Core's Remote Control integration small and explicit.

### Three approved behaviour changes

No other behaviour change is authorised.

1. Remove `procedure`. It is advertised but its handler can only raise.
2. Stop `_camera_pixels` from fabricating a 2048-pixel resolution when camera dimensions are missing.
3. Make standalone `acquire_finish` a no-op instead of replacing the operator's acquisition list
   with `None`.

### Explicitly out of scope

| Proposal | Reason |
|---|---|
| Operation-ID matching for completion callbacks | New hardening and changed completion semantics; handle separately if required. |
| Implementing the four unavailable `get_progress` values | Expands functionality. Keep the existing response shape and document the limitation. |
| Removing the four unavailable `get_progress` fields | Changes the machine-facing response schema. Keep them as `null`. |
| Launching the MCP child with `-m` | Changes proven process-launch behaviour for a cosmetic reduction. Keep path launch and import shims. |
| Removing `SimCore` or `self_test` | Changes the fail-closed startup gate. Keep it and correct only overclaiming comments. |
| Changing the server into a QObject | Changes camera/Core thread behaviour. Preserve the live-validated plain-object server. |
| Replacing `_jsonable` with `json.dumps(default=str)` | Silently changes unexpected protocol values into strings and does not handle invalid dictionary keys. |

If a proposed change cannot be described as "same behaviour, fewer lines" or "same behaviour,
obvious ownership," it is out of scope unless it is one of the three approved fixes.

## Code style — how the result must read

The goal is code a maintainer can **extend**, not code that is impressively short. Terse is not the
same as clean.

- **Docstrings carry the "why".** Every module, class and non-obvious function gets one. Explain the
  reason, the contract, and the trap — not a restatement of the code. Hardware lessons and threading
  constraints belong here.
- **Do not stack comment lines above statements.** If a single line needs a note, put a short trailing
  comment on that line. If it needs a paragraph, it belongs in the enclosing docstring. Blocks of `#`
  commentary sitting above code are out.
- **No filler whitespace.** No blank lines padding the inside of a function, none around comments, none
  for decoration. Keep the blank lines PEP 8 and the linter require between definitions, and no more.
- **Explicit over clever.** Named helpers, not lambdas, closures, factories or dense one-liners that
  hide behaviour. A reader must be able to see what a command does without unwrapping an abstraction.
- **Extension must be obvious.** Adding a command is: write `_handler(core, args)`, add one `COMMANDS`
  entry, add one `_HINTS` entry, add one `VALID_CASES` entry in `tests/support/contracts.py`. State
  that sequence in the command module's docstring so the next person does not have to reverse-engineer
  it. Nothing in this refactor may make that sequence longer or less obvious.
- **No bloat.** The above is not licence to pad. Delete dead code, collapse true duplication, and then
  document what remains properly.

## Hard constraints

- Preserve the GUI: tab position, widgets, labels, fonts, margins, spacing, defaults, port switching,
  enable rules, status text, modal parent, and off-by-default behaviour.
- Among existing upstream modules, edit only `mesoSPIM_Core.py` and `mesoSPIM_MainWindow.py`.
- New production code remains in flat modules under `mesoSPIM/src`; create no package.
- Preserve `handler(core, args)` and `handle_tcp_message(core, payload)`.
- Preserve strict parsing, loaded-config validation, snapshot chunking, the emergency-stop path, and
  the one-operation busy gate.
- Preserve hardware-sensitive threading. Construct the TCP server on Core's thread through the
  queued start slot. Keep `RemoteControlTCPServer` a plain Python object owning its signal
  connections.
- Preserve working error contracts where practical; do not remove explicit validation merely
  because a broad exception handler exists.
- Prefer readable named helpers to dense tables, lambdas, or factories that hide behaviour.

## Tooling and reconstruction

Verified tools (checked on this machine; the root-level
`...\MinicondaZMB\Library\bin\git.exe` does not exist):

```text
git:  C:\ProgramData\MinicondaZMB\envs\builder\Library\bin\git.exe
env:  mesospim-control          (the only conda env with BOTH pytest and PyQt5)
```

The review tree already exists at `Z:\...\repositories\rc-review\` (base `b3c9638`, patch applied).
To rebuild it from scratch:

```powershell
cd Z:\zmbstaff\10374\Protocols_Notes\thom\notes\repositories\mesoSPIM-control
& 'C:\ProgramData\MinicondaZMB\envs\builder\Library\bin\git.exe' fetch origin
& 'C:\ProgramData\MinicondaZMB\envs\builder\Library\bin\git.exe' worktree add ..\rc-review b3c9638
cd ..\rc-review
& 'C:\ProgramData\MinicondaZMB\envs\builder\Library\bin\git.exe' apply '..\mesospim-remotecontrol\0001-Add-optional-Remote-Control-tab-TCP-MCP-named-call-s.patch'
```

Before editing, verify that `HEAD` starts with `b3c9638` and `git status --short` lists the current six
patch files. Adding the tab module makes the final upstream patch contain seven files.

## Phase 0: make the test state faithful

Complete this gate before structural refactoring.

Production `mesoSPIM_StateSingleton` supports `__getitem__`, `__setitem__`, `__len__`, and
`get_parameter_dict`. It has no `.get()`, `__contains__`, or `__delitem__`. Existing offline fakes use
plain dictionaries or ad hoc state objects, so they can hide production-only mistakes.

### Phase 0 steps

1. Update `tests/support/patch_loader.py` so every offline test can load modules from
   `MESOSPIM_RC_SOURCE_ROOT` when it is set. Today only `test_transport_security.py` has a source-tree
   escape hatch; the unit, adversarial, and viability tests otherwise keep loading the old code from
   the patch file. Before trusting red/green results, prove which module paths each suite is executing.
   Preserve patch-file loading as the default when the environment variable is absent.
2. Add a reusable `FakeState` under `tests/support` with the production state access contract:
   - `__getitem__` raises `KeyError` for missing keys;
   - `__setitem__` and `__len__` work;
   - `set_parameters` and `get_parameter_dict` match production behaviour needed by tests;
   - `_state_dict` exists because `get_state_all` uses it to enumerate all keys;
   - no `.get()`, `__contains__`, or `__delitem__`;
   - seed real defaults needed by Remote Control, including `state`, `position`, `selected_row`, and
     `acq_list`.
3. Migrate every fake that represents production Core state to `FakeState`, including the unit,
   adversarial, transport-security, viability, and snapshot fakes. Retain a plain dictionary only
   where a test explicitly verifies generic-dict compatibility.
4. Make shared config fakes faithful enough for every command exercised by the suite. In particular,
   add valid `camera_parameters` with `x_pixels` and `y_pixels`, then use dedicated malformed config
   fakes only in camera-error tests. Do not let the approved camera fix make unrelated tests fail for
   an accidental fake-config omission.
5. Add explicit regression tests for:
   - standalone `acquire_finish` preserving the exact `acq_list` object;
   - `acquire_start` followed by `acquire_finish` restoring the exact prior object;
   - read commands working without `.get()`;
   - missing optional progress keys returning `None` through the production shim.
6. Run the new regression tests against the old production implementation and verify that the
   standalone `acquire_finish` test fails.
7. Apply the approved acquisition-list and camera-dimension fixes, then make the entire suite green.
8. Commit Phase 0 as a green commit. Red-green is a local verification procedure; do not preserve a
   knowingly failing commit.

Only after Phase 0 is green should structural work begin.

## Approved bug fixes

### Standalone `acquire_finish`

`_mesospim_prev_acq_list` is created only by `_acquire_start`. Because `acquire_finish` is independently
allowlisted and has no ordering check, a client can call it without `acquire_start`. The current code
then tries to delete `state["acq_list"]`; production state has no `__delitem__`, so the broad exception
handler assigns `None` to the list.

Required behaviour:

- if no previous list was saved, return successfully without changing `acq_list`;
- if a previous list was saved, restore that exact object and clear the saved value;
- remove the `del`, broad exception handler, and redundant `had` flag.

### Camera dimensions

Normal configurations, including 5056 x 2960 cameras, already supply
`camera_parameters["x_pixels"]` and `camera_parameters["y_pixels"]`. The defect is the fallback used
when those keys or `camera_parameters` are missing; it fabricates 2048 using nonexistent config
attributes.

Required behaviour:

- return the two configured integer dimensions;
- if either dimension is unavailable or invalid, raise a clear `ValueError` naming the missing or
  invalid configuration key;
- test both `get_config` and `acquire_start` callers;
- in `_acquire_start`, resolve and validate camera dimensions and all other fallible response metadata
  before saving/replacing `acq_list` or calling `_defer(core.start, row=0)`;
- never return an error after acquisition state has been changed or hardware work has been queued;
- do not introduce another guessed default.

### Remove `procedure`

Remove the handler, `COMMANDS` entry, hint, contract entry, live/integration special cases, and
documentation that advertises it. Replace procedure-specific tests with assertions that `procedure`
is absent from `COMMANDS`, capabilities, contracts, and MCP `tools/list`. Delete no test files and no
unrelated coverage.

## Phase 1: extract `RemoteControlTab`

Create `mesoSPIM_RemoteControl_Tab.py` containing `RemoteControlTab(QtWidgets.QWidget)`.

Move the GUI construction and behaviour essentially verbatim. The tab owns:

- its widgets;
- mode, host, port, token, pending-start, and running state;
- the MCP `QProcess`;
- start and stop signals sent to Core;
- start/stop/result handling and refresh logic.

MainWindow retains only the import, eager construction, a handle, and teardown.

Mandatory details:

- Construct the tab where `setup_remote_control_tab()` is currently called during
  `initialize_and_connect_widgets()`.
- Do not later assign `self.remote_control = None`; initialization at the current line 168 precedes
  the optional-window attributes around line 195.
- Delete the old MainWindow Remote Control signals and connections, or Core will receive duplicate
  requests.
- Keep tab-to-Core start and stop connections explicitly queued.
- Keep `QMessageBox` parented to MainWindow so modal placement does not change.
- Parent the MCP `QProcess` to the tab.
- Give the tab explicit teardown that does **all three** things `close_app()` does today
  (`mesoSPIM_MainWindow.py:285-288`), not just the first: (1) terminate/kill the MCP child with the
  existing timeouts, (2) **emit the queued stop to Core**, (3) clear the running flag. Invoke that
  teardown from `MainWindow.close_app()` before MainWindow closes. Moving only the kill silently drops
  the Core stop.
- Three MainWindow handles must travel with the tab: `package_directory` (`:95`, used when spawning
  the MCP child), `TabWidget` + `TimelapseTabWidget` (tab placement), and `import secrets` (used to
  mint the internal MCP token).
- Preserve failed-bind feedback and the distinction between TCP and the internal TCP server used by
  MCP.
- Drop `_remote_control_refresh`, redundant initialization guards, and the dead
  `_remote_mode = "Off"` assignment only after the new eager construction makes their ordering
  assumptions impossible.

Moving the GUI does not move TCP-server signal connections.

## Phase 2: declare Core-owned state

Declare both existing pieces of Core-owned Remote Control state in `mesoSPIM_Core.__init__`:

```python
self._remote_session = {
    "operation": None,
    "operation_counter": 0,
    "snapshot": None,
}
self._remote_control_server = None
```

`_remote_session` replaces:

- `_mesospim_remote_operation`;
- `_mesospim_remote_operation_counter`;
- `_mesospim_remote_snapshot`;
- `_mesospim_prev_acq_list`.

The temporary previous acquisition list may be added to the session only while `acquire_start` owns
one, then removed with `pop`. Use an explicit module sentinel to distinguish "nothing was saved" from
a saved value; do not reintroduce the old `(had, value)` tuple or broad deletion fallback.

Initialize the real Core's complete session container eagerly in `Core.__init__`; do not initialize it
to `None`. The container must already exist before either Core-thread dispatch or the direct camera-thread
snapshot callback can access it. Reads must never create or replace session state.

Direct-dispatch fake cores do not construct a TCP server, so give them an explicit session during test
setup. A compatibility helper may accept an absent session only at the outer dispatch boundary, where
it can initialize once before command execution. Do not let arbitrary read helpers lazily create it.

Keep the session owned by Core so server Stop/Start does not erase the busy gate, snapshot, counter, or
saved acquisition list.

Prefer a plain state container plus the existing free functions. Introduce a class only if the final
code is materially clearer, not merely object-oriented. Do not add operation-ID matching.

Write the existing completion milestones together in one comment or constant definition:

- `finished`;
- `snap_image`;
- `time_lapse`;
- `preview_returned_idle`;
- `None` for synchronous completion.

## Phase 3: keep Core integration explicit

Keep the two decorated server lifecycle slots in Core. Moving their bodies to module-level helpers
would add an indirection hop for approximately zero net reduction. Refactor their existing bodies only
as needed to declare state and remove redundant guards.

Preserve the complete existing lifecycle contract:

- stop the old server before restart;
- construct the server on Core's thread;
- retain the server on `core._remote_control_server`;
- run the existing fail-closed smoke check before binding;
- catch and log startup failures;
- emit `sig_remote_control_started(False, message)` on failure;
- report the actual dynamically allocated port;
- emit `sig_remote_control_started(True, "host:port")` on success;
- keep existing operational logging;
- close clients, disconnect completion signals, close the listener, and clear the server reference on
  stop.

Do not chase one-line slots as a line-count target.

Core must not own command dispatch, GUI logic, or the MCP child process.

## Phase 4: simplify the command module conservatively

### Safe duplication removal

- Use one operation-public-snapshot builder instead of the duplicate comprehensions.
- Share the acquisition-list resolver used by disk-space and motion-limit checks.
- Make `_acquire_start` use `_make_acquisition_list` rather than rebuilding one acquisition manually,
  while preserving the exact saved-list and scheduled-start behaviour.
- Collapse the four grouped state setters only if a named helper or small factory is shorter and keeps
  each command's allowed keys obvious.
- Collapse the three ETL wrappers only if defaults, state fallbacks, readback, and error messages remain
  explicit.
- Keep the three thin mode wrappers. They are short, explicit, and preserve incoming command identity;
  replacing them with factories, closures, or partials would be churn rather than cleanup.

Do not collapse the three stage presets. Load/unload and center have different required-key behaviour.

### Snapshot cleanup

- Inline `_snapshot_metadata` into its single caller or otherwise remove its repeated guard.
- **Keep the explicit dtype/shape/`tobytes` guard.** Replacing it with direct attribute access swaps a
  named `TypeError("snapshot image must expose dtype, shape, and tobytes()")` for a bare
  `AttributeError` in `operation["error"]` - which contradicts the "stable, useful protocol error
  messages" requirement below. Four lines is the right price. Inlining `_snapshot_metadata` is the only
  change here.
- Preserve checks for object dtype, byte output, non-empty data, shape conversion, hashing, and bounded
  chunking.
- Preserve stable, useful protocol error messages.

### Keep deliberately

- Keep production-compatible state access. `mesoSPIM_StateSingleton` has no `.get()`; missing keys must
  return defaults through the existing shim. The two helpers may become one short helper if all callers
  remain clear.
- Keep `_HINTS`. Add a test ensuring every hint names an existing command and correct the inaccurate
  `get_info` hint, which promises snapshot metadata that `_get_info` does not return. Important
  description strings should be asserted in tests.
- Keep `_LIMITS` at import time.
- Keep `SimCore` and `self_test`; replace claims that they "prove" safety with accurate "smoke-check"
  wording.
- Keep strict JSON parsing, snapshot chunking, emergency commands, `_completion_kind` signal guards,
  `_stop_activity`'s idle check, and `_move_relative`'s serial-worker path.

### `_jsonable`

Keep `_jsonable`. Its current contract is deliberately lenient: it recursively normalizes containers,
coerces dictionary keys to strings, and stringifies unknown values. Removing it would change live
protocol behaviour for unmodelled config or state values. `json.dumps(default=str)` is not equivalent
because `default` is not applied to dictionary keys.

### `get_progress`

Keep the current response schema. The following fields remain `null` because the production state does
not currently provide their backing keys:

- `current_plane`;
- `total_planes`;
- `current_acquisition`;
- `total_acquisitions`.

Document them as reserved/currently unavailable and add a regression test asserting the stable null
shape. Do not implement or remove them in this refactor.

## Phase 5: simplify the server module conservatively

- Store completion signal/slot pairs in one `self._connections` list and use it for connection and
  disconnection so the lists cannot drift.
- Delete the trivial `_close` wrapper and call `_drop_client` directly.
- Extract one shared frame-header validator for the canonical numeric header and size limit.
- Keep `read_frame` and `FrameDecoder.frames` as separate blocking and incremental buffering
  implementations.
- Keep `AuthGate` as named per-client state; the offline harness uses it directly.
- Keep path-based MCP child launch, the `sys.path` setup, and both import modes.
- Keep the tab out of the server module's import graph so the headless MCP child does not import
  `QtWidgets`.

Document the existing thread compromise accurately: the plain-object server's camera callback captures
snapshot data from the camera thread while TCP dispatch changes session state from Core's thread. This
is current live-validated behaviour. Preserve it rather than attempting an unvalidated threading fix.

Also document the existing recovery limitation: if a remote snap never produces its camera-frame
completion signal, the operation can remain busy until mesoSPIM restarts. Server Stop/Start deliberately
preserves the Core-owned session and is not a recovery path. Operation-ID matching would not solve a
missing completion signal; recovery would require new cancellation or timeout behaviour and is outside
this cleanup.

## Test requirements

Update the in-patch validation test and standalone suite. Delete no test files or unrelated coverage.

Required coverage includes:

- the shared `FakeState` contract and all migrated state-bearing fakes;
- source-root loading in every offline suite, with an assertion or diagnostic proving tests execute the
  refactored worktree rather than stale patch contents;
- shared valid config fakes plus dedicated missing/invalid camera-dimension configs;
- standalone and ordered `acquire_finish` cases;
- camera dimensions from valid config and clear errors for each missing/invalid dimension;
- session survival across TCP server Stop/Start;
- direct `handle_tcp_message(core, payload)` dispatch without a TCP server;
- unchanged incoming-name behavior such as `accepted_command == "start_live"`;
- exactly 55 consistent commands across `COMMANDS`, capabilities, contracts, and MCP `tools/list`.
  **`_HINTS` is deliberately a subset** (21 entries today, 20 after `procedure`) - assert only that
  every hint names an existing command, never that there are 55 hints. Update the two
  `assert len(VALID_CASES) == 56` checks (`tests/support/contracts.py:138`,
  `tests/integration/test_valid_transports.py:38`) to `== 55`;
- stable null `get_progress` fields;
- unchanged `_jsonable` normalization, including non-string dictionary keys and unknown values;
- frame header length, canonical form, payload limit, partial frames, and multiple buffered frames;
- authentication including non-ASCII tokens;
- failed TCP bind and failed MCP-child start feedback;
- GUI close terminating the MCP child without an orphan process.

## Verification sequence

Run offline commands in the `mesospim-control` conda environment.

1. Complete Phase 0 locally: new regression tests fail against the old implementation, approved fixes
   make them pass, and commit only green code.
2. Syntax-check every changed Python file.
3. Run the standalone offline unit and integration suite.
4. Run the in-tree validation test.
5. Launch demo mode and test TCP Start/Stop, MCP Start/Stop, failed MCP-child startup, and failed TCP
   bind. A failure must warn and must not show "running."
6. Run `self_test` over TCP and MCP.
7. Run the live valid and adversarial suites against demo mode.
8. Compare tab placement, widgets, text, fonts, defaults, port switching, status text, and enable
   behaviour with the original patch.
9. Exit with MCP running and confirm no child process remains.
10. On the instrument, validate snapshot, movement, acquisition, stop, and time-lapse completion.
11. Regenerate the patch and verify it contains exactly these seven upstream files:
    - `mesoSPIM/src/mesoSPIM_Core.py`;
    - `mesoSPIM/src/mesoSPIM_MainWindow.py`;
    - `mesoSPIM/src/mesoSPIM_RemoteControl_Tab.py`;
    - `mesoSPIM/src/mesoSPIM_RemoteControl_ValidateAndRunCommands.py`;
    - `mesoSPIM/src/mesoSPIM_RemoteControl_Servers.py`;
    - `mesoSPIM/src/test_remote_control_validation.py`;
    - `pyproject.toml`.
12. Confirm there are no unrelated changes.

## Size and quality bar

Report production and shipped-test lines separately. Production currently adds about 1,960 lines. The
safe reductions are expected to produce roughly 1,680-1,750 production lines; the in-patch validation
test is additional.

Do not chase a line target. Readable validation, accurate hardware comments, explicit ownership,
stable protocol behaviour, and fail-closed startup matter more than a smaller diff.

## Implementation verdict

Proceed after Phase 0 is green. The authorised result is a structural cleanup plus exactly three
approved fixes. Any additional hardening, protocol change, process-launch change, progress
implementation, or threading redesign requires a separate plan and validation cycle.
