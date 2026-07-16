"""The flat ``utils.acquisitions`` module the command module falls back to.

``_acquire_start`` and ``_make_acquisition_list`` try ``from .utils.acquisitions import
...`` first and ``from utils.acquisitions import ...`` second. The offline harness loads
the command module flat, with no package, so the second import is the one that runs --
and the source tree cannot satisfy it: ``mesoSPIM/src/utils/acquisitions.py`` opens with
``from ..plugins.utils import ...``, so importing it as a top-level ``utils`` package
raises "attempted relative import beyond top-level package". Putting ``mesoSPIM/src`` on
sys.path does not help; only the package path ``mesoSPIM.src.utils.acquisitions`` works.

So the classes are imported through the package -- the real ``Acquisition`` and
``AcquisitionList`` production builds its lists from -- and registered under the flat
name the command module asks for. Without a source root there is no tree to import from,
so the patch-file default gets stand-ins with the same access surface.
"""

from __future__ import annotations

import sys
import types

from tests.support import SOURCE_ROOT


class StandInAcquisition(dict):
    """Faithful geometry subset of upstream's dict-like Acquisition."""

    def __init__(self):
        super().__init__(z_start=0, z_end=100, z_step=10, planes=10)

    def get_image_count(self):
        return abs(round((self["z_end"] - self["z_start"]) / self["z_step"])) + 1


class StandInAcquisitionList(list):
    """As much of AcquisitionList as the command module uses: an appendable list."""


def _classes():
    if SOURCE_ROOT is None:
        return StandInAcquisition, StandInAcquisitionList
    from mesoSPIM.src.utils.acquisitions import Acquisition, AcquisitionList

    return Acquisition, AcquisitionList


Acquisition, AcquisitionList = _classes()


def install():
    """Publish the classes as top-level ``utils.acquisitions``.

    sys.modules is consulted before sys.path, so this is what the command module's
    fallback import resolves to regardless of what else is importable.
    """
    package = types.ModuleType("utils")
    package.__path__ = []
    module = types.ModuleType("utils.acquisitions")
    module.Acquisition = Acquisition
    module.AcquisitionList = AcquisitionList
    package.acquisitions = module
    sys.modules["utils"] = package
    sys.modules["utils.acquisitions"] = module


install()
