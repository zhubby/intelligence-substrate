#!/usr/bin/env python3
"""Verify two real Flutter/Go previews through the shipped Caddy HTTPS ingress.

Creates only a new private work/ directory, local certificates, loopback listeners
and owner-labelled preview containers. No system trust/DNS changes, paid models
or external publication. The Worker control transport remains loopback HTTP.
"""
import argparse
import base64
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import socket
import subprocess
import time
from urllib.parse import urlsplit


def main():
    repo = Path(__file__).resolve().parents[1]
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("caddy", "flutter", "go", "docker", "output"):
        parser.add_argument("--" + name, required=True, type=Path)
    parser.add_argument("--docker-host", required=True)
    parser.add_argument("--arch", choices=("arm64", "amd64"), default="arm64")
    parser.add_argument("--tool-wrapper", type=Path)
    parser.add_argument("--chrome", type=Path, default=Path("/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"))
    args = parser.parse_args()
    root = args.output.resolve()
    if not root.is_relative_to((repo / "work").resolve()):
        parser.error("--output must be a new directory under work/")
    application_root = root / "r"
    if len(os.fsencode(application_root / "n/node.sock")) > 100:
        parser.error("--output path is too long for the local preview node Unix socket")
    for name in ("caddy", "flutter", "go", "docker", "chrome", "tool_wrapper"):
        path = getattr(args, name)
        if path is not None and not path.is_file():
            parser.error("Missing local executable or wrapper: " + name)
    root.mkdir(mode=0o700, parents=True, exist_ok=False)
    env = {key: value for key, value in os.environ.items() if not key.startswith(("IS_", "XDG_"))}
    env.update(XDG_CONFIG_HOME=str(root / "config"), XDG_DATA_HOME=str(root / "data"),
               IS_BROWSER_EXECUTABLE=str(args.chrome.resolve()))
    report = {"passed": False, "startedAt": datetime.now(timezone.utc).isoformat(),
              "kind": "real-reference-fullstack-https", "steps": [],
              "systemTrustStoreModified": False, "broadTLSBypass": False,
              "paidModelCalled": False, "externalPublicationPerformed": False,
              "workerTransport": "loopback-http", "controlAndPreviewTransport": "https"}
    processes, logs, ports = [], [], set()

    def save():
        (root / "result.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")

    def run(label, command, data=None, expected=0, timeout=180):
        print(label, flush=True)
        with (root / (label + ".stdout")).open("xb") as out, (root / (label + ".stderr")).open("xb") as err:
            result = subprocess.run(["rtk", "proxy", *map(str, command)], cwd=repo, env=env,
                                    input=data, stdout=out, stderr=err, timeout=timeout)
        report["steps"].append({"name": label, "exitCode": result.returncode})
        save()
        assert result.returncode == expected, label + " failed; inspect private logs"
        return (root / (label + ".stdout")).read_bytes()

    def port():
        while True:
            with socket.socket() as sock:
                sock.bind(("127.0.0.1", 0))
                value = sock.getsockname()[1]
            if value not in ports:
                ports.add(value)
                return value

    def request(label, origin, trust, expected):
        parsed = urlsplit(origin)
        return run(label, ["curl", "--silent", "--show-error", "--noproxy", "*", "--proto", "=https",
            "--cacert", trust, "--resolve", f"{parsed.hostname}:{parsed.port}:127.0.0.1",
            "--http1.1", "--max-time", "10", "--output", root / (label + ".body"),
            "--write-out", "%{http_code} %{ssl_verify_result}", origin + "/healthz"], expected=expected)

    try:
        report["caddyVersion"] = run("caddy-version", [args.caddy.resolve(), "version"]).decode().strip()
        control, preview, control_tls, preview_tls = [port() for _ in range(4)]
        origin, preview_origin = f"https://control.ingress.test:{control_tls}", f"https://preview.ingress.test:{preview_tls}"
        run("ca-create", ["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2",
            "-subj", "/CN=Private fullstack preview CA", "-keyout", root / "ca.key", "-out", root / "ca.pem"])
        run("leaf-create", ["openssl", "req", "-newkey", "rsa:2048", "-nodes", "-subj", "/CN=control.ingress.test",
            "-keyout", root / "leaf.key", "-out", root / "leaf.csr"])
        (root / "leaf.cnf").write_text("basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\nsubjectAltName=DNS:control.ingress.test,DNS:preview.ingress.test\n")
        run("leaf-sign", ["openssl", "x509", "-req", "-in", root / "leaf.csr", "-CA", root / "ca.pem",
            "-CAkey", root / "ca.key", "-CAcreateserial", "-days", "2", "-extfile", root / "leaf.cnf", "-out", root / "leaf.pem"])
        pem = run("leaf-public-key", ["openssl", "x509", "-in", root / "leaf.pem", "-pubkey", "-noout"])
        der = run("leaf-public-key-der", ["openssl", "pkey", "-pubin", "-outform", "DER"], pem)
        pin = base64.b64encode(hashlib.sha256(der).digest()).decode()
        for path in root.glob("*.key"):
            path.chmod(0o600)
        ingress = {"controlListen": f"127.0.0.1:{control}", "previewListen": f"127.0.0.1:{preview}",
                   "origin": origin, "previewOrigin": preview_origin, "pin": pin, "caFile": str(root / "ca.pem")}
        (root / "ingress.json").write_text(json.dumps(ingress))
        source = repo / "deploy/preview.Caddyfile"
        config = source.read_text()
        assert config.count("preview.company.example {") == 1 and config.count("127.0.0.1:8083") == 1
        config = config.replace("preview.company.example {", preview_origin + " {\n\tbind 127.0.0.1\n\ttls " + str(root / "leaf.pem") + " " + str(root / "leaf.key"))
        config = config.replace("127.0.0.1:8083", ingress["previewListen"])
        match = re.search(r"(?m)^\{\s*$", config)
        assert match, "Expected complete shipped Caddy configuration"
        config = config[:match.end()] + "\n\tadmin off\n\tauto_https off\n" + config[match.end():]
        config += f"\n{origin} {{\n\tbind 127.0.0.1\n\ttls {root / 'leaf.pem'} {root / 'leaf.key'}\n\treverse_proxy {ingress['controlListen']}\n}}\n"
        (root / "Caddyfile").write_text(config)
        run("caddy-validate", [args.caddy.resolve(), "validate", "--config", root / "Caddyfile", "--adapter", "caddyfile"])
        log = (root / "caddy-runtime.log").open("xb")
        logs.append(log)
        proxy = subprocess.Popen([str(args.caddy.resolve()), "run", "--config", str(root / "Caddyfile"), "--adapter", "caddyfile"],
                                 cwd=repo, env=env, stdout=log, stderr=subprocess.STDOUT)
        processes.append(proxy)
        until = time.monotonic() + 20
        while True:
            assert proxy.poll() is None, "Caddy startup failed; inspect private log"
            try:
                with socket.create_connection(("127.0.0.1", preview_tls), timeout=.2):
                    break
            except OSError:
                assert time.monotonic() < until, "Caddy startup timed out"
                time.sleep(.1)
        # Healthy TLS, deliberately unavailable upstream; this separately tests
        # certificate chain/hostname without any browser certificate override.
        assert request("control-tls-chain", origin, root / "ca.pem", 0).decode() == "502 0"
        assert request("preview-tls-chain", preview_origin, root / "ca.pem", 0).decode() == "502 0"
        run("unrelated-ca-create", ["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2",
            "-subj", "/CN=Unrelated private CA", "-keyout", root / "unrelated-ca.key", "-out", root / "unrelated-ca.pem"])
        (root / "unrelated-ca.key").chmod(0o600)
        request("untrusted-ca", origin, root / "unrelated-ca.pem", 60)
        report["tlsChainAndHostnameVerified"] = True
        report["untrustedChainRejected"] = True
        run("verifier-build", [args.go.resolve(), "build", "-trimpath", "-o", root / "verifier", "scripts/verify-reference-previews.go"])
        command = [root / "verifier", "-work-dir", application_root, "-flutter", args.flutter.resolve(),
                   "-go", args.go.resolve(), "-docker", args.docker.absolute(), "-docker-host", args.docker_host,
                   "-arch", args.arch, "-tls-ingress", root / "ingress.json"]
        if args.tool_wrapper:
            command.extend(["-tool-wrapper", args.tool_wrapper.resolve()])
        run("actual-https-applications", command, timeout=2400)
        actual = json.loads((application_root / "result.json").read_text())
        assert actual["passed"] and actual["httpsIngress"] and actual["ownedContainersAndImagesReclaimed"]
        phases = {phase: json.loads((application_root / f"browser-{phase}.json").read_text())
                  for phase in ("healthy", "unavailable", "recovered", "stopped")}
        assert all(value["passed"] and value["browserStopped"] and value["httpsIngress"] for value in phases.values())
        report["actualApplications"] = [{"mode": app["mode"], "projectId": app["projectId"], "goalId": app["goalId"],
            "commit": app["commit"], "artifactId": app["artifact"]["id"], "digest": app["artifact"]["data"]["digest"]}
            for app in actual["applications"]]
        report["browser"] = phases
        proxy.terminate()
        assert proxy.wait(timeout=20) == 0
        recovery = json.loads((application_root / "private-recovery.json").read_text())
        browser_cfg = json.loads((application_root / "browser-config.json").read_text())
        storage = json.loads((application_root / "private-storage-state.json").read_text())
        private = [browser_cfg["password"], *json.loads((application_root / "private-session-markers.json").read_text()),
                   *[row["value"] for row in storage["cookies"]],
                   *[app["url"] for app in recovery["apps"]],
                   *[app["url"].split("/p/")[1].split("/")[0] for app in recovery["apps"]]]
        records = []
        for line in (root / "caddy-runtime.log").read_bytes().splitlines():
            try:
                row = json.loads(line)
            except ValueError:
                continue
            assert not row.get("logger", "").startswith("http.log.access"), "Unexpected access log"
            if row.get("status") == 502:
                assert not any(key in row.get("request", {}) for key in ("uri", "headers"))
                assert "resp_headers" not in row
                records.append(row)
        assert len(records) >= 9, "Missing real upstream error diagnostics"
        scanned = [root / "caddy-runtime.log", root / "actual-https-applications.stdout", root / "actual-https-applications.stderr",
                   application_root / "node-initial.log", application_root / "node-recovered.log"]
        assert all(not any(value.encode() in path.read_bytes() for value in private if value) for path in scanned), "Private value in runtime log"
        report.update(passed=True, runtimeSecretsAbsent=True, runtimeSecretValuesChecked=len(private),
            proxyErrorRecords=len(records), accessLogRecords=0, nodeAndControlRestartVerified=True,
            failedWriteNotReplayedAfterRecovery=True, stoppedPreviewsRejected=True,
            formalQualityOrReleaseEvidenceCreated=False, deliveryMeasurementsCreated=False,
            ownedContainersAndImagesReclaimed=True,
            sourceSHA256={str(path.relative_to(repo)): hashlib.sha256(path.read_bytes()).hexdigest()
                for path in (source, Path(__file__), repo / "scripts/verify-reference-previews.go", repo / "scripts/verify-reference-previews.mjs")},
            caddyBinarySHA256=hashlib.sha256(args.caddy.read_bytes()).hexdigest())
    finally:
        for process in reversed(processes):
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=20)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)
        for log in logs:
            log.close()
        closed = []
        for listen in ports:
            try:
                with socket.create_connection(("127.0.0.1", listen), timeout=.2):
                    pass
            except OSError:
                closed.append(listen)
        report.update(processesStopped=all(process.poll() is not None for process in processes),
                      allTaskPortsClosed=len(closed) == len(ports), finishedAt=datetime.now(timezone.utc).isoformat())
        save()
    assert report["allTaskPortsClosed"]
    print(json.dumps({"passed": report["passed"], "result": str(root / "result.json")}))


if __name__ == "__main__":
    main()
