"""Every OpenTofu test belongs to one bounded process; any failure gates release."""
import importlib.util
from pathlib import Path
import re
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location('check_opentofu', Path(__file__).with_name('check-opentofu.py'))
qualifier = importlib.util.module_from_spec(spec)
spec.loader.exec_module(qualifier)


class OpenTofuPartitionTests(unittest.TestCase):
    def test_macos_runs_integration_suites_without_fetching_the_provider(self):
        with (patch.object(qualifier.sys, 'platform', 'darwin'),
              patch.object(qualifier, 'realize') as realize,
              patch.object(qualifier.subprocess, 'run', return_value=Mock(returncode=0)) as run):
            self.assertEqual(qualifier.main(), 0)
        realize.assert_not_called()
        suites = [call for call in run.call_args_list if call.args[0][:2] == ['go', 'test']]
        self.assertEqual({call.kwargs['cwd'].name for call in suites},
                         {'opentofu-up', 'opentofu-runtime-fixture'})
        for call in suites:
            self.assertEqual(call.kwargs['env']['C2OPS_TEST_NATIVE'], '1')
            self.assertTrue(call.kwargs['env']['C2OPS_TEST_GCS_EMULATOR'])

    def test_current_and_future_tests_run_once(self):
        tests = []
        for source in (qualifier.ROOT / 'opentofu-up').glob('*_test.go'):
            tests.extend(re.findall(r'^func (Test\w+)\([^)]*\*testing\.T\)', source.read_text(), re.M))
        self.assertIn('TestNativePackagedRecovery', tests)
        self.assertGreater(len(tests), 20)
        for name in [*tests, 'TestFutureAcceptance']:
            selected = sum(bool(re.search(pattern, name)) if flag == '-run' else not re.search(pattern, name)
                           for flag, pattern in qualifier.PARTITIONS)
            self.assertEqual(selected, 1, name)

    def test_each_failure_is_retained_without_dropping_other_suites(self):
        for failing in (None, 0, 1, 2):
            with self.subTest(failing=failing), patch.object(qualifier.subprocess, 'run') as run:
                run.side_effect = [Mock(returncode=int(i == failing)) for i in range(3)]
                self.assertEqual(qualifier.qualify({'fixture': 'env'}), int(failing is not None))
                self.assertEqual(run.call_count, 3)
                for call in run.call_args_list[:2]:
                    self.assertIn('-timeout=8m', call.args[0])
                    self.assertEqual(call.kwargs['env'], {'fixture': 'env'})


if __name__ == '__main__':
    unittest.main()
