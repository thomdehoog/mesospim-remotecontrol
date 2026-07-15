"""Every read command, driven against the production state access surface.

``mesoSPIM_StateSingleton`` has no ``.get()``, so ``_state_get`` reaches it through the
``__getitem__``/KeyError shim. Under a dict fake that shim's lenient branch is never taken
and the production path is never exercised -- these tests pin it with ``FakeState``.
"""
import pytest

from tests.support.fake_core import UnitCore
from tests.support.fake_state import FakeState
from tests.support.patch_loader import vrc

UNAVAILABLE_PROGRESS_KEYS = (
    "current_plane", "total_planes", "current_acquisition", "total_acquisitions")


def test_the_fake_state_has_no_dict_conveniences():
    """The point of the fake. Production offers none of these; nor may the fake.

    A type with __setitem__ and no __delitem__ raises AttributeError from the shared
    subscript slot, not TypeError -- that is the exception acquire_finish's broad `except`
    used to swallow before overwriting the operator's list with None.
    """
    state = FakeState()
    assert not hasattr(state, "get")
    assert not hasattr(state, "update")
    assert not hasattr(type(state), "__contains__")
    assert not hasattr(type(state), "__delitem__")
    with pytest.raises(AttributeError):
        del state["state"]
    with pytest.raises(KeyError):
        state["no_such_key"]


@pytest.mark.parametrize("command", [
    "hello", "ping", "get_state", "get_position", "get_info", "get_progress",
    "get_capabilities", "get_acquisition_list", "get_limits",
])
def test_read_commands_work_without_a_state_get(command):
    core = UnitCore()
    assert isinstance(vrc.run(core, command, {}), dict)


def test_get_state_reports_the_seeded_values():
    core = UnitCore()
    result = vrc.run(core, "get_state", {})
    assert result["state"] == "idle"
    assert result["intensity"] == 10
    assert result["position"]["x"] == 0.0


def test_get_progress_returns_stable_nulls_for_the_unavailable_fields():
    """Production state has no backing key for these four, so every one of them arrives at
    the client as null through the KeyError shim -- not as a crash, and not as a default."""
    core = UnitCore()
    progress = vrc.run(core, "get_progress", {})
    assert progress["state"] == "idle"
    for key in UNAVAILABLE_PROGRESS_KEYS:
        assert key in progress and progress[key] is None


def test_get_state_all_without_keys_enumerates_the_whole_state():
    core = UnitCore()
    everything = vrc.run(core, "get_state_all", {})
    assert everything["state"] == "idle"
    assert len(everything) == len(core.state)


def test_get_state_all_raises_for_an_unknown_key_exactly_as_the_instrument_does():
    """get_parameter_dict indexes every key it is handed. Existing behaviour, newly
    reproducible offline: a dict fake could not raise this."""
    core = UnitCore()
    with pytest.raises(KeyError):
        vrc.run(core, "get_state_all", {"keys": ["state", "no_such_key"]})
