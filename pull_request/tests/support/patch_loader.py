"""Load the Remote Control modules from the source tree, or from the patch."""
from __future__ import annotations

import sys
import tempfile
from pathlib import Path

from tests.support import SOURCE_ROOT

PULL_REQUEST_ROOT = Path(__file__).resolve().parents[2]
PATCH = next(PULL_REQUEST_ROOT.glob("0001-*.patch"))
_MODULE_DIRECTORY = tempfile.TemporaryDirectory(prefix="rc_under_test_")

MODULES = (
    "mesoSPIM_RemoteControl_ValidateAndRunCommands",
    "mesoSPIM_RemoteControl_Servers",
)

SOURCE = "patch"  #: where the modules under test came from; printed in the pytest header


def extract(path_suffix: str) -> str:
    """Return one patched file body from its new-file hunk."""
    lines = PATCH.read_text(encoding="utf-8").splitlines()
    start = next(
        i for i, line in enumerate(lines) if line.startswith(f"diff --git a/{path_suffix}")
    )
    hunk = next(
        i for i, line in enumerate(lines[start:], start) if line.startswith("@@ ")
    ) + 1
    body = []
    for line in lines[hunk:]:
        if line.startswith("diff --git") or line.startswith("-- "):
            break
        if line.startswith("+") and not line.startswith("++"):
            body.append(line[1:])
        elif line.startswith(" "):
            body.append(line[1:])
    return "\n".join(body)


def _sources() -> dict:
    """Return {module name: source text}, from the tree when a source root is configured.

    Without one the patch file is the source, so an unconfigured run still works -- but
    then a refactor of the tree is validated against stale patch text, which is why SOURCE
    goes in the pytest header. tests.support puts the root on sys.path; nothing here may
    depend on the caller's working directory.
    """
    global SOURCE
    if SOURCE_ROOT is None:
        SOURCE = "patch"
        return {name: extract(f"mesoSPIM/src/{name}.py") for name in MODULES}
    src = SOURCE_ROOT / "mesoSPIM" / "src"
    SOURCE = str(src)
    return {name: (src / f"{name}.py").read_text(encoding="utf-8") for name in MODULES}


def load_modules():
    """Materialize and import the two modules once for the test session.

    They are imported flat, with no package, so the relative import inside the servers
    module is rewritten whichever source it came from. The temp directory goes on the
    front of sys.path so its copies win over anything the source root also exposes.
    """
    sources = _sources()
    servers = sources["mesoSPIM_RemoteControl_Servers"]
    for old_import in (
        "from .mesoSPIM_RemoteControl_ValidateAndRunCommands import",
        "from mesoSPIM.src.mesoSPIM_RemoteControl_ValidateAndRunCommands import",
    ):
        servers = servers.replace(
            old_import, "from mesoSPIM_RemoteControl_ValidateAndRunCommands import"
        )
    sources["mesoSPIM_RemoteControl_Servers"] = servers
    module_dir = Path(_MODULE_DIRECTORY.name)
    for name, text in sources.items():
        (module_dir / f"{name}.py").write_text(text, encoding="utf-8")
    sys.path.insert(0, str(module_dir))
    import mesoSPIM_RemoteControl_ValidateAndRunCommands as validator_module
    import mesoSPIM_RemoteControl_Servers as servers_module
    return validator_module, servers_module


vrc, srv = load_modules()
