"""Conditional request/results agree in the direct and Draft 2020-12 validators."""
import copy
import itertools
import json
import os
from pathlib import Path
import unittest
from unittest.mock import patch

from jsonschema import Draft202012Validator, FormatChecker
from npm_publish_fixture import op

SOURCE = Path(os.environ.get('C2OPS_NPM_SOURCE', Path(__file__).resolve().parents[1] / 'npm_publish'))


class NpmContractTests(unittest.TestCase):
    def agrees(self, value, schema, valid):
        with self.subTest(value=value):
            errors = list(Draft202012Validator(schema, format_checker=FormatChecker()).iter_errors(value))
            self.assertEqual(not errors, valid, str(errors[:1]))
            if valid:
                op.validate(value, schema)
            else:
                with self.assertRaises(op.Failure):
                    op.validate(value, schema)

    def test_phase_request_matrix_and_rejection_before_effects(self):
        base = copy.deepcopy(op.SCHEMA['examples'][0])
        retained = copy.deepcopy(op.SCHEMA['$defs']['result']['examples'][0]['output']['files'][0])
        missing = object()
        for phase, token, resume, desc in itertools.product(
                [missing, 'prepare', 'publish', 'combined', None, 1],
                [False, True], [missing, False, True, 'true'], [False, True]):
            request = copy.deepcopy(base)
            if phase is not missing:
                request['phase'] = phase
            if not token:
                request.pop('registry_token_file')
            if resume is not missing:
                request['resume'] = resume
            if desc:
                request['retained_tarball'] = retained
            valid = ((phase is missing and token or phase == 'prepare' and not token or
                      phase == 'publish' and token and resume is True)
                     and (resume is missing or type(resume) is bool)
                     and desc == (resume is True))
            self.agrees(request, op.SCHEMA, bool(valid))
            if not valid:
                with patch.object(op, 'run', side_effect=AssertionError('subprocess before rejection')), \
                     patch.object(op, 'Network', side_effect=AssertionError('network before rejection')), \
                     self.assertRaises(op.Failure):
                    op.execute(request)
        for token_value in (None, ''):
            request = {**base, 'phase': 'prepare', 'registry_token_file': token_value}
            self.agrees(request, op.SCHEMA, False)

    def test_conditional_results_preserve_legacy_keys(self):
        base = op.SCHEMA['$defs']['result']['examples'][0]
        schema = {'$ref': '#/$defs/result', '$defs': op.SCHEMA['$defs']}
        missing = object()
        for phase, outcome, observed in itertools.product(
                [missing, 'prepare', 'publish', None, 'bad'],
                ['published', 'reconciled', 'not_attempted'], [False, True]):
            result = copy.deepcopy(base)
            out = result['output']
            if phase is not missing:
                out['phase'] = phase
            out['registry']['outcome'] = outcome
            if not observed:
                out['registry'].pop('observed_integrity')
            valid = ((phase == 'prepare' and outcome == 'not_attempted' and not observed) or
                     ((phase is missing or phase == 'publish') and
                      outcome in ('published', 'reconciled') and observed))
            self.agrees(result, schema, valid)
        for field in ('files', 'install_check', 'provenance'):
            result = copy.deepcopy(base)
            result['output'].pop(field)
            self.agrees(result, schema, False)
        result = copy.deepcopy(base)
        result['output']['install_check']['status'] = 'skipped'
        self.agrees(result, schema, False)

    def test_source_generated_contracts_and_examples(self):
        source = json.loads((SOURCE / 'op.yaml').read_text())
        generated = json.loads((SOURCE / 'op.json').read_text())
        self.assertEqual(source['input_schema'], op.SCHEMA)
        self.assertEqual(source['output_schema']['$defs'], op.SCHEMA['$defs'])
        expected = copy.deepcopy(source)
        expected['command'] = ['bin/npm-publish']
        expected.pop('dependencies')
        self.assertEqual(expected, generated)
        for schema in (source['input_schema'], source['output_schema']):
            Draft202012Validator.check_schema(schema)
        for example in op.SCHEMA['examples']:
            self.agrees(example, op.SCHEMA, True)
        for example in op.SCHEMA['$defs']['result']['examples']:
            self.agrees(example, source['output_schema'], True)
