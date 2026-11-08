import { test, expect } from "./fixtures";
import type { Page } from "@playwright/test";
import { Buffer } from "node:buffer";

const origin = "http://127.0.0.1:18082";

function archive() {
  const files: Record<string, string> = {
    "index.html": `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="./assets/app.css"><h1>客户预约 · Preview</h1><p id="ready">正在加载</p><p id="data"></p><button id="choose">选择预约</button><p id="network"></p><p id="session"></p><button id="navigate">尝试离开预览</button><script type="module" src="./assets/app.js"></script></html>`,
    "assets/app.css":
      "body{font:18px system-ui;margin:24px;color:#16324a}button{background:#e5f4ec;color:#16324a;padding:12px;border:1px solid #87b49c;border-radius:6px}",
    "assets/app.js": `import {label} from './label.js';document.querySelector('#ready').textContent=label;fetch(new URL('../data/items.json',import.meta.url)).then(r=>r.json()).then(x=>document.querySelector('#data').textContent=x[0]);document.querySelector('#choose').onclick=e=>e.currentTarget.textContent='预约已选择';fetch('${origin}/api/v1/projects',{credentials:'include'}).then(()=>document.querySelector('#network').textContent='平台访问意外成功').catch(()=>document.querySelector('#network').textContent='平台访问已阻止');try{document.cookie;document.querySelector('#session').textContent='会话读取意外成功'}catch{document.querySelector('#session').textContent='会话已隔离'}document.querySelector('#navigate').onclick=()=>location.href='${origin}/preview-escape-attempt';`,
    "assets/label.js": "export const label='模块已加载';",
    "data/items.json": '["周五 · 09:00"]',
  };
  const blocks: Buffer[] = [];
  for (const [name, text] of Object.entries(files)) {
    const bytes = Buffer.from(text);
    const h = Buffer.alloc(512);
    h.write(name, 0);
    h.write("0000644", 100);
    h.write("0000000", 108);
    h.write("0000000", 116);
    h.write(bytes.length.toString(8).padStart(11, "0"), 124);
    h.write("00000000000", 136);
    h.fill(32, 148, 156);
    h.write("0", 156);
    h.write("ustar\0", 257);
    h.write("00", 263);
    h.write(
      h
        .reduce((sum, n) => sum + n, 0)
        .toString(8)
        .padStart(6, "0") + "\0 ",
      148,
    );
    blocks.push(h, bytes, Buffer.alloc((512 - (bytes.length % 512)) % 512));
  }
  return Buffer.concat([...blocks, Buffer.alloc(1024)]);
}

async function fixture(page: Page, csrf: string, suffix: string) {
  const command = async (name: string, data: unknown) => {
    const response = await page.request.post("/api/v1/commands/" + name, {
      data,
      headers: { Origin: origin, "X-CSRF-Token": csrf },
    });
    expect(response.ok(), await response.text()).toBeTruthy();
    return response.json();
  };
  const project = await command("projects.create", {
    title: "开发预览 " + suffix,
  });
  const goal = await command("goals.create", {
    projectId: project.id,
    title: "客户预约应用",
    outcome: "客户选择可预约时段",
    targets: ["web"],
    criteria: [{ id: "choose", text: "客户选择一个时段" }],
  });
  await command("goals.approve", { id: goal.id });
  const repo = await command("repositories.create", {
    projectId: project.id,
    title: "预约源码",
  });
  const response = await page.request.post(
    "/api/v1/artifacts/upload?" +
      new URLSearchParams({
        goalId: goal.id,
        repositoryId: repo.id,
        commit: repo.data.head,
        target: "web",
        type: "package",
        name: "booking-web.tar",
      }),
    {
      data: archive(),
      headers: {
        Origin: origin,
        "X-CSRF-Token": csrf,
        "Content-Type": "application/octet-stream",
      },
    },
  );
  expect(response.ok(), await response.text()).toBeTruthy();
  const artifact = await response.json();
  const create = () =>
    command("previews.create", {
      artifactId: artifact.id,
      expectedRequirementRevision: 1,
      title: "预约体验",
      reason: "检查预约流程",
      key: crypto.randomUUID(),
    });
  return { command, project, goal, repo, artifact, create };
}

async function open(
  page: Page,
  project: string,
  goal: string,
  mobile: boolean,
) {
  await page.goto("/");
  if (mobile)
    await page.getByRole("button", { name: "打开导航", exact: true }).click();
  await page.getByLabel("选择项目").selectOption(project);
  await page
    .getByRole("navigation", { name: "主导航" })
    .getByRole("button", { name: /^目标与需求/ })
    .click();
  await page.getByLabel("选择交付目标").selectOption(goal);
  if (mobile)
    await page.getByRole("button", { name: "打开导航", exact: true }).click();
  await page
    .getByRole("navigation", { name: "主导航" })
    .getByRole("button", { name: /^代码与评审/ })
    .click();
  await expect(
    page.getByRole("region", { name: "开发预览", exact: true }),
  ).toBeVisible();
}

test("build preview loads real modules and assets, isolates authority and stops without release evidence", async ({
  page,
  ownerSession,
}, info) => {
  const f = await fixture(page, ownerSession.csrf, info.project.name);
  await open(page, f.project.id, f.goal.id, info.project.name === "mobile");
  const section = page.getByRole("region", { name: "开发预览", exact: true });
  const create = section.getByRole("button", {
    name: "创建开发预览",
    exact: true,
  });
  const dialog = page.getByRole("dialog");
  await create.focus();
  await page.keyboard.press("Enter");
  await expect(dialog.getByText(/需求 v1/)).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(create).toBeFocused();
  await create.click();
  await dialog.getByLabel("预览名称", { exact: true }).fill("预约体验");
  await dialog
    .getByLabel("创建原因", { exact: true })
    .fill("检查真实构建的预约交互");
  await dialog.getByRole("button", { name: "创建预览", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  const row = section.getByRole("article", { name: "预约体验", exact: true });
  await expect(row.getByText("可体验", { exact: true })).toBeVisible();
  await row.getByRole("button", { name: "打开预览", exact: true }).click();
  const access = dialog.getByRole("link", {
    name: "在新窗口体验",
    exact: true,
  });
  await expect(access).toBeVisible();
  const popupEvent = page.waitForEvent("popup");
  await access.click();
  const popup = await popupEvent;
  const app = popup.frameLocator('iframe[title="应用开发预览"]');
  await expect(app.getByText("模块已加载", { exact: true })).toBeVisible();
  await expect(app.getByText("周五 · 09:00", { exact: true })).toBeVisible();
  await expect(app.getByText("平台访问已阻止", { exact: true })).toBeVisible();
  await expect(app.getByText("会话已隔离", { exact: true })).toBeVisible();
  await expect(
    app.getByRole("button", { name: "选择预约", exact: true }),
  ).toHaveCSS("background-color", "rgb(229, 244, 236)");
  await app.getByRole("button", { name: "选择预约", exact: true }).click();
  await expect(
    app.getByRole("button", { name: "预约已选择", exact: true }),
  ).toBeVisible();
  let navigationRequests = 0;
  await page.context().route("**/preview-escape-attempt", async (route) => {
    navigationRequests++;
    await route.abort();
  });
  const blockedNavigation = popup.waitForEvent("console", {
    predicate: (message) =>
      message.type() === "error" && message.text().includes("frame-src"),
  });
  await app.getByRole("button", { name: "尝试离开预览", exact: true }).click();
  await blockedNavigation;
  expect(navigationRequests).toBe(0);
  await popup.reload();
  await expect(app.getByText("模块已加载", { exact: true })).toBeVisible();
  const shots = process.env.IS_PREVIEW_SCREENSHOTS;
  if (shots) {
    await page.keyboard.press("Escape");
    await page.screenshot({ path: shots + "/workbench.png", fullPage: true });
    await popup.screenshot({
      path: shots + "/application.png",
      fullPage: true,
    });
  } else await page.keyboard.press("Escape");
  await row.getByRole("button", { name: "停止预览", exact: true }).click();
  await dialog.getByLabel("停止原因", { exact: true }).fill("本轮体验结束");
  await dialog.getByRole("button", { name: "确认停止", exact: true }).click();
  await expect(row.getByText("已停止", { exact: true })).toBeVisible();
  await expect(
    popup.getByText("预览已停止或访问已过期，请返回工作台重新打开。", {
      exact: true,
    }),
  ).toBeVisible({ timeout: 20000 });
  await popup.close();
  const data = await (
    await page.request.get("/api/v1/projects/" + f.project.id + "/workspace")
  ).json();
  expect(
    data.filter((x: { kind: string }) =>
      ["release", "evidence", "deployment"].includes(x.kind),
    ),
  ).toHaveLength(0);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBeTruthy();
});

test("preview creation rejects a changed baseline and access handles loading and retry", async ({
  page,
  ownerSession,
}, info) => {
  const f = await fixture(page, ownerSession.csrf, info.project.name);
  const preview = await f.create();
  await open(page, f.project.id, f.goal.id, info.project.name === "mobile");
  const section = page.getByRole("region", { name: "开发预览", exact: true });
  const dialog = page.getByRole("dialog");
  let release!: () => void;
  const waiting = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route(
    "**/api/v1/previews/" + preview.id + "/access",
    async (route) => {
      await waiting;
      await route.fulfill({
        status: 503,
        contentType: "application/json",
        body: JSON.stringify({
          error: { code: "unavailable", message: "预览入口暂时不可用" },
        }),
      });
    },
  );
  await section.getByRole("button", { name: "打开预览", exact: true }).click();
  await expect(dialog.getByRole("status")).toHaveText("正在准备短期访问…");
  release();
  await expect(dialog.getByRole("alert")).toHaveText("预览入口暂时不可用");
  await page.unroute("**/api/v1/previews/" + preview.id + "/access");
  await dialog
    .getByRole("button", { name: "重试准备链接", exact: true })
    .click();
  await expect(
    dialog.getByRole("link", { name: "在新窗口体验", exact: true }),
  ).toBeVisible();
  await page.keyboard.press("Escape");
  await section
    .getByRole("button", { name: "创建开发预览", exact: true })
    .click();
  await dialog.getByLabel("创建原因", { exact: true }).fill("检查新流程");
  await f.command("goals.revise", {
    id: f.goal.id,
    reason: "增加客户取消流程",
    outcome: "客户预约并取消自己的预约",
  });
  await dialog.getByRole("button", { name: "创建预览", exact: true }).click();
  await expect(dialog.getByText(/需求版本已变化/)).toBeVisible();
  await page.keyboard.press("Escape");
  await open(page, f.project.id, f.goal.id, info.project.name === "mobile");
  await expect(section.getByText("需求已变化", { exact: true })).toBeVisible();
  await expect(
    section.getByRole("button", { name: "打开预览", exact: true }),
  ).toBeDisabled();
});

test("a project viewer can experience a preview but cannot create or stop it, and removal revokes links permanently", async ({
  page,
  browser,
  ownerSession,
}, info) => {
  const f = await fixture(page, ownerSession.csrf, info.project.name);
  const preview = await f.create();
  const password = "preview-browser-viewer-2026";
  const email = "preview-viewer-" + info.project.name + "@example.test";
  const response = await page.request.post("/api/v1/identities", {
    data: { name: "预览查看者", email, password },
    headers: { Origin: origin, "X-CSRF-Token": ownerSession.csrf },
  });
  expect(response.ok(), await response.text()).toBeTruthy();
  const identity = await response.json();
  const member = await f.command("members.grant", {
    projectId: f.project.id,
    identityId: identity.id,
    roles: ["viewer"],
  });
  const context = await browser.newContext({
    baseURL: origin,
    viewport:
      info.project.name === "mobile"
        ? { width: 390, height: 844 }
        : { width: 1440, height: 1000 },
  });
  try {
    const login = await context.request.post("/api/v1/auth/login", {
      data: { email, password },
      headers: { Origin: origin },
    });
    expect(login.ok()).toBeTruthy();
    const viewer = await context.newPage();
    await open(viewer, f.project.id, f.goal.id, info.project.name === "mobile");
    const section = viewer.getByRole("region", {
      name: "开发预览",
      exact: true,
    });
    await expect(
      section.getByRole("button", { name: "创建开发预览", exact: true }),
    ).toBeDisabled();
    await expect(
      section.getByRole("button", { name: "停止预览", exact: true }),
    ).toBeDisabled();
    await section
      .getByRole("button", { name: "打开预览", exact: true })
      .click();
    const link = viewer
      .getByRole("dialog")
      .getByRole("link", { name: "在新窗口体验", exact: true });
    await expect(link).toBeVisible();
    const url = (await link.getAttribute("href"))!;
    expect((await context.request.get(url)).status()).toBe(200);
    await f.command("members.revoke", {
      id: member.id,
      expectedRevision: member.revision,
      reason: "预览评审结束",
    });
    expect((await context.request.get(url)).status()).toBe(404);
    await f.command("members.grant", {
      projectId: f.project.id,
      identityId: identity.id,
      roles: ["viewer"],
      expectedRevision: member.revision + 1,
    });
    expect((await context.request.get(url)).status()).toBe(404);
    const state = await (await context.request.get("/api/v1/auth/me")).json();
    const fresh = await context.request.post(
      "/api/v1/previews/" + preview.id + "/access",
      { data: {}, headers: { Origin: origin, "X-CSRF-Token": state.csrf } },
    );
    expect(fresh.ok(), await fresh.text()).toBeTruthy();
  } finally {
    await context.close();
  }
});
