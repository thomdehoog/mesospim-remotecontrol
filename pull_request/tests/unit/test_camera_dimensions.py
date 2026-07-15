"""The sensor size the protocol reports, and what happens when the config has none.

``_camera_pixels`` used to fabricate 2048 from config attributes that exist in no config
and no source file. It now raises instead, which makes it the first thing in
``_acquire_start`` that can fail -- hence the ordering test at the end: nothing may be
mutated or queued before every fallible value has been computed.
"""
import pytest

from tests.support.fake_core import BadCameraConfig, NoCameraConfig, UnitCore
from tests.support.patch_loader import vrc

VALID_ACQUISITION = {"acquisition": {"filename": "a.tif", "planes": 1}}


def _ignore_deferred_call(function, *args, **kwargs):
    """Stand in for the Qt-deferred ``core.start``; see test_acquire_finish."""


class _DeferRecorder:
    """Records what would have been queued on the event loop, so a test can prove that a
    call which ends in an error queued nothing at all."""

    def __init__(self):
        self.scheduled = []

    def __call__(self, function, *args, **kwargs):
        self.scheduled.append((function, args, kwargs))


@pytest.fixture(autouse=True)
def deferred_calls_need_no_event_loop(monkeypatch):
    monkeypatch.setattr(vrc, "_defer", _ignore_deferred_call)


def test_get_config_reports_the_configured_sensor():
    core = UnitCore()
    assert vrc.run(core, "get_config", {})["camera"] == {"pixels_x": 2048, "pixels_y": 2048}


def test_acquire_start_reports_the_configured_sensor():
    core = UnitCore()
    assert vrc.run(core, "acquire_start", VALID_ACQUISITION)["pixels"] == [2048, 2048]


@pytest.mark.parametrize("camera_parameters", [
    {},
    {"y_pixels": 2048},
    {"x_pixels": 2048},
    {"x_pixels": "wide", "y_pixels": 2048},
    {"x_pixels": 2048, "y_pixels": None},
])
@pytest.mark.parametrize("command,args", [
    ("get_config", {}),
    ("acquire_start", VALID_ACQUISITION),
])
def test_a_missing_or_invalid_dimension_fails_clearly(camera_parameters, command, args):
    """Every missing and every invalid dimension, through both callers. No guessed default."""
    core = UnitCore()
    core.cfg.camera_parameters = camera_parameters
    with pytest.raises(ValueError, match="camera_parameters"):
        vrc.run(core, command, args)


@pytest.mark.parametrize("cfg", [NoCameraConfig(), BadCameraConfig()])
def test_a_failed_acquire_start_does_not_touch_state_or_schedule(cfg, monkeypatch):
    """The whole point of the reordering: no rollback needed, because nothing was done."""
    core = UnitCore()
    core.cfg = cfg
    before = core.state["acq_list"]
    recorder = _DeferRecorder()
    monkeypatch.setattr(vrc, "_defer", recorder)
    with pytest.raises(ValueError):
        vrc.run(core, "acquire_start", VALID_ACQUISITION)
    assert core.state["acq_list"] is before
    assert recorder.scheduled == []
    assert core.calls == []
