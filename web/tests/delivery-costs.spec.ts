import { test, expect } from "./fixtures";
import type { APIRequestContext, Page } from "@playwright/test";

const origin = "http://127.0.0.1:18082";
async function post(
  client: APIRequestContext,
  path: string,
  data: unknown,
  csrf: string,
) {
  const response = await client.post(origin + "/api/v1/" + path, {
    data,
    headers: { Origin: origin, "X-CSRF-Token": csrf },
  });
  const value = await response.json();
  expect(response.ok(), JSON.stringify(value)).toBeTruthy();
  return value;
}
async function costsPage(
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
    .getByRole("button", { name: "企业治理", exact: true })
    .click();
  await page.getByLabel("选择交付目标").selectOption(goal);
  await page.getByRole("tab", { name: "交付成本", exact: true }).click();
}

test("delivery costs retain source history, separate pending money and enforce human ownership", async ({
  page,
  ownerSession,
}, info) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const command = (name: string, data: unknown) =>
    post(page.request, "commands/" + name, data, ownerSession.csrf);
  const suffix = info.project.name + "-" + Date.now();
  const project = await command("projects.create", {
    title: "交付成本 " + suffix,
  });
  const createGoal = (title: string) =>
    command("goals.create", {
      projectId: project.id,
      title,
      outcome: "汇总可追溯的交付费用",
      criteria: [{ id: "costs", text: "费用来源可核对" }],
      targets: ["web"],
      budget: 10,
    });
  const goal = await createGoal("客户反馈成本");
  const otherGoal = await createGoal("预约应用成本");
  await command("goals.approve", { id: goal.id });
  await costsPage(page, project.id, goal.id, info.project.name === "mobile");
  await expect(
    page.getByRole("heading", { name: "交付成本与来源", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("尚未记录：构建、设备、存储、渠道、人工", { exact: true }),
  ).toBeVisible();
  const add = page.getByRole("button", { name: "登记外部费用", exact: true });
  await add.focus();
  await page.keyboard.press("Enter");
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(dialog).not.toBeVisible();
  await expect(add).toBeFocused();
  const fill = async (amount: string) => {
    await dialog.getByLabel("费用名称", { exact: true }).fill("构建资源分摊");
    await dialog.getByLabel("费用类别", { exact: true }).selectOption("build");
    await dialog.getByLabel("登记金额（USD）", { exact: true }).fill(amount);
    await dialog
      .getByLabel("发生时间", { exact: true })
      .fill("2026-01-02T03:04");
    await dialog
      .getByLabel("费用来源标识", { exact: true })
      .fill("invoice-" + suffix + "/line-001");
    await dialog
      .getByLabel("登记依据", { exact: true })
      .fill("合成测试账单：由企业核对的构建资源分摊，非真实发票。");
  };
  await add.click();
  await dialog.getByRole("button", { name: "登记费用", exact: true }).click();
  await expect(dialog).toBeVisible();
  await fill("12.34");
  await dialog.getByRole("button", { name: "登记费用", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  const reportURL =
    "/api/v1/projects/" + project.id + "/costs?goalId=" + goal.id;
  const initial = await (await page.request.get(reportURL)).json();
  expect(initial.recordedTotalCents).toBe(1234);
  expect(initial.unreportedCategories).toEqual([
    "device",
    "storage",
    "channel",
    "labor",
  ]);
  const original = initial.expenses[0];
  const row = page.getByRole("article", {
    name: "费用记录 " + original.id,
    exact: true,
  });
  await expect(row.getByText("$12.34", { exact: true })).toBeVisible();
  // A distinct submit key cannot count the same invoice line twice.
  await add.click();
  await fill("12.34");
  await dialog.getByRole("button", { name: "登记费用", exact: true }).click();
  await expect(dialog.getByRole("alert")).toContainText("费用来源已经登记");
  await page.keyboard.press("Escape");
  await row.getByRole("button", { name: "作废费用", exact: true }).click();
  await dialog.getByRole("button", { name: "确认作废", exact: true }).click();
  await expect(dialog).toBeVisible();
  await dialog
    .getByLabel("作废原因", { exact: true })
    .fill("原始分摊错误，核对后登记更正金额");
  await dialog.getByRole("button", { name: "确认作废", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(
    page.getByText("已作废记录（1）", { exact: true }),
  ).toBeVisible();
  await add.click();
  await fill("10.00");
  await dialog.getByRole("button", { name: "登记费用", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(
    page.getByText("更正自 " + original.id, { exact: true }),
  ).toBeVisible();
  // This endpoint contacts only the task-owned localhost 503 model stub.
  // It exercises the real uncertain-charge path without a paid provider.
  const failed = await page.request.post("/api/v1/model/chat", {
    data: {
      goalId: goal.id,
      messages: [
        { role: "user", content: "Synthetic accounting failure fixture" },
      ],
      max_tokens: 16,
    },
    headers: { Origin: origin, "X-CSRF-Token": ownerSession.csrf },
  });
  expect(failed.status()).toBe(502);
  await page.getByRole("button", { name: "刷新费用", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "核对模型费用", exact: true }),
  ).toBeVisible();
  const pending = await (await page.request.get(reportURL)).json();
  expect(pending.modelPendingCents).toBeGreaterThan(0);
  expect(pending.recordedTotalCents).toBe(1000);
  await page.getByRole("button", { name: "核对模型费用", exact: true }).click();
  await dialog.getByLabel("实际费用（USD）", { exact: true }).fill("0.03");
  await dialog
    .getByLabel("供应商账单与核对依据", { exact: true })
    .fill("合成测试供应商记录：本次调用分摊为 3 美分。");
  await dialog.getByRole("button", { name: "确认费用", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(page.getByText("人工核对金额", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "刷新费用", exact: true }).click();
  await expect
    .poll(
      async () =>
        (await (await page.request.get(reportURL)).json()).recordedTotalCents,
    )
    .toBe(1003);
  await page.getByText("已作废记录（1）", { exact: true }).click();
  await expect(row.getByText("已作废", { exact: true })).toBeVisible();
  await expect(
    row.getByText("原始分摊错误，核对后登记更正金额", { exact: true }),
  ).toBeVisible();
  await page.getByLabel("选择交付目标").selectOption(otherGoal.id);
  await expect(
    page.getByText("尚未记录：构建、设备、存储、渠道、人工", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("构建资源分摊", { exact: true }),
  ).not.toBeVisible();
  await page.getByLabel("选择交付目标").selectOption(goal.id);
  await expect(
    page.getByText("更正自 " + original.id, { exact: true }),
  ).toBeVisible();
  // Read-only participants can inspect financial provenance, but cannot edit it.
  const email = "cost-observer-" + suffix + "@example.test";
  const viewer = await post(
    page.request,
    "identities",
    {
      email,
      name: "成本观察员",
      password: "cost-browser-observer-password-2026",
    },
    ownerSession.csrf,
  );
  await command("members.grant", {
    projectId: project.id,
    identityId: viewer.id,
    roles: ["viewer"],
  });
  const viewerPage = await page
    .context()
    .browser()!
    .newContext({
      viewport: {
        width: info.project.name === "mobile" ? 390 : 1440,
        height: info.project.name === "mobile" ? 844 : 1000,
      },
      isMobile: info.project.name === "mobile",
      hasTouch: info.project.name === "mobile",
    });
  try {
    const login = await post(
      viewerPage.request,
      "auth/login",
      { email, password: "cost-browser-observer-password-2026" },
      "",
    );
    const readonly = await viewerPage.newPage();
    readonly.on("pageerror", (e) => errors.push(e.message));
    await costsPage(
      readonly,
      project.id,
      goal.id,
      info.project.name === "mobile",
    );
    await expect(
      readonly.getByRole("button", { name: "登记外部费用", exact: true }),
    ).toBeDisabled();
    await expect(
      readonly.getByRole("button", { name: "作废费用", exact: true }),
    ).toBeDisabled();
    const denied = await viewerPage.request.post(
      "/api/v1/commands/expenses.record",
      {
        data: { goalId: goal.id, key: "viewer" },
        headers: { Origin: origin, "X-CSRF-Token": login.csrf },
      },
    );
    expect(denied.status()).toBe(403);
  } finally {
    await viewerPage.close();
  }
  await expect
    .poll(
      async () =>
        (await (await page.request.get(reportURL)).json()).modelPendingCents,
    )
    .toBe(0);
  await expect(
    page
      .getByRole("region", { name: "成本概览", exact: true })
      .getByText("$10.03", { exact: true }),
  ).toBeVisible();
  const finalReport = await (await page.request.get(reportURL)).json();
  expect(finalReport.modelBudgetCents).toBe(1000);
  expect(finalReport.complete).toBe(false);
  if (!(await row.isVisible()))
    await page.getByText("已作废记录（1）", { exact: true }).click();
  await expect(row.getByText("$12.34", { exact: true })).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBeTruthy();
  expect(errors).toEqual([]);
  await page.screenshot({
    path:
      (process.env.IS_COST_SCREENSHOTS || "../work/screenshots") +
      "/" +
      info.project.name +
      "-delivery-costs.png",
    fullPage: true,
    animations: "disabled",
  });
});
