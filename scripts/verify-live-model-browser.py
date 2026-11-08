"""Independent real-browser acceptance of a pinned, generated Web artifact.

This operator-owned script may run as a protected native quality pipeline.
The generated server has no network interface beyond its own loopback. A
local HTTP gateway forwards real requests with docker exec; it never mocks
application responses. No platform or model credentials enter the server.
"""
from pathlib import Path
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import argparse
import base64
import hashlib
import json
import os
import re
import shutil
import subprocess
import tarfile
import tempfile
import threading
import time
import uuid


parser = argparse.ArgumentParser()
parser.add_argument("--artifact", default=os.environ.get("IS_ARTIFACT_PATH"))
parser.add_argument("--mode", required=True, choices=["feedback", "booking"])
parser.add_argument("--image", required=True)
parser.add_argument("--docker-host", required=True)
parser.add_argument("--executable", default="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome")
dest = parser.add_mutually_exclusive_group(required=True)
dest.add_argument("--evidence")
dest.add_argument("--evidence-base")
a = parser.parse_args()
assert a.artifact and re.fullmatch(r"[^\s]+@sha256:[a-f0-9]{64}", a.image)
assert a.docker_host.startswith("unix:///"), "acceptance requires an explicit local Docker socket"
assert Path(a.executable).is_file(), "explicit browser executable missing"
if a.evidence_base:
    base_dir = Path(a.evidence_base).resolve()
    base_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
    root = Path(tempfile.mkdtemp(prefix=a.mode + "-", dir=base_dir))
else:
    root = Path(a.evidence).resolve()
    root.mkdir(mode=0o700, exist_ok=False)
app, state, shots = (root / name for name in ["app", "state", "shots"])
for directory in [app, state, shots, root / "browser-home"]:
    directory.mkdir()
artifact = Path(a.artifact).resolve()
digest = hashlib.sha256(artifact.read_bytes()).hexdigest()
with tarfile.open(artifact) as archive:
    members = archive.getmembers()
    assert sorted(m.name for m in members) == ["app.js", "index.html"], "unexpected package members"
    for member in members:
        assert member.isfile() and member.size <= 512000, "unsafe package member"
        body = archive.extractfile(member).read()
        if os.environ.get("IS_ARTIFACT_PATH"):
            assert body == Path(member.name).read_bytes(), "artifact differs from fixed source"
        (app / member.name).write_bytes(body)

name = "is-model-browser-" + uuid.uuid4().hex[:12]
session, secondary = name, name + "-other"
config_path = root / "browser-config.json"
config_path.write_text('{"headed":false,"extensions":[]}\n')
report = {"kind": "actual-generated-artifact-browser-acceptance", "mode": a.mode,
          "artifactSHA256": digest, "passed": False, "productPlanComplete": False,
          "releaseApproved": False, "browser": "Chrome via agent-browser",
          "viewports": [[1440, 1000], [390, 844]], "checks": [], "screenshots": [],
          "syntheticBusinessData": True, "apiResponsesMocked": False,
          "postDelaySeconds": .35, "transport": "local gateway to actual container-loopback HTTP via docker exec"}
commands, requests = [], []
socket_dir = tempfile.mkdtemp(prefix="isb-")
child_env = {"PATH": os.environ["PATH"], "HOME": str(root / "browser-home"),
             "TMPDIR": tempfile.gettempdir(), "LANG": "C.UTF-8", "AGENT_BROWSER_SOCKET_DIR": socket_dir}


def run(args, allow_error=False):
    result = subprocess.run(["rtk", "proxy", *map(str, args)], capture_output=True,
                            text=True, timeout=45, env=child_env)
    commands.append({"program": str(args[0]), "action": str(args[1]) if len(args) > 1 else "",
                     "exitCode": result.returncode})
    if result.returncode and not allow_error:
        raise RuntimeError("acceptance command failed: " + (result.stderr or result.stdout)[-1800:])
    return result


def docker(*args, allow_error=False):
    return run(["docker", "--host", a.docker_host, *args], allow_error)


def browser(*args, other=False):
    result = run(["agent-browser", "--config", config_path, "--session", secondary if other else session,
                  "--executable-path", a.executable, "--headed", "false", "--json", *args])
    value = json.loads(result.stdout)
    assert value.get("success"), str(value)
    return value.get("data", {})


def evaluate(script, other=False):
    return browser("eval", script, other=other).get("result")


def wait(script, other=False):
    browser("wait", "--fn", script, other=other)


forward_js = r"""
const http = require('http');
const input = JSON.parse(process.argv[1]);
const request = http.request({hostname:'127.0.0.1',port:8080,path:input.path,
  method:input.method,headers:{'Content-Type':'application/json'}}, response => {
  const chunks=[];let size=0;
  response.on('data',chunk=>{size+=chunk.length;if(size>1000000){request.destroy();process.exit(2)}chunks.push(chunk)});
  response.on('end',()=>process.stdout.write(JSON.stringify({status:response.statusCode,
    contentType:response.headers['content-type']||'application/octet-stream',body:Buffer.concat(chunks).toString('base64')})));
});
request.setTimeout(8000,()=>request.destroy(new Error('forward timeout')));
request.on('error',()=>{process.exitCode=1});
if(input.body!==null)request.write(Buffer.from(input.body,'base64'));
request.end();
"""


def application_request(path, method="GET", body=None):
    value = {"path": path, "method": method,
             "body": None if body is None else base64.b64encode(body).decode()}
    raw = docker("exec", name, "node", "-e", forward_js, json.dumps(value)).stdout
    result = json.loads(raw)
    return result["status"], result["contentType"], base64.b64decode(result["body"])


def api(path, payload=None):
    status, _, body = application_request(path, "GET" if payload is None else "POST",
                                         None if payload is None else json.dumps(payload).encode())
    return status, json.loads(body)


def ready():
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        try:
            if api("/api/health")[0] == 200:
                return
        except (RuntimeError, ValueError):
            pass
        time.sleep(.1)
    raise AssertionError("application readiness failed")


class Gateway(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def forward(self):
        body = None
        if self.command == "POST":
            length = int(self.headers.get("Content-Length", "0"))
            assert length < 32000
            body = self.rfile.read(length)
            time.sleep(.35)
        try:
            status, content_type, content = application_request(self.path, self.command, body)
        except Exception:
            self.send_error(502)
            return
        requests.append({"method": self.command, "path": self.path, "status": status})
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(content)))
        self.end_headers()
        try:
            self.wfile.write(content)
        except (BrokenPipeError, ConnectionResetError):
            pass  # The offline test may close an in-flight browser connection.

    do_GET = forward
    do_POST = forward


started, gateway = False, None
try:
    report["activeCheck"] = "isolated server startup"
    docker("run", "--detach", "--rm", "--pull", "never", "--name", name,
           "--network", "none", "--read-only", "--cap-drop", "ALL",
           "--security-opt", "no-new-privileges", "--pids-limit", "128",
           "--memory", "256m", "--cpus", "1", "--user", str(os.getuid()) + ":" + str(os.getgid()),
           "--mount", "type=bind,src=" + str(app) + ",dst=/app,readonly",
           "--mount", "type=bind,src=" + str(state) + ",dst=/state",
           "--tmpfs", "/tmp:rw,nosuid,nodev,size=16m", "-e", "STATE_FILE=/state/state.json",
           "-e", 'APP_SLOTS=[{"id":"slot-1","startsAt":"2030-01-01T10:00:00Z"},{"id":"slot-2","startsAt":"2030-01-01T11:00:00Z"}]',
           "--workdir", "/app", "--entrypoint", "node", a.image, "app.js")
    started = True
    info = json.loads(docker("inspect", name).stdout)[0]
    limits = info["HostConfig"]
    assert limits["ReadonlyRootfs"] and "ALL" in limits["CapDrop"] and not limits["Privileged"]
    assert limits["PidsLimit"] == 128 and limits["Memory"] == 268435456 and limits["NanoCpus"] == 1000000000
    assert limits["NetworkMode"] == "none" and not limits.get("PortBindings")
    assert any("no-new-privileges" in s for s in limits["SecurityOpt"])
    report["runtime"] = {"networkDisabled": True, "readOnlyRoot": True, "nonRootUser": info["Config"]["User"],
                         "droppedCapabilities": limits["CapDrop"], "memoryBytes": limits["Memory"],
                         "sourceReadOnly": True, "isolatedState": True, "publishedPorts": False}
    ready()
    gateway = ThreadingHTTPServer(("127.0.0.1", 0), Gateway)
    threading.Thread(target=gateway.serve_forever, daemon=True).start()
    base = "http://127.0.0.1:" + str(gateway.server_port)
    report["activeCheck"] = "actual browser startup"
    browser("set", "viewport", "1440", "1000")
    browser("open", base)
    browser("wait", "--load", "networkidle")
    (root / "desktop-snapshot.json").write_text(json.dumps(browser("snapshot", "-i"), ensure_ascii=False, indent=2))
    assert evaluate('!!document.querySelector("#status[aria-live]") && !!document.querySelector("#refresh")')
    if a.mode == "feedback":
        report["activeCheck"] = "feedback submission and pending duplicate prevention"
        title = "浏览器验收-" + uuid.uuid4().hex[:8]
        content = "<script>window.__IS_INJECTED=true</script>中文普通文本"
        browser("fill", "#title", title)
        browser("fill", "#content", content)
        evaluate('document.querySelector("#submit").click();document.querySelector("#submit").click();true')
        wait('!document.querySelector("#submit").disabled && document.querySelector("#list").textContent.includes(' + json.dumps(title) + ')')
        assert len([r for r in api("/api/feedback")[1] if r["title"] == title]) == 1
        assert evaluate('window.__IS_INJECTED !== true && !document.querySelector("#list script") && document.querySelector("#list").textContent.includes(' + json.dumps(content) + ')')
        report["checks"] += ["actual DOM submission stored exactly once while pending", "script input rendered as ordinary text"]
        report["activeCheck"] = "feedback offline recovery and restart"
        browser("fill", "#title", "断网应保留")
        browser("fill", "#content", "未提交的内容")
        old = evaluate('document.querySelector("#status").textContent')
        browser("set", "offline", "on")
        browser("click", "#submit")
        wait('!document.querySelector("#submit").disabled && document.querySelector("#status").textContent !== ' + json.dumps(old))
        assert evaluate('document.querySelector("#title").value === "断网应保留" && document.querySelector("#content").value === "未提交的内容" && !document.querySelector("#refresh").disabled')
        browser("set", "offline", "off")
        browser("click", "#refresh")
        wait('document.querySelector("#list").textContent.includes(' + json.dumps(title) + ')')
        docker("restart", name)
        ready()
        browser("reload")
        wait('document.querySelector("#list").textContent.includes(' + json.dumps(title) + ')')
        assert len(api("/api/feedback")[1]) == 1
        report["checks"] += ["offline error preserves inputs and permits refresh recovery", "actual container restart preserves browser-visible feedback"]
    else:
        report["activeCheck"] = "browser reservation sends the actual string slot ID"
        browser("open", base, other=True)
        browser("wait", "--load", "networkidle", other=True)
        evaluate('document.querySelector("#reserve-slot-1").click();document.querySelector("#reserve-slot-1").click();true')
        wait('!document.querySelector("#refresh").disabled')
        token = evaluate('document.querySelector("#booking-token").value')
        assert isinstance(token, str) and len(token) >= 32, "browser reservation returned no credential; status=" + str(evaluate('document.querySelector("#status").textContent'))
        assert any(s["id"] == "slot-1" and s["reserved"] for s in api("/api/slots")[1])
        old = evaluate('document.querySelector("#status").textContent', other=True)
        browser("click", "#reserve-slot-1", other=True)
        wait('!document.querySelector("#refresh").disabled && document.querySelector("#status").textContent !== ' + json.dumps(old), other=True)
        assert any(r["method"] == "POST" and r["path"] == "/api/bookings" and r["status"] == 409 for r in requests), "second browser did not exercise real conflict"
        assert len([r for r in requests if r["method"] == "POST" and r["path"] == "/api/bookings" and r["status"] == 201]) == 1
        report["checks"] += ["two browser clients exercise availability and real reservation conflict", "pending duplicate clicks yield one successful reservation"]
        report["activeCheck"] = "cancellation after actual server restart and browser reload"
        docker("restart", name)
        ready()
        browser("reload")
        browser("wait", "--load", "networkidle")
        assert any(s["id"] == "slot-1" and s["reserved"] for s in api("/api/slots")[1])
        assert evaluate('!document.querySelector("#booking-token").readOnly'), "credential input cannot restore the saved credential after reload"
        old = evaluate('document.querySelector("#status").textContent')
        browser("fill", "#booking-token", "1" * 64)
        browser("click", "#cancel")
        wait('!document.querySelector("#cancel").disabled && document.querySelector("#status").textContent !== ' + json.dumps(old))
        assert any(s["id"] == "slot-1" and s["reserved"] for s in api("/api/slots")[1])
        browser("fill", "#booking-token", token)
        browser("click", "#cancel")
        wait('!document.querySelector("#cancel").disabled')
        assert all(not s["reserved"] for s in api("/api/slots")[1]), "correct credential did not cancel through the UI"
        browser("fill", "#booking-token", "")
        report["checks"].append("wrong credential rejected and saved credential cancels after actual restart")
        report["activeCheck"] = "booking offline refresh and recovery"
        old = evaluate('document.querySelector("#status").textContent')
        browser("set", "offline", "on")
        browser("click", "#refresh")
        wait('document.querySelector("#status").textContent !== ' + json.dumps(old))
        assert evaluate('!document.querySelector("#refresh").disabled')
        browser("set", "offline", "off")
        browser("click", "#refresh")
        wait('document.querySelector("#status").textContent === "" || document.querySelector("#status").textContent.includes("成功")')
        report["checks"].append("offline refresh error and online recovery")
    report["activeCheck"] = "responsive layout, keyboard and page errors"
    for width, height in [(1440, 1000), (390, 844)]:
        browser("set", "viewport", str(width), str(height))
        assert evaluate('document.documentElement.scrollWidth <= innerWidth'), "horizontal layout overflow"
        path = shots / (a.mode + "-" + str(width) + ".png")
        browser("screenshot", str(path))
        report["screenshots"].append({"file": str(path), "sha256": hashlib.sha256(path.read_bytes()).hexdigest(), "viewport": [width, height]})
        urls = evaluate('performance.getEntriesByType("resource").map(x=>x.name)')
        assert all(u.startswith(base + "/") for u in urls), "undeclared external resource"
    browser("press", "Tab")
    assert evaluate('document.activeElement && document.activeElement !== document.body')
    assert not browser("errors").get("errors", [])
    assert not browser("errors", other=True).get("errors", []) if a.mode == "booking" else True
    report["checks"] += ["desktop and 390px layout without horizontal overflow", "no undeclared external resources", "keyboard focus and no uncaught page errors"]
    report["passed"] = True
    report.pop("activeCheck", None)
except Exception as error:
    report["error"] = str(error)
    try:
        report["visibleStatus"] = evaluate('document.querySelector("#status")?.textContent')
        browser("screenshot", str(shots / "failure.png"))
    except Exception:
        pass
finally:
    for current in [session, secondary]:
        try:
            run(["agent-browser", "--config", config_path, "--session", current, "close"], allow_error=True)
        except Exception:
            pass
    if gateway:
        gateway.shutdown()
        gateway.server_close()
    if started:
        docker("stop", name, allow_error=True)
        report["ownedRuntimeStopped"] = docker("inspect", name, allow_error=True).returncode != 0
    shutil.rmtree(socket_dir)
    report["requests"], report["commands"] = requests, commands
    (root / "result.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
print(json.dumps({"passed": report["passed"], "evidence": str(root), "artifactSHA256": digest,
                  "activeCheck": report.get("activeCheck"), "error": report.get("error"), "checks": report["checks"]}, ensure_ascii=False))
raise SystemExit(0 if report["passed"] else 1)
