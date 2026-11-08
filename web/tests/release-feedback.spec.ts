import { test, expect } from "./fixtures";
import { metricFixture, openMetrics } from "./metric-fixtures";
import { mkdir } from "node:fs/promises";
import path from "node:path";

async function published(
  page: Parameters<typeof metricFixture>[0],
  csrf: string,
  suffix: string,
) {
  const f = await metricFixture(page, csrf, suffix);
  await f.command("releases.approve", { id: f.release.id });
  await f.command("releases.transition", {
    id: f.release.id,
    target: "web",
    state: "published",
    externalId: "synthetic-published-version",
    proofArtifactId: f.proof.id,
  });
  return f;
}

test("late report and defect stay on the published historical version after a lost upload reply", async ({
  page,
  ownerSession,
}, info) => {
  const mobile = info.project.name === "mobile";
  const f = await published(
    page,
    ownerSession.csrf,
    info.project.name + Date.now(),
  );
  await f.command("goals.revise", {
    id: f.goal.id,
    reason: "New scope removes Web but does not remove deployed history",
    targets: ["windows"],
  });
  await openMetrics(page, f.project.id, f.goal.id, mobile);
  await page.getByLabel("效能需求版本").selectOption("1");
  await expect(
    page.getByRole("button", { name: "确认可运行预览", exact: true }),
  ).toBeDisabled();
  await expect(
    page.getByRole("button", { name: "登记上线缺陷", exact: true }),
  ).toBeEnabled();
  const upload = page.getByRole("button", {
    name: "上传上线问题报告",
    exact: true,
  });
  await upload.focus();
  await page.keyboard.press("Enter");
  const dialog = page.getByRole("dialog");
  await dialog
    .getByLabel("已发布版本与制品", { exact: true })
    .selectOption(f.release.id + "/" + f.artifact.id);
  const bytes = Buffer.from(
    "Synthetic late feedback after revision; not an actual enterprise defect\n",
  );
  await dialog.getByLabel("问题报告文件", { exact: true }).setInputFiles({
    name: "late-issue.txt",
    mimeType: "text/plain",
    buffer: bytes,
  });
  const keys: string[] = [];
  let accepted: any;
  await page.route("**/api/v1/releases/*/reports?*", async (route) => {
    keys.push(new URL(route.request().url()).searchParams.get("key") || "");
    const response = await route.fetch();
    expect(response.ok(), await response.text()).toBeTruthy();
    accepted = await response.json();
    if (keys.length === 1)
      await route.fulfill({
        status: 503,
        contentType: "application/json",
        body: JSON.stringify({
          error: {
            code: "synthetic_reply_loss",
            message: "上传回复中断，请保留原文件并重试",
          },
        }),
      });
    else await route.fulfill({ response });
  });
  await dialog.getByRole("button", { name: "上传报告", exact: true }).click();
  await expect(dialog.getByRole("alert")).toContainText("上传回复中断");
  await dialog.getByRole("button", { name: "上传报告", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  expect(keys).toHaveLength(2);
  expect(keys[0]).toBeTruthy();
  expect(keys[1]).toBe(keys[0]);
  expect(accepted.data.requirementRevision).toBe(1);
  expect(accepted.data.releaseId).toBe(f.release.id);
  const workspace = await (
    await page.request.get("/api/v1/projects/" + f.project.id + "/workspace")
  ).json();
  expect(
    workspace.filter(
      (r: any) =>
        r.kind === "artifact" && r.data.purpose === "release-feedback",
    ),
  ).toHaveLength(1);
  await page.getByRole("button", { name: "登记上线缺陷", exact: true }).click();
  await dialog
    .getByLabel("候选版本", { exact: true })
    .selectOption(f.release.id);
  await dialog
    .getByLabel("报告附件", { exact: true })
    .selectOption(accepted.id);
  await dialog
    .getByLabel("观测来源标识", { exact: true })
    .fill("late-ticket-42");
  await dialog
    .getByLabel("观测依据", { exact: true })
    .fill("本机合成问题记录，检验准确历史归属，不证明真实业务故障。");
  await dialog.getByRole("button", { name: "保存观测", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  const card = page.getByRole("group", { name: "上线缺陷", exact: true });
  await expect(card.locator("dd")).toHaveText("1");
  const reportURL =
    "/api/v1/projects/" +
    f.project.id +
    "/delivery-metrics?goalId=" +
    f.goal.id;
  const history = await (
    await page.request.get(reportURL + "&revision=1")
  ).json();
  const observation = history.observations.find(
    (r: any) => r.reference === "late-ticket-42",
  );
  expect(observation.requirementRevision).toBe(1);
  expect(observation.usable).toBe(true);
  const row = page.getByRole("article", {
    name: "观测记录 " + observation.id,
    exact: true,
  });
  await expect(
    row.getByRole("link", { name: "查看报告附件", exact: true }),
  ).toHaveAttribute("href", "/api/v1/artifacts/" + accepted.id + "/content");
  const downloaded = await page.request.get(
    "/api/v1/artifacts/" + accepted.id + "/content",
  );
  expect(await downloaded.body()).toEqual(bytes);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  if (process.env.IS_QUALITY_SCREENSHOTS) {
    await mkdir(process.env.IS_QUALITY_SCREENSHOTS, { recursive: true });
    await page.screenshot({
      path: path.join(
        process.env.IS_QUALITY_SCREENSHOTS,
        "release-feedback-" + info.project.name + ".png",
      ),
      fullPage: true,
    });
  }
  await row.getByRole("button", { name: "作废观测", exact: true }).click();
  await dialog
    .getByLabel("作废原因", { exact: true })
    .fill("合成工单更正，保留原记录");
  await dialog.getByRole("button", { name: "确认作废", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(card.locator("dd")).toHaveText("0");
  await page.getByRole("button", { name: "登记上线缺陷", exact: true }).click();
  await dialog
    .getByLabel("候选版本", { exact: true })
    .selectOption(f.release.id);
  await dialog
    .getByLabel("报告附件", { exact: true })
    .selectOption(accepted.id);
  await dialog
    .getByLabel("观测来源标识", { exact: true })
    .fill("late-ticket-42");
  await dialog
    .getByLabel("观测依据", { exact: true })
    .fill("合成工单的更正记录，归属原发布版本。");
  await dialog.getByRole("button", { name: "保存观测", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(card.locator("dd")).toHaveText("1");
  const corrected = await (
    await page.request.get(reportURL + "&revision=1")
  ).json();
  expect(
    corrected.observations.find((r: any) => r.status === "recorded")
      .replacesMeasurementId,
  ).toBe(observation.id);
  await page.getByLabel("效能需求版本").selectOption("");
  await expect(card.locator("dd")).toHaveText("0");
  expect(
    (await (await page.request.get(reportURL)).json())
      .reportedProductionDefects,
  ).toBe(0);
  await expect(
    page.getByRole("button", { name: "上传上线问题报告", exact: true }),
  ).toBeDisabled();
});

test("changed report bytes after a lost reply cannot overwrite the accepted report", async ({
  page,
  ownerSession,
}, info) => {
  const f = await published(
    page,
    ownerSession.csrf,
    "conflict" + info.project.name + Date.now(),
  );
  await openMetrics(
    page,
    f.project.id,
    f.goal.id,
    info.project.name === "mobile",
  );
  const upload = page.getByRole("button", {
    name: "上传上线问题报告",
    exact: true,
  });
  await upload.click();
  const dialog = page.getByRole("dialog");
  await dialog
    .getByLabel("已发布版本与制品", { exact: true })
    .selectOption(f.release.id + "/" + f.artifact.id);
  const file = dialog.getByLabel("问题报告文件", { exact: true });
  await file.setInputFiles({
    name: "issue.txt",
    mimeType: "text/plain",
    buffer: Buffer.from("original report"),
  });
  let calls = 0;
  await page.route("**/api/v1/releases/*/reports?*", async (route) => {
    calls++;
    if (calls === 1) {
      const response = await route.fetch();
      expect(response.ok()).toBeTruthy();
      await route.fulfill({
        status: 503,
        contentType: "application/json",
        body: JSON.stringify({
          error: { message: "上传回复中断，请保留原文件并重试" },
        }),
      });
    } else await route.continue();
  });
  await dialog.getByRole("button", { name: "上传报告", exact: true }).click();
  await expect(dialog.getByRole("alert")).toContainText("上传回复中断");
  await file.setInputFiles({
    name: "issue.txt",
    mimeType: "text/plain",
    buffer: Buffer.from("different bytes"),
  });
  await dialog.getByRole("button", { name: "上传报告", exact: true }).click();
  await expect(dialog.getByRole("alert")).toContainText(
    "幂等键已用于不同的请求内容",
  );
  expect(calls).toBe(2);
  const rows = await (
    await page.request.get("/api/v1/projects/" + f.project.id + "/workspace")
  ).json();
  const reports = rows.filter(
    (r: any) => r.kind === "artifact" && r.data.purpose === "release-feedback",
  );
  expect(reports).toHaveLength(1);
  expect(
    await (
      await page.request.get("/api/v1/artifacts/" + reports[0].id + "/content")
    ).text(),
  ).toBe("original report");
  await page.keyboard.press("Escape");
  await expect(dialog).not.toBeVisible();
  await expect(upload).toBeFocused();
  await openMetrics(
    page,
    f.project.id,
    f.goal.id,
    info.project.name === "mobile",
  );
  await page.getByRole("button", { name: "登记业务验收", exact: true }).click();
  await expect(
    dialog
      .getByLabel("报告附件", { exact: true })
      .locator('option[value="' + reports[0].id + '"]'),
  ).toHaveCount(0);
  await page.keyboard.press("Escape");
});
