"""Child diagnostics must stay visible without corrupting protocol stdout."""

from concurrent.futures import ThreadPoolExecutor
from contextlib import redirect_stderr
import importlib.util
import io
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import time
import unittest

ROOT = Path(__file__).resolve().parents[1]


def load_op(name):
    spec = importlib.util.spec_from_file_location(name + "_op", ROOT / name / "main.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class RecordingStderr(io.StringIO):
    def __init__(self):
        super().__init__()
        self.received = threading.Event()

    def write(self, text):
        result = super().write(text)
        if "diagnostic" in self.getvalue():
            self.received.set()
        return result


class StderrTests(unittest.TestCase):
    def test_missing_cli_does_not_leave_a_stderr_reader_waiting(self):
        for name in ("aider", "kimi"):
            with self.subTest(op=name), tempfile.TemporaryDirectory() as directory:
                args = ([str(Path(directory) / "missing-cli")], directory, dict(os.environ))
                if name == "kimi":
                    args += (1,)
                with self.assertRaises(FileNotFoundError):
                    load_op(name).execute(*args)

    def test_cli_stderr_is_live_and_retained_on_success_and_failure(self):
        for name in ("aider", "kimi"):
            for status in (0, 7):
                with self.subTest(op=name, status=status), tempfile.TemporaryDirectory() as directory:
                    release = Path(directory) / "release"
                    code = """
import os, pathlib, sys, time
# No newline: forwarding must not wait for a complete line or process exit.
os.write(2, 'diagnostic: café'.encode())
while not pathlib.Path(sys.argv[1]).exists():
    time.sleep(0.01)
print('{"ok":true}')
sys.exit(int(sys.argv[2]))
"""
                    op = load_op(name)
                    sink = RecordingStderr()
                    args = ([sys.executable, "-c", code, str(release), str(status)], directory, dict(os.environ))
                    if name == "kimi":
                        args += (None,)
                    with redirect_stderr(sink), ThreadPoolExecutor(max_workers=1) as pool:
                        future = pool.submit(op.execute, *args)
                        try:
                            self.assertTrue(sink.received.wait(3), "stderr stayed buffered in the child capture")
                            self.assertFalse(future.done(), "child should still be waiting for release")
                        finally:
                            release.touch()
                        result = future.result(timeout=5)
                    self.assertEqual(result.returncode, status)
                    self.assertEqual(result.stdout, '{"ok":true}\n')
                    self.assertEqual(result.stderr, "diagnostic: café")
                    self.assertEqual(sink.getvalue(), result.stderr)

    def test_kimi_timeout_retains_and_forwards_stderr(self):
        op = load_op("kimi")
        sink = io.StringIO()
        with redirect_stderr(sink), self.assertRaises(subprocess.TimeoutExpired) as raised:
            op.execute([sys.executable, "-c", "import os,time; os.write(2,b'timeout detail'); time.sleep(30)"],
                       ROOT, dict(os.environ), 0.5)
        self.assertEqual(raised.exception.stderr, "timeout detail")
        self.assertEqual(sink.getvalue(), "timeout detail")

    @unittest.skipUnless(os.name == "posix", "process groups require POSIX")
    def test_kimi_timeout_covers_descendant_stderr_after_stdout_closes(self):
        op = load_op("kimi")
        code = """
import os, subprocess, sys
subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(30)'], stdout=subprocess.DEVNULL)
os.write(2,b'descendant detail')
"""
        sink = io.StringIO()
        started = time.monotonic()
        with redirect_stderr(sink), self.assertRaises(subprocess.TimeoutExpired) as raised:
            op.execute([sys.executable, "-c", code], ROOT, dict(os.environ), 0.5)
        self.assertLess(time.monotonic() - started, 3)
        self.assertEqual(raised.exception.stderr, "descendant detail")
        self.assertEqual(sink.getvalue(), "descendant detail")


if __name__ == "__main__":
    unittest.main()
