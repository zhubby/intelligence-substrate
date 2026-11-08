// Isolated real Docker/HTTP/browser acceptance with test identities and synthetic
// pre-release gates. This is not a public production deployment or store test.
import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import { mkdtemp, mkdir, writeFile, readFile } from "node:fs/promises";
import { createRequire } from "node:module";
import net from "node:net";
import http from "node:http";
import https from "node:https";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repo = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const require = createRequire(path.join(repo, "web/package.json"));
const { chromium } = require("@playwright/test");
const hash = (b) => createHash("sha256").update(b).digest("hex");
const root = await mkdtemp(path.join(repo, "work/runtime-smoke-"));
const instance = "smoke-" + randomBytes(6).toString("hex");
const run = (program, args, options = {}) =>
  execFileSync(program, args, {
    cwd: repo,
    encoding: "utf8",
    stdio: ["pipe", "pipe", "pipe"],
    ...options,
  });
const docker = process.env.IS_DOCKER_BINARY || run("which", ["docker"]).trim();
const dockerHost =
  process.env.DOCKER_HOST ||
  run(docker, [
    "context",
    "inspect",
    "--format",
    "{{.Endpoints.docker.Host}}",
  ]).trim();
const arch = run(docker, ["version", "--format", "{{.Server.Arch}}"]).trim();
const binary = (name) => path.join(repo, "bin/is-" + name);
const port = async () => {
  const server = net.createServer();
  await new Promise((r) => server.listen(0, "127.0.0.1", r));
  const p = server.address().port;
  await new Promise((r) => server.close(r));
  return p;
};
const platformPort = await port(),
  gatewayPort = await port();
const base = "http://127.0.0.1:" + platformPort;
const processes = [];
let tls, browser, owner;
const start = (program, args, env = {}) => {
  const p = spawn(program, args, {
    cwd: repo,
    env: { ...process.env, ...env },
    stdio: ["ignore", "ignore", "pipe"],
  });
  p.stderr.on("data", () => {});
  processes.push(p);
  return p;
};
const ready = async (url) => {
  for (let i = 0; i < 100; i++) {
    try {
      if ((await fetch(url)).ok) return;
    } catch {}
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error("readiness failed: " + url);
};
async function stop(p) {
  if (p.exitCode !== null) return;
  p.kill("SIGTERM");
  await new Promise((resolve) => {
    const timer = setTimeout(() => {
      p.kill("SIGKILL");
      resolve();
    }, 5000);
    p.once("exit", () => {
      clearTimeout(timer);
      resolve();
    });
  });
}
class Session {
  cookie = "";
  csrf = "";
  async post(route, data, raw = false) {
    const response = await fetch(base + "/api/v1/" + route, {
      method: "POST",
      headers: {
        Origin: base,
        "Content-Type": raw ? "application/octet-stream" : "application/json",
        Cookie: this.cookie,
        "X-CSRF-Token": this.csrf,
      },
      body: raw ? data : JSON.stringify(data),
    });
    const result = await response.json();
    assert(response.ok, route + ": " + JSON.stringify(result));
    for (const value of response.headers.getSetCookie())
      this.cookie = value.split(";", 1)[0];
    if (route === "auth/login") this.csrf = result.csrf;
    return result;
  }
  command(name, input) {
    return this.post("commands/" + name, input);
  }
  async workspace(project) {
    const response = await fetch(
      base + "/api/v1/projects/" + project + "/workspace",
      { headers: { Cookie: this.cookie } },
    );
    assert(response.ok);
    return response.json();
  }
}
try {
  start(
    binary("platform"),
    [
      "-listen",
      "127.0.0.1:" + platformPort,
      "-data",
      path.join(root, "control"),
    ],
    {
      IS_DEV_MODE: "true",
      IS_BOOTSTRAP_EMAIL: "runtime@example.test",
      IS_BOOTSTRAP_PASSWORD: "runtime-local-gate-fixture-2026",
      IS_BOOTSTRAP_PASSWORD_FILE: "",
      IS_DATABASE_URL: "",
      IS_PUBLIC_URL: "",
      IS_MODEL_BASE_URL: "",
      IS_MODEL_NAME: "",
      IS_OIDC_ISSUER: "",
    },
  );
  await ready(base + "/healthz");
  const admin = new Session();
  await admin.post("auth/login", {
    email: "runtime@example.test",
    password: "runtime-local-gate-fixture-2026",
  });
  const project = await admin.command("projects.create", {
    title: "Actual container runtime acceptance",
  });
  const goal = await admin.command("goals.create", {
    projectId: project.id,
    title: "Serve feedback with durable data and recover the previous version",
    outcome: "Exercise real container publication and governed recovery",
    targets: ["web"],
    criteria: [
      {
        id: "feedback",
        text: "Feedback persists across version changes",
        method: "manual",
      },
    ],
    budget: 10,
  });
  await admin.command("goals.approve", { id: goal.id });
  const plan = await admin.command("plans.propose", {
    goalId: goal.id,
    summary:
      "Isolated runtime acceptance; synthetic pre-release authorization gates",
    tasks: [
      {
        id: "code",
        title: "Runtime acceptance source",
        kind: "code",
        role: "developer",
        runtime: "linux",
      },
    ],
  });
  await admin.command("plans.approve", { id: plan.id });
  const source = String.raw`package main
import("encoding/json";"fmt";"net/http";"os";"sync";"path/filepath")
var version="development"
func main(){var mu sync.Mutex;items:=[]string{};if b,e:=os.ReadFile("/data/feedback.json");e==nil{if json.Unmarshal(b,&items)!=nil{panic("invalid saved feedback")}}
http.HandleFunc("/",func(w http.ResponseWriter,r *http.Request){w.Header().Set("X-App-Version",version);w.Header().Set("Cache-Control","no-store")
if r.URL.Path=="/api/health"{if b,_:=os.ReadFile("/data/failure");string(b)==version{w.WriteHeader(503)};fmt.Fprint(w,"health");return}
if r.URL.Path=="/api/failure"&&r.Method=="POST"{if os.WriteFile("/data/failure",[]byte(version),0600)!=nil{w.WriteHeader(500)};return}
if r.URL.Path=="/api/feedback"{mu.Lock();defer mu.Unlock();w.Header().Set("Content-Type","application/json");if r.Method=="POST"{var item struct{Text string};if json.NewDecoder(http.MaxBytesReader(w,r.Body,4096)).Decode(&item)!=nil||item.Text==""{w.WriteHeader(400);return};next:=append(append([]string{},items...),item.Text);file,e:=os.CreateTemp("/data","feedback-");if e!=nil{w.WriteHeader(500);return};if json.NewEncoder(file).Encode(next)!=nil||file.Sync()!=nil||file.Close()!=nil||os.Rename(file.Name(),filepath.Join("/data","feedback.json"))!=nil{w.WriteHeader(500);return};items=next};json.NewEncoder(w).Encode(items);return}
if r.URL.Path!="/"{w.WriteHeader(404);return};w.Header().Set("Content-Type","text/html; charset=utf-8");fmt.Fprint(w,"<!doctype html><html lang='zh-CN'><meta charset='utf-8'><title>实际后端发布验收</title><style>body{font:18px system-ui;max-width:760px;margin:64px auto;padding:20px;color:#234239}input,button{font:inherit;padding:10px}li{margin:16px}</style><h1>客户反馈</h1><p>实际 Docker 后端与持久数据验收</p><form><label>反馈内容 <input name='text' required></label> <button>提交反馈</button></form><ul></ul><script>const f=document.querySelector('form'),u=document.querySelector('ul');async function draw(){const a=await(await fetch('/api/feedback')).json();u.replaceChildren(...a.map(x=>{const li=document.createElement('li');li.textContent=x;return li}))}f.onsubmit=async e=>{e.preventDefault();await fetch('/api/feedback',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({text:f.text.value})});f.reset();await draw()};draw()</script></html>")});if e:=http.ListenAndServe(":8080",nil);e!=nil{panic(e)}}`;
  const imageDir = path.join(root, "image");
  await mkdir(path.join(imageDir, "data"), { recursive: true });
  await writeFile(
    path.join(imageDir, "data/.initialized"),
    "owned application data volume",
  );
  await writeFile(path.join(imageDir, "main.go"), source);
  // Deliberately request root in the image; the runtime must override it.
  await writeFile(
    path.join(imageDir, "Dockerfile"),
    'FROM scratch\nLABEL org.intelligence-substrate.data-contract=feedback-v1\nCOPY app /app\nCOPY --chown=10001:10001 data /data\nUSER 0\nENTRYPOINT ["/app"]\n',
  );
  const repository = await admin.command("repositories.create", {
    projectId: project.id,
    title: "runtime-feedback",
  });
  const change = await admin.command("repositories.commit", {
    id: repository.id,
    branch: "runtime-test",
    message: "Add real runtime acceptance backend",
    files: {
      "main.go": source,
      Dockerfile: await readFile(path.join(imageDir, "Dockerfile"), "utf8"),
    },
  });
  const meta = {
    goalId: goal.id,
    repositoryId: repository.id,
    commit: change.data.lastCommit,
    target: "web",
  };
  const upload = async (session, type, bytes, name, extra = {}) =>
    session.post(
      "artifacts/upload?" +
        new URLSearchParams({ ...meta, type, name, ...extra }),
      bytes,
      true,
    );
  const createActor = async (name, roles) => {
    const email = name + "@example.test";
    const identity = await admin.post("identities", {
      email,
      name,
      password: "runtime-test-identity-password-2026",
    });
    await admin.command("members.grant", {
      projectId: project.id,
      identityId: identity.id,
      name,
      roles,
    });
    const session = new Session();
    await session.post("auth/login", {
      email,
      password: "runtime-test-identity-password-2026",
    });
    return session;
  };
  const officer = await createActor("runtime-release", ["release", "quality"]);
  const verifier = await createActor("runtime-verifier", ["quality"]);
  const worker = await admin.command("workers.register", {
    projectId: project.id,
    title: "Actual trusted container publisher",
    roles: ["release"],
    capabilities: ["publish"],
    runtime: "linux",
  });
  const credential = await admin.post("tokens", {
    projectId: project.id,
    workerId: worker.id,
  });
  const tokenFile = path.join(root, "worker-token");
  await writeFile(tokenFile, credential.token, { mode: 0o600 });
  const cert = path.join(root, "localhost.crt"),
    tlsKey = path.join(root, "localhost.key");
  run("openssl", [
    "req",
    "-x509",
    "-newkey",
    "rsa:2048",
    "-nodes",
    "-keyout",
    tlsKey,
    "-out",
    cert,
    "-subj",
    "/CN=localhost",
    "-days",
    "1",
    "-addext",
    "subjectAltName=IP:127.0.0.1",
  ]);
  tls = https.createServer(
    { key: await readFile(tlsKey), cert: await readFile(cert) },
    (request, response) => {
      const upstream = http.request(
        {
          hostname: "127.0.0.1",
          port: gatewayPort,
          path: request.url,
          method: request.method,
          headers: request.headers,
        },
        (r) => {
          response.writeHead(r.statusCode, r.headers);
          r.pipe(response);
        },
      );
      upstream.on("error", () => {
        response.writeHead(503);
        response.end();
      });
      request.pipe(upstream);
    },
  );
  await new Promise((r) => tls.listen(0, "127.0.0.1", r));
  const origin = "https://127.0.0.1:" + tls.address().port;
  const config = {
    instanceId: instance,
    stateDir: path.join(root, "runtime"),
    projectId: project.id,
    goalId: goal.id,
    appId: "feedback",
    publicUrl: origin,
    dockerBinary: docker,
    dockerHost,
    containerPort: 8080,
    healthPath: "/api/health",
    persistentData: true,
    dataContract: "feedback-v1",
    memoryMB: 128,
    cpus: 1,
    platformUrl: base,
    workerTokenFile: tokenFile,
  };
  owner = hash(instance + ":" + project.id + ":" + goal.id + ":feedback").slice(
    0,
    24,
  );
  const configFile = path.join(root, "runtime.json");
  await writeFile(configFile, JSON.stringify(config), { mode: 0o600 });
  run(binary("runtime"), ["-config", configFile, "-mode", "init"]);
  const workerConfig = path.join(root, "worker.json");
  await writeFile(
    workerConfig,
    JSON.stringify({
      driver: "native",
      allowNative: false,
      stateDir: path.join(root, "worker"),
      publishers: {
        web: [binary("runtime"), "-config", configFile, "-mode", "publish"],
      },
      statusProviders: {
        web: [binary("runtime"), "-config", configFile, "-mode", "status"],
      },
      rollbackProviders: {
        web: [binary("runtime"), "-config", configFile, "-mode", "rollback"],
      },
    }),
  );
  const work = () =>
    run(binary("worker"), ["-config", workerConfig, "-once"], {
      env: {
        ...process.env,
        IS_PLATFORM_URL: base,
        IS_WORKER_ALLOW_HTTP: "true",
        IS_WORKER_TOKEN_FILE: tokenFile,
      },
    });
  const monitor = () =>
    run(binary("runtime"), [
      "-config",
      configFile,
      "-mode",
      "monitor",
      "-once",
    ]);
  const assertIsolation = (activation) => {
    const ids = run(docker, [
      "container",
      "ls",
      "--filter",
      "label=org.intelligence-substrate.owner=" + owner,
      "--format",
      "{{.ID}}",
    ])
      .trim()
      .split(/\s+/)
      .filter(Boolean);
    assert.equal(
      ids.length,
      1,
      "there must be exactly one running application writer",
    );
    const [container] = JSON.parse(
      run(docker, ["container", "inspect", ids[0]]),
    );
    assert.equal(container.Config.User, "10001:10001");
    assert.equal(
      container.Config.Labels["org.intelligence-substrate.activation"],
      activation.split(":").at(-1),
    );
    assert.equal(container.HostConfig.ReadonlyRootfs, true);
    assert(container.HostConfig.CapDrop.includes("ALL"));
    assert(
      container.HostConfig.SecurityOpt.some((v) =>
        v.startsWith("no-new-privileges"),
      ),
    );
    assert.equal(container.HostConfig.PidsLimit, 128);
    assert.equal(container.HostConfig.Memory, 128 * 1024 * 1024);
    assert.equal(container.HostConfig.NanoCpus, 1e9);
    assert.equal(
      Object.keys(container.HostConfig.PortBindings || {}).length,
      0,
    );
    const network = "isrt-" + owner;
    assert.deepEqual(Object.keys(container.NetworkSettings.Networks), [
      network,
    ]);
    assert.equal(
      JSON.parse(run(docker, ["network", "inspect", network]))[0].Internal,
      true,
    );
    assert.equal(container.Mounts.length, 1);
    assert.equal(container.Mounts[0].Type, "volume");
    assert.equal(container.Mounts[0].Destination, "/data");
    assert.equal(container.Mounts[0].Name, network + "-data");
  };
  const publish = async (version) => {
    run(
      "go",
      [
        "build",
        "-trimpath",
        "-ldflags",
        "-X main.version=" + version,
        "-o",
        path.join(imageDir, "app"),
        path.join(imageDir, "main.go"),
      ],
      {
        env: { ...process.env, GOOS: "linux", GOARCH: arch, CGO_ENABLED: "0" },
      },
    );
    const imageTag = instance + ":" + version;
    run(docker, ["build", "--network=none", "--tag", imageTag, imageDir]);
    const archive = path.join(root, version + ".tar");
    run(docker, ["image", "save", "--output", archive, imageTag]);
    const artifact = await upload(
      admin,
      "package",
      await readFile(archive),
      version + ".tar",
    );
    const proof = await upload(
      verifier,
      "report",
      Buffer.from(
        "Synthetic pre-release authorization gate for isolated runtime acceptance; not production QA.",
      ),
      "precondition.txt",
    );
    await verifier.command("evidence.record", {
      artifactId: artifact.id,
      proofArtifactId: proof.id,
      method: "manual",
      verificationEnvironment: "browser",
      criteria: ["feedback"],
      statement:
        "Synthetic gate fixture; actual Docker and browser behavior is tested after activation.",
    });
    const release = await admin.command("releases.create", {
      goalId: goal.id,
      version,
      commit: meta.commit,
      artifactIds: [artifact.id],
    });
    await officer.command("releases.approve", { id: release.id });
    const task = await officer.command("releases.dispatch", {
      id: release.id,
      target: "web",
      workerId: worker.id,
      key: version,
    });
    work();
    const state = await admin.workspace(project.id);
    const d = state.find(
      (x) =>
        x.kind === "deployment" &&
        x.status === "active" &&
        x.data.releaseId === release.id,
    );
    assert(d, "actual publisher did not register the deployed application");
    assertIsolation(d.data.externalId);
    return { deployment: d, release, artifact, task, archive };
  };
  const first = await publish("1.0.0");
  let gateway = start(binary("runtime"), [
    "-config",
    configFile,
    "-mode",
    "gateway",
    "-listen",
    "127.0.0.1:" + gatewayPort,
  ]);
  await ready("http://127.0.0.1:" + gatewayPort + "/__substrate/health");
  browser = await chromium.launch({
    executablePath: process.env.IS_BROWSER_EXECUTABLE || undefined,
    headless: true,
  });
  const context = await browser.newContext({ ignoreHTTPSErrors: true });
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(origin);
  await page.getByLabel("反馈内容").fill("第一版写入：真实后端持久数据");
  await page.getByRole("button", { name: "提交反馈", exact: true }).click();
  await page.getByRole("listitem").filter({ hasText: "第一版写入" }).waitFor();
  await page.reload();
  assert.equal(await page.locator("li").count(), 1);
  const second = await publish("1.1.0");
  await page.reload();
  assert.equal(await page.locator("li").count(), 1);
  await page.getByLabel("反馈内容").fill("第二版写入：回滚后必须保留");
  await page.getByRole("button", { name: "提交反馈", exact: true }).click();
  await page.getByRole("listitem").filter({ hasText: "第二版写入" }).waitFor();
  const failed = await context.request.post(origin + "/api/failure");
  assert(failed.ok());
  for (let i = 0; i < 3; i++) monitor();
  let state = await admin.workspace(project.id);
  assert.equal(
    state.filter((x) => x.kind === "incident" && x.status === "open").length,
    1,
  );
  const rollback = await admin.command("runtime.rollback.propose", {
    id: second.deployment.id,
    destinationId: first.deployment.id,
    reason:
      "Actual test: v1.1 health endpoint fails; recover v1.0 with persisted feedback",
    key: "recover",
  });
  await officer.command("runtime.rollback.approve", { id: rollback.id });
  work();
  const response = await context.request.get(origin + "/api/feedback");
  assert.equal(response.headers()["x-app-version"], "1.0.0");
  assert.equal(
    response.headers()["x-substrate-artifact-sha256"],
    first.artifact.data.digest,
  );
  assert.equal((await response.json()).length, 2);
  await page.reload();
  assert.equal(await page.locator("li").count(), 2);
  assert.deepEqual(errors, []);
  for (let i = 0; i < 2; i++) monitor();
  state = await admin.workspace(project.id);
  assert.equal(
    state.filter((x) => x.kind === "incident" && x.status === "resolved")
      .length,
    1,
  );
  const active = state.find(
    (x) => x.kind === "deployment" && x.status === "active",
  );
  assert.equal(active.data.verificationStatus, "required");
  assert.equal(active.data.version, "1.0.0");
  assertIsolation(active.data.externalId);
  await admin.command("goals.revise", {
    id: goal.id,
    reason:
      "Future client scope changes; retain operational recovery and verification for the existing service",
    targets: ["windows"],
  });
  const actualReport = {
    scope:
      "actual local Docker/TLS/browser operational acceptance; test actor identities",
    activationId: active.data.externalId,
    artifactDigest: first.artifact.data.digest,
    checks: [
      "v1/v2 backend writes",
      "health incident",
      "human-gated rollback",
      "two feedback records preserved",
      "accurate version headers",
      "future baseline target removal retains operational proof",
    ],
  };
  const proof = await upload(
    verifier,
    "report",
    Buffer.from(JSON.stringify(actualReport)),
    "actual-runtime-report.json",
    { deploymentId: active.id },
  );
  await verifier.command("runtime.verify", {
    id: active.id,
    proofArtifactId: proof.id,
    statement:
      "Actual isolated harness checked this activation in Chrome and verified the backend records and version headers. Test identity; not a production or physical-device acceptance.",
  });
  const replay = {
    protocolVersion: "1",
    action: "publish",
    projectId: project.id,
    goalId: goal.id,
    operationKey: second.task.data.operationKey,
    releaseId: second.release.id,
    target: "web",
    version: "1.1.0",
    artifactId: second.artifact.id,
    artifactPath: second.archive,
    artifactDigest: second.artifact.data.digest,
    artifactSize: second.artifact.data.size,
    commit: meta.commit,
    binding: second.release.data.binding,
  };
  run(binary("runtime"), ["-config", configFile, "-mode", "publish"], {
    input: JSON.stringify(replay),
  });
  assert.equal(
    (await context.request.get(origin + "/api/feedback")).headers()[
      "x-app-version"
    ],
    "1.0.0",
  );
  await stop(gateway);
  gateway = start(binary("runtime"), [
    "-config",
    configFile,
    "-mode",
    "gateway",
    "-listen",
    "127.0.0.1:" + gatewayPort,
  ]);
  await ready("http://127.0.0.1:" + gatewayPort + "/__substrate/health");
  await page.reload();
  assert.equal(await page.locator("li").count(), 2);
  await page.screenshot({
    path: path.join(root, "actual-backend.png"),
    fullPage: true,
  });
  const result = {
    status: "passed",
    directory: root,
    scope:
      "actual Docker scratch images, restricted containers, real backend writes, local TLS, gateway restart, health incident/recovery, approved rollback, preserved data, replay safety and Chrome; synthetic pre-release gates; no public production/store/native acceptance",
    versions: ["1.0.0", "1.1.0", "1.0.0 rollback"],
    feedbackRecords: 2,
    activationId: active.data.externalId,
    containerOwner: owner,
    cleanup: "test containers stopped; test images and data volume retained",
  };
  await writeFile(
    path.join(root, "result.json"),
    JSON.stringify(result, null, 2),
  );
  console.log(JSON.stringify(result, null, 2));
} finally {
  if (browser) await browser.close();
  if (tls)
    await new Promise((r) => {
      tls.closeAllConnections();
      tls.close(r);
    });
  for (const p of [...processes].reverse()) await stop(p);
  if (owner) {
    const ids = run(docker, [
      "container",
      "ls",
      "--all",
      "--filter",
      "label=org.intelligence-substrate.owner=" + owner,
      "--format",
      "{{.ID}}",
    ])
      .trim()
      .split(/\s+/)
      .filter(Boolean);
    if (ids.length) run(docker, ["container", "stop", "--time", "5", ...ids]);
  }
}
