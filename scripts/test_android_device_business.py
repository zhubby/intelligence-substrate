import contextlib
import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
import json
import os
from pathlib import Path
import socket
import signal
import shutil
import ssl
import subprocess
import tempfile
import threading
import time
import unittest
import xml.etree.ElementTree as ET

from test_android_device_quality import PROFILE
from android_quality_common import tool_command


REPO = Path(__file__).resolve().parents[1]


class ReferenceHTTPS:
    """Real reference Go API, local TLS termination, no device/hardware simulation."""
    def __init__(self, root, binary, cert, key, mode='feedback'):
        self.root, self.binary, self.cert, self.key, self.mode = root, binary, cert, key, mode
        self.requests = []
        self.redirect = False
        self.unknown_write = False
        self.plaintext = 0

    def __enter__(self):
        self.root.mkdir()
        with socket.socket() as probe:
            probe.bind(('127.0.0.1', 0))
            port = probe.getsockname()[1]
        self.process = subprocess.Popen(tool_command([self.binary]), stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL, start_new_session=True, env={**os.environ, 'APP_MODE': self.mode,
                'APP_LISTEN': '127.0.0.1:' + str(port), 'APP_STATE_FILE': str(self.root / 'state.json'),
                'APP_SLOTS': json.dumps([{'id': 'first', 'startsAt': '2026-12-01T09:00:00+08:00', 'reserved': False}])})
        self.backend_port = port
        for _ in range(100):
            try:
                with socket.create_connection(('127.0.0.1', port), timeout=0.1):
                    break
            except OSError:
                if self.process.poll() is not None:
                    raise RuntimeError('reference backend exited')
                time.sleep(0.02)
        else:
            raise RuntimeError('reference backend did not listen')
        owner = self
        class Plain(BaseHTTPRequestHandler):
            def do_GET(self):
                owner.plaintext += 1
                self.send_response(200); self.end_headers()
            do_POST = do_GET
            def log_message(self, *args):
                pass
        self.trap = ThreadingHTTPServer(('127.0.0.1', 0), Plain)
        self.trap_thread = threading.Thread(target=self.trap.serve_forever, daemon=True)
        self.trap_thread.start()
        class Proxy(BaseHTTPRequestHandler):
            def do_GET(self):
                length = int(self.headers.get('Content-Length', '0'))
                body = self.rfile.read(length)
                owner.requests.append((self.command, self.path, body))
                if owner.redirect:
                    self.send_response(302)
                    self.send_header('Location', 'http://127.0.0.1:' + str(owner.trap.server_port) + self.path)
                    self.end_headers()
                    return
                connection = http.client.HTTPConnection('127.0.0.1', owner.backend_port, timeout=3)
                try:
                    connection.request(self.command, self.path, body=body, headers={'Content-Type': 'application/json'})
                    response = connection.getresponse()
                    payload, status = response.read(), response.status
                finally:
                    connection.close()
                if owner.unknown_write and self.command == 'POST':
                    owner.unknown_write = False
                    status, payload = 502, b'{"error":"upstream result unknown"}'
                self.send_response(status)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Content-Length', str(len(payload)))
                self.end_headers(); self.wfile.write(payload)
            do_POST = do_GET
            def log_message(self, *args):
                pass
        self.server = ThreadingHTTPServer(('127.0.0.1', 0), Proxy)
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(self.cert, self.key)
        self.server.socket = context.wrap_socket(self.server.socket, server_side=True)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.url = 'https://localhost:' + str(self.server.server_port)
        return self

    def __exit__(self, *args):
        for server, thread in [(self.server, self.thread), (self.trap, self.trap_thread)]:
            server.shutdown(); server.server_close(); thread.join(timeout=3)
        try:
            os.killpg(self.process.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        try:
            self.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            os.killpg(self.process.pid, signal.SIGKILL); self.process.wait(timeout=5)


class NativeUIBoundary:
    """Replaces the unavailable Android UI only; all business state is real Go/TLS."""
    def __init__(self, api, mode='feedback', tamper=False):
        self.api, self.mode, self.tamper = api, mode, tamper
        self.fields, self.message = {}, ''
        self.token = None
        self.initial = self.api.request('/api/feedback' if mode == 'feedback' else '/api/slots')

    def find(self, text, editable=False):
        actual = self.fields.get(text, '') if editable else self.message
        if text == '请保存取消凭据：' and self.token:
            actual = text + self.token
        if text == '可以预约' and not self.api.request('/api/slots')[0]['reserved']:
            actual = text
        if text == '已预约' and self.api.request('/api/slots')[0]['reserved']:
            actual = text
        if text not in actual and not editable:
            records = self.api.request('/api/feedback') if self.mode == 'feedback' else []
            if not any(text in item['title'] for item in records):
                raise AssertionError('native UI did not show ' + text)
        return ET.Element('node', {'text': actual})

    def tap(self, text, editable=False):
        if text in {'刷新', '已保存'}:
            return
        try:
            if text == '提交反馈':
                if not self.fields.get('反馈标题'):
                    self.message = '请输入反馈标题。'
                    return
                self.api.request('/api/feedback', {'title': self.fields['反馈标题'],
                    'content': 'tampered' if self.tamper else self.fields.get('详细说明', '')}, expected=201)
                self.message = self.fields['反馈标题']
            elif text == '预约':
                result = self.api.request('/api/bookings', {'slotId': 'first'}, expected=201)
                self.token = result['bookingToken']
                self.fields['取消凭据'] = self.token
                self.message = '请保存取消凭据：' + self.token
            elif text == '取消我的预约':
                self.api.request('/api/bookings/cancel', {'bookingToken': self.fields['取消凭据']})
                self.message = '可以预约'
        except RuntimeError as error:
            self.message = str(error)

    def enter(self, text, value, replace=False):
        self.fields[text] = value

    def launch(self):
        pass

    def capture(self, name):
        pass


class AndroidDeviceBusinessTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory()
        cls.root = Path(cls.temp.name)
        cls.binary = cls.root / 'reference-api'
        compile_result = subprocess.run(tool_command(['go', 'build', '-trimpath', '-o', str(cls.binary), '.']),
            cwd=REPO / 'blueprints/flutter-go/backend', capture_output=True, timeout=60)
        if compile_result.returncode:
            raise RuntimeError(compile_result.stderr.decode())
        cls.cert, cls.key = cls.root / 'cert.pem', cls.root / 'key.pem'
        openssl = '/opt/homebrew/opt/openssl@3/bin/openssl' if Path('/opt/homebrew/opt/openssl@3/bin/openssl').is_file() else shutil.which('openssl')
        if not openssl:
            raise RuntimeError('OpenSSL is required for actual local TLS contract verification')
        result = subprocess.run(tool_command([openssl, 'req', '-x509',
            '-newkey', 'rsa:2048', '-nodes', '-days', '1', '-subj', '/CN=localhost',
            '-addext', 'subjectAltName=DNS:localhost,IP:127.0.0.1', '-keyout', str(cls.key), '-out', str(cls.cert)]),
            capture_output=True, timeout=30)
        if result.returncode:
            raise RuntimeError(result.stderr.decode())

    @classmethod
    def tearDownClass(cls):
        cls.temp.cleanup()

    def setUp(self):
        spec = importlib.util.spec_from_file_location('android_device_quality', REPO / 'scripts/android_device_quality.py')
        self.module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.module)
        self.assertTrue(callable(getattr(self.module, 'HTTPSAPI', None)), 'dedicated verified HTTPS API client is missing')
        self.assertTrue(callable(getattr(self.module, 'run_business', None)), 'independent UI/API business verifier is missing')

    def backend(self, mode='feedback'):
        return ReferenceHTTPS(self.root / (self.id().split('.')[-1] + '-' + mode), self.binary, self.cert, self.key, mode)

    def api(self, server):
        return self.module.HTTPSAPI(server.url, ca_file=self.cert)

    def test_https_trust_is_checked_and_redirect_never_reaches_plaintext(self):
        with self.backend() as server:
            with self.assertRaises(Exception):
                self.module.HTTPSAPI(server.url).request('/api/health')
            api = self.api(server)
            self.assertEqual(api.request('/api/health'), {'status': 'ok', 'mode': 'feedback'})
            server.redirect = True
            with self.assertRaises(RuntimeError):
                api.request('/api/feedback', {'title': 'never-plaintext'}, expected=201)
            self.assertEqual(server.plaintext, 0)

    def test_unknown_post_is_not_replayed_after_backend_commits(self):
        with self.backend() as server:
            api = self.api(server)
            server.unknown_write = True
            with self.assertRaises(RuntimeError):
                api.request('/api/feedback', {'title': 'once', 'content': 'result unknown'}, expected=201)
            records = api.request('/api/feedback')
            self.assertEqual(len(records), 1)
            self.assertEqual(records[0]['title'], 'once')
            self.assertEqual(len([r for r in server.requests if r[0] == 'POST']), 1)

    def test_feedback_checks_real_api_receipt_and_relaunch_state(self):
        with self.backend() as server:
            api = self.api(server)
            report = {'checks': [], 'runId': 'test-feedback-123'}
            self.module.run_business(NativeUIBoundary(api), api, PROFILE, report)
            records = api.request('/api/feedback')
            self.assertEqual(len(records), 1)
            self.assertIn('test-feedback-123', records[0]['title'])
            self.assertTrue(report['businessVerified'])
            self.assertFalse(report['backendRestartVerified'])

    def test_mismatched_ui_write_fails_independent_business_verification(self):
        with self.backend() as server:
            api = self.api(server)
            report = {'checks': [], 'runId': 'tamper-123'}
            with self.assertRaises((ValueError, AssertionError)):
                self.module.run_business(NativeUIBoundary(api, tamper=True), api, PROFILE, report)
            self.assertNotIn('businessVerified', report)
            self.assertEqual(len(api.request('/api/feedback')), 1)

    def test_booking_conflict_and_cancellation_ownership_use_real_server(self):
        with self.backend('booking') as server:
            api = self.api(server)
            report = {'checks': [], 'runId': 'test-booking-123'}
            profile = {**PROFILE, 'mode': 'booking', 'slotId': 'first'}
            self.module.run_business(NativeUIBoundary(api, 'booking'), api, profile, report)
            self.assertFalse(api.request('/api/slots')[0]['reserved'])
            persisted = json.loads((server.root / 'state.json').read_text())
            self.assertEqual(persisted['bookings'], {})
            self.assertTrue(report['businessVerified'])

    def test_nonempty_feedback_backend_is_not_mutated(self):
        with self.backend() as server:
            api = self.api(server)
            existing = api.request('/api/feedback', {'title': 'existing'}, expected=201)
            with self.assertRaises(ValueError):
                self.module.run_business(NativeUIBoundary(api), api, PROFILE, {'checks': [], 'runId': 'new'})
            self.assertEqual(api.request('/api/feedback'), [existing])


if __name__ == '__main__':
    unittest.main()
