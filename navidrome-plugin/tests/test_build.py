"""Build-only contract tests: all Docker calls use a fake CLI, never a daemon."""
import json
import os
from pathlib import Path
import shutil
import signal
import stat
import subprocess
import sys
import tempfile
import time
import unittest


FAKE_DOCKER = r'''
import json, os, pathlib, signal, sys, time
args = sys.argv[1:]
root = pathlib.Path(os.environ["FAKE_ROOT"])
with (root / "calls").open("a") as log:
    log.write(json.dumps(args) + "\n")
assert args[0] == "build", args
assert args[1] == "-f", args
assert args[3] == "--output", args
assert args[4].startswith("type=local,dest="), args
assert len(args) == 6, args
export = pathlib.Path(args[4].split("dest=", 1)[1])
assert export.parent.stat().st_mode & 0o777 == 0o700
mode = os.environ.get("FAKE_MODE", "")
if mode in ("build-fail", "daemon-fail"): sys.exit(1)
if mode == "blocked":
    assert (export.parent.parent / ".build.lock").is_dir()
    (root / "started").touch()
    deadline = time.monotonic() + 10
    while not (root / "release").exists():
        if time.monotonic() >= deadline: sys.exit("timed out waiting for release")
        time.sleep(0.01)
export.mkdir()
package = export / "navidrome-nowplaying.ndp"
if mode != "missing":
    package.write_bytes(b"" if mode == "empty" else b"new package")
if mode == "export-fail": sys.exit(1)
if mode == "export-symlink":
    package.unlink()
    package.symlink_to(root / "keep")
if mode == "export-directory":
    package.unlink()
    package.mkdir()
artifact = export.parent.parent / package.name
if mode == "changed": artifact.write_bytes(b"concurrent package")
if mode == "removed": artifact.unlink()
if mode == "created": artifact.write_bytes(b"concurrent package")
if mode in ("symlink", "directory"):
    artifact.unlink()
    if mode == "symlink": artifact.symlink_to(root / "keep")
    else: artifact.mkdir()
if mode.startswith("signal-"):
    os.kill(os.getppid(), getattr(signal, mode.removeprefix("signal-")))
'''


class BuildTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="build tests ")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.repo = self.root / "checkout with spaces"
        self.plugin = self.repo / "navidrome-plugin"
        self.plugin.mkdir(parents=True)
        source = Path(__file__).resolve().parents[1]
        for name in ("build.sh", "Dockerfile", "Dockerfile.dockerignore"):
            shutil.copy2(source / name, self.plugin / name)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        docker = self.bin / "docker"
        docker.write_text("#!" + sys.executable + "\n" + FAKE_DOCKER)
        docker.chmod(0o755)
        self.output = self.repo / "build" / "navidrome-plugin"
        self.output.mkdir(parents=True)
        self.artifact = self.output / "navidrome-nowplaying.ndp"
        self.artifact.write_bytes(b"old package")
        self.keep = self.root / "keep"
        self.keep.write_bytes(b"keep")
        self.unrelated = self.output / "other.ndp"
        self.unrelated.write_bytes(b"other plugin")
        self.other_temp = self.output / ".build-someone-else"
        self.other_temp.mkdir()

    def run_build(self, mode="", args=None, missing_docker=False):
        env = dict(os.environ, FAKE_ROOT=str(self.root), FAKE_MODE=mode)
        env["PATH"] = str(self.bin) if missing_docker else str(self.bin) + os.pathsep + os.environ["PATH"]
        if missing_docker:
            (self.bin / "docker").unlink()
        return subprocess.run(
            ["/bin/sh", str(self.plugin / "build.sh"), *(args or [])],
            cwd=self.root, env=env, capture_output=True, text=True,
        )

    def calls(self):
        log = self.root / "calls"
        return [json.loads(line) for line in log.read_text().splitlines()] if log.exists() else []

    def assert_clean(self):
        self.assertFalse((self.output / ".build.lock").exists())
        self.assertEqual(set(self.output.iterdir()), {self.artifact, self.unrelated, self.other_temp})
        self.assertEqual(self.unrelated.read_bytes(), b"other plugin")
        self.assertEqual(list(self.other_temp.iterdir()), [])
        self.assertEqual(self.keep.read_bytes(), b"keep")
        # A build is the only permitted Docker API: no production target or containers.
        self.assertTrue(all(call[0] == "build" for call in self.calls()))

    def test_default_build_and_update_keep_directory_inode(self):
        inode = self.output.stat().st_ino
        for _ in range(2):
            result = self.run_build()
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(self.artifact.read_bytes(), b"new package")
            self.assertEqual(stat.S_IMODE(self.artifact.stat().st_mode), 0o644)
            self.assertEqual(self.output.stat().st_ino, inode)
            self.assertIn(str(self.artifact), result.stdout)
            build = self.calls()[-1]
            self.assertEqual(build[:4], ["build", "-f", str(self.plugin / "Dockerfile"), "--output"])
            self.assertEqual(build[-1], str(self.repo))
            self.assertTrue(build[4].startswith(f"type=local,dest={self.output}/.build-"))
            self.assertTrue(build[4].endswith("/export"))
            self.assert_clean()

    def test_custom_relative_path_with_spaces_and_shell_characters(self):
        path = "custom output 'quoted' $(not-executed)"
        result = self.run_build(args=[path])
        self.assertEqual(result.returncode, 0, result.stderr)
        custom = self.root / path
        self.assertEqual((custom / self.artifact.name).read_bytes(), b"new package")
        self.assertEqual(list(custom.iterdir()), [custom / self.artifact.name])
        self.assertEqual(self.artifact.read_bytes(), b"old package")
        self.assert_clean()

    def test_existing_output_is_readable_by_nonroot(self):
        self.output.chmod(0o700)
        result = self.run_build()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(stat.S_IMODE(self.output.stat().st_mode) & 0o555, 0o555)
        self.assert_clean()

    def test_custom_absolute_output_directory(self):
        custom = self.root / "absolute output"
        result = self.run_build(args=[str(custom)])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((custom / self.artifact.name).read_bytes(), b"new package")
        self.assertEqual(list(custom.iterdir()), [custom / self.artifact.name])
        self.assert_clean()

    def test_absent_default_output_is_created_readable(self):
        shutil.rmtree(self.repo / "build")
        result = self.run_build()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.artifact.read_bytes(), b"new package")
        for path in (self.repo / "build", self.output):
            self.assertEqual(stat.S_IMODE(path.stat().st_mode) & 0o555, 0o555)
        self.assertEqual(list(self.output.iterdir()), [self.artifact])

    def test_failures_preserve_existing_package(self):
        for mode in ("build-fail", "daemon-fail", "export-fail", "missing", "empty", "export-symlink", "export-directory"):
            with self.subTest(mode=mode):
                result = self.run_build(mode)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(self.artifact.read_bytes(), b"old package")
                self.assert_clean()

    def test_concurrent_build_rejected_without_export(self):
        inode = self.output.stat().st_ino
        env = dict(os.environ, FAKE_ROOT=str(self.root), FAKE_MODE="blocked")
        env["PATH"] = str(self.bin) + os.pathsep + os.environ["PATH"]
        first = subprocess.Popen(
            ["/bin/sh", str(self.plugin / "build.sh")],
            cwd=self.root, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
        )
        try:
            deadline = time.monotonic() + 5
            while not (self.root / "started").exists():
                self.assertIsNone(first.poll(), "first build exited before marker")
                self.assertLess(time.monotonic(), deadline, "first build did not reach marker")
                time.sleep(0.01)
            lock = self.output / ".build.lock"
            lock_inode = lock.stat().st_ino
            temps = set(self.output.glob(".build-*"))
            second = self.run_build()
            self.assertNotEqual(second.returncode, 0)
            self.assertIn("Already building", second.stderr)
            self.assertEqual(len(self.calls()), 1)
            self.assertEqual(lock.stat().st_ino, lock_inode)
            self.assertEqual(set(self.output.glob(".build-*")), temps)
            self.assertEqual(self.artifact.read_bytes(), b"old package")
            (self.root / "release").touch()
            stdout, stderr = first.communicate(timeout=5)
            self.assertEqual(first.returncode, 0, stderr)
            self.assertIn(str(self.artifact), stdout)
            self.assertEqual(self.artifact.read_bytes(), b"new package")
            self.assertEqual(self.output.stat().st_ino, inode)
            self.assert_clean()
        finally:
            (self.root / "release").touch()
            if first.poll() is None:
                first.terminate()
            first.communicate(timeout=15)

    def test_signals_cleanup_private_temps_and_lock(self):
        for name in ("SIGHUP", "SIGINT", "SIGTERM"):
            with self.subTest(signal=name):
                result = self.run_build("signal-" + name)
                self.assertEqual(result.returncode, 128 + getattr(signal, name), result.stderr)
                self.assertEqual(self.artifact.read_bytes(), b"old package")
                self.assert_clean()

    def test_changed_destination_is_not_overwritten(self):
        result = self.run_build("changed")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("changed during build", result.stderr)
        self.assertEqual(self.artifact.read_bytes(), b"concurrent package")
        self.assert_clean()

    def test_destination_removed_or_created_during_build(self):
        result = self.run_build("removed")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.artifact.exists())
        result = self.run_build("created")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.artifact.read_bytes(), b"concurrent package")
        self.assert_clean()

    def test_destination_symlink_or_directory_before_and_during_build(self):
        for mode in ("symlink", "directory"):
            for during in (False, True):
                with self.subTest(mode=mode, during=during):
                    self.artifact.unlink()
                    self.artifact.write_bytes(b"old package")
                    if not during:
                        self.artifact.unlink()
                        if mode == "symlink": self.artifact.symlink_to(self.keep)
                        else: self.artifact.mkdir()
                    before_calls = len(self.calls())
                    result = self.run_build(mode if during else "")
                    self.assertNotEqual(result.returncode, 0)
                    if not during:
                        self.assertEqual(len(self.calls()), before_calls)
                    self.assert_clean()
                    if mode == "directory": self.artifact.rmdir()
                    else: self.artifact.unlink()
                    self.artifact.write_bytes(b"old package")

    def test_dangling_artifact_symlink_and_fifo_rejected(self):
        self.artifact.unlink()
        self.artifact.symlink_to(self.root / "does-not-exist")
        result = self.run_build()
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue(self.artifact.is_symlink())
        self.assertEqual(self.calls(), [])
        self.assert_clean()
        self.artifact.unlink()
        os.mkfifo(self.artifact)
        result = self.run_build()
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue(stat.S_ISFIFO(self.artifact.stat().st_mode))
        self.assertEqual(self.calls(), [])
        self.assert_clean()

    def test_output_path_hazards_rejected(self):
        link = self.root / "link"
        link.symlink_to(self.output, target_is_directory=True)
        dangling = self.root / "dangling"
        dangling.symlink_to(self.root / "missing")
        for path in (link, link / "child", dangling, self.keep, self.keep / "child"):
            with self.subTest(path=path):
                result = self.run_build(args=[str(path)])
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(self.calls(), [])
                self.assert_clean()

    def test_invalid_arguments(self):
        for args in (["one", "two"], [""], ["-bad"], ["/"], ["a,b"], ["a\nb"], ["a\tb"],
                     ["a/../b"], ["a/./b"], ["a//b"], ["a/"], ["a/.."], ["a/."]):
            with self.subTest(args=args):
                result = self.run_build(args=args)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(self.calls(), [])
                self.assert_clean()
        self.assertIn("commas", self.run_build(args=["a,b"]).stderr)

    def test_missing_docker(self):
        result = self.run_build(missing_docker=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Docker CLI is required", result.stderr)
        self.assertEqual(self.calls(), [])
        self.assert_clean()


if __name__ == "__main__":
    unittest.main()
