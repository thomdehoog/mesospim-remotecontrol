"""The acquisition list acquire_start stashes and acquire_finish restores.

Both commands are independently allowlisted, so a client can call either one alone. These
tests drive them against ``FakeState``, whose ``__delitem__``-less access surface is the
production one: with a dict fake the old ``del state["acq_list"]`` quietly succeeded and
the bug was invisible.
"""
import pytest

from tests.support.fake_core import UnitCore
from tests.support.patch_loader import vrc


def _ignore_deferred_call(function, *args, **kwargs):
    """Stand in for the Qt-deferred ``core.start``: there is no event loop at this level,
    and what these tests assert is the acquisition list, not the scheduling."""


def _refuse_to_defer(function, *args, **kwargs):
    raise RuntimeError("the event loop refused the deferred call")


@pytest.fixture(autouse=True)
def deferred_calls_need_no_event_loop(monkeypatch):
    monkeypatch.setattr(vrc, "_defer", _ignore_deferred_call)


def test_standalone_acquire_finish_leaves_the_list_alone():
    """A client may call acquire_finish without acquire_start. It must not touch the list."""
    core = UnitCore()
    before = core.state["acq_list"]
    vrc.run(core, "acquire_finish", {})
    assert core.state["acq_list"] is before


def test_acquire_start_then_finish_restores_the_exact_object():
    core = UnitCore()
    before = core.state["acq_list"]
    vrc.run(core, "acquire_start", {"acquisition": {"filename": "a.tif", "planes": 1}})
    assert core.state["acq_list"] is not before
    vrc.run(core, "acquire_finish", {})
    assert core.state["acq_list"] is before


def test_a_repeated_acquire_finish_does_not_restore_a_stale_list():
    """The saved list is cleared on restore, so the second call has nothing to put back."""
    core = UnitCore()
    original = core.state["acq_list"]
    vrc.run(core, "acquire_start", {"acquisition": {"filename": "a.tif", "planes": 1}})
    vrc.run(core, "acquire_finish", {})
    replacement = vrc._make_acquisition_list([{"filename": "b.tif", "planes": 1}])
    core.state["acq_list"] = replacement
    vrc.run(core, "acquire_finish", {})
    assert core.state["acq_list"] is replacement
    assert core.state["acq_list"] is not original


def test_an_unschedulable_acquire_start_puts_the_list_back(monkeypatch):
    """An error must never be returned once state has changed, so a failed schedule rolls
    the operator's list back and leaves nothing saved for acquire_finish to restore."""
    core = UnitCore()
    before = core.state["acq_list"]
    monkeypatch.setattr(vrc, "_defer", _refuse_to_defer)
    with pytest.raises(RuntimeError):
        vrc.run(core, "acquire_start", {"acquisition": {"filename": "a.tif", "planes": 1}})
    assert core.state["acq_list"] is before
    assert core.calls == []
    vrc.run(core, "acquire_finish", {})
    assert core.state["acq_list"] is before
