import json
import subprocess
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / 'scripts' / 'go-test-annotations.py'
PKG = 'github.com/dawksh/supertree/internal/process'


def event(**kwargs):
    return json.dumps(kwargs) + '\n'


def run_summary(data, status=1):
    return subprocess.run([sys.executable, str(SCRIPT), str(status)], input=data,
                          text=True, capture_output=True, check=True).stdout


class AnnotationsTest(unittest.TestCase):
    def test_nested_failure_only_names_no_output(self):
        data = event(Action='output', Package=PKG, Output='SECRET::error::bad\n')
        data += event(Action='fail', Package=PKG, Test='TestRun/child_1')
        data += event(Action='fail', Package=PKG)
        self.assertEqual(run_summary(data),
                         f'::error::Go test failed: {PKG} TestRun/child_1\n'
                         f'::error::Go package failed: {PKG}\n')

    def test_invalid_names_cannot_inject_or_expose_output(self):
        data = ''.join(event(Action='fail', Package=p, Test=t, Output='SECRET') for p, t in [
            (PKG, 'TestX%0A::error::INJECT'), (PKG, 'TestX\nINJECT'),
            ('other.example/SECRET', 'TestX'), (PKG, 'TestX/../secret'),
            (PKG, 'TestX/secret with spaces')])
        self.assertEqual(run_summary(data), '::error::Go test failed without safe package or test diagnostics\n')

    def test_package_only_failure(self):
        self.assertEqual(run_summary(event(Action='fail', Package=PKG)),
                         f'::error::Go package failed: {PKG}\n')

    def test_malformed_and_compile_failure(self):
        data = 'not json SECRET\n' + event(Action='output', Package=PKG, Output='compile SECRET')
        data += event(Action='fail', Package=PKG, FailedBuild=PKG)
        self.assertEqual(run_summary(data), f'::error::Go package failed: {PKG}\n')

    def test_no_events_and_success(self):
        self.assertEqual(run_summary('compiler SECRET\n'),
                         '::error::Go test failed without safe package or test diagnostics\n')
        self.assertEqual(run_summary('', 0), '')

    def test_wrapper_preserves_exit_and_logs(self):
        wrapper = ROOT / 'scripts' / 'go-test-ci.sh'
        payload = event(Action='fail', Package=PKG, Test='TestRun', Output='SECRET')
        cmd = [sys.executable, '-c', 'import sys; print(sys.argv[1], end=""); sys.exit(7)', payload]
        result = subprocess.run(['bash', str(wrapper), *cmd], text=True, capture_output=True)
        self.assertEqual(result.returncode, 7)
        self.assertIn(payload, result.stdout)
        self.assertIn(f'::error::Go test failed: {PKG} TestRun\n', result.stdout)

    def test_non_objects_and_wrong_types(self):
        self.assertEqual(run_summary('null\n[]\n' + event(Action='fail', Package=['x'])),
                         '::error::Go test failed without safe package or test diagnostics\n')

    def test_duplicate_and_escaped_injection(self):
        valid = event(Action='fail', Package=PKG, Test='TestRun')
        malicious = event(Action='fail', Package=PKG, Test='TestRun%0D%0A::error::injected')
        self.assertEqual(run_summary(valid + valid + malicious),
                         f'::error::Go test failed: {PKG} TestRun\n')

    def test_wrapper_success(self):
        wrapper = ROOT / 'scripts' / 'go-test-ci.sh'
        result = subprocess.run(['bash', str(wrapper), sys.executable, '-c', 'print("ok")'],
                                text=True, capture_output=True)
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stdout, 'ok\n')


if __name__ == '__main__':
    unittest.main()
