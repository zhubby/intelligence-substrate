// Invoked by verify-preview-ingress.py. Requests reach real platform/Caddy
// processes. Raw URLs, credentials and Playwright failures remain under work/.
import fs from "node:fs";
import { createRequire } from "node:module";

const cfg = JSON.parse(fs.readFileSync(0, "utf8"));
const { chromium, expect } = createRequire(cfg.repo + "/web/package.json")(
  "@playwright/test",
);
const privatePath = cfg.root + "/private-markers.json";
const privateValues = fs.existsSync(privatePath)
  ? JSON.parse(fs.readFileSync(privatePath, "utf8"))
  : [];
const remember = (value) => {
  privateValues.push(value);
  fs.writeFileSync(privatePath, JSON.stringify(privateValues), { mode: 0o600 });
};
const browser = await chromium.launch({
  headless: true,
  executablePath: cfg.chrome,
  args: [
    "--no-proxy-server",
    "--host-resolver-rules=MAP *.ingress.test 127.0.0.1, EXCLUDE localhost",
    "--ignore-certificate-errors-spki-list=" + cfg.pin,
    "--disable-background-networking",
  ],
});
const result = {
  passed: false,
  phase: cfg.phase,
  browserVersion: browser.version(),
  viewports: [],
  files: [],
  apiResponsesMocked: false,
  exactLeafSPKITrusted: true,
  broadTLSBypass: false,
};

async function api(page, path, data, csrf, raw = false, expected = 200) {
  const response = await page.evaluate(
    async ({ path, data, csrf, raw }) => {
      const headers = {
        "Content-Type": raw ? "application/octet-stream" : "application/json",
      };
      if (csrf) headers["X-CSRF-Token"] = csrf;
      const r = await fetch("/api/v1/" + path, {
        method: data === undefined ? "GET" : "POST",
        credentials: "same-origin",
        headers,
        body:
          data === undefined
            ? undefined
            : raw
              ? Uint8Array.from(atob(data), (c) => c.charCodeAt(0))
              : JSON.stringify(data),
      });
      return { status: r.status, body: await r.json() };
    },
    { path, data, csrf, raw },
  );
  expect(response.status, "Real API status for " + path.split("?")[0]).toBe(
    expected,
  );
  return response.body;
}

async function login(context, page) {
  await page.goto(cfg.origin);
  const received = page.waitForResponse(
    (r) => r.url() === cfg.origin + "/api/v1/auth/login",
  );
  const session = await api(page, "auth/login", {
    email: cfg.email,
    password: cfg.password,
  });
  remember(session.csrf);
  const setCookie = (await (await received).allHeaders())["set-cookie"];
  expect(setCookie).toMatch(/; Secure/i);
  expect(setCookie).toMatch(/; HttpOnly/i);
  expect(setCookie).toMatch(/; SameSite=Strict/i);
  expect(setCookie).not.toMatch(/; Domain=/i);
  const cookie = (await context.cookies(cfg.origin)).find(
    (c) => c.name === "is_session",
  );
  expect(cookie).toBeDefined();
  remember(cookie.value);
  expect(cookie.domain).toBe("control.ingress.test");
  expect(cookie.secure && cookie.httpOnly).toBe(true);
  expect(cookie.sameSite).toBe("Strict");
  expect(await context.cookies(cfg.previewOrigin)).toHaveLength(0);
  expect(await page.evaluate(() => document.cookie)).not.toContain(
    "is_session",
  );
  result.secureHostOnlySession = true;
  result.siblingHostnameCookieAbsent = true;
  return session;
}

async function fixture(page, session, suffix) {
  const command = (name, data) =>
    api(page, "commands/" + name, data, session.csrf);
  const project = await command("projects.create", {
    title: "HTTPS 入口验证 " + suffix,
  });
  const goal = await command("goals.create", {
    projectId: project.id,
    title: "HTTPS 客户预约",
    outcome: "在受控开发预览中完成预约交互",
    targets: ["web"],
    criteria: [{ id: "booking", text: "客户选择预约时段" }],
  });
  await command("goals.approve", { id: goal.id });
  const repository = await command("repositories.create", {
    projectId: project.id,
    title: "入口验收源码",
  });
  const artifact = await api(
    page,
    "artifacts/upload?" +
      new URLSearchParams({
        goalId: goal.id,
        repositoryId: repository.id,
        commit: repository.data.head,
        target: "web",
        type: "package",
        name: "https-preview.tar",
      }),
    fs.readFileSync(cfg.root + "/application.tar").toString("base64"),
    session.csrf,
    true,
  );
  const preview = await command("previews.create", {
    artifactId: artifact.id,
    expectedRequirementRevision: 1,
    title: "客户预约 · HTTPS",
    reason: "验证独立 TLS 入口与会话边界",
    key: "preview-" + suffix,
  });
  return { command, project, goal, repository, artifact, preview };
}

async function access(page, session, preview) {
  const value = await api(
    page,
    "previews/" + preview.id + "/access",
    {},
    session.csrf,
  );
  const url = new URL(value.url);
  expect(url.origin).toBe(cfg.previewOrigin);
  remember(url.pathname.split("/")[2]);
  return value.url;
}

async function application(popup, width, f, label) {
  const pending = [];
  const failures = [];
  const files = [];
  const collect = (request) => {
    if (!request.url().startsWith(cfg.previewOrigin)) return;
    pending.push(
      (async () => {
        const headers = await Promise.race([
          request.allHeaders(),
          new Promise((_, reject) =>
            setTimeout(
              () => reject(new Error("Request headers timeout")),
              5000,
            ),
          ),
        ]);
        expect(headers.cookie || "").toBe("");
        expect(headers.authorization || "").toBe("");
        expect(headers.referer || "").toBe("");
        const url = new URL(request.url());
        expect(url.search).toBe("");
        if (!url.pathname.includes("/files/")) return;
        const response = await request.response();
        expect(response.status()).toBe(200);
        const received = response.headers();
        expect(received["x-substrate-artifact-sha256"]).toBe(
          f.artifact.data.digest,
        );
        expect(received["x-substrate-commit"]).toBe(f.repository.data.head);
        expect(received["x-substrate-requirement-revision"]).toBe("1");
        expect(received["cache-control"]).toContain("no-store");
        expect(received["access-control-allow-origin"]).toBe("*");
        expect(received["content-security-policy"]).toContain(
          "sandbox allow-scripts",
        );
        files.push({
          file: url.pathname.split("/files/")[1],
          status: response.status(),
          bindingsMatched: true,
          credentialsAbsent: true,
        });
      })().catch((error) => failures.push(error)),
    );
  };
  popup.on("requestfinished", collect);
  const response = await popup.reload();
  expect(response.status()).toBe(200);
  const tls = await response.securityDetails();
  expect(tls.protocol).toMatch(/^TLS 1\.[23]$/);
  result.tlsProtocol = tls.protocol;
  const app = popup.frameLocator('iframe[title="应用开发预览"]');
  await expect(
    app.getByRole("heading", { name: "客户预约 · HTTPS 预览", exact: true }),
  ).toBeVisible();
  await expect(
    app.getByText("模块已加载 周五 09:00", { exact: true }),
  ).toBeVisible();
  await expect(app.getByText("平台访问已阻止", { exact: true })).toBeVisible();
  await expect(app.getByText("会话已隔离", { exact: true })).toBeVisible();
  await app.getByLabel("客户姓名", { exact: true }).fill("验收客户");
  await app.getByRole("button", { name: "确认预约", exact: true }).click();
  await expect(
    app.getByText("验收客户，已预约周五 09:00", { exact: true }),
  ).toBeVisible();
  expect(
    await popup.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await Promise.all(pending);
  popup.off("requestfinished", collect);
  if (failures.length) throw failures[0];
  expect(new Set(files.map((f) => f.file))).toEqual(
    new Set([
      "index.html",
      "assets/app.css",
      "assets/app.js",
      "assets/label.js",
      "data/items.json",
    ]),
  );
  result.files.push(...files);
  result.viewports.push(width);
  await popup.screenshot({
    path: cfg.root + "/application-" + label + ".png",
    fullPage: true,
  });
}

async function revoked(popup) {
  await expect(
    popup.getByText("预览已停止或访问已过期，请返回工作台重新打开。", {
      exact: true,
    }),
  ).toBeVisible({ timeout: 22000 });
  expect(await popup.locator("iframe").count()).toBe(0);
  const status = await popup.evaluate(
    async () => (await fetch(location.pathname + "status")).status,
  );
  expect(status).toBe(404);
}

try {
  if (cfg.phase === "healthy") {
    for (const width of [1440, 390]) {
      const context = await browser.newContext({
        viewport: { width, height: width === 390 ? 844 : 1000 },
      });
      context.setDefaultTimeout(15000);
      const page = await context.newPage();
      let session = await login(context, page);
      const f = await fixture(page, session, String(width));
      await page.goto(cfg.origin);
      if (width === 390)
        await page
          .getByRole("button", { name: "打开导航", exact: true })
          .click();
      await page.getByLabel("选择项目").selectOption(f.project.id);
      await page
        .getByRole("navigation", { name: "主导航" })
        .getByRole("button", { name: /^目标与需求/ })
        .click();
      await page.getByLabel("选择交付目标").selectOption(f.goal.id);
      if (width === 390)
        await page
          .getByRole("button", { name: "打开导航", exact: true })
          .click();
      await page
        .getByRole("navigation", { name: "主导航" })
        .getByRole("button", { name: /^代码与评审/ })
        .click();
      await page
        .getByRole("region", { name: "开发预览", exact: true })
        .getByRole("button", { name: "打开预览", exact: true })
        .click();
      const link = page
        .getByRole("dialog")
        .getByRole("link", { name: "在新窗口体验", exact: true });
      await expect(link).toBeVisible();
      const held = await link.getAttribute("href");
      remember(new URL(held).pathname.split("/")[2]);
      const opened = context.waitForEvent("page");
      await link.click();
      const popup = await opened;
      await popup.waitForLoadState();
      await application(popup, width, f, String(width));
      const noApi = await popup.evaluate(
        async () => (await fetch("/api/v1/projects")).status,
      );
      expect(noApi).toBe(404);
      const top = await context.newPage();
      expect((await top.goto(held + "files/index.html")).status()).toBe(403);
      await top.close();
      if (width === 1440) {
        await api(page, "auth/logout", {}, session.csrf);
        await revoked(popup);
        session = await login(context, page);
        const old = await context.newPage();
        expect((await old.goto(held)).status()).toBe(404);
        await old.close();
        const fresh = await access(page, session, f.preview);
        expect((await popup.goto(fresh)).status()).toBe(200);
        result.logoutRevokedAccess = true;
        result.newSessionDidNotReviveOldAccess = true;
      }
      await api(
        page,
        "commands/previews.stop",
        {
          id: f.preview.id,
          expectedRevision: 1,
          key: "stop-" + width,
          reason: "完成 HTTPS 入口演练",
        },
        session.csrf,
      );
      await revoked(popup);
      const resources = await api(
        page,
        "projects/" + f.project.id + "/workspace",
        undefined,
        session.csrf,
      );
      expect(
        resources.some((r) =>
          ["release", "evidence", "deployment"].includes(r.kind),
        ),
      ).toBe(false);
      await context.close();
    }
    const context = await browser.newContext();
    const page = await context.newPage();
    const session = await login(context, page);
    const f = await fixture(page, session, "recovery");
    const url = await access(page, session, f.preview);
    fs.writeFileSync(
      cfg.root + "/private-recovery.json",
      JSON.stringify({ url, fixture: f }),
      { mode: 0o600 },
    );
    await context.close();
    result.stopRevokedAccess = true;
    result.previewControlAPIAbsent = true;
    result.topLevelHTMLDenied = true;
    result.releaseEvidenceCreated = false;
  } else if (cfg.phase === "recovered") {
    const { url, fixture: f } = JSON.parse(
      fs.readFileSync(cfg.root + "/private-recovery.json", "utf8"),
    );
    const context = await browser.newContext({
      viewport: { width: 1440, height: 1000 },
    });
    const popup = await context.newPage();
    expect((await popup.goto(url)).status()).toBe(200);
    await application(popup, 1440, f, "recovered");
    const page = await context.newPage();
    const session = await login(context, page);
    await api(
      page,
      "commands/previews.stop",
      {
        id: f.preview.id,
        expectedRevision: 1,
        key: "stop-recovered",
        reason: "完成代理故障恢复演练",
      },
      session.csrf,
    );
    await revoked(popup);
    result.heldAccessSurvivedRestart = true;
    result.stopRevokedAccess = true;
    await context.close();
  } else {
    throw new Error("Unknown local verification phase");
  }
  result.passed = true;
  fs.writeFileSync(
    cfg.root + "/browser-" + cfg.phase + ".json",
    JSON.stringify(result, null, 2) + "\n",
  );
} finally {
  await browser.close();
}
