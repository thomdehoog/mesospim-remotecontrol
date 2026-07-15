"""Shared test infrastructure.

Resolves ``MESOSPIM_RC_SOURCE_ROOT`` once, in the one place every support module can
reach: ``patch_loader`` builds the modules under test from it, ``acquisitions`` imports
production's real acquisition classes from it, and ``test_transport_security`` imports
the packaged servers from it. Resolving it in the package body means the root is on
sys.path before any support module runs, whichever module a test file imports first.

Unset, everything falls back to the patch file, so an unconfigured run still works.
"""
from __future__ import annotations

import os
import sys
from pathlib import Path

_CONFIGURED = os.environ.get("MESOSPIM_RC_SOURCE_ROOT", "").strip()
SOURCE_ROOT = Path(_CONFIGURED) if _CONFIGURED else None

if SOURCE_ROOT is not None:
    if not (SOURCE_ROOT / "mesoSPIM" / "src").is_dir():
        raise RuntimeError(
            f"MESOSPIM_RC_SOURCE_ROOT={_CONFIGURED!r} has no mesoSPIM/src directory")
    if str(SOURCE_ROOT) not in sys.path:
        sys.path.insert(0, str(SOURCE_ROOT))
