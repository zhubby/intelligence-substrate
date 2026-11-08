import { test, expect } from "./fixtures";
import type { Page } from "@playwright/test";

async function fixture(page: Page, csrf: string, suffix: string) {
  const command = async (name: string, data: unknown) => {
    const response = await page.request.post("/api/v1/commands/" + name, {
      data,
      headers: { Origin: "http://127.0.0.1:18082", "X-CSRF-Token": csrf },
    });
    expect(response.ok(), await response.text()).toBeTruthy();
    return response.json();
  };
  const project = await command("projects.create", {
    title: "蓝图交付 " + suffix,
  });
  const goal = await command("goals.create", {
    projectId: project.id,
    title: "企业预约应用",
    outcome: "客户管理自己的预约",
    criteria: [{ id: "booking", text: "客户可预约并取消本人预约" }],
    targets: ["web"],
  });
  await command("goals.approve", { id: goal.id });
  const workspace = async () =>
    (
      await page.request.get("/api/v1/projects/" + project.id + "/workspace")
    ).json();
  return { command, project, goal, workspace };
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
  await page.getByRole("tab", { name: "交付方案", exact: true }).click();
}

async function fill(page: Page, name: string) {
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("应用名", { exact: true }).fill(name);
  await dialog
    .getByLabel("应用命名空间", { exact: true })
    .fill("com.enterprise");
  await dialog.getByLabel("参考业务", { exact: true }).selectOption("booking");
  await dialog
    .getByLabel("选用原因", { exact: true })
    .fill("从标准多端源码开始，逐项实现并验证业务。 ");
}

test("blueprint response replay, model selection and human approval preserve one frozen source", async ({
  page,
  ownerSession,
}, info) => {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  const f = await fixture(page, ownerSession.csrf, info.project.name);
  await open(page, f.project.id, f.goal.id, info.project.name === "mobile");
  const create = page.getByRole("button", { name: "选用蓝图", exact: true });
  await expect(create).toBeEnabled();
  await create.focus();
  await page.keyboard.press("Enter");
  await page.keyboard.press("Escape");
  await expect(create).toBeFocused();
  const requests: { key: string }[] = [];
  let lost = true;
  await page.route("**/commands/blueprints.propose", async (route) => {
    requests.push(route.request().postDataJSON());
    const response = await route.fetch();
    if (lost) {
      lost = false;
      await route.fulfill({
        status: 503,
        contentType: "application/json",
        body: JSON.stringify({
          error: { message: "蓝图已保存，响应暂不可用" },
        }),
      });
    } else await route.fulfill({ response });
  });
  await create.click();
  await fill(page, "enterprise_booking");
  const dialog = page.getByRole("dialog");
  const shot = process.env.IS_PLAN_SCREENSHOTS;
  if (shot)
    await page.screenshot({
      path: shot + "/blueprint-form.png",
      animations: "disabled",
    });
  const save = dialog.getByRole("button", {
    name: "保存蓝图提案",
    exact: true,
  });
  await save.click();
  await expect(dialog.getByRole("alert")).toContainText("响应暂不可用");
  await save.click();
  await expect(dialog).not.toBeVisible();
  expect(requests).toHaveLength(2);
  expect(requests[0].key).toBe(requests[1].key);
  const selections = (await f.workspace()).filter(
    (r: { kind: string }) => r.kind === "blueprint",
  );
  expect(selections).toHaveLength(1);
  await page
    .getByLabel("规划采用的蓝图", { exact: true })
    .selectOption(selections[0].id);
  await expect(
    page.getByLabel("从蓝图初始化空应用源码", { exact: true }),
  ).toBeChecked();
  await page.getByRole("button", { name: "生成方案", exact: true }).click();
  await expect(
    dialog.getByText("蓝图 v1 · enterprise_booking", { exact: true }),
  ).toBeVisible();
  await dialog
    .getByRole("button", { name: "生成交付方案", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  const before = await f.workspace();
  expect(
    before.filter((r: { kind: string }) => r.kind === "task"),
  ).toHaveLength(0);
  const plan = before.find((r: { kind: string }) => r.kind === "plan");
  expect(plan.data.blueprint.id).toBe(selections[0].id);
  expect(plan.data.initializeBlueprint).toBe(true);
  expect(plan.data.tasks[0].blueprintSeed.data.digest).toBe(
    selections[0].data.digest,
  );
  const article = page.getByRole("article", {
    name: "交付方案 v1",
    exact: true,
  });
  await expect(
    article.getByText("从已选蓝图初始化应用源码", { exact: true }),
  ).toBeVisible();
  await article.getByRole("button", { name: "批准方案", exact: true }).click();
  await expect(
    dialog.getByText("蓝图 v1 · enterprise_booking", { exact: true }),
  ).toBeVisible();
  await dialog.getByRole("button", { name: "确认批准", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  const after = await f.workspace();
  const approval = after.find(
    (r: { kind: string; data: { type: string } }) =>
      r.kind === "approval" && r.data.type === "plan",
  );
  expect(approval.data.blueprint).toEqual(plan.data.blueprint);
  expect(after.filter((r: { kind: string }) => r.kind === "task")).toHaveLength(
    2,
  );
  expect(
    after.filter(
      (r: { kind: string }) => r.kind === "evidence" || r.kind === "release",
    ),
  ).toHaveLength(0);
  await page.getByRole("tab", { name: "交付方案", exact: true }).click();
  await expect(
    page
      .getByRole("region", { name: "应用蓝图", exact: true })
      .getByText(/当前批准蓝图/),
  ).toBeVisible();
  if (shot)
    await page.screenshot({
      path: shot + "/blueprint-approved.png",
      fullPage: true,
      animations: "disabled",
    });
  expect(errors).toEqual([]);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBeTruthy();
});

test("manual selection can preserve existing source and stale proposal dialogs cannot change baseline", async ({
  page,
  ownerSession,
}, info) => {
  const f = await fixture(page, ownerSession.csrf, "人工 " + info.project.name);
  await open(page, f.project.id, f.goal.id, info.project.name === "mobile");
  await page.getByRole("button", { name: "选用蓝图", exact: true }).click();
  await fill(page, "manual_booking");
  const dialog = page.getByRole("dialog");
  await dialog
    .getByRole("button", { name: "保存蓝图提案", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  const selection = (await f.workspace()).find(
    (r: { kind: string }) => r.kind === "blueprint",
  );
  await page
    .getByLabel("规划采用的蓝图", { exact: true })
    .selectOption(selection.id);
  await page.getByLabel("从蓝图初始化空应用源码", { exact: true }).uncheck();
  await page.getByRole("button", { name: "人工编制", exact: true }).click();
  await dialog
    .getByLabel("方案与交付路径", { exact: true })
    .fill("参考蓝图的应用标识，审阅并继续已有源码。");
  await dialog
    .getByRole("button", { name: "提交方案审阅", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  const plan = (await f.workspace()).find(
    (r: { kind: string }) => r.kind === "plan",
  );
  expect(plan.data.blueprint.id).toBe(selection.id);
  expect(plan.data.initializeBlueprint).toBe(false);
  expect(
    plan.data.tasks.every((t: { blueprintSeed?: unknown }) => !t.blueprintSeed),
  ).toBeTruthy();
  await page.getByRole("button", { name: "选用蓝图", exact: true }).click();
  await fill(page, "stale_booking");
  await f.command("goals.revise", { id: f.goal.id, reason: "业务范围已变化" });
  await dialog
    .getByRole("button", { name: "保存蓝图提案", exact: true })
    .click();
  await expect(dialog.getByRole("alert")).toContainText(/需求/);
  await page.keyboard.press("Escape");
  expect(
    (await f.workspace()).filter(
      (r: { kind: string }) => r.kind === "blueprint",
    ),
  ).toHaveLength(1);
  await expect(
    page.getByRole("button", { name: "选用蓝图", exact: true }),
  ).toBeDisabled();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBeTruthy();
});
