#!/usr/bin/env python3
"""Exercise the shipped preview ingress with real local TLS, Caddy and Chrome.

All runtime files (including credentials/certificates/logs) stay in a new private
directory under work/. No system trust, DNS, existing service or container edits.
"""
import argparse
import base64
from datetime import datetime, timezone
import hashlib
import io
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import socket
import subprocess
import tarfile
import time
from urllib.parse import urlsplit


def main():
    repo = Path(__file__).resolve().parents[1]
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--caddy", required=True, type=Path)
    parser.add_argument("--chrome", default="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--preview-config", default=repo / "deploy/preview.Caddyfile", type=Path)
    args = parser.parse_args()
    root = args.output.resolve()
    if not root.is_relative_to((repo / "work").resolve()):
        parser.error("--output must be a new directory under this repository's work/")
    caddy, chrome, source = args.caddy.resolve(), args.chrome.resolve(), args.preview_config.resolve()
    for path in (caddy, chrome, source):
        if not path.is_file():
            parser.error("Required local dependency does not exist")
    root.mkdir(mode=0o700, parents=True, exist_ok=False)
    environment = {k: v for k, v in os.environ.items() if not k.startswith(("IS_", "XDG_"))}
    environment.update(XDG_CONFIG_HOME=str(root / "config"), XDG_DATA_HOME=str(root / "data"))
    report = {"passed": False, "startedAt": datetime.now(timezone.utc).isoformat(),
              "productionMode": True, "apiResponsesMocked": False,
              "paidModelCalled": False, "externalPublicationPerformed": False,
              "systemTrustStoreModified": False, "broadTLSBypass": False, "steps": []}
    processes, logs, ports = [], [], set()
    server = proxy = None

    def save():
        (root / "result.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")

    def run(label, command, data=None, required=True):
        print(label, flush=True)
        with (root / (label + ".stdout")).open("xb") as out, (root / (label + ".stderr")).open("xb") as err:
            result = subprocess.run(["rtk", "proxy", *map(str, command)], cwd=repo,
                                    env=environment, input=data, stdout=out, stderr=err, timeout=180)
        report["steps"].append({"name": label, "exitCode": result.returncode})
        save()
        if required:
            assert result.returncode == 0, label + " failed; inspect private logs"
        return result.returncode, (root / (label + ".stdout")).read_bytes()

    def port():
        while True:
            with socket.socket() as sock:
                sock.bind(("127.0.0.1", 0))
                value = sock.getsockname()[1]
            if value not in ports:
                ports.add(value)
                return value

    def start(label, command, env=None):
        log = (root / (label + ".log")).open("xb")
        logs.append(log)
        process = subprocess.Popen(list(map(str, command)), cwd=repo, env=env or environment,
                                   stdout=log, stderr=subprocess.STDOUT)
        processes.append(process)
        return process

    def ready(process, listen):
        until = time.monotonic() + 20
        while time.monotonic() < until:
            assert process.poll() is None, "Service startup failed; inspect private logs"
            try:
                with socket.create_connection(("127.0.0.1", listen), timeout=.2):
                    return
            except OSError:
                time.sleep(.1)
        raise AssertionError("Service readiness timed out")

    def stop(process):
        if process is not None and process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=20)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)
                raise AssertionError("Service did not shut down gracefully")
            assert process.returncode == 0, "Service did not exit cleanly"

    def request(label, url, expected, headers=(), trust=None, success=True):
        parsed = urlsplit(url)
        code, body = run(label, ["curl", "--silent", "--show-error", "--noproxy", "*",
            "--proto", "=https", "--cacert", trust or root / "ca.pem",
            "--resolve", f"{parsed.hostname}:{parsed.port}:127.0.0.1", "--http1.1",
            "--max-time", "10", "--dump-header", root / (label + ".headers"),
            "--output", root / (label + ".body"), "--write-out", "%{http_code} %{ssl_verify_result}",
            *[part for header in headers for part in ("--header", header)], url], required=success)
        if success:
            status, verified = body.decode().split()
            allowed = expected if isinstance(expected, tuple) else (expected,)
            assert int(status) in allowed and verified == "0", label + " status/TLS verification failed"
        else:
            assert code == 60, "Untrusted CA was unexpectedly accepted"
        return (root / (label + ".body")).read_bytes() if success else b""

    try:
        _, version = run("caddy-version", [caddy, "version"])
        report["caddyVersion"] = version.decode().strip()
        report["caddyBinarySHA256"] = hashlib.sha256(caddy.read_bytes()).hexdigest()
        control, preview, control_tls, preview_tls = [port() for _ in range(4)]
        origin = f"https://control.ingress.test:{control_tls}"
        preview_origin = f"https://preview.ingress.test:{preview_tls}"
        run("ca-create", ["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2",
            "-subj", "/CN=Private preview verification CA", "-keyout", root / "ca.key", "-out", root / "ca.pem"])
        run("leaf-create", ["openssl", "req", "-newkey", "rsa:2048", "-nodes",
            "-subj", "/CN=control.ingress.test", "-keyout", root / "leaf.key", "-out", root / "leaf.csr"])
        (root / "leaf.cnf").write_text("basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\nsubjectAltName=DNS:control.ingress.test,DNS:preview.ingress.test\n")
        run("leaf-sign", ["openssl", "x509", "-req", "-in", root / "leaf.csr", "-CA", root / "ca.pem",
            "-CAkey", root / "ca.key", "-CAcreateserial", "-days", "2", "-extfile", root / "leaf.cnf", "-out", root / "leaf.pem"])
        _, pem = run("leaf-public-key", ["openssl", "x509", "-in", root / "leaf.pem", "-pubkey", "-noout"])
        _, der = run("leaf-public-key-der", ["openssl", "pkey", "-pubin", "-outform", "DER"], pem)
        pin = base64.b64encode(hashlib.sha256(der).digest()).decode()
        for key in root.glob("*.key"):
            key.chmod(0o600)
        run("platform-build", ["go", "build", "-trimpath", "-o", root / "is-platform", "./cmd/platform"])
        run("frontend-build", ["node", repo / "web/node_modules/vite/bin/vite.js", "build", repo / "web", "--outDir", root / "static"])

        config = source.read_text()
        assert config.count("preview.company.example {") == 1 and config.count("127.0.0.1:8083") == 1, "Unexpected preview template; update local verification adapter"
        config = config.replace("preview.company.example {", preview_origin + " {\n\tbind 127.0.0.1\n\ttls " + str(root / "leaf.pem") + " " + str(root / "leaf.key"))
        config = config.replace("127.0.0.1:8083", "127.0.0.1:" + str(preview))
        global_match = re.search(r"(?m)^\{\s*$", config)
        local_options = "\n\tadmin off\n\tauto_https off\n"
        if global_match:
            config = config[:global_match.end()] + local_options + config[global_match.end():]
        else:
            config = "{" + local_options + "}\n" + config
        config += f"\n{origin} {{\n\tbind 127.0.0.1\n\ttls {root / 'leaf.pem'} {root / 'leaf.key'}\n\treverse_proxy 127.0.0.1:{control}\n}}\n"
        (root / "Caddyfile").write_text(config)
        run("caddy-validate", [caddy, "validate", "--config", root / "Caddyfile", "--adapter", "caddyfile"])
        password = secrets.token_urlsafe(32)
        (root / "bootstrap-password").write_text(password)
        (root / "bootstrap-password").chmod(0o600)
        platform_env = {**environment, "IS_DEV_MODE": "false", "IS_PUBLIC_URL": origin,
            "IS_PREVIEW_URL": preview_origin, "IS_PREVIEW_LISTEN": f"127.0.0.1:{preview}",
            "IS_BOOTSTRAP_EMAIL": "ingress-owner@example.test", "IS_BOOTSTRAP_PASSWORD_FILE": str(root / "bootstrap-password")}
        platform_command = [root / "is-platform", "-listen", f"127.0.0.1:{control}", "-data", root / "state", "-static", root / "static"]
        server = start("platform-initial", platform_command, platform_env)
        ready(server, control)
        proxy = start("caddy-runtime", [caddy, "run", "--config", root / "Caddyfile", "--adapter", "caddyfile"])
        ready(proxy, preview_tls)
        ready(proxy, control_tls)
        request("control-tls", origin + "/healthz", 200)
        request("preview-tls", preview_origin + "/api/v1/projects", 404)
        run("unrelated-ca-create", ["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2",
            "-subj", "/CN=Unrelated private CA", "-keyout", root / "unrelated-ca.key", "-out", root / "unrelated-ca.pem"])
        (root / "unrelated-ca.key").chmod(0o600)
        request("untrusted-ca", origin + "/healthz", 0, trust=root / "unrelated-ca.pem", success=False)
        report["tlsChainAndHostnameVerified"] = True
        report["untrustedCARejected"] = True

        files = {
            "index.html": '<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="./assets/app.css"><main><h1>客户预约 · HTTPS 预览</h1><p id="ready">正在加载</p><label>客户姓名 <input id="customer" aria-label="客户姓名"></label><button id="choose">确认预约</button><p id="result"></p><p id="network"></p><p id="session"></p></main><script type="module" src="./assets/app.js"></script></html>',
            "assets/app.css": 'body{font:18px system-ui;margin:24px;color:#16324a;background:#f4f8f6}main{max-width:640px;margin:48px auto}label{display:block;margin:24px 0}input{padding:10px;max-width:65%}button{padding:12px 20px;border:0;border-radius:6px;background:#176d5e;color:white}#result{min-height:28px}',
            "assets/app.js": "import {label} from './label.js';let slot='';fetch(new URL('../data/items.json',import.meta.url)).then(r=>r.json()).then(x=>{slot=x[0];document.querySelector('#ready').textContent=label+' '+slot});document.querySelector('#choose').onclick=()=>document.querySelector('#result').textContent=document.querySelector('#customer').value+'，已预约'+slot;fetch('" + origin + "/api/v1/projects',{credentials:'include'}).then(()=>document.querySelector('#network').textContent='平台访问意外成功').catch(()=>document.querySelector('#network').textContent='平台访问已阻止');try{document.cookie;document.querySelector('#session').textContent='会话读取意外成功'}catch{document.querySelector('#session').textContent='会话已隔离'}",
            "assets/label.js": "export const label='模块已加载';",
            "data/items.json": '["周五 09:00"]',
        }
        with tarfile.open(root / "application.tar", "w", format=tarfile.USTAR_FORMAT) as archive:
            for name, text in files.items():
                body = text.encode()
                member = tarfile.TarInfo(name)
                member.size = len(body)
                member.mode = 0o644
                archive.addfile(member, io.BytesIO(body))
        browser_config = {"repo": str(repo), "root": str(root), "chrome": str(chrome), "origin": origin,
            "previewOrigin": preview_origin, "pin": pin, "email": "ingress-owner@example.test", "password": password}
        run("browser-healthy", ["node", repo / "scripts/verify-preview-ingress.mjs"], json.dumps({**browser_config, "phase": "healthy"}).encode())
        report["browser"] = json.loads((root / "browser-healthy.json").read_text())
        assert report["browser"]["passed"]
        recovery = json.loads((root / "private-recovery.json").read_text())
        request("before-failure", recovery["url"], 200)
        stop(server)
        marker = secrets.token_urlsafe(32)
        (root / "private-fault-marker").write_text(marker)
        (root / "private-fault-marker").chmod(0o600)
        fault_headers = ["Referer: " + recovery["url"], "Authorization: Bearer " + marker,
                         "Cookie: synthetic=" + marker, "X-Synthetic-Secret: " + marker]
        for i, suffix in enumerate(("", "status", "files/assets/app.js", "?synthetic=" + marker)):
            request("proxy-failure-" + str(i), recovery["url"] + suffix, 502, fault_headers)
        request("wrong-host", recovery["url"], 421, ["Host: control.ingress.test:" + str(control_tls)])
        server = start("platform-recovered", platform_command, platform_env)
        ready(server, control)
        request("after-recovery", recovery["url"], 200)
        asset = request("asset-after-recovery", recovery["url"] + "files/assets/app.js", 200)
        assert asset == files["assets/app.js"].encode(), "Recovered asset bytes changed"
        run("browser-recovered", ["node", repo / "scripts/verify-preview-ingress.mjs"], json.dumps({**browser_config, "phase": "recovered"}).encode())
        report["browserAfterRecovery"] = json.loads((root / "browser-recovered.json").read_text())
        assert report["browserAfterRecovery"]["passed"]
        request("stopped-after-recovery", recovery["url"], 404)
        stop(proxy)
        raw = (root / "caddy-runtime.log").read_bytes()
        private = [password, marker, *json.loads((root / "private-markers.json").read_text())]
        errors, access_records = [], 0
        for line in raw.splitlines():
            try:
                value = json.loads(line)
            except ValueError:
                continue
            if value.get("logger", "").startswith("http.log.access"):
                access_records += 1
            if value.get("status") == 502:
                errors.append(value)
                assert "uri" not in value.get("request", {}) and "headers" not in value.get("request", {}), "Sensitive request metadata in proxy error log"
                assert "resp_headers" not in value, "Response headers in proxy error log"
        assert len(errors) == 4, "Expected four useful proxy error diagnostics"
        assert access_records == 0, "Preview access logging unexpectedly enabled"
        scanned = [root / "caddy-runtime.log", root / "platform-initial.log", root / "platform-recovered.log"]
        assert all(not any(value.encode() in path.read_bytes() for value in private if value) for path in scanned), "Credential entered runtime log"
        report.update(passed=True, proxyErrorRecords=len(errors), runtimeSecretsAbsent=True,
            runtimeSecretValuesChecked=len(private), errorStatusRetained=True, accessLogRecords=access_records,
            wrongHostRejected=True, heldAccessSurvivedRestart=True, recoveredAssetBytesMatched=True,
            sourceSHA256={str(path.relative_to(repo)): hashlib.sha256(path.read_bytes()).hexdigest()
                for path in (source, Path(__file__), repo / "scripts/verify-preview-ingress.mjs") if path.is_relative_to(repo)},
            platformBinarySHA256=hashlib.sha256((root / "is-platform").read_bytes()).hexdigest())
    finally:
        for process in reversed(processes):
            stop(process)
        for log in logs:
            log.close()
        closed = []
        for listen in ports:
            try:
                with socket.create_connection(("127.0.0.1", listen), timeout=.2):
                    pass
            except OSError:
                closed.append(listen)
        report.update(processesStopped=all(p.poll() is not None for p in processes),
            portsCheckedClosed=sorted(closed), allTaskPortsClosed=len(closed) == len(ports),
            finishedAt=datetime.now(timezone.utc).isoformat())
        save()
    assert report["allTaskPortsClosed"]
    print(json.dumps({"passed": report["passed"], "result": str(root / "result.json")}))


if __name__ == "__main__":
    main()
