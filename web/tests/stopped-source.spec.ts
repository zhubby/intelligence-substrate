import { test, expect } from "./fixtures";

// These browser cases verify how authenticated worker reports are presented.
// Actual source retention/adoption is exercised by the Go HTTP/Worker tests.
for (const mode of ["local", "failed"] as const) {
  test(`stopped source ${mode} reports remain distinct from a platform candidate`, async ({
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
      title: `中断源码报告 ${mode} ${info.project.name} ${Date.now()}`,
    });
    const goal = await command("goals.create", {
      projectId: project.id,
      title: "保留中断的代码",
      outcome: "源码报告与平台候选分别显示",
      targets: ["web"],
      criteria: [{ id: "safe", text: "独立验证后才允许继续交付" }],
      budget: 1,
    });
    await command("goals.approve", { id: goal.id });
    const plan = await command("plans.propose", {
      goalId: goal.id,
      summary: "记录中断的源文件",
      tasks: [
        {
          id: "code",
          title: "中断源码任务",
          kind: "code",
          role: "developer",
          runtime: "linux",
          budgetCents: 10,
        },
      ],
    });
    await command("plans.approve", { id: plan.id });
    const worker = await command("workers.register", {
      projectId: project.id,
      title: "专用源码执行节点",
      roles: ["developer"],
      capabilities: ["code"],
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
      const claimed = await client.post("/api/v1/commands/tasks.claim", {
        data: {},
      });
      expect(claimed.ok()).toBeTruthy();
      const run = await claimed.json();
      const digest = "a".repeat(64);
      const reported = await client.post("/api/v1/commands/tasks.complete", {
        data: {
          id: run.id,
          leaseToken: run.data.leaseToken,
          exitCode: 1,
          result: {
            blocked: true,
            reason: "模型工作项预算不足",
            ...(mode === "local"
              ? {
                  stoppedSource: {
                    incomplete: true,
                    status: "local_only",
                    archive: ".stopped-source-fixture/source.tar",
                    digest,
                    synchronizationError: "平台连接未确认",
                  },
                }
              : { sourcePreservationError: "源码包含无效链接" }),
          },
          log: "原模型错误与非零退出仍保留",
        },
      });
      expect(reported.ok()).toBeTruthy();
      await page.goto("/");
      if (info.project.name === "mobile")
        await page.getByRole("button", { name: "打开导航", exact: true }).click();
      await page.getByLabel("选择项目").selectOption(project.id);
      if (info.project.name === "mobile")
        await page.getByRole("button", { name: "关闭导航", exact: true }).click();
      await page.getByRole("tab", { name: "执行记录", exact: true }).click();
      const notice = page.getByLabel("中断源码保存");
      await expect(notice).toBeVisible();
      if (mode === "local") {
        await expect(notice).toContainText("执行节点报告源码已保存在本地，尚未同步到平台");
        await expect(notice).toContainText("专用源码执行节点");
        await expect(notice).toContainText(digest);
      } else {
        await expect(notice).toContainText("执行节点报告未能保存中断源码");
      }
      await expect(
        page.getByRole("button", { name: "核对源码：中断源码任务", exact: true }),
      ).toHaveCount(0);
      await expect(notice).not.toContainText("source.tar");
      await expect(page.locator("body")).not.toContainText(credential.token);
      expect(
        await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
      ).toBeTruthy();
      await page.screenshot({
        path: `../work/screenshots/${info.project.name}-stopped-source-${mode}.png`,
        fullPage: true,
        animations: "disabled",
      });
    } finally {
      await client.dispose();
    }
  });
}
