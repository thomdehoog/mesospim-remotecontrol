# Review prompt: mesoSPIM Remote Control cleanup plan

Act as a skeptical senior maintainer reviewing a structural cleanup plan for a hardware-control
application. Read the plan and real patched code before reaching conclusions.

Canonical plan:

```text
Z:\zmbstaff\10374\Protocols_Notes\thom\notes\repositories\mesospim-remotecontrol\REFACTOR_PLAN_REVIEWED.md
```

Executable implementation guide:

```text
Z:\zmbstaff\10374\Protocols_Notes\thom\notes\repositories\mesospim-remotecontrol\IMPLEMENTATION.md
```

Treat the policy plan as authoritative for scope and the implementation guide as the proposed concrete
execution. If they conflict, report the conflict; do not silently choose one.

Repositories:

```text
Z:\zmbstaff\10374\Protocols_Notes\thom\notes\repositories\mesoSPIM-control\
Z:\zmbstaff\10374\Protocols_Notes\thom\notes\repositories\mesospim-remotecontrol\
```

Prepare the exact review tree if it does not already exist:

```powershell
cd Z:\zmbstaff\10374\Protocols_Notes\thom\notes\repositories\mesoSPIM-control
& 'C:\ProgramData\MinicondaZMB\envs\builder\Library\bin\git.exe' fetch origin
& 'C:\ProgramData\MinicondaZMB\envs\builder\Library\bin\git.exe' worktree add ..\rc-review b3c9638
cd ..\rc-review
& 'C:\ProgramData\MinicondaZMB\envs\builder\Library\bin\git.exe' apply '..\mesospim-remotecontrol\0001-Add-optional-Remote-Control-tab-TCP-MCP-named-call-s.patch'
```

Verify `HEAD` is `b3c9638` and the original patch changes six files. The planned refactor adds a tab
module, making seven upstream patch files.

## Review boundary

The requested outcome is clean, professional, readable, maintainable code without bloat,
overengineering, or speculative redesign. This is not a feature project.

The plan authorises exactly three behaviour changes:

1. remove the dead `procedure` command;
2. stop fabricating a 2048 camera resolution when dimensions are missing;
3. prevent standalone `acquire_finish` from nulling the operator's acquisition list.

Everything else must preserve behaviour, GUI, protocol shape, process launch, completion semantics,
and threading.

Do not recommend operation-ID hardening, implementing/removing `get_progress` fields, `-m` MCP
launch, removing `SimCore`/`self_test`, turning the server into a QObject, or unrelated API redesign
as part of this cleanup.

## Questions to answer

1. Does any plan step change behaviour beyond the three approved fixes?
2. Does Phase 0 faithfully model `mesoSPIM_StateSingleton` across every state-bearing fake, or can
   dict-only tests still hide production failures?
3. Does extracting `RemoteControlTab` preserve construction order, queued connections, modal parent,
   tab placement, enable rules, failure feedback, and MCP process teardown?
4. Is Core ownership explicit and correct for both `_remote_session` and
   `_remote_control_server`?
5. Do the proposed session changes preserve Stop/Start survival and direct dispatch without adding
   new completion semantics?
6. Do command collapses preserve defaults, incoming command names, validation, accepted-command
   reporting, error behavior, and hardware-specific paths?
7. Does snapshot cleanup retain dtype, object-dtype, bytes, non-empty, shape, hashing, and chunk-size
   checks?
8. Does server cleanup preserve blocking versus incremental buffering, authentication, signal
   connection ownership, client teardown, path-based MCP launch, and thread behavior?
9. Are all `procedure` references replaced with absence assertions without deleting unrelated test
   coverage?
10. Is the camera-dimension failure contract precise and consistently tested in both callers?
11. Are the four unavailable `get_progress` fields kept as stable `null` fields and documented?
12. Are there duplicated, contradictory, stale, or encoding-corrupted instructions remaining?
13. Is any abstraction being introduced primarily to look cleaner while producing equal or greater
   code and indirection?
14. Would a mesoSPIM maintainer be able to understand and safely own the result?

Also verify these concrete implementation points introduced after the first review:

15. Both Git paths are checked on disk; use the one that actually exists rather than trusting prose.
16. `patch_loader` adds the source tree's `mesoSPIM/src` directory before flat acquisition imports and
    every suite proves whether it loaded the patch or worktree.
17. New tests import `tests.support`, not an assumed top-level `support` package.
18. `FakeState.get_parameter_dict` and `get_parameter_list` contain executable semantics that raise
    `KeyError` like production, without copying Qt mutex machinery.
19. `UnitCore` supplies every method evaluated by the acquisition regression tests, including `start`.
20. Every legacy `_mesospim_remote_*` fixture/reset/cleanup reference is migrated to `_remote_session`.
21. `_acquire_start` rolls back both `acq_list` and saved session state if scheduling itself raises.
22. The grouped-setter proposal contains explicit named wiring and is retained only if the final diff
    is genuinely shorter and clearer.
23. All three `VALID_CASES == 56` assertions and every semantic `procedure`/command-count reference are
    updated, while unrelated corpus counts are left alone.
24. A normal configured 5056 x 2960 camera is not incorrectly described as taking the fallback path.
25. Markdown files contain no mojibake or contradictory correction paragraphs.
26. `FakeState` gives each instance independent nested defaults; mutating `position` in one test
    cannot contaminate another test.
27. MainWindow deletion is by explicit remote-method name and cannot consume `choose_snap_folder` or
    any following unrelated method.
28. The plan does not add unused runtime constants or new validation merely to document completion
    milestones.

## Required output

Provide prioritized findings. Mark each finding:

- **CONFIRMED** when verified in code, with clickable `file:line` citations;
- **SPECULATIVE** when it depends on an untested runtime assumption.

For every problem, state the concrete failure scenario and the smallest correction. Separate existing
bugs from regressions the plan would introduce. End with one verdict:

- `execute as written`;
- `execute with these changes`;
- `rethink`.

Do not edit code or the plan during review.
