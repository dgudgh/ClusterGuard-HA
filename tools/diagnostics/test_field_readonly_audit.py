import importlib.util
from pathlib import Path
import unittest


spec = importlib.util.spec_from_file_location('field_audit', Path(__file__).with_name('field-readonly-audit.py'))
audit = importlib.util.module_from_spec(spec)
spec.loader.exec_module(audit)


class FieldAuditTests(unittest.TestCase):
    def tearDown(self):
        audit.secrets.clear()

    def test_sensitive_fields_and_free_text_are_redacted(self):
        audit.secrets.append('fixture-only-secret')
        source = {'password': 'abc', 'token_env': 'ENV_NAME', 'nested': [{'primary_conninfo': 'host=db password=abc'}],
                  'message': 'authorization: Bearer abc\nconnected to db\nDSN=postgres://u:abc@host/db\npassfile=/private/path\nerror fixture-only-secret'}
        masked = str(audit.scrub(source))
        for private in ('abc', 'ENV_NAME', '/private/path', 'fixture-only-secret'):
            self.assertNotIn(private, masked)
        self.assertIn('connected to db', masked)

    def test_upstream_summary_excludes_auth_and_paths(self):
        original = audit.run
        try:
            audit.run = lambda args: {'exit_code': 0, 'stdout': "host=db port=55432 application_name=node password='sample secret' passfile=/private/path"}
            result = audit.pg_connection_summary(['psql'])
            self.assertEqual(result['host'], 'db')
            self.assertTrue(result['inline_auth_present'])
            self.assertTrue(result['auth_file_configured'])
            self.assertNotIn('sample secret', str(result))
            self.assertNotIn('/private/path', str(result))
        finally:
            audit.run = original

    def test_invalid_upstream_does_not_export_raw_output(self):
        original = audit.run
        try:
            audit.run = lambda args: {'exit_code': 0, 'stdout': "password='unterminated-private"}
            self.assertNotIn('unterminated-private', str(audit.pg_connection_summary(['psql'])))
        finally:
            audit.run = original


if __name__ == '__main__':
    unittest.main()
