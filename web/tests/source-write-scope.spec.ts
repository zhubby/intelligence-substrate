import { test, expect } from "./fixtures";

test("approved write paths survive the manual form, approval and task view", async ({
  page,
  ownerSession,
}, info) => {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  const command = async (name: string, data: unknown) => {
    const response = await page.request.post("/api/v1/commands/" + name, {
      data,
      headers: {
        Origin: "http://127.0.0.1:18082",
        "X-CSRF-Token": ownerSession.csrf,
      },
    });
    expect(response.ok(), await response.text()).toBeTruthy();
    return response.json();
  };
  const project = await command("projects.create", {
    title: "源码范围 " + info.project.name,
  });
  const goal = await command("goals.create", {
    projectId: project.id,
    title: "仅修改已批准的后端与路由",
    outcome: "保护其他界面代码，修改范围需在方案中审查。",
    criteria: [{ id: "scope", text: "只修改批准的文件并独立测试" }],
    targets: ["web"],
  });
  await command("goals.approve", { id: goal.id });
  await page.goto("/");
  if (info.project.name === "mobile")
    await page.getByRole("button", { name: "打开导航", exact: true }).click();
  await page.getByLabel("选择项目").selectOption(project.id);
  await page
    .getByRole("navigation", { name: "主导航" })
    .getByRole("button", { name: /^目标与需求/ })
    .click();
  await page.getByLabel("选择交付目标").selectOption(goal.id);
  await page.getByRole("tab", { name: "交付方案", exact: true }).click();
  await page.getByRole("button", { name: "人工编制", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog
    .getByLabel("方案与交付路径", { exact: true })
    .fill("仅修改后端目录和一个路由文件，保持其余界面字节不变。");
  const mode = dialog.getByLabel("源码修改范围", { exact: true });
  await expect(mode).toHaveValue("repository");
  await mode.selectOption("paths");
  const paths = dialog.getByLabel("允许修改的路径", { exact: true });
  await expect(paths).toBeVisible();
  await expect(paths).toHaveAttribute("required", "");
  await paths.fill("backend/\napp/[id]/page.tsx");
  const proposed = page.waitForResponse((response) =>
    response.url().endsWith("/api/v1/commands/plans.propose"),
  );
  await dialog
    .getByRole("button", { name: "提交方案审阅", exact: true })
    .click();
  const response = await proposed;
  expect(response.ok(), await response.text()).toBeTruthy();
  const plan = await response.json();
  const recipe = plan.data.tasks.find(
    (task: { kind: string }) => task.kind === "code",
  );
  expect(recipe.writePaths).toEqual(["app/[id]/page.tsx", "backend/"]);
  expect(recipe).not.toHaveProperty("writePathsText");
  expect(recipe).not.toHaveProperty("writeScopeMode");
  await expect(dialog).not.toBeVisible();
  await expect(page.getByText("backend/", { exact: true })).toBeVisible();
  await expect(
    page.getByText("app/[id]/page.tsx", { exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "批准方案", exact: true }).click();
  await expect(dialog.getByText("backend/", { exact: true })).toBeVisible();
  await expect(
    dialog.getByText("app/[id]/page.tsx", { exact: true }),
  ).toBeVisible();
  const screenshots = process.env.IS_PLAN_SCREENSHOTS;
  if (screenshots)
    await page.screenshot({
      path: screenshots + "/scope-approval.png",
      fullPage: true,
      animations: "disabled",
    });
  await dialog.getByRole("button", { name: "确认批准", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  const workspace = await (
    await page.request.get("/api/v1/projects/" + project.id + "/workspace")
  ).json();
  const task = workspace.find(
    (item: { kind: string; data: { kind: string; planId: string } }) =>
      item.kind === "task" &&
      item.data.kind === "code" &&
      item.data.planId === plan.id,
  );
  expect(task.data.writePaths).toEqual(["app/[id]/page.tsx", "backend/"]);
  await page
    .getByRole("button", {
      name: "查看工作项：实现业务功能与多端应用",
      exact: true,
    })
    .click();
  await expect(dialog.getByText("backend/", { exact: true })).toBeVisible();
  await expect(
    dialog.getByText("app/[id]/page.tsx", { exact: true }),
  ).toBeVisible();
  if (screenshots)
    await page.screenshot({
      path: screenshots + "/scope-task.png",
      fullPage: true,
      animations: "disabled",
    });
  await page.keyboard.press("Escape");
  await page.getByRole("tab", { name: "交付方案", exact: true }).click();
  // Historical plans without a scope must show their actual repository-wide authority.
  await command("plans.propose", {
    goalId: goal.id,
    summary: "旧配方兼容范围显示",
    tasks: [
      {
        id: "legacy",
        title: "全仓库范围任务",
        kind: "code",
        role: "developer",
        runtime: "linux",
      },
    ],
  });
  await page.reload();
  await page.getByRole("tab", { name: "交付方案", exact: true }).click();
  await expect(
    page.getByText("全仓库（未限定路径）", { exact: true }),
  ).toBeVisible();
  expect(errors).toEqual([]);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBeTruthy();
});
