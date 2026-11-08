#!/usr/bin/env python3
"""Run browser suites with isolated state and deterministic local model fixtures.

All generated configs, builds, reports and synthetic credentials stay in work/.
This runner never uses the existing preview or a configured external provider.
"""
import argparse
from datetime import datetime, timezone
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import shlex
import shutil
import socket
import subprocess
import tempfile
import threading


def main():
    repo = Path(__file__).resolve().parents[1]
    suites = sorted(path.name.removesuffix(".spec.ts") for path in (repo / "web/tests").glob("*.spec.ts"))
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--suite", action="append", choices=suites, help="Repeat to select suites; defaults to all")
    parser.add_argument("--projects", nargs="+", choices=["desktop", "mobile"], default=["desktop", "mobile"])
    parser.add_argument("--work-dir", type=Path, help="New evidence directory; never overwrites existing files")
    args = parser.parse_args()
    # Preview links come from platform records; do not collide with the user's
    # existing preview listener on the former fixed port 18083.
    with socket.socket() as reservation:
        reservation.bind(("127.0.0.1", 0))
        preview_port = reservation.getsockname()[1]
    if args.work_dir:
        root = args.work_dir.resolve()
        root.mkdir(mode=0o700, parents=True, exist_ok=False)
    else:
        (repo / "work").mkdir(exist_ok=True)
        root = Path(tempfile.mkdtemp(prefix="workbench-check-", dir=repo / "work"))
    prefix = ["rtk", "proxy"] if shutil.which("rtk") else []
    browser = os.environ.get("IS_BROWSER_EXECUTABLE")
    chrome = Path("/Applications/Google Chrome.app/Contents/MacOS/Google Chrome")
    if not browser and chrome.is_file():
        browser = str(chrome)
    env = {key: value for key, value in os.environ.items() if not key.startswith("IS_")}
    report = {"startedAt": datetime.now(timezone.utc).isoformat(), "passed": False, "steps": [], "projects": [],
              "syntheticLocalModel": True, "paidModelCalled": False, "sourceSHA256": {}, "workDir": str(root)}

    def save():
        (root / "result.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")

    def run(label, argv, cwd=repo, environment=env):
        print(label, flush=True)
        with (root / (label + ".log")).open("xb") as log:
            process = subprocess.run(prefix + list(map(str, argv)), cwd=cwd, env=environment, stdout=log, stderr=subprocess.STDOUT)
        report["steps"].append({"name": label, "exitCode": process.returncode})
        save()
        return process.returncode

    calls = []

    class Provider(BaseHTTPRequestHandler):
        def do_POST(self):
            payload = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            model = payload.get("model")
            valid = self.path == "/v1/chat/completions" and model in {"local-clarification-fixture", "local-cost-fixture", "local-requirements-fixture", "local-plan-fixture"}
            failed = model == "local-cost-fixture" or "local-fixture-failure" in json.dumps(payload)
            calls.append({"path": self.path, "model": model, "status": 503 if failed else 200})
            self.send_response(400 if not valid else 503 if failed else 200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            questions = {"questions": [{"question": "预约取消后，已支付款项如何处理？", "context": "退款规则会影响流程和验收范围。"}]}
            if model == "local-requirements-fixture":
                questions = {"outcome": "客户预约服务并管理本人预约。", "brief": "客户选择服务与时段并取消本人预约；员工管理可约时段。首版排除在线支付。", "criteria": [{"text": "客户只能取消本人的预约。", "method": "automated"}]}
            if model == "local-plan-fixture":
                questions = {"summary": "依据已批准范围实现客户预约与本人取消。", "risk": "独立质量验证仍需执行。", "tasks": [{"id": "analysis", "title": "分析已批准预约范围", "role": "analysis", "runtime": "linux", "kind": "analysis"}]}
            result = {"error": {"message": "Synthetic local model failure"}} if failed or not valid else {
                "id": "synthetic-local-clarification", "model": model,
                "choices": [{"finish_reason": "stop", "message": {"content": json.dumps(questions, ensure_ascii=False)}}],
                "usage": {"prompt_tokens": 10, "completion_tokens": 30}}
            self.wfile.write(json.dumps(result).encode())

        def log_message(self, *_):
            pass

    provider = ThreadingHTTPServer(("127.0.0.1", 0), Provider)
    thread = threading.Thread(target=provider.serve_forever, daemon=True)
    thread.start()
    report["providerPort"] = provider.server_port
    try:
        binary = root / "is-platform"
        assert run("platform-build", ["go", "build", "-trimpath", "-o", binary, "./cmd/platform"]) == 0
        assert run("frontend-types", ["node", "node_modules/typescript/bin/tsc", "-b", "--pretty", "false"], repo / "web") == 0
        assert run("frontend-build", ["node", "node_modules/vite/bin/vite.js", "build", "--outDir", root / "static"], repo / "web") == 0
        report["platformBinarySHA256"] = hashlib.sha256(binary.read_bytes()).hexdigest()
        for suite in args.suite or suites:
            report["sourceSHA256"][suite + ".spec.ts"] = hashlib.sha256((repo / "web/tests" / (suite + ".spec.ts")).read_bytes()).hexdigest()
            for project in args.projects:
                label = suite + "-" + project
                screenshots = root / "screenshots" / label
                screenshots.mkdir(parents=True)
                result_path = root / (label + ".json")
                model = "local-clarification-fixture" if suite == "clarifications" else "local-cost-fixture" if suite == "delivery-costs" else "local-requirements-fixture" if suite == "requirement-drafts" else "local-plan-fixture" if suite in {"plan-generation", "designs", "blueprints"} else ""
                width, height = (390, 844) if project == "mobile" else (1440, 1000)
                config = {
                    "testDir": str(repo / "web/tests"), "testMatch": suite + ".spec.ts", "workers": 1, "retries": 0, "timeout": 60000,
                    "reporter": [["json", {"outputFile": str(result_path)}]], "outputDir": str(root / (label + "-traces")),
                    "use": {"baseURL": "http://127.0.0.1:18082", "trace": "retain-on-failure", "launchOptions": {"executablePath": browser} if browser else {}},
                    "webServer": {
                        "command": shlex.join(prefix + [str(binary), "-listen", "127.0.0.1:18082", "-data", str(root / (label + "-state")), "-static", str(root / "static")]),
                        "cwd": str(repo), "url": "http://127.0.0.1:18082/healthz", "reuseExistingServer": False, "timeout": 30000,
                        "env": {"IS_DEV_MODE": "true", "IS_BLUEPRINT_ROOT": str(repo), "IS_BOOTSTRAP_EMAIL": "browser@example.test", "IS_BOOTSTRAP_PASSWORD": "browser-test-password-2026",
                                "IS_MODEL_BASE_URL": f"http://127.0.0.1:{provider.server_port}/v1" if model else "", "IS_MODEL_NAME": model,
                                "IS_MODEL_INPUT_USD_PER_MILLION": "1", "IS_MODEL_OUTPUT_USD_PER_MILLION": "1", "IS_MODEL_EXTERNAL": "false", "IS_MODEL_ALLOW_HTTP": "true", "IS_OIDC_ISSUER": "",
                                "IS_PUBLIC_URL": "http://127.0.0.1:18082" if suite == "development-previews" else "",
                                "IS_PREVIEW_LISTEN": f"127.0.0.1:{preview_port}" if suite == "development-previews" else "",
                                "IS_PREVIEW_URL": f"http://localhost:{preview_port}" if suite == "development-previews" else ""}},
                    "projects": [{"name": project, "use": {"browserName": "chromium", "viewport": {"width": width, "height": height}, "isMobile": project == "mobile", "hasTouch": project == "mobile"}}]}
                config_path = root / (label + ".config.ts")
                config_path.write_text("import {defineConfig} from " + json.dumps(str(repo / "web/node_modules/@playwright/test/index.mjs")) + ";\nexport default defineConfig(" + json.dumps(config) + ");\n")
                count_before = len(calls)
                code = run(label, ["node", repo / "web/node_modules/@playwright/test/cli.js", "test", "--config", config_path], environment={**env,
                    "IS_CLARIFICATION_SCREENSHOTS": str(screenshots), "IS_COST_SCREENSHOTS": str(screenshots), "IS_INCIDENT_SCREENSHOTS": str(screenshots), "IS_REQUIREMENT_SCREENSHOTS": str(screenshots), "IS_PLAN_SCREENSHOTS": str(screenshots), "IS_QUALITY_SCREENSHOTS": str(screenshots), "IS_PREVIEW_SCREENSHOTS": str(screenshots)})
                result = json.loads(result_path.read_text())
                item = {"suite": suite, "name": project, "exitCode": code, "stats": result["stats"], "stateDir": str(root / (label + "-state")), "localProviderCalls": calls[count_before:]}
                report["projects"].append(item)
                save()
                assert code == 0 and not any(result["stats"][key] for key in ["skipped", "unexpected", "flaky"]), f"{label} failed; see {root}"
                expected_calls = 2 if suite in {"clarifications", "requirement-drafts", "plan-generation"} else 1 if suite in {"delivery-costs", "designs", "blueprints"} else 0
                assert len(item["localProviderCalls"]) == expected_calls, "unexpected model replay or provider call"
        report["passed"] = True
    finally:
        provider.shutdown()
        provider.server_close()
        thread.join(timeout=5)
        report.update(providerStopped=not thread.is_alive(), localProviderCalls=calls, finishedAt=datetime.now(timezone.utc).isoformat())
        save()
    print(json.dumps({"passed": True, "browserFlows": sum(item["stats"]["expected"] for item in report["projects"]), "result": str(root / "result.json")}), flush=True)


if __name__ == "__main__":
    main()
