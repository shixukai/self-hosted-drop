"""Exercise the CLI against a private in-process HTTP fixture, without credentials."""
import json
import os
import pathlib
import socket
import subprocess
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


SCRIPT = pathlib.Path(__file__).resolve().parents[1] / 'scripts' / 'drop-upload.py'


class UploadFixture:
    def __init__(self, statuses, publication_status='APPLIED', upload_failure=False):
        self.statuses = statuses
        self.publication_status = publication_status
        self.upload_failure = upload_failure
        self.uploads = []
        self.publication_keys = []
        fixture = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def respond(self, status, value):
                body = json.dumps(value).encode()
                self.send_response(status)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Content-Length', str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def do_GET(self):
                if self.path.startswith('/api/v1/sites?'):
                    self.respond(200, {'items': [{'id': 'site-test', 'slug': 'test-site', 'generation': 7, 'url': 'http://test.drop.localhost'}]})
                elif self.path.startswith('/api/v1/deployments/'):
                    index = int(self.path.rsplit('-', 1)[1])
                    self.respond(200, fixture.deployment(index))
                elif self.path.startswith('/api/v1/publications/'):
                    self.respond(200, {'id': 'publication-test', 'status': fixture.publication_status, 'errorCode': 'GATEWAY_FAILED' if fixture.publication_status == 'FAILED' else ''})
                else:
                    self.respond(404, {'code': 'NOT_FOUND'})

            def do_POST(self):
                body = self.rfile.read(int(self.headers['Content-Length']))
                if self.path.endswith('/deployments'):
                    fixture.uploads.append(self.headers['Idempotency-Key'])
                    if fixture.upload_failure is True or fixture.upload_failure == len(fixture.uploads):
                        self.connection.shutdown(socket.SHUT_RDWR)
                        self.connection.close()
                        return
                    self.respond(202, fixture.deployment(len(fixture.uploads)-1))
                elif self.path.endswith('/publications'):
                    fixture.publication_keys.append(self.headers['Idempotency-Key'])
                    self.respond(202, {'id': 'publication-test', 'status': 'PENDING', 'deploymentId': json.loads(body)['deploymentId']})
                else:
                    self.respond(404, {'code': 'NOT_FOUND'})

        self.server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def close(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()

    def deployment(self, index):
        status = self.statuses[min(index, len(self.statuses)-1)]
        return {'id': 'deployment-'+str(index), 'status': status, 'errorCode': 'UNSAFE_PATH' if status == 'FAILED' else '', 'report': {'inspectionReportHash': 'test-hash', 'warnings': []}}

    def run(self, *flags):
        with tempfile.TemporaryDirectory(prefix='drop-cli-test-') as directory:
            artifact = pathlib.Path(directory) / 'index.html'
            artifact.write_text('<html>fixture</html>')
            environment = os.environ.copy()
            environment['DROP_TOKEN'] = 'test-only-placeholder'
            return subprocess.run([sys.executable, str(SCRIPT), str(artifact), '--base', 'http://127.0.0.1:'+str(self.server.server_port), '--site', 'test-site', *flags], env=environment, text=True, capture_output=True, timeout=10)


class DropUploadCLITest(unittest.TestCase):
    def fixture(self, *args, **kwargs):
        fixture = UploadFixture(*args, **kwargs)
        self.addCleanup(fixture.close)
        return fixture

    def test_default_failed_replays_keep_generated_key_and_report_context(self):
        fixture = self.fixture(['FAILED'])
        first, second = fixture.run(), fixture.run()
        self.assertEqual(first.returncode, 3)
        self.assertEqual(second.returncode, 3)
        self.assertEqual(fixture.uploads[0], fixture.uploads[1])
        result = json.loads(first.stderr)
        self.assertEqual(result['deploymentId'], 'deployment-0')
        self.assertEqual(result['status'], 'FAILED')
        self.assertIsNone(result['publicationId'])
        self.assertEqual(result['idempotencyKey'], fixture.uploads[0])

    def test_retry_creates_one_fresh_bounded_key_after_failure_or_cancellation(self):
        for status in ('FAILED', 'CANCELED'):
            with self.subTest(status=status):
                fixture = self.fixture([status, 'READY'])
                result = fixture.run('--retry-failed')
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(len(fixture.uploads), 2)
                self.assertNotEqual(*fixture.uploads)
                self.assertLessEqual(len(fixture.uploads[1]), 128)
                self.assertEqual(json.loads(result.stdout)['deploymentId'], 'deployment-1')

    def test_retry_is_bounded_when_second_attempt_fails(self):
        fixture = self.fixture(['FAILED'])
        result = fixture.run('--retry-failed')
        self.assertEqual(result.returncode, 3)
        self.assertEqual(len(fixture.uploads), 2)
        self.assertEqual(json.loads(result.stderr)['deploymentId'], 'deployment-1')

    def test_retry_does_not_repeat_entry_selection_or_unknown_transport_result(self):
        for statuses, transport_failure in ((['AWAITING_INPUT'], False), (['READY'], True)):
            with self.subTest(statuses=statuses, transport_failure=transport_failure):
                fixture = self.fixture(statuses, upload_failure=transport_failure)
                result = fixture.run('--retry-failed')
                self.assertEqual(result.returncode, 3)
                self.assertEqual(len(fixture.uploads), 1)
                self.assertEqual(json.loads(result.stderr)['idempotencyKey'], fixture.uploads[0])

    def test_explicit_key_and_retry_option_are_rejected_before_http(self):
        fixture = self.fixture(['READY'])
        result = fixture.run('--idempotency-key', 'explicit-test-key', '--retry-failed')
        self.assertEqual(result.returncode, 2)
        self.assertIn('cannot be combined', result.stderr)
        self.assertEqual(fixture.uploads, [])

    def test_unknown_retry_result_reports_the_fresh_key_without_repeating(self):
        fixture = self.fixture(['FAILED'], upload_failure=2)
        result = fixture.run('--retry-failed')
        self.assertEqual(result.returncode, 3)
        self.assertEqual(len(fixture.uploads), 2)
        error = json.loads(result.stderr)
        self.assertEqual(error['status'], 'UNKNOWN')
        self.assertIsNone(error['deploymentId'])
        self.assertEqual(error['idempotencyKey'], fixture.uploads[1])

    def test_publication_failure_preserves_generation_key_and_diagnostic_ids(self):
        fixture = self.fixture(['READY'], publication_status='FAILED')
        result = fixture.run('--publish', '--retry-failed')
        self.assertEqual(result.returncode, 3)
        self.assertEqual(len(fixture.uploads), 1)
        self.assertEqual(fixture.publication_keys, ['publish-'+fixture.uploads[0]+'-7'])
        error = json.loads(result.stderr)
        self.assertEqual(error['deploymentId'], 'deployment-0')
        self.assertEqual(error['publicationId'], 'publication-test')
        self.assertEqual(error['status'], 'FAILED')


if __name__ == '__main__':
    unittest.main()
