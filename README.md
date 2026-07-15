# mesoSPIM Remote Control

A remote-control contribution to [mesoSPIM-control](https://github.com/mesoSPIM/mesoSPIM-control):
an optional, off-by-default tab that exposes 55 allowlisted commands over framed TCP and over MCP,
both funnelled through one validated dispatch path.

This repository holds two different things, and keeps them apart on purpose.

## `pull_request/` — the contribution

Everything that is offered to mesoSPIM upstream:

| | |
|---|---|
| `0001-Add-optional-Remote-Control-tab-*.patch` | The change itself: **seven files**, cut against `release/candidate-py312` at `b3c9638`. |
| `README.md` | The PR description: what it does, the wire protocol, security posture, bench verification. |
| `ARCHITECTURE.md` | How it fits together: the single choke point, where the session state lives, the threading model, and the known limitations. |
| `REMOTE_CONTROL_REFERENCE.md` | The API: how to connect, the call format, every command's arguments, and the completion and safety rules. Since MCP's `inputSchema` is only `{"type": "object"}`, this manual *is* the contract an integrator works from. |
| `TESTING.md` | How to run the suites — **read the part about which code is under test**. |
| `tests/` | Unit, integration and opt-in live suites, plus the fakes. |

Run the offline suite from inside `pull_request/`:

```powershell
python tests/run.py offline all
```

## Root — how it was built

`REFACTOR_PLAN_REVIEWED.md` (policy and scope), `IMPLEMENTATION.md` (the code-level guide),
`REVIEW_PROMPT.md` and `PLAN.md`. These are the working record of the structural cleanup: what was
deliberately **not** done and why, the two latent bugs it uncovered, and the reasoning behind the
decisions a reviewer is most likely to question. They are not part of the contribution.

## Licence

GPL-3.0, inherited from mesoSPIM-control, which the patch derives from.
