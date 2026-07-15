"""Offline fakes: a production-contract state, a recording Core, and a fake cfg."""
import contextlib

from PyQt5 import QtCore   # the fake shim installed by conftest


@contextlib.contextmanager
def defer_recorder():
    """Make QTimer.singleShot QUEUE the body instead of firing it immediately, so a test can see
    the faithful processing->completed transition: run a WAIT command (op stays processing), then
    fire the recorded body and observe completion — exactly the production ordering."""
    pending = []
    original = QtCore.QTimer.singleShot
    QtCore.QTimer.singleShot = staticmethod(lambda _msec, fn: pending.append(fn))
    try:
        yield pending
    finally:
        QtCore.QTimer.singleShot = staticmethod(original)


class FakeCfg:
    version = "test-1.0"
    filterdict = {"Empty": 0, "515LP": 1}
    zoomdict = {"1x": 1, "2x": 2}
    laserdict = {"488 nm": 0, "561 nm": 1}
    shutteroptions = ("Left", "Right", "Both")
    pixelsize = {"1x": 1.0, "2x": 0.5}
    binning_dict = {"1x1": (1, 1), "2x2": (2, 2)}
    camera_parameters = {"x_pixels": 2048, "y_pixels": 2048, "subsampling": [1, 2, 4]}
    stage_parameters = {"x_min": -25000, "x_max": 25000, "y_min": -50000, "y_max": 50000,
                        "z_min": -25000, "z_max": 25000, "f_min": 0, "f_max": 98000,
                        "theta_min": -999, "theta_max": 999,
                        "stage_type": "DemoStage",
                        "y_load_position": 45000, "y_unload_position": 0,
                        "x_center_position": 0, "z_center_position": 0}

    def __init__(self):
        # Each Core owns its config containers. Tests that probe a malformed config must not leak
        # the mutation into later cases through class-level dictionaries.
        cls = type(self)
        self.filterdict = dict(cls.filterdict)
        self.zoomdict = dict(cls.zoomdict)
        self.laserdict = dict(cls.laserdict)
        self.pixelsize = dict(cls.pixelsize)
        self.binning_dict = dict(cls.binning_dict)
        self.camera_parameters = dict(cls.camera_parameters)
        self.camera_parameters["subsampling"] = list(cls.camera_parameters.get("subsampling", []))
        self.stage_parameters = dict(cls.stage_parameters)


class FakeState:
    """The production mesoSPIM_StateSingleton access surface: __getitem__ raising KeyError,
    __setitem__, __len__, get_parameter_dict — and NO .get()/__contains__/__delitem__."""

    def __init__(self, **values):
        self._state_dict = dict(values)

    def __getitem__(self, key):
        return self._state_dict[key]              # KeyError on a miss, like production

    def __setitem__(self, key, value):
        self._state_dict[key] = value

    def __len__(self):
        return len(self._state_dict)

    def get_parameter_dict(self, keys):
        return {key: self._state_dict[key] for key in keys}


class FakeCore:
    """A Core stand-in that records the hardware calls remote commands make."""

    def __init__(self, **state_values):
        self.cfg = FakeCfg()
        defaults = {"state": "idle", "position": {"x_pos": 0.0, "y_pos": 0.0, "z_pos": 0.0,
                                                   "f_pos": 0.0, "theta_pos": 0.0},
                    "acq_list": [{}], "selected_row": 0, "shutterstate": "closed",
                    "filter": "Empty", "zoom": "1x", "laser": "488 nm", "intensity": 10,
                    "ETL_cfg_file": "etl.csv", "folder": "/data",
                    "current_framenumber": 0}
        # the ETL readback keys must exist: _state_snapshot reads them via get_parameter_dict
        for etl in ("etl_l_delay_%", "etl_l_ramp_rising_%", "etl_l_ramp_falling_%", "etl_l_amplitude",
                    "etl_l_offset", "etl_r_delay_%", "etl_r_ramp_rising_%", "etl_r_ramp_falling_%",
                    "etl_r_amplitude", "etl_r_offset"):
            defaults[etl] = 0.0
        defaults.update(state_values)
        defaults.setdefault("position_absolute", dict(defaults["position"]))
        self.state = FakeState(**defaults)
        self._remote_session = {"operation": None, "counter": 0}
        self.calls = []
        self.timelapse_active = False

    def _record(self, name, *args, **kwargs):
        self.calls.append((name, args, kwargs))

    # movement / stage
    def move_absolute(self, targets, wait_until_done=False, **k):
        self._record("move_absolute", targets, wait_until_done=wait_until_done, **k)
        for key, value in targets.items():
            position_key = key.replace("_abs", "_pos")
            self.state["position"][position_key] = float(value)
            self.state["position_absolute"][position_key] = float(value)

    def move_relative(self, deltas, wait_until_done=False, **k):
        self._record("move_relative", deltas, wait_until_done=wait_until_done)
        for key, value in deltas.items():
            position_key = key.replace("_rel", "_pos")
            self.state["position"][position_key] += float(value)
            self.state["position_absolute"][position_key] += float(value)

    def zero_axes(self, axes):
        self._record("zero_axes", axes)

    def unzero_axes(self, axes):
        self._record("unzero_axes", axes)

    # settings
    def state_request_handler(self, settings):
        self._record("state_request_handler", settings)

    def set_filter(self, value, wait_until_done=False):
        self._record("set_filter", value, wait_until_done=wait_until_done)

    def set_zoom(self, value, wait_until_done=False, update_etl=True):
        self._record("set_zoom", value, wait_until_done=wait_until_done, update_etl=update_etl)

    def set_laser(self, value, wait_until_done=False, update_etl=True):
        self._record("set_laser", value, wait_until_done=wait_until_done, update_etl=update_etl)

    def set_intensity(self, value, wait_until_done=False):
        self._record("set_intensity", value, wait_until_done=wait_until_done)

    def set_shutterconfig(self, value):
        self._record("set_shutterconfig", value)

    def open_shutters(self):
        self._record("open_shutters")

    def close_shutters(self):
        self._record("close_shutters")

    def set_state(self, mode):
        self._record("set_state", mode)
        self.state["state"] = mode

    # activity
    def start(self, row=None):
        self._record("start", row=row)
        # Production's synchronous start() returns only after it has left the run state. The
        # completion signal is deliberately omitted so tests can inspect a pending operation.
        self.state["state"] = "idle"

    def preview_acquisition(self, z_update=True):
        self._record("preview_acquisition", z_update=z_update)

    def stop(self):
        self._record("stop")
        self.state["state"] = "idle"

    def run_time_lapse(self, tpoints=1, time_interval_sec=0):
        self.timelapse_active = True
        self._record("run_time_lapse", tpoints=tpoints, time_interval_sec=time_interval_sec)

    def stop_time_lapse(self):
        self.timelapse_active = False
        self._record("stop_time_lapse")

    def get_free_disk_space(self, acq_list):
        return 1_000_000

    def get_required_disk_space(self, acq_list):
        return 500_000

    def check_motion_limits(self, acq_list):
        return []

    # signals used by emit() paths — a minimal bound signal: records every emit (so command tests
    # can assert a signal fired) AND drives connected slots (so the real Acceptor wiring is testable)
    class _Sig:
        def __init__(self, core, name):
            self._core, self._name = core, name
            self._slots = []

        def connect(self, slot, *a, **k):
            self._slots.append(slot)

        def disconnect(self, slot=None):
            self._slots = [] if slot is None else [s for s in self._slots if s is not slot]

        def emit(self, *args):
            # impl records emits under an "emit:"-prefixed name; the pull_request wire fake
            # (support/fakes.py::_Sig) deliberately records under the BARE signal name instead, to
            # match contracts.EXPECTED_CORE_CALL. Do NOT "unify" these — the existing impl tests
            # depend on this prefix.
            self._core._record("emit:" + self._name, *args)
            for slot in list(self._slots):
                slot(*args)

    def __getattr__(self, name):
        if name.startswith("sig_"):
            sig = FakeCore._Sig(self, name)
            object.__setattr__(self, name, sig)
            return sig
        raise AttributeError(name)
