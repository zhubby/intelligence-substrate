import { test, expect } from "./fixtures";
import type { Page } from "@playwright/test";

const origin = "http://127.0.0.1:18082";
async function fixture(
  page: Page,
  csrf: string,
  title: string,
  ownerRecipient = true,
) {
  const command = async (name: string, data: unknown, token?: string) => {
    const r = await page.request.post("/api/v1/commands/" + name, {
      data,
      headers: token
        ? { Authorization: "Bearer " + token }
        : { Origin: origin, "X-CSRF-Token": csrf },
    });
    expect(r.ok(), await r.text()).toBeTruthy();
    return r.json();
  };
  const project = await command("projects.create", {
    title: "协作等待 " + title,
  });
  const workspace = await (
    await page.request.get(`/api/v1/projects/${project.id}/workspace`)
  ).json();
  const ownerId = workspace.find((x: any) => x.kind === "member").data
    .identityId;
  const goal = await command("goals.create", {
    projectId: project.id,
    title: "预约接口约定",
    outcome: "按批准范围确定预约时间格式",
    targets: ["web"],
    criteria: [{ id: "utc", text: "时间统一使用 UTC" }],
    budget: 100,
  });
  await command("goals.approve", { id: goal.id });
  const plan = await command("plans.propose", {
    goalId: goal.id,
    summary: "先确认接口再实现",
    tasks: [
      {
        id: "analysis",
        title: "分析时间约定",
        kind: "analysis",
        role: "developer",
        runtime: "linux",
        budgetCents: 100,
      },
      ...(!ownerRecipient
        ? [
            {
              id: "review",
              title: "核对接口",
              kind: "analysis",
              role: "architect",
              runtime: "linux",
              budgetCents: 100,
            },
          ]
        : []),
    ],
  });
  await command("plans.approve", { id: plan.id });
  const worker = await command("workers.register", {
    projectId: project.id,
    title: "接口分析节点",
    roles: ["developer"],
    runtime: "linux",
    capabilities: ["analysis"],
  });
  let recipient = ownerId;
  if (!ownerRecipient) {
    const other = await command("workers.register", {
      projectId: project.id,
      title: "架构协作者",
      roles: ["architect"],
      runtime: "linux",
      capabilities: ["analysis"],
    });
    recipient = "worker:" + other.id;
  }
  const r = await page.request.post("/api/v1/tokens", {
    data: { projectId: project.id, workerId: worker.id },
    headers: { Origin: origin, "X-CSRF-Token": csrf },
  });
  expect(r.ok()).toBeTruthy();
  const token = (await r.json()).token;
  const run = await command("tasks.claim", {}, token);
  const taskId = run.data.taskId;
  const current = await (
    await page.request.get(`/api/v1/projects/${project.id}/workspace`)
  ).json();
  const recipientTaskId = current.find(
    (x: any) => x.kind === "task" && x.data.logicalId === "review",
  )?.id;
  const request = await command(
    "messages.send",
    {
      projectId: project.id,
      goalId: goal.id,
      runId: run.id,
      leaseToken: run.data.leaseToken,
      recipient,
      ...(ownerRecipient ? {} : { recipientTaskId }),
      type: "clarification",
      key: "browser-request",
      payload: { question: "请确认预约接口是否使用 UTC？" },
    },
    token,
  );
  // This UI fixture submits a trusted Worker wait through the real API. The Go
  // Worker tests separately execute actual Agent loops and durable journals.
  const wait = await command(
    "tasks.wait",
    {
      id: run.id,
      leaseToken: run.data.leaseToken,
      requestMessageId: request.id,
      reason: "等待接口协作者确认时间约定",
      summary: "已阅读批准需求，尚待确认接口约定。",
      timeoutSeconds: 600,
      steps: 2,
      source: { repositoryId: "", commit: "" },
    },
    token,
  );
  return { project, goal, run, taskId, wait, worker, token, command };
}

async function open(
  page: Page,
  projectId: string,
  goalId: string,
  mobile: boolean,
) {
  await page.goto("/");
  if (mobile)
    await page.getByRole("button", { name: "打开导航", exact: true }).click();
  await page.getByLabel("选择项目").selectOption(projectId);
  await page
    .getByRole("navigation", { name: "主导航" })
    .getByRole("button", { name: /^目标与需求/ })
    .click();
  await page.getByLabel("选择交付目标").selectOption(goalId);
  await page.getByRole("tab", { name: "执行记录", exact: true }).click();
  await page
    .getByRole("button", { name: "查看工作项：分析时间约定", exact: true })
    .click();
}

test("addressed human replies to a suspended analysis task without claiming approval", async ({
  page,
  ownerSession,
}, info) => {
  const f = await fixture(page, ownerSession.csrf, info.project.name);
  await open(page, f.project.id, f.goal.id, info.project.name === "mobile");
  const dialog = page.getByRole("dialog");
  const section = dialog.getByRole("region", { name: "协作等待", exact: true });
  await expect(
    section.getByText("等待协作者回复", { exact: true }),
  ).toBeVisible();
  await expect(
    section.getByText("请确认预约接口是否使用 UTC？", { exact: false }),
  ).toBeVisible();
  await expect(
    section.getByText("执行已挂起，已释放节点；回复后由可用节点继续分析。", {
      exact: true,
    }),
  ).toBeVisible();
  const input = section.getByLabel("回复内容", { exact: true });
  await input.fill("使用 UTC；此回复不批准发布。");
  await section
    .getByRole("button", { name: "发送协作回复", exact: true })
    .focus();
  await page.keyboard.press("Enter");
  await expect(
    section.getByText("已收到回复，等待节点继续", { exact: true }),
  ).toBeVisible();
  await expect(section.getByLabel("回复内容", { exact: true })).toHaveCount(0);
  await expect(dialog.locator(".badge").first()).toHaveText("待执行");
  await expect(
    dialog.getByText("已收到指定协作者回复，等待可用节点继续", { exact: true }),
  ).toBeVisible();
  await expect(
    dialog.getByRole("region", { name: "协作上下文", exact: true }),
  ).toContainText("使用 UTC；此回复不批准发布。");
  const state = await (
    await page.request.get(`/api/v1/projects/${f.project.id}/workspace`)
  ).json();
  expect(state.find((x: any) => x.id === f.taskId).status).toBe("pending");
  expect(state.filter((x: any) => x.kind === "message_receipt")).toHaveLength(
    1,
  );
  expect(
    state
      .filter((x: any) => x.kind === "approval")
      .every((x: any) => x.data.type !== "release"),
  ).toBeTruthy();
  const shot = process.env.IS_QUALITY_SCREENSHOTS;
  if (shot)
    await page.screenshot({
      path: shot + "/message-wait.png",
      fullPage: true,
      animations: "disabled",
    });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBeTruthy();
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "查看工作项：分析时间约定", exact: true }),
  ).toBeFocused();
});

test("reply failure preserves text and stable key for a safe lost-response retry", async ({
  page,
  ownerSession,
}, info) => {
  const f = await fixture(
    page,
    ownerSession.csrf,
    "丢失响应 " + info.project.name,
  );
  await open(page, f.project.id, f.goal.id, info.project.name === "mobile");
  const section = page
    .getByRole("dialog")
    .getByRole("region", { name: "协作等待", exact: true });
  await page.route(
    "**/commands/messages.send",
    async (route) => {
      const response = await route.fetch();
      expect(response.ok()).toBeTruthy();
      await route.fulfill({
        status: 503,
        json: {
          error: { code: "fixture_lost_ack", message: "回复已提交但响应丢失" },
        },
      });
    },
    { times: 1 },
  );
  await section
    .getByLabel("回复内容", { exact: true })
    .fill("确认 UTC，保留这一条回复。");
  await section
    .getByRole("button", { name: "发送协作回复", exact: true })
    .click();
  // Refresh before a retry may remove the form because the authoritative wait
  // is already ready; a retry that races refresh uses the same persisted key.
  await expect(
    section
      .getByRole("alert")
      .or(section.getByText("已收到回复，等待节点继续", { exact: true })),
  ).toBeVisible();
  const button = section.getByRole("button", {
    name: "发送协作回复",
    exact: true,
  });
  if (await button.isVisible()) await button.click();
  await expect(
    section.getByText("已收到回复，等待节点继续", { exact: true }),
  ).toBeVisible();
  const state = await (
    await page.request.get(`/api/v1/projects/${f.project.id}/workspace`)
  ).json();
  expect(
    state.filter(
      (x: any) =>
        x.kind === "message" &&
        x.data.causationId === f.wait.data.requestMessageId,
    ),
  ).toHaveLength(1);
});

test("an owner cannot reply as a different collaborator and stale waits lose their form", async ({
  page,
  ownerSession,
}, info) => {
  const f = await fixture(
    page,
    ownerSession.csrf,
    "权限 " + info.project.name,
    false,
  );
  await open(page, f.project.id, f.goal.id, info.project.name === "mobile");
  const section = page
    .getByRole("dialog")
    .getByRole("region", { name: "协作等待", exact: true });
  await expect(
    section.getByText("仅指定接收人可回复此请求。", { exact: true }),
  ).toBeVisible();
  await expect(section.getByLabel("回复内容", { exact: true })).toHaveCount(0);
  await f.command("goals.revise", {
    id: f.goal.id,
    reason: "调整业务结果",
    outcome: "新的范围",
  });
  await expect(section.getByText("等待已取消", { exact: true })).toBeVisible({
    timeout: 15000,
  });
  await expect(section.getByLabel("回复内容", { exact: true })).toHaveCount(0);
});
