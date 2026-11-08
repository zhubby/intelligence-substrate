import { test, expect } from "./fixtures";
import { metricFixture } from "./metric-fixtures";

// All channel receipts in this test are synthetic; this checks governed UI.
test("rejection inputs remain visible in plans and viewers cannot confirm repair scope", async ({
  page,
  ownerSession,
}, info) => {
  const origin = "http://127.0.0.1:18082";
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  const f = await metricFixture(
    page,
    ownerSession.csrf,
    "拒审修复 " + info.project.name,
  );
  await f.command("releases.approve", { id: f.release.id });
  await f.command("releases.transition", {
    id: f.release.id,
    target: "web",
    state: "awaiting_review",
    externalId: "synthetic-booking-100",
    proofArtifactId: f.proof.id,
  });
  await f.command("releases.transition", {
    id: f.release.id,
    target: "web",
    state: "rejected",
    reason: "预约取消缺少确认步骤",
  });
  const workspace = async () =>
    (
      await page.request.get("/api/v1/projects/" + f.project.id + "/workspace")
    ).json();
  const issue = (await workspace()).find(
    (item: any) => item.kind === "release_issue",
  );
  const openPlans = async () => {
    await page.goto("/");
    if (info.project.name === "mobile")
      await page.getByRole("button", { name: "打开导航", exact: true }).click();
    await page.getByLabel("选择项目").selectOption(f.project.id);
    await page
      .getByRole("navigation", { name: "主导航" })
      .getByRole("button", { name: /^目标与需求/ })
      .click();
    await page.getByLabel("选择交付目标").selectOption(f.goal.id);
    await page.getByRole("tab", { name: "交付方案", exact: true }).click();
  };
  await openPlans();
  await expect(
    page.getByRole("button", { name: "生成方案", exact: true }),
  ).toBeDisabled();
  await expect(
    page.getByText("有拒审问题尚待确认修复要求，请先到构建与发布处理。", {
      exact: true,
    }),
  ).toBeVisible();
  const instructions =
    "取消预约前确认意图，并验证重复请求不能取消其他人的预约。";
  await f.command("release_issues.triage", {
    id: issue.id,
    expectedRevision: issue.revision,
    expectedBinding: issue.data.binding,
    key: "confirmed-privacy",
    strategy: "new-build",
    instructions,
  });
  const plan = await f.command("plans.propose", {
    goalId: f.goal.id,
    summary: "补齐预约取消确认并重新验证",
    tasks: [
      {
        id: "repair",
        title: "修复取消预约确认",
        kind: "code",
        role: "developer",
        runtime: "linux",
      },
    ],
  });
  expect(plan.data.releaseIssues[0].source.externalId).toBe(
    "synthetic-booking-100",
  );
  await openPlans();
  await expect(
    page.getByRole("button", { name: "生成方案", exact: true }),
  ).toBeEnabled();
  const proposal = page.getByRole("article", {
    name: "交付方案 v2",
    exact: true,
  });
  await proposal.getByText("关联拒审修复（1）", { exact: true }).click();
  await expect(
    proposal.getByText("已确认修复要求：" + instructions, { exact: true }),
  ).toBeVisible();
  const screenshots = process.env.IS_INCIDENT_SCREENSHOTS;
  if (screenshots)
    await page.screenshot({
      path: screenshots + "/plan-repair-input.png",
      fullPage: true,
      animations: "disabled",
    });

  const email = "issue-viewer-" + info.project.name + "@example.test";
  const identityResponse = await page.request.post("/api/v1/identities", {
    data: {
      email,
      name: "拒审只读观察员",
      password: "issue-viewer-password-2026",
    },
    headers: { Origin: origin, "X-CSRF-Token": ownerSession.csrf },
  });
  expect(identityResponse.ok(), await identityResponse.text()).toBeTruthy();
  const viewer = await identityResponse.json();
  await f.command("members.grant", {
    projectId: f.project.id,
    identityId: viewer.id,
    name: viewer.name,
    roles: ["viewer"],
  });
  const login = await page.request.post("/api/v1/auth/login", {
    data: { email, password: "issue-viewer-password-2026" },
    headers: { Origin: origin },
  });
  expect(login.ok()).toBeTruthy();
  const session = await login.json();
  await page.goto("/");
  if (info.project.name === "mobile")
    await page.getByRole("button", { name: "打开导航", exact: true }).click();
  await page.getByLabel("选择项目").selectOption(f.project.id);
  await page
    .getByRole("navigation", { name: "主导航" })
    .getByRole("button", { name: "构建与发布", exact: true })
    .click();
  await page.getByLabel("选择交付目标").selectOption(f.goal.id);
  await expect(
    page.getByRole("button", { name: "调整修复要求", exact: true }),
  ).toBeDisabled();
  const denied = await page.request.post(
    "/api/v1/commands/release_issues.triage",
    {
      data: {
        id: issue.id,
        expectedRevision: 2,
        expectedBinding: issue.data.binding,
        key: "viewer-attempt",
        strategy: "new-build",
        instructions: "Viewer must not change scope",
      },
      headers: { Origin: origin, "X-CSRF-Token": session.csrf },
    },
  );
  expect(denied.status()).toBe(403);
  if (screenshots)
    await page.screenshot({
      path: screenshots + "/viewer-readonly.png",
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
