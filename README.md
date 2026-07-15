# mesoSPIM Remote Control

This repository contains an optional Remote Control tab for
[mesoSPIM-control](https://github.com/mesoSPIM/mesoSPIM-control). It is off by default and must be
started by an operator. It offers the same 53 named commands through framed TCP or a small
MCP-compatible HTTP endpoint.

Only one transport can run at a time. Every ordinary mutation goes through the same validation,
hardware limits, one-operation-at-a-time gate, accepted-operation reply, and completion polling.

## Repository layout

### `pull_request/`

This is the contribution intended for upstream mesoSPIM:

| Path | Purpose |
| --- | --- |
| `0001-Add-mesoSPIM-Remote-Control.patch` | The seven-file upstream patch, based on `release/candidate-py312` at `b3c9638` |
| `README.md` | Feature overview, security, and current verification status |
| `ARCHITECTURE.md` | Components, threads, operation lifecycle, and known limits |
| `REMOTE_CONTROL_REFERENCE.md` | Connection details, call format, commands, errors, and polling rules |
| `TESTING.md` | Offline, real-PyQt, DemoStage, and adversarial test instructions |
| `tests/` | Automated and operator-gated tests |

Run the hardware-free suite from the repository root:

```powershell
python pull_request/tests/run.py offline all
```

### `impl/`

This is the readable source used to generate the five new Remote Control modules in the patch. It
also contains the main unit-test suite. See [`impl/README.md`](impl/README.md).

## Current status

[`REVIEW_REPORT.md`](REVIEW_REPORT.md) is the current handoff. The unified asynchronous mutation
model passes the implementation, generated-patch, formatting, real-PyQt, TCP DemoStage, and MCP
DemoStage gates. Only the final normal File > Exit process/port/worker check remains before the
upstream pull request is ready.

## Licence

GPL-3.0, inherited from mesoSPIM-control.
