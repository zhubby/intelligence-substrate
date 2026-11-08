"""Protected black-box checks for the live-model reference variants.

Run inside the isolated quality container. This source is supplied by the
operator through the pipeline command, outside the generated repository.
"""
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
import hashlib
import json
import os
import socket
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.error
import urllib.request


mode = sys.argv[1]
assert mode in {"feedback", "booking"}
artifact = Path(os.environ["IS_ARTIFACT_PATH"])
checks = []
with tempfile.TemporaryDirectory(prefix="independent-app-") as directory:
    root = Path(directory)
    with tarfile.open(artifact) as archive:
        members = archive.getmembers()
        assert sorted(m.name for m in members) == ["app.js", "index.html"], "unexpected package members"
        for member in members:
            assert member.isfile() and member.size <= 512000, "unsafe or oversized package member"
            body = archive.extractfile(member).read()
            assert body == Path(member.name).read_bytes(), "artifact differs from fixed source"
            (root / member.name).write_bytes(body)
    checks.append("artifact bytes equal the control-plane-pinned source")
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
    base = f"http://127.0.0.1:{port}"
    state = root / "state.json"
    env = {"PATH": os.environ["PATH"], "HOME": str(root), "PORT": str(port), "HOST": "127.0.0.1",
           "STATE_FILE": str(state), "APP_SLOTS": json.dumps([{"id": "slot-1", "startsAt": "2030-01-01T10:00:00Z"},
                                                              {"id": "slot-2", "startsAt": "2030-01-01T11:00:00Z"}])}

    def request(path, payload=None):
        body = None if payload is None else json.dumps(payload, ensure_ascii=False).encode()
        req = urllib.request.Request(base + path, data=body, headers={"Content-Type": "application/json"})
        try:
            response = urllib.request.urlopen(req, timeout=5)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            raw = response.read(1000000)
            return response.code, json.loads(raw)

    log = (root / "server.log").open("wb")

    def start():
        child = subprocess.Popen(["node", "app.js"], cwd=root, env=env, stdout=log, stderr=log)
        deadline = time.monotonic() + 8
        while time.monotonic() < deadline:
            assert child.poll() is None, "generated application exited before readiness"
            try:
                if request("/api/health")[0] == 200:
                    return child
            except (OSError, ValueError):
                pass
            time.sleep(.05)
        child.terminate()
        child.wait(timeout=5)
        raise AssertionError("generated application did not become ready")

    def stop(child):
        child.terminate()
        try:
            child.wait(timeout=5)
        except subprocess.TimeoutExpired:
            child.kill()
            child.wait(timeout=5)

    process = start()
    try:
        with urllib.request.urlopen(base + "/", timeout=5) as response:
            assert response.status == 200 and b"<" in response.read(), "no actual web client"
        if mode == "feedback":
            assert request("/api/feedback") == (200, [])
            for payload in [{}, {"title": " "}, {"title": "界" * 67}, {"title": "valid", "content": "x" * 8001}]:
                assert request("/api/feedback", payload)[0] == 422, "invalid feedback was accepted"
            assert request("/api/feedback") == (200, []), "invalid request partially persisted"
            title, content = "Independent feedback", "<script>window.__injected=true</script>中文"
            status, created = request("/api/feedback", {"title": title, "content": content})
            assert status == 201 and created["title"] == title and created["content"] == content
            assert created.get("id") and created.get("createdAt")
            status, rows = request("/api/feedback")
            assert status == 200 and len(rows) == 1 and rows[0] == created
            stop(process)
            process = start()
            assert request("/api/feedback") == (200, [created]), "feedback lost on server restart"
            checks += ["F1 actual write and matching list", "F2 UTF-8 and oversized input rejection without partial data",
                       "F3 process restart persistence"]
        else:
            status, slots = request("/api/slots")
            assert status == 200 and len(slots) == 2 and not any(s["reserved"] for s in slots)
            with ThreadPoolExecutor(max_workers=12) as pool:
                results = list(pool.map(lambda _: request("/api/bookings", {"slotId": "slot-1"}), range(12)))
            assert sum(status == 201 for status, _ in results) == 1
            assert sum(status == 409 for status, _ in results) == 11, "concurrent booking did not conflict"
            token = next(body["bookingToken"] for status, body in results if status == 201)
            assert isinstance(token, str) and len(token) >= 32, "booking credential too short"
            stored = state.read_text()
            assert token not in stored and hashlib.sha256(token.encode()).hexdigest() in stored
            assert request("/api/bookings/cancel", {"bookingToken": "wrong-token"})[0] in {403, 404}
            assert any(s["reserved"] for s in request("/api/slots")[1] if s["id"] == "slot-1")
            stop(process)
            process = start()
            assert any(s["reserved"] for s in request("/api/slots")[1] if s["id"] == "slot-1")
            assert request("/api/bookings/cancel", {"bookingToken": token})[0] == 200
            stop(process)
            process = start()
            assert all(not s["reserved"] for s in request("/api/slots")[1])
            other_status, other = request("/api/bookings", {"slotId": "slot-2"})
            assert other_status == 201 and other["bookingToken"] != token
            checks += ["B1 current slot availability", "B2 twelve actual concurrent requests yield one success",
                       "B3 distinct opaque credentials and SHA256-only server storage",
                       "B4 cancellation requires the correct credential", "B5 reservation and cancellation survive restart"]
        print(json.dumps({"passed": True, "mode": mode, "checks": checks}, ensure_ascii=False))
    finally:
        stop(process)
        log.close()
