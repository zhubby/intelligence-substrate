// Real gateway/node/Docker/Flutter business interactions, no API stubs.
import fs from "node:fs";
import http from "node:http";
import https from "node:https";
import assert from "node:assert/strict";
import { createRequire } from "node:module";
const cfg = JSON.parse(fs.readFileSync(process.argv[2], "utf8"));
const phase = process.argv[3];
const { chromium, expect } = createRequire(cfg.repo + "/web/package.json")(
  "@playwright/test",
);
const tlsArgs = cfg.tls
  ? [
      "--host-resolver-rules=" +
        [cfg.origin, cfg.previewOrigin]
          .map((value) => "MAP " + new URL(value).hostname + " 127.0.0.1")
          .join(", ") +
        ", EXCLUDE localhost",
      "--ignore-certificate-errors-spki-list=" + cfg.tls.pin,
    ]
  : [];
const browser = await chromium.launch({
  headless: true,
  executablePath:
    process.env.IS_BROWSER_EXECUTABLE ||
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
  args: ["--no-proxy-server", "--disable-background-networking", ...tlsArgs],
});
const report = {
  phase,
  passed: false,
  browser: browser.version(),
  apiResponsesMocked: false,
  httpsIngress: !!cfg.tls,
  exactLeafSPKITrusted: !!cfg.tls,
  broadTLSBypass: false,
  checks: [],
  pageErrors: [],
  consoleErrors: [],
  externalRequests: [],
  viewports: [],
  bindings: [],
  tlsResponses: [],
  responseErrors: [],
};
let recovery;
const check = (name) => report.checks.push({ name, passed: true });
const sanitize = (s) => s.replace(/\/p\/[^/]+\//g, "/p/[private]/");
async function closedRequest(url, data) {
  const target = new URL(url);
  assert.equal(target.origin, cfg.previewOrigin);
  return new Promise((resolve, reject) => {
    const request = (cfg.tls ? https : http).request(
      target,
      {
        ...(cfg.tls ? { ca: fs.readFileSync(cfg.tls.caFile) } : {}),
        family: 4,
        autoSelectFamily: false,
        lookup: (_hostname, _options, callback) =>
          callback(null, "127.0.0.1", 4),
        method: data === undefined ? "GET" : "POST",
        headers:
          data === undefined ? {} : { "Content-Type": "application/json" },
      },
      (response) => {
        response.resume();
        response.on("end", () => resolve(response.statusCode));
      },
    );
    request.setTimeout(10000, () =>
      request.destroy(new Error("Local HTTPS probe timed out")),
    );
    request.on("error", reject);
    request.end(data === undefined ? undefined : JSON.stringify(data));
  });
}
const markerPath = cfg.root + "/private-session-markers.json";
const markers = fs.existsSync(markerPath)
  ? JSON.parse(fs.readFileSync(markerPath, "utf8"))
  : [];
const remember = (value) => {
  if (value) {
    markers.push(value);
    fs.writeFileSync(markerPath, JSON.stringify(markers), { mode: 0o600 });
  }
};
async function api(page, path, data, csrf, expected = 200) {
  const result = await page.evaluate(
    async ({ path, data, csrf }) => {
      const r = await fetch("/api/v1/" + path, {
        method: data === undefined ? "GET" : "POST",
        headers: {
          "Content-Type": "application/json",
          ...(csrf ? { "X-CSRF-Token": csrf } : {}),
        },
        body: data === undefined ? undefined : JSON.stringify(data),
      });
      return { status: r.status, body: await r.json() };
    },
    { path, data, csrf },
  );
  assert.equal(result.status, expected, path);
  remember(result.body.csrf);
  if (result.body.url) {
    remember(result.body.url);
    remember(result.body.url.split("/p/")[1]?.split("/")[0]);
  }
  return result.body;
}
async function context(options = {}) {
  const c = await browser.newContext(options);
  await c.route("**/*", async (route) => {
    const url = new URL(route.request().url());
    if ([cfg.origin, cfg.previewOrigin].includes(url.origin))
      return route.continue();
    report.externalRequests.push(url.origin);
    await route.abort();
  });
  return c;
}
async function open(c, url, artifact) {
  const page = await c.newPage();
  page.on("pageerror", (e) => report.pageErrors.push(e.message));
  page.on("console", (m) => {
    if (m.type() === "error") report.consoleErrors.push(sanitize(m.text()));
  });
  const requests = [];
  page.on("response", (response) => {
    if (
      !response.url().startsWith(cfg.previewOrigin) ||
      !response.url().includes("/files/")
    )
      return;
    if (
      ["substrate-preview/backend", "substrate-preview/manifest.json"].some(
        (name) => new URL(response.url()).pathname.endsWith("/files/" + name),
      )
    )
      return; // These private-file denials are asserted separately below.
    requests.push(
      (async () => {
        assert.equal(response.status(), 200);
        const h = await response.allHeaders();
        assert.equal(h["x-substrate-artifact-sha256"], artifact.data.digest);
        assert.equal(h["x-substrate-commit"], artifact.data.commit);
        const sent = await response.request().allHeaders();
        assert.equal(sent.cookie || "", "");
        assert.equal(sent.authorization || "", "");
        assert.equal(sent.referer || "", "");
      })().catch((error) => {
        report.responseErrors.push(sanitize(error.stack || String(error)));
      }),
    );
  });
  const first = page.waitForResponse(
    (r) =>
      r.url().startsWith(url + "api/") &&
      r.request().method() === "GET" &&
      r.status() === 200,
    { timeout: 60000 },
  );
  first.catch(() => {}); // Navigation failure must still reach the report/finally.
  const navigation = await page.goto(url);
  assert.equal(navigation.status(), 200);
  if (cfg.tls) {
    const security = await navigation.securityDetails();
    assert(security);
    assert.equal(security.protocol, "TLS 1.3");
    report.tlsResponses.push({ artifactId: artifact.id, ...security });
  }
  const frame = page.frameLocator("iframe");
  await frame
    .locator("flt-semantics-placeholder")
    .waitFor({ state: "attached", timeout: 60000 });
  await frame.locator("flt-semantics-placeholder").dispatchEvent("click");
  await expect(
    frame.getByRole("button", { name: "刷新", exact: true }),
  ).toBeVisible();
  await first;
  assert.equal(
    await frame.getByRole("button", { name: "应用更新", exact: true }).count(),
    0,
  );
  await Promise.all(requests);
  assert.deepEqual(report.responseErrors, []);
  report.bindings.push({
    artifactId: artifact.id,
    digest: artifact.data.digest,
    commit: artifact.data.commit,
    verifiedStaticResponses: requests.length,
  });
  return { page, frame, url };
}
async function visible(app, text) {
  await expect
    .poll(
      () =>
        app.frame
          .locator("flt-semantics")
          .evaluateAll(
            (nodes, t) =>
              nodes.some(
                (n) =>
                  (n.getAttribute("aria-label") || "").includes(t) ||
                  (n.textContent || "").includes(t),
              ),
            text,
          ),
      { timeout: 15000 },
    )
    .toBe(true);
}
async function input(app, name, value) {
  const field = app.frame.getByRole("textbox", { name, exact: true });
  await field.click();
  await expect(field).toBeFocused();
  await app.page.evaluate(
    () =>
      new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))),
  );
  await app.page.keyboard.press("ControlOrMeta+A");
  await app.page.keyboard.insertText(value);
  await expect(field).toHaveValue(value);
  await app.page.keyboard.press("Tab");
}
async function business(app, path, data) {
  return app.page.evaluate(
    async ({ url, path, data }) => {
      const r = await fetch(url + "api/" + path, {
        method: data === undefined ? "GET" : "POST",
        credentials: "omit",
        headers:
          data === undefined ? {} : { "Content-Type": "application/json" },
        body: data === undefined ? undefined : JSON.stringify(data),
      });
      return { status: r.status, body: await r.json() };
    },
    { url: app.url, path, data },
  );
}
try {
  if (phase === "healthy") {
    const c = await context({ viewport: { width: 1440, height: 1000 } });
    const control = await c.newPage();
    await control.goto(cfg.origin + "/healthz");
    const session = await api(control, "auth/login", {
      email: cfg.email,
      password: cfg.password,
    });
    const command = (name, data) =>
      api(control, "commands/" + name, data, session.csrf);
    if (cfg.tls) {
      const cookies = await c.cookies(cfg.origin);
      const cookie = cookies.find((value) => value.name === "is_session");
      assert(cookie);
      assert.equal(cookie.domain, new URL(cfg.origin).hostname);
      assert(cookie.secure && cookie.httpOnly);
      assert.equal(cookie.sameSite, "Strict");
      assert.deepEqual(await c.cookies(cfg.previewOrigin), []);
      assert.equal(await control.evaluate(() => document.cookie), "");
      check("secure-http-only-host-only-session-and-no-preview-cookie");
    }
    recovery = { apps: [], independentPreviewId: "" };
    for (const app of cfg.applications) {
      const access = await api(
        control,
        "previews/" + app.preview.id + "/access",
        {},
        session.csrf,
      );
      const opened = await open(c, access.url, app.artifact);
      recovery.apps.push({
        mode: app.mode,
        url: access.url,
        artifact: app.artifact,
        previewId: app.preview.id,
      });
      for (const privateName of [
        "substrate-preview/backend",
        "substrate-preview/manifest.json",
      ]) {
        const status = await opened.page.evaluate(
          async (url) => (await fetch(url, { credentials: "omit" })).status,
          access.url + "files/" + privateName,
        );
        assert.equal(status, 404);
      }
      await api(
        control,
        "commands/measurements.record",
        {
          goalId: app.goalId,
          type: "preview",
          artifactId: app.artifact.id,
          evidenceId: app.preview.id,
          reference: "preview-is-not-quality-" + app.mode,
          key: "preview-is-not-quality-" + app.mode,
          note: "A private preview resource cannot replace independent runtime evidence",
        },
        session.csrf,
        409,
      );
      const metrics = await api(
        control,
        "projects/" + app.projectId + "/delivery-metrics?goalId=" + app.goalId,
      );
      assert.equal(metrics.previewSamples, 0);
      assert.equal(metrics.meanPreviewSeconds, null);
      assert.deepEqual(metrics.observations, []);
      check(
        "preview-alone-cannot-fill-quality-or-delivery-metrics-" + app.mode,
      );
      if (app.mode === "feedback") {
        await opened.frame
          .getByRole("button", { name: "提交反馈", exact: true })
          .click();
        await visible(opened, "请输入反馈标题。");
        assert.deepEqual((await business(opened, "feedback")).body, []);
        await input(opened, "反馈标题", "完整预览反馈");
        await input(opened, "详细说明", "通过平台隔离后端保存");
        await opened.frame
          .getByRole("button", { name: "提交反馈", exact: true })
          .dblclick();
        await visible(opened, "完整预览反馈");
        const records = await business(opened, "feedback");
        assert.equal(records.status, 200);
        assert.equal(records.body.length, 1);
        assert.equal(records.body[0].content, "通过平台隔离后端保存");
        const separate = await command("previews.create", {
          artifactId: app.artifact.id,
          expectedRequirementRevision: 1,
          title: "独立数据预览",
          reason: "Verify exact same package has independent temporary storage",
          key: "independent-data",
        });
        recovery.independentPreviewId = separate.id;
        const otherAccess = await api(
          control,
          "previews/" + separate.id + "/access",
          {},
          session.csrf,
        );
        const other = await open(c, otherAccess.url, app.artifact);
        assert.deepEqual((await business(other, "feedback")).body, []);
        await other.page.close();
        check(
          "actual-feedback-validation-single-write-and-preview-data-isolation",
        );
      } else {
        const stale = await open(c, access.url, app.artifact);
        await opened.frame
          .getByRole("button", { name: "预约", exact: true })
          .click();
        await visible(opened, "预约成功");
        await opened.frame
          .getByRole("button", { name: "已保存", exact: true })
          .click();
        await visible(opened, "已预约");
        const credential = opened.frame.getByRole("textbox", {
          name: "取消凭据",
        });
        await credential.click();
        await expect(credential).toHaveValue(/^[a-f0-9]{48}$/);
        const token = await credential.inputValue();
        await stale.frame
          .getByRole("button", { name: "预约", exact: true })
          .click();
        await visible(stale, "This slot is already reserved");
        await input(opened, "取消凭据", "wrong-token");
        await opened.frame
          .getByRole("button", { name: "取消我的预约", exact: true })
          .click();
        await visible(opened, "Booking not found");
        assert.equal((await business(opened, "slots")).body[0].reserved, true);
        await input(opened, "取消凭据", token);
        await opened.frame
          .getByRole("button", { name: "取消我的预约", exact: true })
          .click();
        await visible(opened, "可以预约");
        assert.equal((await business(opened, "slots")).body[0].reserved, false);
        await stale.page.close();
        check("actual-booking-conflict-capability-and-cancellation");
      }
      await opened.page.screenshot({
        path: cfg.root + "/" + app.mode + "-desktop.png",
        fullPage: true,
      });
      report.viewports.push({ mode: app.mode, width: 1440 });
      const mobile = await context({
        viewport: { width: 390, height: 844 },
        isMobile: true,
        hasTouch: true,
        storageState: await c.storageState(),
      });
      const small = await open(mobile, access.url, app.artifact);
      await visible(
        small,
        app.mode === "feedback" ? "完整预览反馈" : "可以预约",
      );
      assert(
        await small.page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      );
      if (app.mode === "feedback") {
        await input(small, "反馈标题", "移动宽度反馈");
        await small.frame
          .getByRole("button", { name: "提交反馈", exact: true })
          .click();
        await visible(small, "移动宽度反馈");
        assert.equal((await business(small, "feedback")).body.length, 2);
      } else {
        await small.frame
          .getByRole("button", { name: "预约", exact: true })
          .click();
        await visible(small, "预约成功");
        await small.frame
          .getByRole("button", { name: "已保存", exact: true })
          .click();
        const credential = small.frame.getByRole("textbox", {
          name: "取消凭据",
        });
        await credential.click();
        await expect(credential).toHaveValue(/^[a-f0-9]{48}$/);
        await small.frame
          .getByRole("button", { name: "取消我的预约", exact: true })
          .click();
        await visible(small, "可以预约");
      }
      await small.page.screenshot({
        path: cfg.root + "/" + app.mode + "-mobile.png",
        fullPage: true,
      });
      report.viewports.push({ mode: app.mode, width: 390 });
      await mobile.close();
      await opened.page.close();
    }
    await c.storageState({ path: cfg.root + "/private-storage-state.json" });
    fs.writeFileSync(
      cfg.root + "/private-recovery.json",
      JSON.stringify(recovery),
      { mode: 0o600 },
    );
    check("actual-four-browser-layouts-and-business-actions");
  } else if (phase === "unavailable" || phase === "stopped") {
    recovery = JSON.parse(
      fs.readFileSync(cfg.root + "/private-recovery.json", "utf8"),
    );
    const c = await context({
      viewport: { width: 1440, height: 1000 },
      storageState: cfg.root + "/private-storage-state.json",
    });
    const expected = phase === "unavailable" ? 502 : 404;
    report.rejectedRequests = [];
    for (const app of recovery.apps) {
      const page = await c.newPage();
      const received = page.waitForResponse(
        (response) => response.url() === app.url,
      );
      received.catch(() => {});
      try {
        await page.goto(app.url);
      } catch (error) {
        // Chrome renders an internal error document for Caddy's empty 502.
        // The network response must still independently prove the exact status.
        if (
          phase !== "unavailable" ||
          !error.message.includes("net::ERR_HTTP_RESPONSE_CODE_FAILURE")
        )
          throw error;
      }
      assert.equal((await received).status(), expected);
      for (const path of [
        "files/main.dart.js",
        "api/" + (app.mode === "feedback" ? "feedback" : "slots"),
      ]) {
        // Gateway error documents intentionally forbid script connections.
        // Verify their assets/API with an independently verified HTTP client.
        const status = await closedRequest(app.url + path);
        assert.equal(status, expected);
        report.rejectedRequests.push({
          mode: app.mode,
          path,
          status,
          source: cfg.tls ? "node-https-verified-ca" : "node-loopback-http",
        });
      }
      if (phase === "unavailable" && app.mode === "feedback") {
        const status = await closedRequest(app.url + "api/feedback", {
          title: "Must not be replayed after recovery",
          content: "Controlled offline verification",
        });
        assert.equal(status, 502);
        report.failedWriteNotRetried = true;
      }
      await page.close();
    }
    check(
      phase === "unavailable"
        ? "real-caddy-upstream-failure-rejects-shell-assets-api-and-write"
        : "stopped-previews-reject-original-shell-assets-and-business-api",
    );
  } else if (phase === "recovered") {
    recovery = JSON.parse(
      fs.readFileSync(cfg.root + "/private-recovery.json", "utf8"),
    );
    const c = await context({
      viewport: { width: 1440, height: 1000 },
      storageState: cfg.root + "/private-storage-state.json",
    });
    for (const app of recovery.apps) {
      const opened = await open(c, app.url, app.artifact);
      if (app.mode === "feedback") {
        const r = await business(opened, "feedback");
        assert.equal(r.body.length, 2);
        await visible(opened, "移动宽度反馈");
      } else {
        assert.equal((await business(opened, "slots")).body[0].reserved, false);
        await visible(opened, "可以预约");
      }
      await opened.page.close();
    }
    check(
      "original-private-grants-and-temporary-container-data-survive-node-and-control-restart",
    );
  } else throw new Error("Unsupported phase");
  assert.deepEqual(report.externalRequests, []);
  assert.deepEqual(report.responseErrors, []);
  assert.deepEqual(report.pageErrors, []);
  // The tested booking conflict and wrong cancellation legitimately return
  // 409/404; Chrome may report those as resource diagnostics in its console.
  assert.deepEqual(
    report.consoleErrors.filter(
      (s) =>
        !/^Failed to load resource: the server responded with a status of (404|409)\b/.test(
          s,
        ),
    ),
    [],
  );
  report.passed = true;
} catch (error) {
  report.error = sanitize(error.stack || String(error));
  for (const c of browser.contexts())
    for (const page of c.pages()) {
      try {
        await page.screenshot({
          path: cfg.root + "/failure-" + phase + "-" + Date.now() + ".png",
          fullPage: true,
        });
      } catch {}
    }
  process.exitCode = 1;
} finally {
  await browser.close();
  report.browserStopped = true;
  fs.writeFileSync(
    cfg.root + "/browser-" + phase + ".json",
    JSON.stringify(report, null, 2) + "\n",
  );
}
