# Running the tests

```text
tests/
|-- support/       shared contracts, clients, fake Core + FakeState, module loader, live gates
|-- unit/          validation and protocol behavior
|-- integration/   real loopback MCP and TCP behavior
`-- live/          opt-in tests against running mesoSPIM
```

## Which code is under test — read this first

`tests/support/patch_loader.py` can load the Remote Control modules from **two** places, and it is
not always obvious which one you got:

- **Patch mode** (the default) reads them out of `0001-*.patch`. This is what a reviewer with only
  the patch file gets.
- **Source-tree mode** reads them from a checkout, when `MESOSPIM_RC_SOURCE_ROOT` points at one.

```powershell
$env:MESOSPIM_RC_SOURCE_ROOT = 'C:\path\to\mesoSPIM-control'
python -m pytest tests -m offline
```

Every run prints which it used:

```text
remote-control modules under test: C:\path\to\mesoSPIM-control\mesoSPIM\src
```

**Check that line.** If you are changing the code and it says `patch`, you are testing the old
patch text and your changes are not being exercised at all — a green run means nothing. After
changing the source, regenerate the patch so **both** modes agree; a red patch-mode run means the
patch is stale.

## The state fakes are not dicts, on purpose

Production `mesoSPIM_StateSingleton` is a QObject with `__getitem__` (raising `KeyError`),
`__setitem__`, `__len__`, `set_parameters`, `get_parameter_dict`, `get_parameter_list` and
`block_signals`. It has **no `.get()`, no `__contains__`, no `__delitem__`**.

A plain-dict fake therefore green-lights code that dies on the instrument — that is not
hypothetical, it is how a bug that silently nulled the operator's acquisition list survived. So
every state-bearing fake uses `tests/support/fake_state.py::FakeState`, which offers exactly the
production surface and nothing else. **Do not add `.get()` or `.update()` to it** to make a test
pass; the test is telling you the truth.

## Commands

```powershell
python tests/run.py offline all
python tests/run.py live valid mcp
python tests/run.py live valid tcp
python tests/run.py live adversarial mcp
python tests/run.py live adversarial tcp
python tests/run.py live adversarial both
```

Offline tests exercise both transports automatically and need no hardware. Live tests retain the
environment, operator, `DemoStage` and state-restoration gates documented in [README.md](README.md).
Pass passwords through environment variables; never store them in files.

The offline suite cannot see threading or completion behaviour — those are hardware-sensitive and
only the live suites reach them. An offline-green result is necessary, not sufficient.
