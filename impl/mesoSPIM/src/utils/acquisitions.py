"""Minimal stand-ins for mesoSPIM's Acquisition / AcquisitionList.

In the real tree these come from mesoSPIM's own utils.acquisitions. For the offline suite they
only need to be a dict-like row and a list-like container so _make_acquisition_list works.
"""


class Acquisition(dict):
    """Small faithful subset of upstream's dict-like acquisition."""

    def __init__(self):
        super().__init__(z_start=0, z_end=100, z_step=10, planes=10)

    def get_image_count(self):
        return abs(round((self["z_end"] - self["z_start"]) / self["z_step"])) + 1


class AcquisitionList(list):
    def __init__(self, rows=()):
        super().__init__(rows)
