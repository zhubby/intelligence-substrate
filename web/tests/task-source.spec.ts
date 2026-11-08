import { test, expect } from "./fixtures";

// Uses actual control-plane claims, lease-bound Git commits and source conflict
// resolution. This is UI/API acceptance, without a model or build executor.
test("task details preserve the source and explain unintegrated producer commits", async ({
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
    title: "源码追溯 " + info.project.name + " " + Date.now(),
  });
  const repo = await command("repositories.create", {
    projectId: project.id,
    title: "source-provenance",
  });
  const goal = await command("goals.create", {
    projectId: project.id,
    title: "并行实现与源码集成",
    outcome: "构建必须包含实际源码结果",
    criteria: [{ id: "source", text: "两个功能都已集成" }],
    targets: ["web"],
    budget: 100,
  });
  await command("goals.approve", { id: goal.id });
  const plan = await command("plans.propose", {
    goalId: goal.id,
    summary: "分别实现两个功能后构建",
    tasks: [
      {
        id: "left",
        title: "实现反馈录入",
        kind: "code",
        role: "developer",
        runtime: "linux",
        repositoryId: repo.id,
        branch: "left",
        budgetCents: 10,
      },
      {
        id: "right",
        title: "实现反馈列表",
        kind: "code",
        role: "developer",
        runtime: "linux",
        repositoryId: repo.id,
        branch: "right",
        budgetCents: 10,
      },
      {
        id: "build",
        title: "构建完整应用",
        kind: "build",
        role: "developer",
        runtime: "linux",
        repositoryId: repo.id,
        dependsOn: ["left", "right"],
        budgetCents: 10,
      },
    ],
  });
  await command("plans.approve", { id: plan.id });
  const worker = await command("workers.register", {
    projectId: project.id,
    title: "API 源码验收节点",
    roles: ["developer"],
    capabilities: ["code", "build"],
    runtime: "linux",
  });
  const tokenResponse = await page.request.post("/api/v1/tokens", {
    data: { projectId: project.id, workerId: worker.id },
    headers: { Origin: origin, "X-CSRF-Token": session.csrf },
  });
  expect(tokenResponse.ok()).toBeTruthy();
  const credential = await tokenResponse.json();
  const client = await playwright.request.newContext({
    baseURL: origin,
    extraHTTPHeaders: { Authorization: "Bearer " + credential.token },
  });
  try {
    const work = async (name: string, data: unknown) => {
      const response = await client.post("/api/v1/commands/" + name, { data });
      const result = await response.json();
      expect(response.ok(), JSON.stringify(result)).toBeTruthy();
      return result;
    };
    const produced = [];
    for (let i = 0; i < 2; i++) {
      const run = await work("tasks.claim", {});
      expect(run.kind).toBe("run");
      const binding = run.data.task.sourceBinding;
      const committed = await work("repositories.commit", {
        id: repo.id,
        branch: binding.targetBranch,
        baseCommit: binding.commit,
        expectedHead: binding.expectedHead,
        runId: run.id,
        leaseToken: run.data.leaseToken,
        message: "Implement " + binding.targetBranch,
        files: { [binding.targetBranch + ".txt"]: "actual source" },
      });
      await work("tasks.complete", {
        id: run.id,
        leaseToken: run.data.leaseToken,
        exitCode: 0,
        result: { repositoryId: repo.id, commit: committed.data.lastCommit },
      });
      produced.push({
        run: run.id,
        commit: committed.data.lastCommit,
        title: run.title,
      });
    }
    expect((await work("tasks.claim", {})).kind).toBe("worker");
    await page.goto("/");
    if (info.project.name === "mobile")
      await page.getByRole("button", { name: "打开导航", exact: true }).click();
    await page.getByLabel("选择项目").selectOption(project.id);
    if (info.project.name === "mobile")
      await page.getByRole("button", { name: "关闭导航", exact: true }).click();
    await page.getByRole("tab", { name: "执行记录", exact: true }).click();
    const sourceButton = page.getByRole("button", {
      name: "查看工作项：" + produced[0].title,
      exact: true,
    });
    await expect(sourceButton).toBeVisible();
    await sourceButton.focus();
    await page.keyboard.press("Enter");
    const dialog = page.getByRole("dialog");
    await expect(
      dialog.getByRole("heading", { name: "固定源码输入", exact: true }),
    ).toBeVisible();
    await expect(
      dialog.getByText(repo.data.head, { exact: true }),
    ).toBeVisible();
    await page.screenshot({
      path: "../work/screenshots/" + info.project.name + "-task-source.png",
      fullPage: true,
    });
    await page.keyboard.press("Escape");
    await expect(dialog).not.toBeVisible();
    await expect(sourceButton).toBeFocused();
    await page
      .getByRole("button", { name: "查看工作项：构建完整应用", exact: true })
      .click();
    await expect(
      dialog.getByRole("heading", { name: "源码需要处理", exact: true }),
    ).toBeVisible();
    for (const source of produced) {
      await expect(
        dialog.getByText(source.commit, { exact: true }),
      ).toBeVisible();
      await expect(dialog.getByText(source.run, { exact: true })).toBeVisible();
    }
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBeTruthy();
    expect(
      await dialog.evaluate((el) => el.scrollWidth <= el.clientWidth),
    ).toBeTruthy();
    await page.screenshot({
      path:
        "../work/screenshots/" +
        info.project.name +
        "-task-source-conflict.png",
      fullPage: true,
    });
    await page.keyboard.press("Escape");
    await expect(dialog).not.toBeVisible();
  } finally {
    await client.dispose();
  }
});
