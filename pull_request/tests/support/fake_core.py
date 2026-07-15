"""Small shared Core configurations used by offline tests."""
from tests.support.fake_state import FakeState


class UnitConfig:
    filterdict = {"Empty": 0, "515LP": 1}
    zoomdict = {"1x": 1, "2x": 2}
    laserdict = {"488 nm": 0, "561 nm": 1}
    shutteroptions = ["Left", "Right", "Both"]
    camera_parameters = {"x_pixels": 2048, "y_pixels": 2048}
    stage_parameters = {
        "x_min": -25000,
        "x_max": 25000,
        "y_min": -50000,
        "y_max": 50000,
        "z_min": -25000,
        "z_max": 25000,
        "f_min": 0,
        "f_max": 98000,
        "y_load_position": 1000,
        "y_unload_position": -1000,
        "x_center_position": 0,
        "z_center_position": 0,
    }


class TransportConfig(UnitConfig):
    stage_parameters = {
        **UnitConfig.stage_parameters,
        "theta_min": -999,
        "theta_max": 999,
    }


class NoCameraConfig(UnitConfig):
    """Camera-dimension error tests ONLY: a config that never had a camera section."""

    camera_parameters = {}


class BadCameraConfig(UnitConfig):
    """Camera-dimension error tests ONLY: a sensor size that is not an integer."""

    camera_parameters = {"x_pixels": "wide", "y_pixels": 2048}


class UnitCore:
    """A Core with the production state contract, recording the calls that reach it."""

    def __init__(self):
        self.cfg = UnitConfig()
        self.state = FakeState()
        self.calls = []

    def start(self, *args, **kwargs):
        self.calls.append(("start", args, kwargs))
