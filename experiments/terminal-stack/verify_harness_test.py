"""The live verifier must not type into a low-quota model chooser."""
import importlib.util
import os
from pathlib import Path
import unittest
import time
import signal
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location("harness_check", Path(__file__).with_name("verify_harness.py"))
harness = importlib.util.module_from_spec(spec)
spec.loader.exec_module(harness)


class InputGuardTest(unittest.TestCase):
    def test_live_input_stops_before_any_terminal_write(self):
        warnings = [
            "Approaching rate limits\n1. Switch to gpt-6-luna\n2. Keep current model",
            "You have less than 10% of your 5h limit left.",
            "You have less than 5% of your\n5h limit left.",
            "Usage limit reached",
            "5h limit: only 4% left · resets later",
            "Weekly limit: 10% left",
            "Switch to gpt-6-luna for lower credit usage?",
        ]
        for cls in (harness.HarnessCheck, harness.DesktopHarnessCheck):
            for warning in warnings:
                with self.subTest(outer=cls.__name__, warning=warning):
                    check = cls.__new__(cls)
                    check.teardown = False
                    check.text = lambda: warning
                    check.drain = Mock()
                    check.gui_command = Mock()
                    with patch.object(harness.os, "write") as write:
                        with self.assertRaises(harness.QuotaBlocked):
                            check.send(b"\r")
                        write.assert_not_called()
                    check.gui_command.assert_not_called()

    def test_stopping_the_fixture_does_not_require_model_interaction(self):
        check = harness.HarnessCheck.__new__(harness.HarnessCheck)
        check.teardown = True
        check.guard_quota("Approaching rate limits")

    def test_available_quota_does_not_block_normal_input(self):
        check = harness.HarnessCheck.__new__(harness.HarnessCheck)
        check.teardown = False
        check.guard_quota("5h limit: 100% left")

    def test_explicit_low_quota_override_never_handles_model_choosers(self):
        check = harness.HarnessCheck.__new__(harness.HarnessCheck)
        check.teardown = False
        check.allow_low_quota = True
        check.guard_quota("5h limit: only 4% left")
        for text in ("Approaching rate limits", "Switch to gpt-6-luna", "Usage limit reached"):
            with self.subTest(text=text), self.assertRaises(harness.QuotaBlocked):
                check.guard_quota(text)


class OuterReaderTest(unittest.TestCase):
    def test_pty_output_drains_while_inspection_is_idle(self):
        child, fd = harness.pty.fork()
        if child == 0:
            data = b"x" * (2 * 1024 * 1024) + b"READER-DRAINED"
            while data:
                data = data[os.write(1, data):]
            os._exit(0)
        check = harness.HarnessCheck.__new__(harness.HarnessCheck)
        check.fd = fd
        os.set_blocking(fd, False)
        check.start_reader()
        reaped = False
        try:
            deadline = time.monotonic() + 5
            while time.monotonic() < deadline:
                pid, status = os.waitpid(child, os.WNOHANG)
                if pid:
                    reaped = True
                    self.assertEqual(status, 0)
                    break
                time.sleep(.01)
            self.assertTrue(reaped, "PTY writer blocked without any inspection polling")
            deadline = time.monotonic() + 1
            while time.monotonic() < deadline:
                with check.reader_lock:
                    tail = check.outer_tail
                if tail.endswith(b"READER-DRAINED"):
                    break
                time.sleep(.01)
            self.assertTrue(tail.endswith(b"READER-DRAINED"))
            self.assertLessEqual(len(tail), 65536)
        finally:
            if not reaped:
                os.kill(child, signal.SIGKILL)
                os.waitpid(child, 0)
            check.stop_reader()
            os.close(fd)


if __name__ == "__main__":
    unittest.main()
