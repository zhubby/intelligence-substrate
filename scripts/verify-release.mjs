// Local binary acceptance, with synthetic approved identities and a temporary
// TLS certificate. Does not submit to stores or modify the workbench's data.
import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import { createHash, createPublicKey, verify } from "node:crypto";
import { mkdir, mkdtemp, readFile, writeFile } from "node:fs/promises";
import http from "node:http";
import https from "node:https";
import path from "node:path";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";

const repo = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const require = createRequire(path.join(repo, "web/package.json"));
const { chromium } = require("@playwright/test");
const hash = (v) => createHash("sha256").update(v).digest("hex");
await mkdir(path.join(repo, "work"), { recursive: true });
const root = await mkdtemp(path.join(repo, "work/release-smoke-"));
const binary = (name) => path.join(repo, "bin/is-" + name);
const run = (name, args, options = {}) =>
  execFileSync(binary(name), args, {
    cwd: repo,
    encoding: "utf8",
    stdio: ["pipe", "pipe", "pipe"],
    ...options,
  });
const dist = path.join(root, "distribution");
const keyFile = path.join(root, "update-key");
const publicKey = JSON.parse(run("publisher", ["-init-key", keyFile]));
run("distribution", ["-root", dist, "-init"]);
const server = spawn(
  binary("distribution"),
  ["-root", dist, "-listen", "127.0.0.1:0"],
  { cwd: repo, stdio: ["ignore", "ignore", "pipe"] },
);
let browser, tls;
try {
  const backend = await new Promise((resolve, reject) => {
    const timer = setTimeout(
      () => reject(new Error("distribution readiness timeout")),
      10000,
    );
    let output = "";
    server.stderr.on("data", (b) => {
      output += b;
      const found = output.match(/address=127\.0\.0\.1:(\d+)/);
      if (found) {
        clearTimeout(timer);
        resolve(Number(found[1]));
      }
    });
    server.once("error", reject);
    server.once("exit", (code) => {
      clearTimeout(timer);
      reject(new Error("distribution exited: " + code));
    });
  });
  const cert = path.join(root, "localhost.crt"),
    tlsKey = path.join(root, "localhost.key");
  execFileSync(
    "openssl",
    [
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
    ],
    { stdio: ["ignore", "ignore", "pipe"] },
  );
  const ca = await readFile(cert);
  tls = https.createServer(
    { key: await readFile(tlsKey), cert: ca },
    (request, response) => {
      const upstream = http.request(
        {
          hostname: "127.0.0.1",
          port: backend,
          path: request.url,
          method: request.method,
        },
        (r) => {
          response.writeHead(r.statusCode, r.headers);
          r.pipe(response);
        },
      );
      upstream.on("error", () => {
        response.writeHead(502);
        response.end();
      });
      request.pipe(upstream);
    },
  );
  await new Promise((resolve) => tls.listen(0, "127.0.0.1", resolve));
  const origin = "https://127.0.0.1:" + tls.address().port;
  const configFile = path.join(root, "publisher.json");
  await writeFile(
    configFile,
    JSON.stringify({
      stateDir: path.join(root, "publisher"),
      profiles: [
        {
          projectId: "project_smoke",
          goalId: "goal_smoke",
          target: "web",
          driver: "hosted",
          appId: "feedback",
          channel: "stable",
          distributionRoot: dist,
          publicUrl: origin,
          signingKeyFile: keyFile,
        },
      ],
    }),
    { mode: 0o600 },
  );
  const site = path.join(root, "site");
  await mkdir(site);
  await writeFile(
    path.join(site, "index.html"),
    `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><title>客户反馈 · 本地发布验收</title><style>body{font:18px system-ui;max-width:720px;margin:64px auto;padding:24px;color:#17392e}input,button{font:inherit;padding:12px}li{margin:16px 0}</style><h1>客户反馈</h1><p>本地发布程序与分发服务验收</p><form><label>反馈内容 <input required name="feedback"></label> <button>提交反馈</button></form><ul></ul><script>const f=document.querySelector('form'),u=document.querySelector('ul');function draw(){u.replaceChildren(...JSON.parse(localStorage.getItem('feedback')||'[]').map(x=>{const l=document.createElement('li');l.textContent=x;return l}))}f.onsubmit=e=>{e.preventDefault();const a=JSON.parse(localStorage.getItem('feedback')||'[]');a.push(f.feedback.value);localStorage.setItem('feedback',JSON.stringify(a));f.reset();draw()};draw()</script></html>`,
  );
  const packageFile = path.join(root, "feedback.tar.gz");
  execFileSync("tar", ["-czf", packageFile, "-C", site, "index.html"], {
    env: { ...process.env, COPYFILE_DISABLE: "1" },
  });
  const content = await readFile(packageFile);
  const request = {
    protocolVersion: "1",
    action: "publish",
    operationKey: hash("local-smoke-first"),
    projectId: "project_smoke",
    goalId: "goal_smoke",
    releaseId: "release_smoke",
    target: "web",
    version: "1.0.0",
    artifactId: "artifact_smoke",
    artifactPath: packageFile,
    artifactDigest: hash(content),
    artifactSize: content.length,
    commit: "1".repeat(40),
    binding: "b".repeat(64),
  };
  const publish = (r, extra = []) =>
    JSON.parse(
      run("publisher", ["-config", configFile, ...extra], {
        input: JSON.stringify(r),
      }),
    );
  const first = publish(request);
  assert.equal(first.state, "published");
  assert.equal(publish(request).externalId, first.externalId);
  assert.equal(
    publish(
      {
        ...request,
        action: "status",
        externalId: first.externalId,
        artifactPath: "",
      },
      ["-status-only"],
    ).state,
    "published",
  );
  assert.throws(() => publish(request, ["-status-only"]));
  const get = (url) =>
    new Promise((resolve, reject) => {
      https
        .get(url, { ca }, (r) => {
          const chunks = [];
          r.on("data", (b) => chunks.push(b));
          r.on("end", () =>
            resolve({
              status: r.statusCode,
              headers: r.headers,
              body: Buffer.concat(chunks),
            }),
          );
          r.on("error", reject);
        })
        .on("error", reject);
    });
  const envelope = JSON.parse(
    (await get(origin + "/updates/feedback/web/stable.json")).body,
  );
  const key = createPublicKey({
    key: Buffer.concat([
      Buffer.from("302a300506032b6570032100", "hex"),
      Buffer.from(publicKey.publicKey, "base64"),
    ]),
    format: "der",
    type: "spki",
  });
  const payload = Buffer.from(envelope.payload, "base64");
  assert(
    verify(
      null,
      Buffer.concat([
        Buffer.from("intelligence-substrate-update-v1\n"),
        payload,
      ]),
      key,
      Buffer.from(envelope.signature, "base64"),
    ),
  );
  const feed = JSON.parse(payload);
  assert.equal(feed.sequence, 1);
  assert.equal(feed.sha256, request.artifactDigest);
  const download = await get(feed.url);
  assert.equal(download.status, 200);
  assert.equal(hash(download.body), request.artifactDigest);
  const second = {
    ...request,
    operationKey: hash("local-smoke-second"),
    version: "1.1.0",
    releaseId: "release_smoke_2",
  };
  publish(second);
  publish(request);
  const current = JSON.parse(
    Buffer.from(
      JSON.parse((await get(origin + "/updates/feedback/web/stable.json")).body)
        .payload,
      "base64",
    ),
  );
  assert.equal(current.version, "1.1.0");
  assert.equal(current.sequence, 2);
  assert.equal(
    publish(
      {
        ...request,
        action: "status",
        externalId: first.externalId,
        artifactPath: "",
      },
      ["-status-only"],
    ).state,
    "halted",
  );
  run("publisher", ["-config", configFile, "-renew-feeds"]);
  const renewed = JSON.parse(
    Buffer.from(
      JSON.parse((await get(origin + "/updates/feedback/web/stable.json")).body)
        .payload,
      "base64",
    ),
  );
  assert.equal(renewed.version, "1.1.0");
  assert.equal(renewed.sequence, 3);
  const deployed = await get(first.url);
  assert.equal(deployed.status, 200);
  assert.equal(deployed.headers["x-substrate-release-id"], "release_smoke_2");
  browser = await chromium.launch({
    executablePath: process.env.IS_BROWSER_EXECUTABLE || undefined,
    headless: true,
  });
  const context = await browser.newContext({ ignoreHTTPSErrors: true });
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(first.url);
  await page.getByLabel("反馈内容").fill("实际 Chrome 提交与持久化验收");
  await page.getByRole("button", { name: "提交反馈", exact: true }).click();
  await page.waitForFunction(
    () =>
      document.querySelector("li")?.textContent ===
      "实际 Chrome 提交与持久化验收",
  );
  await page.reload();
  assert.equal(
    await page.locator("li").textContent(),
    "实际 Chrome 提交与持久化验收",
  );
  assert.deepEqual(errors, []);
  await page.screenshot({
    path: path.join(root, "published-web.png"),
    fullPage: true,
  });
  const result = {
    status: "passed",
    evidence:
      "actual CLI, local TLS, download hash, signed index, replay, renewal and Chrome; synthetic approval identities; no store or native device acceptance",
    directory: root,
    version: renewed.version,
    sequence: renewed.sequence,
  };
  await writeFile(
    path.join(root, "result.json"),
    JSON.stringify(result, null, 2),
  );
  console.log(JSON.stringify(result, null, 2));
} finally {
  if (browser) await browser.close();
  if (tls)
    await new Promise((resolve) => {
      tls.closeAllConnections();
      tls.close(resolve);
    });
  server.kill("SIGTERM");
  await new Promise((resolve) => {
    if (server.exitCode !== null) return resolve();
    const timer = setTimeout(() => {
      server.kill("SIGKILL");
      resolve();
    }, 5000);
    server.once("exit", () => {
      clearTimeout(timer);
      resolve();
    });
  });
}
