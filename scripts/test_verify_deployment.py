"""Safety regressions for the opt-in Docker deployment drill (no daemon required)."""
import importlib.util
import io
import json
from pathlib import Path
import sys
import tarfile
import tempfile
import threading
import unittest
from unittest import mock
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

spec = importlib.util.spec_from_file_location(
    "verify_deployment", Path(__file__).with_name("verify-deployment.py")
)
deployment = importlib.util.module_from_spec(spec)
spec.loader.exec_module(deployment)


class DeploymentSafetyTests(unittest.TestCase):
    def test_work_directory_is_exclusive_and_private(self):
        with tempfile.TemporaryDirectory() as parent:
            path = Path(parent) / "owned"
            self.assertEqual(deployment.create_work_dir(path), path.resolve())
            self.assertEqual(path.stat().st_mode & 0o777, 0o700)
            marker = path / "keep"
            marker.write_bytes(b"existing data")
            with self.assertRaises(FileExistsError):
                deployment.create_work_dir(path)
            self.assertEqual(marker.read_bytes(), b"existing data")

    def test_symlink_target_and_missing_parent_do_not_create_data(self):
        with tempfile.TemporaryDirectory() as parent:
            parent = Path(parent)
            target = parent / "target"
            target.mkdir()
            link = parent / "link"
            link.symlink_to(target, target_is_directory=True)
            with self.assertRaises(FileExistsError):
                deployment.create_work_dir(link)
            with self.assertRaises(FileNotFoundError):
                deployment.create_work_dir(parent / "missing" / "new")
            self.assertEqual(list(target.iterdir()), [])

    def test_image_reference_cannot_be_an_option_or_shell_expression(self):
        valid = [
            "intelligence-substrate:ci", "postgres:17-alpine",
            "localhost:5001/team/platform:Build-1",
            "registry.example/team/platform@sha256:" + "a" * 64,
        ]
        invalid = ["", "-bad", "image name", "image\nnext", "$(id)",
                   "image;id", "repo/../image", "image@sha256:short", "https://registry/image"]
        for value in valid:
            with self.subTest(valid=value):
                self.assertEqual(deployment.validate_image(value), value)
        for value in invalid:
            with self.subTest(invalid=value), self.assertRaises(ValueError):
                deployment.validate_image(value)

    def test_command_failure_redacts_arguments_output_and_exception(self):
        with tempfile.TemporaryDirectory() as directory:
            runner = deployment.CommandRunner(Path(directory))
            secret = "sensitive-fixture-password-12345"
            runner.redactor.add(secret)
            with self.assertRaises(deployment.CommandError) as failure:
                runner.run([sys.executable, "-c",
                            "import sys;print(sys.argv[1]);print(sys.argv[1],file=sys.stderr);sys.exit(7)", secret])
            serialized = json.dumps(runner.commands) + str(failure.exception)
            self.assertNotIn(secret, serialized)
            self.assertIn("[REDACTED]", serialized)
            self.assertEqual(runner.commands[0]["exitCode"], 7)
            self.assertEqual(json.loads((Path(directory) / "commands.json").read_text()), runner.commands)

    def test_timeout_records_redacted_output_and_terminates_child(self):
        with tempfile.TemporaryDirectory() as directory:
            runner = deployment.CommandRunner(Path(directory))
            secret = "timeout-sensitive-token-12345"
            runner.redactor.add(secret)
            with self.assertRaises(deployment.CommandError):
                runner.run([sys.executable, "-c", "import time;print('" + secret + "',flush=True);time.sleep(30)"], timeout=0.3)
            self.assertEqual(runner.commands[0]["exitCode"], 124)
            self.assertNotIn(secret, json.dumps(runner.commands))

    def test_missing_executable_is_reported_without_losing_command_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            runner = deployment.CommandRunner(Path(directory))
            with mock.patch.object(deployment.subprocess, "Popen", side_effect=FileNotFoundError("missing-program")):
                with self.assertRaises(deployment.CommandError):
                    runner.run([str(Path(directory) / "missing-program")])
            self.assertEqual(runner.commands[0]["exitCode"], 127)
            self.assertIn("missing-program", runner.commands[0]["stderr"])

    def test_private_binary_output_is_returned_but_not_logged(self):
        with tempfile.TemporaryDirectory() as directory:
            runner = deployment.CommandRunner(Path(directory))
            raw = b"private-backup-body\x00\xff"
            result = runner.run([sys.executable, "-c", "import sys;sys.stdout.buffer.write(sys.stdin.buffer.read())"], input=raw, private_output=True)
            self.assertEqual(result.stdout, raw)
            self.assertNotIn("private-backup-body", json.dumps(runner.commands))
            self.assertEqual(runner.commands[0]["stdout"]["bytes"], len(raw))

    def test_tls_proxy_verifies_certificate_and_forwards_only_fixed_upstream(self):
        received = []

        class Upstream(BaseHTTPRequestHandler):
            def do_POST(self):
                body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
                received.append((self.path, self.headers.get("Origin"), body))
                self.send_response(201)
                self.send_header("Set-Cookie", "is_session=fixture; Secure; HttpOnly; SameSite=Strict")
                self.end_headers()
                self.wfile.write(b"forwarded:" + body)

            def log_message(self, *_):
                pass

        server = ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            with tempfile.TemporaryDirectory() as directory:
                runner = deployment.CommandRunner(Path(directory))
                with deployment.TLSProxy(runner, server.server_port) as proxy:
                    with self.assertRaises(URLError):
                        urlopen(proxy.origin + "/healthz", timeout=5)
                    request = Request(proxy.origin + "/api/check", data=b"body", headers={"Origin": proxy.origin}, method="POST")
                    with urlopen(request, context=proxy.client_context, timeout=5) as response:
                        self.assertEqual(response.status, 201)
                        self.assertEqual(response.read(), b"forwarded:body")
                        self.assertIn("Secure", response.headers["Set-Cookie"])
                    with self.assertRaises(HTTPError) as failure:
                        urlopen(Request(proxy.origin + "/api/check", data=b"bad", headers={"Host": "elsewhere.example"}), context=proxy.client_context, timeout=5)
                    self.assertEqual(failure.exception.code, 421)
                    failure.exception.close()
                self.assertEqual(received, [("/api/check", proxy.origin, b"body")])
                self.assertEqual((Path(directory) / "tls-key.pem").stat().st_mode & 0o777, 0o600)
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=5)

    def test_corrupt_archive_preserves_structure_but_changes_exact_artifact(self):
        buffer = io.BytesIO()
        with tarfile.open(fileobj=buffer, mode="w:gz") as archive:
            for name, body in [("state.json", b'{"version":1}'), ("artifacts/ab/" + "a" * 64, b"artifact-body"), ("repos/repo.git/HEAD", b"ref: refs/heads/main\n")]:
                info = tarfile.TarInfo(name)
                info.size = len(body)
                archive.addfile(info, io.BytesIO(body))
        damaged = deployment.corrupt_artifact_archive(buffer.getvalue())
        with tarfile.open(fileobj=io.BytesIO(damaged), mode="r:gz") as archive:
            self.assertEqual(archive.extractfile("state.json").read(), b'{"version":1}')
            self.assertNotEqual(archive.extractfile("artifacts/ab/" + "a" * 64).read(), b"artifact-body")
            self.assertEqual(archive.extractfile("repos/repo.git/HEAD").read(), b"ref: refs/heads/main\n")
        with self.assertRaises(ValueError):
            empty = io.BytesIO()
            with tarfile.open(fileobj=empty, mode="w:gz"):
                pass
            deployment.corrupt_artifact_archive(empty.getvalue())


if __name__ == "__main__":
    unittest.main()
