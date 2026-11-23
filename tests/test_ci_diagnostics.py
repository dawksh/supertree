import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / 'scripts' / 'go-test-annotations.py'
WRAPPER = ROOT / 'scripts' / 'go-test-ci.sh'
PKG = 'github.com/dawksh/supertree/internal/process'
FALLBACK = '::error::Go test failed without safe package diagnostics\n'


def event(**kwargs):
    return json.dumps(kwargs) + '\n'


def run_summary(data, status=1):
    return subprocess.run([sys.executable, str(SCRIPT), str(status)], input=data,
                          text=True, capture_output=True, check=True).stdout


def run_wrapper(code, env=None):
    return subprocess.run(['bash', str(WRAPPER), sys.executable, '-c', code],
                          cwd=ROOT, text=True, capture_output=True, env=env)


class AnnotationsTest(unittest.TestCase):
    def test_nested_secret_name_never_published(self):
        data = event(Action='fail', Package=PKG, Test='TestLogin/password_Secret123')
        data += event(Action='fail', Package=PKG)
        self.assertEqual(run_summary(data), f'::error::Go package failed: {PKG}\n')

    def test_test_event_without_package_failure_is_not_published(self):
        self.assertEqual(run_summary(event(Action='fail', Package=PKG, Test='TestSecret')), FALLBACK)

    def test_invalid_names_cannot_inject_or_expose_output(self):
        data = ''.join(event(Action='fail', Package=p, Output='SECRET') for p in [
            PKG + '%0A::error::INJECT', PKG + '\nINJECT', 'other.example/SECRET', PKG + '/../secret'])
        self.assertEqual(run_summary(data), FALLBACK)

    def test_package_only_failure_and_deduplication(self):
        data = event(Action='fail', Package=PKG, Output='SECRET')
        self.assertEqual(run_summary(data + data), f'::error::Go package failed: {PKG}\n')

    def test_malformed_and_compile_failure(self):
        data = 'not json SECRET\n' + event(Action='output', Package=PKG, Output='compile SECRET')
        data += event(Action='fail', Package=PKG, FailedBuild=PKG)
        self.assertEqual(run_summary(data), f'::error::Go package failed: {PKG}\n')

    def test_no_events_and_success(self):
        self.assertEqual(run_summary('compiler SECRET\n'), FALLBACK)
        self.assertEqual(run_summary('', 0), '')

    def test_non_objects_and_wrong_types(self):
        self.assertEqual(run_summary('null\n[]\n' + event(Action='fail', Package=['x'])), FALLBACK)

    def test_wrapper_preserves_exit_and_logs(self):
        payload = event(Action='fail', Package=PKG)
        code = f'import sys; print({payload!r}, end=""); sys.exit(7)'
        result = run_wrapper(code)
        self.assertEqual(result.returncode, 7)
        self.assertIn(payload, result.stdout)
        self.assertIn(f'::error::Go package failed: {PKG}\n', result.stdout)

    def test_raw_stdout_and_stderr_workflow_commands_are_inert(self):
        code = 'import sys; sys.stdout.write("::error::out\\r::warning::out2\\n##[error]legacy\\n"); sys.stderr.write("::error::err\\n##[warning]legacy2\\n")'
        result = run_wrapper(code)
        self.assertEqual(result.returncode, 0)
        for line in (result.stdout + result.stderr).replace('\r', '\n').splitlines():
            self.assertFalse(line.startswith('::'), line)
        self.assertIn('::error::out', result.stdout)
        self.assertIn('::error::err', result.stderr)
        self.assertNotIn('##[', result.stdout + result.stderr)

    def test_vet_mode_sanitizes_and_uses_vet_failure(self):
        code = 'import sys; print("::error::injected"); sys.exit(7)'
        result = subprocess.run(['bash', str(WRAPPER), '--vet', sys.executable, '-c', code],
                                cwd=ROOT, text=True, capture_output=True)
        self.assertEqual(result.returncode, 7)
        self.assertIn('[go] ::error::injected', result.stdout)
        self.assertIn('::error::Go vet failed', result.stdout)
        self.assertNotIn('::error::Go test failed', result.stdout)

    def test_wrapper_success(self):
        result = run_wrapper('print("ok")')
        self.assertEqual(result.returncode, 0)
        self.assertIn('ok', result.stdout)

    def test_tee_failure_fails_job_and_original_failure_wins(self):
        with tempfile.TemporaryDirectory() as tmp:
            tee = Path(tmp) / 'tee'
            tee.write_text('#!/bin/sh\ncat >/dev/null\nexit 9\n')
            tee.chmod(0o755)
            env = dict(os.environ, PATH=tmp + os.pathsep + os.environ['PATH'])
            self.assertEqual(run_wrapper('print("ok")', env).returncode, 9)
            self.assertEqual(run_wrapper('import sys; sys.exit(7)', env).returncode, 7)

    def test_log_filter_failure_fails_job(self):
        with tempfile.TemporaryDirectory() as tmp:
            python = Path(tmp) / 'python3'
            python.write_text(f'#!/bin/sh\ncase "$1" in *go-test-log.py) exit 6;; esac\nexec {sys.executable} "$@"\n')
            python.chmod(0o755)
            env = dict(os.environ, PATH=tmp + os.pathsep + os.environ['PATH'])
            self.assertEqual(run_wrapper('pass', env).returncode, 6)

    def test_summarizer_failure_fails_job_and_original_failure_wins(self):
        with tempfile.TemporaryDirectory() as tmp:
            python = Path(tmp) / 'python3'
            python.write_text(f'#!/bin/sh\ncase "$1" in *go-test-annotations.py) exit 8;; esac\nexec {sys.executable} "$@"\n')
            python.chmod(0o755)
            env = dict(os.environ, PATH=tmp + os.pathsep + os.environ['PATH'])
            self.assertEqual(run_wrapper('print("ok")', env).returncode, 8)
            self.assertEqual(run_wrapper('import sys; sys.exit(7)', env).returncode, 7)


if __name__ == '__main__':
    unittest.main()
