import { test, expect } from "./fixtures";

test("task collaboration context preserves delivery, source and revision", async ({
  page,
  playwright,
  ownerSession: session,
}, info) => {
  const origin = "http://127.0.0.1:18082";
  const command = async (name: string, data: unknown) => {
    const response = await page.request.post("/api/v1/commands/" + name, {
      data,
      headers: { Origin: origin, "X-CSRF-Token": session.csrf },
    });
    const result = await response.json();
    expect(response.ok(), JSON.stringify(result)).toBeTruthy();
    return result;
  };
  const project = await command("projects.create", {
    title: "持久协作 " + info.project.name + " " + Date.now(),
  });
  const goal = await command("goals.create", {
    projectId: project.id,
    title: "确认时间接口",
    outcome: "按批准基线明确接口",
    targets: ["web"],
    criteria: [{ id: "utc", text: "统一使用 UTC" }],
    budget: 100,
  });
  await command("goals.approve", { id: goal.id });
  const plan = await command("plans.propose", {
    goalId: goal.id,
    summary: "持久记录澄清内容",
    tasks: [
      {
        id: "analysis",
        title: "核对接口约定",
        kind: "analysis",
        role: "developer",
        runtime: "linux",
        budgetCents: 100,
      },
    ],
  });
  await command("plans.approve", { id: plan.id });
  const worker = await command("workers.register", {
    projectId: project.id,
    title: "协作验收节点",
    roles: ["developer"],
    capabilities: ["analysis"],
    runtime: "linux",
  });
  const issued = await page.request.post("/api/v1/tokens", {
    data: { projectId: project.id, workerId: worker.id },
    headers: { Origin: origin, "X-CSRF-Token": session.csrf },
  });
  expect(issued.ok()).toBeTruthy();
  const credential = await issued.json();
  const client = await playwright.request.newContext({
    baseURL: origin,
    extraHTTPHeaders: { Authorization: "Bearer " + credential.token },
  });
  try {
    const receive = async (run: any) => {
      const response = await client.post("/api/v1/commands/messages.receive", {
        data: { runId: run.id, leaseToken: run.data.leaseToken },
      });
      const result = await response.json();
      expect(response.ok(), JSON.stringify(result)).toBeTruthy();
      return result;
    };
    const claimed = await client.post("/api/v1/commands/tasks.claim", {
      data: {},
    });
    expect(claimed.ok()).toBeTruthy();
    const run = await claimed.json();
    expect(run.kind).toBe("run");
    const content = "统一使用 UTC；协作回复仍需独立测试与发布审批。";
    const message = await command("messages.send", {
      projectId: project.id,
      goalId: goal.id,
      recipient: credential.actor.id,
      type: "clarification",
      key: "question",
      payload: {
        contract: content,
        longReference: "contract_" + "x".repeat(180),
      },
    });
    const inbox = await receive(run);
    expect(inbox.data.items).toHaveLength(1);
    expect(inbox.data.items[0].data.message.id).toBe(message.id);
    const repeat = await receive(run);
    expect(repeat.data.items[0].id).toBe(inbox.data.items[0].id);
    await page.goto("/");
    if (info.project.name === "mobile")
      await page.getByRole("button", { name: "打开导航", exact: true }).click();
    await page.getByLabel("选择项目").selectOption(project.id);
    if (info.project.name === "mobile")
      await page.getByRole("button", { name: "关闭导航", exact: true }).click();
    await page.getByRole("tab", { name: "执行记录", exact: true }).click();
    const button = page.getByRole("button", {
      name: "查看工作项：核对接口约定",
      exact: true,
    });
    await button.focus();
    await page.keyboard.press("Enter");
    const dialog = page.getByRole("dialog");
    const context = dialog.getByRole("region", {
      name: "协作上下文",
      exact: true,
    });
    await expect(context).toBeVisible();
    await expect(context.locator(".ant-bubble")).toBeVisible();
    await expect(
      context.getByText("已进入工作项上下文；不表示模型已理解或执行。", {
        exact: true,
      }),
    ).toBeVisible();
    await expect(context.getByText(message.id, { exact: true })).toBeVisible();
    await expect(context.getByText(run.id, { exact: true })).toBeVisible();
    await expect(context.getByText("v1", { exact: true })).toBeVisible();
    await expect(context.getByText(content, { exact: false })).toBeVisible();
    expect(await dialog.innerText()).not.toContain(run.data.leaseToken);
    expect(
      await dialog.evaluate((el) => el.scrollWidth <= el.clientWidth),
    ).toBeTruthy();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBeTruthy();
    await page.screenshot({
      path: "../work/screenshots/" + info.project.name + "-agent-inbox.png",
      fullPage: true,
    });
    await page.keyboard.press("Escape");
    await expect(dialog).not.toBeVisible();
    await expect(button).toBeFocused();
  } finally {
    await client.dispose();
  }
});
