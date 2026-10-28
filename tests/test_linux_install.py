"""Exercise the Linux release installer without modifying the real user profile."""

import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import time
import unittest


ROOT = Path(__file__).resolve().parents[1]


class LinuxInstallerTests(unittest.TestCase):
    def test_install_and_launch_after_removing_archive(self):
        self.check_install("桌面 user", None)

    def test_custom_data_directory_and_quoted_executable_path(self):
        self.check_install('desktop % user $cash `tick` "quote" \\slash', "custom data")

    def test_relative_xdg_data_home_uses_default(self):
        self.check_install("desktop user", "relative")

    def check_install(self, home_name, data_setting):
        with tempfile.TemporaryDirectory(prefix="mrs-install-test-") as temporary:
            root = Path(temporary)
            test_home = root / home_name
            test_home.mkdir()
            package = root / "extracted package"
            package.mkdir()
            for name in ("install.sh", "metrics_render_sender.desktop"):
                shutil.copy2(ROOT / "packaging/linux" / name, package / name)
            shutil.copy2(ROOT / "src/metrics_render_sender/assets/tray.svg",
                         package / "metrics_render_sender.svg")
            binary = package / "metrics_render_sender"
            binary.write_text('#!/bin/sh\nprintf started > "$MRS_INSTALL_TEST_MARKER"\n')
            binary.chmod(0o755)

            environment = dict(os.environ)
            environment["HOME"] = str(test_home)
            environment.pop("XDG_DATA_HOME", None)
            data_dir = test_home / ".local/share"
            if data_setting == "relative":
                environment["XDG_DATA_HOME"] = "relative-data"
            elif data_setting is not None:
                data_dir = root / data_setting
                environment["XDG_DATA_HOME"] = str(data_dir)
            marker = root / "launched"
            environment["MRS_INSTALL_TEST_MARKER"] = str(marker)

            # Run from another directory and reinstall to exercise replacement.
            for _ in range(2):
                subprocess.run(["bash", str(package / "install.sh")], cwd=root,
                               env=environment, check=True, capture_output=True, text=True)
            executable = test_home / ".local/lib/metrics_render_sender/metrics_render_sender"
            desktop = data_dir / "applications/metrics_render_sender.desktop"
            icon = data_dir / "icons/hicolor/scalable/apps/metrics_render_sender.svg"
            self.assertEqual(executable.read_bytes(), binary.read_bytes())
            self.assertTrue(os.access(executable, os.X_OK))
            self.assertTrue(desktop.is_file())
            self.assertTrue(icon.is_file())
            self.assertFalse((root / "relative-data").exists())
            shutil.rmtree(package)

            validator = shutil.which("desktop-file-validate")
            if validator:
                subprocess.run([validator, str(desktop)], check=True, capture_output=True)
            launcher = shutil.which("gio")
            if not launcher:
                self.skipTest("gio is required to verify actual desktop entry launching")
            launched = subprocess.run([launcher, "launch", str(desktop)], env=environment,
                                      capture_output=True, text=True, timeout=5)
            self.assertEqual(launched.returncode, 0, launched.stderr + desktop.read_text())
            deadline = time.monotonic() + 5
            while not marker.exists() and time.monotonic() < deadline:
                time.sleep(0.01)
            self.assertEqual(marker.read_text(), "started")


if __name__ == "__main__":
    unittest.main()
