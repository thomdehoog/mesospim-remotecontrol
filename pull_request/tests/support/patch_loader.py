"""Load the Remote Control modules from the source tree, or from the patch.

The testable modules are Config (constants), Dispatcher (validate-and-run), Commands (the 53
blocks), and Servers (transports). They are loaded flat -- no package -- so every inter-module
relative import is rewritten to a flat import first. Importing Servers pulls in PyQt5, so the fake
Qt shim in ``tests.support`` must already be installed (it is, at package import)."""
from __future__ import annotations

import sys
import tempfile
from pathlib import Path

from tests.support import SOURCE_ROOT

PULL_REQUEST_ROOT = Path(__file__).resolve().parents[2]
PATCH = next(PULL_REQUEST_ROOT.glob("0001-*.patch"))
_MODULE_DIRECTORY = tempfile.TemporaryDirectory(prefix="rc_under_test_")

# Dependency order: Config and Dispatcher import nothing inter-module; Commands imports both; Servers
# imports all three. All four are written to the temp dir before any is imported.
MODULES = (
    "mesoSPIM_RemoteControl_Config",
    "mesoSPIM_RemoteControl_Dispatcher",
    "mesoSPIM_RemoteControl_Commands",
    "mesoSPIM_RemoteControl_Servers",
)

# Every inter-module import shape -> its flat equivalent. Covers `from .MOD import ...`,
# `from . import MOD [as ...]`, the packaged `from mesoSPIM.src.MOD import ...` (source-tree mode),
# and the `.utils.acquisitions` helper the acquisitions shim publishes flat.
_REWRITES = {
    "from .mesoSPIM_RemoteControl_Config import": "from mesoSPIM_RemoteControl_Config import",
    "from .mesoSPIM_RemoteControl_Dispatcher import": "from mesoSPIM_RemoteControl_Dispatcher import",
    "from .mesoSPIM_RemoteControl_Commands import": "from mesoSPIM_RemoteControl_Commands import",
    "from . import mesoSPIM_RemoteControl_Config": "import mesoSPIM_RemoteControl_Config",
    "from . import mesoSPIM_RemoteControl_Dispatcher": "import mesoSPIM_RemoteControl_Dispatcher",
    "from . import mesoSPIM_RemoteControl_Commands": "import mesoSPIM_RemoteControl_Commands",
    "from .utils.acquisitions import": "from utils.acquisitions import",
    "from mesoSPIM.src.mesoSPIM_RemoteControl_Config import": "from mesoSPIM_RemoteControl_Config import",
    "from mesoSPIM.src.mesoSPIM_RemoteControl_Dispatcher import": "from mesoSPIM_RemoteControl_Dispatcher import",
    "from mesoSPIM.src.mesoSPIM_RemoteControl_Commands import": "from mesoSPIM_RemoteControl_Commands import",
}

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
    """Return {module name: source text}, from the tree when a source root is configured, else from
    the patch. SOURCE goes in the pytest header so a green run against stale patch text is visible."""
    global SOURCE
    if SOURCE_ROOT is None:
        SOURCE = "patch"
        return {name: extract(f"mesoSPIM/src/{name}.py") for name in MODULES}
    src = SOURCE_ROOT / "mesoSPIM" / "src"
    SOURCE = str(src)
    return {name: (src / f"{name}.py").read_text(encoding="utf-8") for name in MODULES}


def _flatten_imports(text: str) -> str:
    for old, new in _REWRITES.items():
        text = text.replace(old, new)
    return text


def load_modules():
    """Materialize and import the four modules once for the test session, flat (no package), so the
    inter-module relative imports are rewritten whichever source they came from."""
    sources = {name: _flatten_imports(text) for name, text in _sources().items()}
    module_dir = Path(_MODULE_DIRECTORY.name)
    for name, text in sources.items():
        (module_dir / f"{name}.py").write_text(text, encoding="utf-8")
    sys.path.insert(0, str(module_dir))
    import mesoSPIM_RemoteControl_Config as config_module
    import mesoSPIM_RemoteControl_Dispatcher as dispatcher_module
    import mesoSPIM_RemoteControl_Commands as commands_module
    import mesoSPIM_RemoteControl_Servers as servers_module
    return config_module, dispatcher_module, commands_module, servers_module


config, dispatcher, commands, srv = load_modules()
