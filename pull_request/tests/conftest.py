"""Pytest grouping for the organized mesoSPIM Remote Control tests."""
from pathlib import Path


def pytest_configure(config):
    config.addinivalue_line("markers", "unit: Qt-free unit test")
    config.addinivalue_line("markers", "integration: real loopback transport test")
    config.addinivalue_line("markers", "offline: test that does not require mesoSPIM")
    config.addinivalue_line("markers", "live: opt-in test against a running mesoSPIM")
    config.addinivalue_line("markers", "valid: valid command or protocol test")
    config.addinivalue_line(
        "markers", "normal: functional, validation, protocol, and viability tests")
    config.addinivalue_line(
        "markers", "adversarial: bounded hostile-input and transport-abuse tests")
    config.addinivalue_line(
        "markers", "live_valid: opt-in valid calls that change and restore a live device")
    config.addinivalue_line(
        "markers", "live_demo_all: opt-in demo-only sweep of every allowlisted command")
    config.addinivalue_line(
        "markers", "live_adversarial: opt-in bounded concurrency stress against DemoStage")


def pytest_report_header(config):
    """Name the code actually under test, so a refactor can never be validated green
    against stale patch text by accident."""
    from tests.support import patch_loader
    return f"remote-control modules under test: {patch_loader.SOURCE}"


def pytest_collection_modifyitems(items):
    """Apply level, intent, and backward-compatible public markers."""
    for item in items:
        path = Path(str(item.fspath))
        level = path.parent.name
        module_name = path.name
        if level == "live":
            markers = ["live"]
            if module_name == "test_adversarial.py":
                markers += ["adversarial", "live_adversarial"]
            elif module_name == "test_all_commands.py":
                markers += ["valid", "live_demo_all"]
            else:
                markers += ["valid", "live_valid"]
        else:
            markers = [level, "offline"]
            if module_name in {"test_adversarial.py", "test_transport_security.py"}:
                markers += ["adversarial"]
            else:
                markers += ["valid", "normal"]
        for marker in markers:
            item.add_marker(marker)
