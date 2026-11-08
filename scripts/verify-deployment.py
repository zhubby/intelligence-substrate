#!/usr/bin/env python3
"""Opt-in, isolated production-configuration Compose and recovery acceptance.

Images must already be cached. The verifier never pulls, builds, changes daemon
configuration, uses repository secrets, or calls a model/release connector.
Only result.json and commands.json are intended for sharing. All other files
may contain private fixture credentials, a TLS key or an identity backup.
"""
import argparse
from datetime import datetime, timezone
import hashlib
from http import client as http_client
from http.cookiejar import CookieJar
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import io
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import signal
import socket
import ssl
import subprocess
import sys
import tarfile
import threading
import time
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode, urlsplit
from urllib.request import build_opener, HTTPSHandler, HTTPCookieProcessor, ProxyHandler, Request


def digest(body):
    return hashlib.sha256(body).hexdigest()


def create_work_dir(path):
    path = Path(path).absolute()
    # mkdir is exclusive even for a dangling symlink. Do not resolve the target
    # first, which could otherwise turn a symlink into an unrelated directory.
    target = path.parent.resolve(strict=True) / path.name
    target.mkdir(mode=0o700)
    target.chmod(0o700)
    return target


def validate_image(value):
    if (not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._:/@-]{0,511}", value)
            or "://" in value or any(x in ("", ".", "..") for x in value.split("/"))
            or value.count("@") > 1):
        raise ValueError("image must be a Docker image reference, not an option, URL or expression")
    if "@" in value and not re.fullmatch(r"sha256:[a-f0-9]{64}", value.split("@", 1)[1]):
        raise ValueError("image digest must be an exact sha256 reference")
    return value


class Redactor:
    def __init__(self):
        self.values = set()

    def add(self, value):
        if value:
            self.values.add(str(value))

    def text(self, value):
        if isinstance(value, bytes):
            value = value.decode("utf-8", errors="replace")
        for secret in sorted(self.values, key=len, reverse=True):
            value = value.replace(secret, "[REDACTED]")
        return value


class CommandError(RuntimeError):
    pass


class CommandRunner:
    def __init__(self, root, env=None):
        self.root = Path(root)
        self.env = dict(os.environ if env is None else env)
        self.commands = []
        self.redactor = Redactor()

    def run(self, args, *, input=None, timeout=120, expected=0,
            private_output=False, env=None):
        args = list(map(str, args))
        # RTK is required in the author's workspace, but is not a dependency of
        # the exported product or GitHub-hosted CI environment.
        command = (["rtk", "proxy"] if shutil.which("rtk") and args[0] != "rtk" else []) + args
        started = time.monotonic()
        interrupted = False
        try:
            process = subprocess.Popen(command, stdin=subprocess.PIPE if input is not None else subprocess.DEVNULL,
                                       stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                       env=self.env if env is None else env, start_new_session=True)
        except OSError as error:
            stdout, stderr, code = b"", str(error).encode(), 127
        else:
            try:
                stdout, stderr = process.communicate(input=input, timeout=timeout)
                code = process.returncode
            except (subprocess.TimeoutExpired, KeyboardInterrupt) as error:
                os.killpg(process.pid, signal.SIGKILL)
                stdout, stderr = process.communicate()
                interrupted = isinstance(error, KeyboardInterrupt)
                code = 130 if interrupted else 124
        record = {
            "command": [self.redactor.text(x) for x in command], "exitCode": code,
            "elapsedSeconds": round(time.monotonic() - started, 3),
            "stdout": {"bytes": len(stdout), "sha256": digest(stdout)} if private_output else self.redactor.text(stdout),
            "stderr": self.redactor.text(stderr),
        }
        self.commands.append(record)
        self.save()
        if interrupted:
            raise KeyboardInterrupt
        if expected is not None and code != expected:
            raise CommandError(self.redactor.text(
                f"command {len(self.commands)} exited {code}, expected {expected}: "
                + " ".join(command) + "\n" + stderr.decode("utf-8", errors="replace")[-4000:]))
        return subprocess.CompletedProcess(command, code, stdout, stderr)

    def save(self):
        # Re-redact previous records when a new runtime secret is registered.
        body = self.redactor.text(json.dumps(self.commands, indent=2))
        self.commands = json.loads(body)
        (self.root / "commands.json").write_text(body + "\n")


HOP_HEADERS = {"connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
               "te", "trailer", "transfer-encoding", "upgrade", "content-length"}


class TLSProxy:
    """Fixture ingress: loopback only, exact Host, fixed loopback upstream."""
    def __init__(self, runner, upstream_port):
        self.runner = runner
        self.upstream_port = upstream_port
        self.cert = runner.root / "tls-cert.pem"
        self.key = runner.root / "tls-key.pem"
        self.server = None

    def __enter__(self):
        self.runner.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
                         "-keyout", self.key, "-out", self.cert, "-days", "1",
                         "-subj", "/CN=localhost", "-addext", "subjectAltName=DNS:localhost,IP:127.0.0.1"])
        self.key.chmod(0o600)
        proxy = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def forward(self):
                parsed = urlsplit(self.path)
                if self.headers.get("Host") != urlsplit(proxy.origin).netloc:
                    self.send_error(421, "unexpected host")
                    return
                if not self.path.startswith("/") or parsed.scheme or parsed.netloc:
                    self.send_error(400, "origin-form request required")
                    return
                if self.headers.get("Transfer-Encoding"):
                    self.send_error(501, "fixture proxy requires a content length")
                    return
                try:
                    length = int(self.headers.get("Content-Length", "0"))
                except ValueError:
                    self.send_error(400)
                    return
                if length < 0 or length > 513 * 1024 * 1024:
                    self.send_error(413)
                    return
                body = self.rfile.read(length) if length else None
                headers = {k: v for k, v in self.headers.items() if k.lower() not in HOP_HEADERS}
                headers["Host"] = urlsplit(proxy.origin).netloc
                connection = http_client.HTTPConnection("127.0.0.1", proxy.upstream_port, timeout=15)
                try:
                    connection.request(self.command, self.path, body=body, headers=headers)
                    response = connection.getresponse()
                    payload = response.read()
                    self.send_response(response.status)
                    for key, value in response.getheaders():
                        if key.lower() not in HOP_HEADERS:
                            self.send_header(key, value)
                    self.send_header("Content-Length", str(len(payload)))
                    self.send_header("Connection", "close")
                    self.end_headers()
                    if self.command != "HEAD":
                        self.wfile.write(payload)
                except (OSError, http_client.HTTPException):
                    self.send_error(502, "fixture upstream unavailable")
                finally:
                    connection.close()
                    self.close_connection = True

            do_GET = do_HEAD = do_POST = forward

            def log_message(self, *_):
                pass

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.origin = f"https://127.0.0.1:{self.server.server_port}"
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(self.cert, self.key)
        self.server.socket = context.wrap_socket(self.server.socket, server_side=True)
        self.client_context = ssl.create_default_context(cafile=str(self.cert))
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        return self

    def __exit__(self, *_):
        if self.server:
            self.server.shutdown()
            self.server.server_close()
            self.thread.join(timeout=5)


class Client:
    def __init__(self, proxy):
        self.proxy = proxy
        self.jar = CookieJar()
        self.opener = build_opener(ProxyHandler({}), HTTPSHandler(context=proxy.client_context), HTTPCookieProcessor(self.jar))
        self.csrf = ""

    def request(self, method, path, data=None, *, expected=200, headers=None, csrf=True, raw=False):
        request_headers = {"Origin": self.proxy.origin}
        if csrf and self.csrf:
            request_headers["X-CSRF-Token"] = self.csrf
        if isinstance(data, dict):
            data = json.dumps(data).encode()
            request_headers["Content-Type"] = "application/json"
        request_headers.update(headers or {})
        request = Request(self.proxy.origin + path, data=data, headers=request_headers, method=method)
        try:
            response = self.opener.open(request, timeout=20)
        except HTTPError as error:
            response = error
        with response:
            body = response.read()
            status = response.status
            response_headers = response.headers
        for cookie in self.jar:
            self.proxy.runner.redactor.add(cookie.value)
        if status != expected:
            raise RuntimeError(f"{method} {path.split('?')[0]} returned {status}, expected {expected}")
        return (body if raw else json.loads(body)), response_headers

    def login(self, email, password):
        session, headers = self.request("POST", "/api/v1/auth/login", {"email": email, "password": password})
        self.csrf = session["csrf"]
        self.proxy.runner.redactor.add(self.csrf)
        cookie = headers.get("Set-Cookie", "")
        if not all(x in cookie for x in ("Secure", "HttpOnly", "SameSite=Strict")):
            raise RuntimeError("production session cookie lacks required attributes")
        return session

    def command(self, name, data, **options):
        return self.request("POST", "/api/v1/commands/" + name, data, **options)[0]


def corrupt_artifact_archive(body):
    output = io.BytesIO()
    changed = False
    with tarfile.open(fileobj=io.BytesIO(body), mode="r:gz") as source, tarfile.open(fileobj=output, mode="w:gz") as target:
        for info in source:
            content = source.extractfile(info).read() if info.isfile() else None
            if not changed and info.isfile() and info.name.startswith("artifacts/") and content:
                content = bytes([content[0] ^ 1]) + content[1:]
                changed = True
            target.addfile(info, io.BytesIO(content) if content is not None else None)
    if not changed:
        raise ValueError("backup has no nonempty artifact to corrupt")
    return output.getvalue()


def available_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


class DeploymentDrill:
    def __init__(self, runner, repo, platform_image, postgres_image):
        self.runner, self.root, self.repo = runner, runner.root, repo
        self.platform_image, self.postgres_image = platform_image, postgres_image
        self.project = "isdrill-" + secrets.token_hex(6)
        self.overrides = []
        self.started = False
        self.checks = []
        self.images = {}
        self.port = available_port()
        self.email = "deployment-drill@example.test"
        self.password = ""
        self.database_password = ""
        self.observations = {}

    def check(self, name, **details):
        self.checks.append({"name": name, "passed": True, **details})
        print("PASS " + name, flush=True)

    def compose(self, *args, **options):
        command = ["docker", "compose", "--project-name", self.project,
                   "--project-directory", self.repo, "--env-file", self.root / ".env",
                   "-f", self.repo / "compose.yaml"]
        for override in self.overrides:
            command.extend(["-f", override])
        return self.runner.run(command + list(args), **options)

    def configure(self, proxy):
        # Exercise the real initializer in an isolated copy. Its root-relative
        # behavior must never create or replace the checkout's configuration.
        (self.root / "scripts").mkdir()
        shutil.copyfile(self.repo / "scripts/init-deploy.py", self.root / "scripts/init-deploy.py")
        shutil.copyfile(self.repo / ".env.example", self.root / ".env.example")
        self.runner.run([sys.executable, self.root / "scripts/init-deploy.py", "--public-url", proxy.origin, "--admin-email", self.email])
        self.password = (self.root / ".secrets/bootstrap-password").read_text()
        self.runner.redactor.add(self.password)
        values = dict(line.split("=", 1) for line in (self.root / ".env").read_text().splitlines() if line and not line.startswith("#") and "=" in line)
        self.database_password = values["IS_POSTGRES_PASSWORD"]
        self.runner.redactor.add(self.database_password)
        self.environment = {
            "IS_DATABASE_URL": self.dsn("substrate"), "IS_PUBLIC_URL": proxy.origin,
            "IS_BOOTSTRAP_EMAIL": self.email, "IS_BOOTSTRAP_PASSWORD_FILE": "/run/secrets/bootstrap_password",
            "IS_DEV_MODE": "false", "IS_DATA_DIR": "/data", "IS_STATIC_DIR": "/app/web",
            "IS_MODEL_BASE_URL": "", "IS_MODEL_NAME": "", "IS_MODEL_API_KEY": "", "IS_MODEL_API_KEY_FILE": "",
            "IS_OIDC_ISSUER": "", "IS_OIDC_CLIENT_ID": "", "IS_OIDC_CLIENT_SECRET": "", "IS_OIDC_CLIENT_SECRET_FILE": "",
        }
        override = {
            "services": {
                "database": {"image": self.postgres_image, "restart": "no"},
                "platform": {"image": self.platform_image, "restart": "no", "environment": self.environment},
            },
            "secrets": {"bootstrap_password": {"file": str(self.root / ".secrets/bootstrap-password")}},
        }
        config = self.root / "compose.drill.json"
        config.write_text(json.dumps(override, indent=2) + "\n")
        self.overrides.append(config)
        # All Compose interpolation comes from this private explicit env file;
        # inherited IS_/COMPOSE_ variables are removed from the child environment.
        with (self.root / ".env").open("a") as file:
            file.write(f"\nIS_HTTP_PORT={self.port}\nIS_PLATFORM_IMAGE={self.platform_image}\nIS_POSTGRES_IMAGE={self.postgres_image}\n")
        normalized = json.loads(self.compose("config", "--format", "json", private_output=True).stdout)
        platform = normalized["services"]["platform"]
        if (platform["image"] != self.platform_image or platform["environment"]["IS_DEV_MODE"] != "false"
                or platform["environment"]["IS_MODEL_BASE_URL"] or platform["environment"]["IS_OIDC_ISSUER"]):
            raise RuntimeError("isolated production configuration was not applied")
        if any(port.get("host_ip") != "127.0.0.1" or port["published"] != str(self.port) for port in platform["ports"]):
            raise RuntimeError("platform port must be the task's loopback port")
        self.check("private-production-configuration", composeProject=self.project, origin=proxy.origin)

    def dsn(self, name):
        return f"postgres://substrate:{self.database_password}@database:5432/{name}?sslmode=disable"

    def wait_ready(self, proxy, seconds=90):
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            try:
                Client(proxy).request("GET", "/healthz")
                return
            except (RuntimeError, OSError, URLError, http_client.HTTPException):
                time.sleep(0.25)
        raise RuntimeError("production TLS/database health did not become ready")

    def inspect_runtime(self):
        platform_id = self.compose("ps", "-q", "platform").stdout.decode().strip()
        database_id = self.compose("ps", "-q", "database").stdout.decode().strip()
        inspected = json.loads(self.runner.run(["docker", "inspect", platform_id, database_id], private_output=True).stdout)
        platform, database = inspected
        host = platform["HostConfig"]
        if (platform["Config"]["User"] != "10001:10001" or not host["ReadonlyRootfs"]
                or "ALL" not in host["CapDrop"] or not any("no-new-privileges" in value for value in host["SecurityOpt"])):
            raise RuntimeError("container isolation differs from production Compose policy")
        if any(database["HostConfig"].get("PortBindings", {}).values()):
            raise RuntimeError("database must not publish a host port")
        self.compose("exec", "-T", "platform", "sh", "-c", "test \"$(id -u)\" = 10001 && test -w /data && test -w /tmp && test -r /etc/ssl/certs/ca-certificates.crt")
        refused = self.compose("exec", "-T", "platform", "touch", "/app/forbidden-write", expected=None)
        if refused.returncode == 0:
            raise RuntimeError("read-only container unexpectedly accepted an application-directory write")
        git_version = self.compose("exec", "-T", "platform", "git", "--version").stdout.decode().strip()
        pg_version = self.compose("exec", "-T", "database", "psql", "-U", "substrate", "-d", "substrate", "-Atc", "SHOW server_version").stdout.decode().strip()
        if not pg_version.startswith("17."):
            raise RuntimeError("the deployment drill requires PostgreSQL 17")
        self.observations["containerPolicy"] = {"uid": 10001, "readOnlyRoot": True, "capDrop": host["CapDrop"],
            "noNewPrivileges": True, "databaseHostPort": False, "git": git_version, "postgres": pg_version}
        self.check("nonroot-readonly-private-database", **self.observations["containerPolicy"])

    def exercise_application(self, proxy):
        public = Client(proxy)
        public.request("GET", "/api/v1/projects", expected=401)
        public.request("POST", "/api/v1/auth/login", {"email": self.email, "password": self.password}, headers={"Origin": "https://untrusted.example"}, expected=403)
        self.client = Client(proxy)
        self.session = self.client.login(self.email, self.password)
        self.client.command("projects.create", {"title": "CSRF must reject"}, csrf=False, expected=403)
        self.client.command("projects.create", {"title": "Origin must reject"}, headers={"Origin": "https://untrusted.example"}, expected=403)
        self.check("tls-secure-session-origin-and-csrf", actorId=self.session["actor"]["id"])
        html, _ = public.request("GET", "/", raw=True)
        assets = re.findall(rb'(?:src|href)="(/assets/[^"?]+)"', html)
        if not assets:
            raise RuntimeError("container frontend contains no bundled assets")
        asset_hashes = {}
        for asset in assets:
            body, headers = public.request("GET", asset.decode(), raw=True)
            if not body or "text/html" in headers.get("Content-Type", ""):
                raise RuntimeError("bundled asset was absent or served as SPA HTML")
            asset_hashes[asset.decode()] = digest(body)
        self.observations["static"] = {"indexSHA256": digest(html), "assets": asset_hashes}
        self.check("container-bundled-frontend", **self.observations["static"])
        self.project_resource = self.client.command("projects.create", {"title": "Container recovery fixture"})
        self.goal = self.client.command("goals.create", {
            "projectId": self.project_resource["id"], "title": "Container recovery",
            "outcome": "Verify isolated production-mode persistence and recovery", "targets": ["web"],
            "criteria": [{"id": "recovery", "text": "Exact source and artifact survive recovery", "method": "manual"}], "budget": 1,
        })
        # A synthetic fixture approval, not approval for any real deployment.
        approved = self.client.command("goals.approve", {"id": self.goal["id"]})
        self.goal, _ = self.client.request("GET", "/api/v1/resources/" + self.goal["id"])
        # Tx.Put stamps its stored value; the command's by-value response can
        # retain the earlier updatedAt. Establish the exact persistence baseline
        # from the authoritative read, while checking every other response field.
        differences = sorted(key for key in set(approved) | set(self.goal) if approved.get(key) != self.goal.get(key))
        if set(differences) - {"updatedAt"} or self.goal["data"]["approvedRevision"] != self.goal["revision"]:
            raise RuntimeError("approved requirement readback differs in business fields")
        self.observations["baselineReadback"] = {"commandReadbackDifferences": differences, "requirementRevision": self.goal["revision"]}
        self.repository = self.client.command("repositories.create", {"projectId": self.project_resource["id"], "title": "container-fixture"})
        self.source_body = "Container production-mode recovery fixture\n"
        change = self.client.command("repositories.commit", {"id": self.repository["id"], "branch": "drill-source",
            "message": "Container fixture source", "files": {"README.md": self.source_body}})
        self.commit = change["data"]["lastCommit"]
        token, _ = self.client.request("POST", "/api/v1/tokens", {"projectId": self.project_resource["id"], "name": "deployment-drill"}, expected=201)
        self.token = token["token"]
        self.runner.redactor.add(self.token)
        self.exercise_git(proxy)
        self.artifact_body = b"isolated-container-fixture-artifact\x00\xff\n"
        query = urlencode({"goalId": self.goal["id"], "repositoryId": self.repository["id"], "commit": self.commit,
            "target": "web", "name": "drill.bin", "type": "package"})
        self.artifact, _ = self.client.request("POST", "/api/v1/artifacts/upload?" + query, self.artifact_body,
            headers={"Content-Type": "application/octet-stream"})
        self.verify_data(self.client)
        self.check("requirements-git-and-artifact", projectId=self.project_resource["id"], goalId=self.goal["id"],
                   repositoryId=self.repository["id"], commit=self.commit, artifactSHA256=digest(self.artifact_body))

    def exercise_git(self, proxy):
        # Token is supplied only through a private askpass helper/environment,
        # never through a URL, command argument or Git configuration file.
        token_file = self.root / "git-token"
        token_file.write_text(self.token)
        token_file.chmod(0o600)
        helper = self.root / "git-askpass.py"
        helper.write_text("#!" + sys.executable + "\nimport os,sys\nfrom pathlib import Path\nprint('drill' if 'Username' in sys.argv[1] else Path(os.environ['IS_DRILL_TOKEN_FILE']).read_text())\n")
        helper.chmod(0o700)
        environment = {k: v for k, v in self.runner.env.items() if not k.startswith("GIT_")}
        environment.update({"GIT_ASKPASS": str(helper), "GIT_TERMINAL_PROMPT": "0", "GIT_CONFIG_NOSYSTEM": "1",
            "GIT_CONFIG_GLOBAL": os.devnull, "IS_DRILL_TOKEN_FILE": str(token_file)})
        base = ["git", "-c", "http.sslCAInfo=" + str(proxy.cert), "-c", "credential.helper="]
        clone = self.root / "clone"
        self.runner.run(base + ["clone", proxy.origin + "/git/" + self.repository["id"] + ".git", clone], env=environment)
        self.runner.run(base + ["-C", clone, "fetch", "origin", "drill-source"], env=environment)
        self.runner.run(base + ["-C", clone, "checkout", "-b", "drill-push", "FETCH_HEAD"], env=environment)
        if (clone / "README.md").read_text() != self.source_body:
            raise RuntimeError("Git clone did not reproduce exact API-created source")
        (clone / "transport.txt").write_text("Actual Git smart-HTTP push\n")
        self.runner.run(base + ["-C", clone, "add", "transport.txt"], env=environment)
        self.runner.run(base + ["-C", clone, "-c", "user.name=Container Drill", "-c", "user.email=drill@example.test",
                                "commit", "-m", "Exercise container Git transport"], env=environment)
        self.runner.run(base + ["-C", clone, "push", "origin", "HEAD:refs/heads/drill-push"], env=environment)
        refused = self.runner.run(base + ["-C", clone, "push", "origin", "HEAD:refs/heads/main"], env=environment, expected=None)
        if refused.returncode == 0 or b"main is protected" not in refused.stderr:
            raise RuntimeError("protected-main Git push was not explicitly rejected")
        self.pushed_commit = self.runner.run(base + ["-C", clone, "rev-parse", "HEAD"], env=environment).stdout.decode().strip()
        self.git_environment, self.git_base, self.clone = environment, base, clone
        self.check("real-tls-git-clone-push-and-main-protection", pushedCommit=self.pushed_commit)

    def verify_data(self, client):
        goal, _ = client.request("GET", "/api/v1/resources/" + self.goal["id"])
        if goal != self.goal:
            raise RuntimeError("requirement baseline/approval changed across persistence boundary")
        view, _ = client.request("GET", "/api/v1/repositories/" + self.repository["id"] + "?branch=drill-source&path=README.md")
        if view["content"] != self.source_body.rstrip("\n"):
            raise RuntimeError("stored Git source differs")
        refs, _ = client.request("GET", "/api/v1/repositories/" + self.repository["id"] + "/refs")
        if refs["heads"]["drill-push"] != self.pushed_commit or refs["heads"]["drill-source"] != self.commit:
            raise RuntimeError("stored Git references differ")
        body, _ = client.request("GET", "/api/v1/artifacts/" + self.artifact["id"] + "/content", raw=True)
        if body != self.artifact_body or self.artifact["data"]["digest"] != digest(body):
            raise RuntimeError("content-addressed artifact differs")
        audits, _ = client.request("GET", "/api/v1/audit?" + urlencode({"projectId": self.project_resource["id"]}))
        workspace, _ = client.request("GET", "/api/v1/projects/" + self.project_resource["id"] + "/workspace")
        snapshot = {"resources": sorted(workspace, key=lambda resource: resource["id"]), "audit": audits}
        if hasattr(self, "expected_snapshot"):
            if snapshot != self.expected_snapshot:
                raise RuntimeError("full project resources or audit history changed across persistence boundary")
        else:
            self.expected_snapshot = snapshot
        self.observations["projectSnapshot"] = {"resourceCount": len(workspace), "auditEntries": len(audits),
            "sha256": digest(json.dumps(snapshot, sort_keys=True, separators=(",", ":")).encode())}
        self.observations["audit"] = {"entries": len(audits), "lastHash": audits[-1]["hash"] if audits else ""}
        if not any(audit["action"] == "goals.approve" for audit in audits):
            raise RuntimeError("requirement approval audit was lost")

    def restart_and_backup(self, proxy):
        before = self.observations["audit"].copy()
        self.compose("stop", "platform", "database")
        self.compose("up", "-d", "--no-build", "--pull", "never", "--wait", "--wait-timeout", "90", "database", "platform", timeout=150)
        self.wait_ready(proxy)
        self.verify_data(self.client)
        if self.observations["audit"] != before:
            raise RuntimeError("read-only persistence verification changed/lost audit history")
        Client(proxy).request("GET", "/api/v1/projects", headers={"Authorization": "Bearer " + self.token})
        self.runner.run(self.git_base + ["-C", self.clone, "fetch", "origin"], env=self.git_environment)
        self.check("persistent-database-and-data-volume-restart", sessionPreserved=True, delegatedTokenPreserved=True)
        self.compose("stop", "platform")
        self.compose("run", "--rm", "-T", "--no-deps", "--pull", "never", "--entrypoint", "is-admin", "platform",
                     "-data", "/data", "verify")
        self.compose("run", "--rm", "-T", "--no-deps", "--pull", "never", "--entrypoint", "is-admin", "platform",
                     "-data", "/data", "-archive", "/data/drill-backup.tar.gz", "backup")
        backup = self.compose("run", "--rm", "-T", "--no-deps", "--pull", "never", "--entrypoint", "cat", "platform",
                              "/data/drill-backup.tar.gz", private_output=True).stdout
        path = self.root / "private-backup.tar.gz"
        path.write_bytes(backup)
        path.chmod(0o600)
        with tarfile.open(fileobj=io.BytesIO(backup), mode="r:gz") as archive:
            state = json.load(archive.extractfile("state.json"))
        if set(state["tables"]) != {"resources", "identities", "audit", "idempotency"}:
            raise RuntimeError("backup unexpectedly includes credentials or omits required state")
        damaged = corrupt_artifact_archive(backup)
        self.compose("run", "--rm", "-T", "--no-deps", "--pull", "never", "--entrypoint", "sh", "platform",
                     "-c", "umask 077; cat > /data/drill-corrupt.tar.gz", input=damaged)
        self.check("quiesced-private-backup", backupSHA256=digest(backup), bytes=len(backup),
                   tableRows={name: len(rows) for name, rows in state["tables"].items()}, omittedCredentials=True)

    def restore(self, proxy):
        self.compose("exec", "-T", "database", "createdb", "-U", "substrate", "substrate_restore")
        override = self.root / "compose.restore.json"
        override.write_text(json.dumps({
            "services": {"platform": {"environment": {"IS_DATABASE_URL": self.dsn("substrate_restore"), "IS_DATA_DIR": "/data/restored"},
                "volumes": ["restored:/data", "platform:/source:ro"]}},
            "volumes": {"restored": {}},
        }, indent=2) + "\n")
        self.overrides.append(override)
        failed = self.compose("run", "--rm", "-T", "--no-deps", "--pull", "never", "--entrypoint", "is-admin", "platform",
            "-data", "/data/rejected", "-archive", "/source/drill-corrupt.tar.gz", "restore", expected=None)
        if failed.returncode == 0 or "制品完整性验证失败" not in failed.stderr.decode(errors="replace"):
            raise RuntimeError("damaged-artifact backup was not rejected by integrity validation")
        self.compose("run", "--rm", "-T", "--no-deps", "--pull", "never", "--entrypoint", "test", "platform", "!", "-e", "/data/rejected")
        counts = self.compose("exec", "-T", "database", "psql", "-U", "substrate", "-d", "substrate_restore", "-Atc",
                              "SELECT (SELECT COUNT(*) FROM resources)+(SELECT COUNT(*) FROM identities)+(SELECT COUNT(*) FROM audit)+(SELECT COUNT(*) FROM credentials)+(SELECT COUNT(*) FROM idempotency)").stdout.decode().strip()
        if counts != "0":
            raise RuntimeError("failed restore left imported state in the target database")
        self.check("corrupt-restore-transaction-and-directory-rollback", importedRows=0)
        self.compose("run", "--rm", "-T", "--no-deps", "--pull", "never", "--entrypoint", "is-admin", "platform",
                     "-data", "/data/restored", "-archive", "/source/drill-backup.tar.gz", "restore")
        self.compose("run", "--rm", "-T", "--no-deps", "--pull", "never", "--entrypoint", "is-admin", "platform", "-data", "/data/restored", "verify")
        refused = self.compose("run", "--rm", "-T", "--no-deps", "--pull", "never", "--entrypoint", "is-admin", "platform",
                     "-data", "/data/restored", "-archive", "/source/drill-backup.tar.gz", "restore", expected=None)
        if refused.returncode == 0 or b"restore requires a new destination" not in refused.stderr:
            raise RuntimeError("restore unexpectedly overwrote its existing destination")
        self.compose("up", "-d", "--no-build", "--pull", "never", "--wait", "--wait-timeout", "90", "platform", timeout=150)
        self.wait_ready(proxy)
        self.client.request("GET", "/api/v1/auth/me", expected=401)
        Client(proxy).request("GET", "/api/v1/projects", headers={"Authorization": "Bearer " + self.token}, expected=401)
        fresh = Client(proxy)
        session = fresh.login(self.email, self.password)
        if session["actor"]["id"] != self.session["actor"]["id"]:
            raise RuntimeError("restored login created a different bootstrap identity")
        self.verify_data(fresh)
        fresh_token, _ = fresh.request("POST", "/api/v1/tokens", {"projectId": self.project_resource["id"], "name": "restored-drill"}, expected=201)
        self.runner.redactor.add(fresh_token["token"])
        Client(proxy).request("GET", "/api/v1/projects", headers={"Authorization": "Bearer " + fresh_token["token"]})
        (self.root / "git-token").write_text(fresh_token["token"])
        self.runner.run(self.git_base + ["-C", self.clone, "fetch", "origin"], env=self.git_environment)
        protected = self.runner.run(self.git_base + ["-C", self.clone, "push", "origin", "HEAD:refs/heads/main"], env=self.git_environment, expected=None)
        if protected.returncode == 0 or b"main is protected" not in protected.stderr:
            raise RuntimeError("restored repository lost its protected-main hook")
        terminal_audit, _ = fresh.request("GET", "/api/v1/audit?" + urlencode({"projectId": self.project_resource["id"]}))
        self.observations["afterFreshAuthorizationAudit"] = {"entries": len(terminal_audit), "lastHash": terminal_audit[-1]["hash"]}
        self.check("restored-source-artifact-history-and-fresh-authorization", identityPreserved=True,
                   oldSessionRejected=True, oldDelegatedTokenRejected=True, freshTokenAccepted=True,
                   restoredGitTransport=True, protectedMainPreserved=True)

    def run(self):
        for role, image in (("platform", self.platform_image), ("postgres", self.postgres_image)):
            result = self.runner.run(["docker", "image", "inspect", image, "--format", "{{json .Id}} {{.Os}}/{{.Architecture}}"])
            self.images[role] = {"reference": image, "identity": result.stdout.decode().strip()}
        for kind in ("container", "volume", "network"):
            args = ["docker", "ps", "-aq"] if kind == "container" else ["docker", kind, "ls", "-q"]
            existing = self.runner.run(args + ["--filter", "label=com.docker.compose.project=" + self.project]).stdout.strip()
            if existing:
                raise RuntimeError("generated Compose project already has resources; refusing to reuse it")
        with TLSProxy(self.runner, self.port) as proxy:
            self.configure(proxy)
            self.started = True
            self.compose("up", "-d", "--no-build", "--pull", "never", "--wait", "--wait-timeout", "90", "database", "platform", timeout=150)
            self.wait_ready(proxy)
            self.inspect_runtime()
            self.exercise_application(proxy)
            self.restart_and_backup(proxy)
            self.restore(proxy)

    def cleanup(self):
        if not self.started:
            return {"attempted": False, "complete": True, "reason": "no containers were launched"}
        cleanup = self.compose("down", "--volumes", "--timeout", "15", expected=None, timeout=90)
        leftovers = {}
        for kind in ("container", "volume", "network"):
            args = ["docker", "ps", "-aq"] if kind == "container" else ["docker", kind, "ls", "-q"]
            result = self.runner.run(args + ["--filter", "label=com.docker.compose.project=" + self.project], expected=None)
            leftovers[kind] = result.stdout.decode().split()
            if result.returncode:
                leftovers[kind].append("inspection-failed")
        return {"attempted": True, "complete": cleanup.returncode == 0 and not any(leftovers.values()), "remaining": leftovers}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--work-dir", type=Path, required=True, help="new private directory; parent must exist")
    parser.add_argument("--platform-image", required=True, type=validate_image, help="already-built full platform image")
    parser.add_argument("--postgres-image", default="postgres:17-alpine", type=validate_image, help="already-pulled PostgreSQL 17 image")
    args = parser.parse_args(argv)
    root = create_work_dir(args.work_dir)
    # Keep Docker's existing connection/configuration, but remove platform and
    # Compose settings that could select a real model or repository deployment.
    environment = {key: value for key, value in os.environ.items() if not key.startswith(("IS_", "COMPOSE_"))}
    runner = CommandRunner(root, environment)
    drill = DeploymentDrill(runner, Path(__file__).resolve().parents[1], args.platform_image, args.postgres_image)
    result = {"kind": "isolated-production-container-drill", "startedAt": datetime.now(timezone.utc).isoformat(),
        "passed": False, "productionRollout": False, "overallP3Complete": False,
        "realModels": False, "realStoreWrites": False, "physicalDevices": False,
        "browserAcceptance": False, "composeProject": drill.project}
    try:
        drill.run()
        result["passed"] = True
    except (Exception, KeyboardInterrupt) as error:
        result["error"] = runner.redactor.text(str(error))
    finally:
        try:
            result["cleanup"] = drill.cleanup()
        except Exception as error:
            result["cleanup"] = {"attempted": True, "complete": False, "error": runner.redactor.text(str(error))}
        result["passed"] = result["passed"] and result["cleanup"]["complete"]
        result.update({"finishedAt": datetime.now(timezone.utc).isoformat(), "checks": drill.checks,
                       "images": drill.images, "observations": drill.observations, "commandCount": len(runner.commands)})
        runner.save()
        public = runner.redactor.text(json.dumps(result, ensure_ascii=False, indent=2)) + "\n"
        (root / "result.json").write_text(public)
        print(public, end="")
    return 0 if result["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
