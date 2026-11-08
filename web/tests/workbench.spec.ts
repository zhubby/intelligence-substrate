import { test, expect, type Page } from "@playwright/test";
async function navigate(page: Page, name: string) {
  if ((page.viewportSize()?.width || 0) < 700)
    await page.getByRole("button", { name: "打开导航", exact: true }).click();
  await page
    .getByRole("navigation", { name: "主导航" })
    .getByRole("button", { name, exact: true })
    .click();
}

test("a real delivery workspace preserves approvals, waiting work, code and revision invalidation", async ({
  page,
}, info) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto("/");
  await page
    .getByLabel("企业邮箱", { exact: true })
    .fill("browser@example.test");
  await page
    .getByLabel("密码", { exact: true })
    .fill("browser-test-password-2026");
  await page.getByRole("button", { name: "登录工作台", exact: true }).click();
  if (info.project.name === "mobile")
    await expect(
      page.getByRole("button", { name: "打开导航", exact: true }),
    ).toBeVisible();
  else await expect(page.getByLabel("选择项目")).toBeVisible();
  if (info.project.name === "mobile")
    await page.getByRole("button", { name: "打开导航", exact: true }).click();
  const projectButton = page.getByRole("button", {
    name: "新建项目",
    exact: true,
  });
  await expect(projectButton).toBeAttached();
  await projectButton.click();
  const dialog = page.getByRole("dialog");
  const projectName = "客户反馈 " + info.project.name + " " + Date.now();
  await dialog.getByLabel("项目名称", { exact: true }).fill(projectName);
  await dialog
    .getByLabel("项目背景", { exact: true })
    .fill("收集客户反馈，验证需求到交付的完整追溯。");
  await dialog.getByRole("button", { name: "创建项目", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  if (info.project.name === "mobile")
    await page.getByRole("button", { name: "关闭导航", exact: true }).click();
  await page
    .getByRole("button", { name: "创建第一个目标", exact: true })
    .click();
  await dialog
    .getByLabel("交付目标", { exact: true })
    .fill("客户可以提交并查看反馈");
  await dialog
    .getByLabel("业务结果", { exact: true })
    .fill("客户在各端提交反馈，并查看最新处理结果。");
  await dialog
    .getByLabel("验收标准 1", { exact: true })
    .fill("提交反馈后能在反馈列表中看到记录");
  const checks = dialog
    .getByRole("group", { name: "必选交付目标" })
    .getByRole("checkbox");
  for (let i = 1; i < (await checks.count()); i++)
    await checks.nth(i).uncheck();
  await dialog
    .getByRole("button", { name: "创建交付目标", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "客户可以提交并查看反馈", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "审阅并确认", exact: true }).click();
  await dialog.getByRole("button", { name: "确认批准", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await page.getByRole("tab", { name: "交付方案", exact: true }).click();
  await page.getByRole("button", { name: "生成方案", exact: true }).click();
  await dialog
    .getByRole("button", { name: "生成交付方案", exact: true })
    .click();
  await expect(
    page.getByRole("alert").filter({ hasText: "尚未配置模型" }),
  ).toBeVisible();
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: "人工编制", exact: true }).click();
  await dialog
    .getByLabel("方案与交付路径", { exact: true })
    .fill("先确定交互和接口，再实现反馈应用，最后独立验证并发布。");
  await dialog
    .getByRole("button", { name: "提交方案审阅", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  await page.getByRole("button", { name: "批准方案", exact: true }).click();
  await dialog.getByRole("button", { name: "确认批准", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "工作项", exact: true }),
  ).toBeVisible();
  await expect(page.getByText("待执行", { exact: true })).toHaveCount(2);
  await expect(page.getByText("已完成", { exact: true })).toHaveCount(0);
  await page.getByRole("tab", { name: "需求基线", exact: true }).click();
  await page.screenshot({
    path:
      (process.env.IS_COST_SCREENSHOTS || "../work/screenshots") +
      "/" +
      info.project.name +
      "-workspace.png",
    fullPage: true,
  });
  await navigate(page, "代码与评审");
  await page
    .getByRole("button", { name: "创建第一个仓库", exact: true })
    .click();
  await dialog.getByLabel("仓库名称", { exact: true }).fill("feedback-app");
  await dialog
    .getByRole("button", { name: "创建 Git 仓库", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  await page.getByRole("button", { name: "提交变更", exact: true }).click();
  await dialog.getByLabel("文件路径", { exact: true }).fill("README.md");
  await dialog
    .getByLabel("文件内容", { exact: true })
    .fill("# Feedback app\nReal source tracked by the platform.");
  await dialog
    .getByLabel("提交说明", { exact: true })
    .fill("Add feedback specification");
  await dialog
    .getByRole("button", { name: "提交到工作分支", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  await expect(
    page.getByText("Real source tracked by the platform.", { exact: false }),
  ).toBeVisible();
  await navigate(page, "质量与证据");
  await expect(
    page.getByRole("heading", { name: "暂无实际制品", exact: true }),
  ).toBeVisible();
  await navigate(page, "构建与发布");
  await expect(
    page.getByRole("heading", { name: "还没有发布候选版本", exact: true }),
  ).toBeVisible();
  await navigate(page, "Agent 与执行");
  await page.getByRole("button", { name: "注册节点", exact: true }).click();
  await dialog.getByLabel("节点名称", { exact: true }).fill("Linux 开发节点");
  await dialog.getByLabel("授权为独立质量节点", { exact: false }).check();
  await dialog
    .getByRole("button", { name: "注册执行节点", exact: true })
    .click();
  await expect(dialog.getByRole("alert")).toContainText("独立质量身份不能兼任");
  await page.keyboard.press("Escape");
  await expect(dialog).not.toBeVisible();
  await navigate(page, "目标与需求");
  await page.getByRole("button", { name: "变更基线", exact: false }).click();
  await dialog.getByLabel("变更原因", { exact: true }).fill("增加反馈内容说明");
  await dialog
    .getByLabel("验收标准 1", { exact: true })
    .fill("提交反馈后，列表显示内容与处理状态");
  await dialog
    .getByRole("button", { name: "创建新需求版本", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  await expect(
    page.getByText("基线 v2", { exact: false }).first(),
  ).toBeVisible();
  await page.getByRole("tab", { name: "交付方案", exact: true }).click();
  await expect(page.getByText("已失效", { exact: true })).toBeVisible();
  await navigate(page, "企业治理");
  await expect(page.getByText("变更需求基线", { exact: true })).toBeVisible();
  await page.getByRole("tab", { name: "项目知识", exact: true }).click();
  await page.getByRole("button", { name: "添加知识", exact: true }).click();
  await dialog.getByLabel("知识标题", { exact: true }).fill("领域约定");
  await dialog.getByLabel("来源", { exact: true }).fill("客户访谈");
  await dialog
    .getByLabel("内容", { exact: true })
    .fill("仅接受客户主动提交的反馈");
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await page.getByRole("button", { name: /领域约定/ }).click();
  await dialog.getByRole("button", { name: "创建新版本", exact: true }).click();
  await dialog
    .getByLabel("知识内容", { exact: true })
    .fill("客户可提交并查看公开反馈");
  await dialog.getByLabel("修订原因", { exact: true }).fill("确认公开范围");
  await dialog.getByRole("button", { name: "保存新版本", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(
    page.getByRole("button", { name: /领域约定/ }).filter({ hasText: "v2" }),
  ).toBeVisible();
  await page.getByRole("tab", { name: "协作消息", exact: true }).click();
  const me = await (await page.request.get("/api/v1/auth/me")).json();
  await page.getByRole("button", { name: "发送消息", exact: true }).click();
  await dialog.getByLabel("收件人", { exact: true }).selectOption(me.actor.id);
  await dialog
    .getByLabel("消息内容", { exact: true })
    .fill("请确认反馈的公开范围");
  await dialog.getByRole("button", { name: "发送消息", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await page.getByRole("button", { name: "领取我的消息", exact: true }).click();
  await dialog.getByRole("button", { name: "领取下一条", exact: true }).click();
  await expect(dialog).toContainText("请确认反馈的公开范围");
  await dialog.getByRole("button", { name: "确认已处理", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(page.getByText("已交付", { exact: true })).toBeVisible();
  await navigate(page, "Agent 与执行");
  await page.getByRole("button", { name: "注册节点", exact: true }).click();
  await dialog.getByLabel("节点名称", { exact: true }).fill("凭据验证节点");
  await dialog
    .getByRole("button", { name: "注册执行节点", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  await page.getByRole("button", { name: "访问凭据", exact: true }).click();
  await dialog
    .getByRole("button", { name: "签发 30 天有效凭据", exact: true })
    .click();
  await expect(
    dialog.getByText("保存节点访问凭据", { exact: true }),
  ).toBeVisible();
  await page.keyboard.press("Escape");
  await navigate(page, "企业治理");
  await page.getByRole("tab", { name: "访问凭据", exact: true }).click();
  await page.getByRole("button", { name: "撤销", exact: true }).click();
  await dialog.getByRole("button", { name: "撤销凭据", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(
    page.getByText("尚无项目访问凭据，可在执行节点中签发。", { exact: true }),
  ).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBeTruthy();
  expect(errors).toEqual([]);
});
