import { test, expect } from "./fixtures";
import { mkdir } from "node:fs/promises";
import path from "node:path";
import { metricFixture, openMetrics } from "./metric-fixtures";

test("version-bound observations preserve first failure, corrections and unknown coverage", async ({
  page,
  ownerSession,
}, info) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const f = await metricFixture(
    page,
    ownerSession.csrf,
    info.project.name + Date.now(),
  );
  await openMetrics(
    page,
    f.project.id,
    f.goal.id,
    info.project.name === "mobile",
  );
  await expect(
    page.getByRole("heading", { name: "交付效能与观测依据", exact: true }),
  ).toBeVisible();
  const previewCard = page.getByRole("group", { name: "需求到预览" });
  const businessCard = page.getByRole("group", { name: "首次业务验收" });
  await expect(previewCard).toContainText("待采集");
  await expect(businessCard).toContainText("0 / 1 个需求版本已观察");
  const mobileOverflow = await page.evaluate(() => ({
    viewport: document.documentElement.clientWidth,
    scrollWidth: document.documentElement.scrollWidth,
    offenders: [...document.querySelectorAll("main *")]
      .filter(
        (el) =>
          el.getBoundingClientRect().right >
          document.documentElement.clientWidth + 1,
      )
      .map((el) => ({
        tag: el.tagName,
        class: el.className,
        right: el.getBoundingClientRect().right,
      }))
      .slice(0, 12),
  }));
  await info.attach("initial-layout-width", {
    body: JSON.stringify(mobileOverflow),
    contentType: "application/json",
  });
  expect(mobileOverflow.scrollWidth).toBeLessThanOrEqual(
    mobileOverflow.viewport + 1,
  );
  const add = page.getByRole("button", { name: "登记业务验收", exact: true });
  await add.focus();
  await page.keyboard.press("Enter");
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(dialog).not.toBeVisible();
  await expect(add).toBeFocused();
  const fillBasis = async (reference: string) => {
    await dialog.getByLabel("观测来源标识", { exact: true }).fill(reference);
    await dialog
      .getByLabel("观测依据", { exact: true })
      .fill("合成测试记录：验证 UI 与统计口径，不代表真实业务验收。");
  };
  const recordBusiness = async (result: string, reference: string) => {
    await add.click();
    await dialog
      .getByLabel("候选版本", { exact: true })
      .selectOption(f.release.id);
    await dialog.getByLabel("验收结果", { exact: true }).selectOption(result);
    await dialog
      .getByLabel("报告附件", { exact: true })
      .selectOption(f.proof.id);
    await fillBasis(reference);
    if (info.project.name === "mobile") {
      await dialog
        .getByRole("button", { name: "保存观测", exact: true })
        .scrollIntoViewIfNeeded();
      const geometry = await page.evaluate(() => {
        const describe = (el: Element | null) => {
          if (!el) return null;
          const r = el.getBoundingClientRect();
          const s = getComputedStyle(el);
          return {
            tag: el.tagName,
            class: el.className,
            rect: { x: r.x, y: r.y, width: r.width, height: r.height },
            scrollWidth: el.scrollWidth,
            clientWidth: el.clientWidth,
            overflow: s.overflow,
            position: s.position,
            fontSize: s.fontSize,
          };
        };
        const button = document.querySelector(".modal button[type=submit]");
        const r = button?.getBoundingClientRect();
        return {
          viewport: {
            innerWidth,
            innerHeight,
            scrollX,
            scrollY,
            scale: visualViewport?.scale,
            offsetLeft: visualViewport?.offsetLeft,
            offsetTop: visualViewport?.offsetTop,
            visualWidth: visualViewport?.width,
            visualHeight: visualViewport?.height,
          },
          root: describe(document.documentElement),
          overlay: describe(document.querySelector(".modal-overlay")),
          modal: describe(document.querySelector(".modal")),
          form: describe(document.querySelector(".modal form")),
          button: describe(button),
          hit: r
            ? describe(
                document.elementFromPoint(
                  r.x + r.width / 2,
                  r.y + r.height / 2,
                ),
              )
            : null,
        };
      });
      await info.attach("mobile-form-geometry", {
        body: JSON.stringify(geometry, null, 2),
        contentType: "application/json",
      });
    }
    await dialog.getByRole("button", { name: "保存观测", exact: true }).click();
    await expect(dialog).not.toBeVisible();
  };
  await recordBusiness("failed", "review-1");
  await recordBusiness("passed", "review-2");
  await expect(businessCard).toContainText("0.0%");
  await expect(businessCard).toContainText("1 / 1 个需求版本已观察");
  await page
    .getByRole("button", { name: "确认可运行预览", exact: true })
    .click();
  await dialog
    .getByLabel("独立运行证据", { exact: true })
    .selectOption(f.evidenceId);
  await fillBasis("preview-1");
  await dialog.getByRole("button", { name: "保存观测", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(previewCard).not.toContainText("待采集");
  const reportURL =
    "/api/v1/projects/" +
    f.project.id +
    "/delivery-metrics?goalId=" +
    f.goal.id;
  const report = await (await page.request.get(reportURL)).json();
  expect(report.firstPassRate).toBe(0);
  expect(report.acceptanceSamples).toBe(1);
  expect(report.previewSamples).toBe(1);
  expect(report.totalCostPerSuccessfulDeliveryCents).toBeNull();
  const failure = report.observations.find(
    (r: any) => r.reference === "review-1",
  );
  const row = page.getByRole("article", {
    name: "观测记录 " + failure.id,
    exact: true,
  });
  await expect(
    row.getByRole("link", { name: "查看报告附件", exact: true }),
  ).toHaveAttribute("href", "/api/v1/artifacts/" + f.proof.id + "/content");
  await expect(row).toContainText("首次观察");
  await page.getByRole("button", { name: "登记本人投入", exact: true }).click();
  const start = new Date(
    Math.ceil(new Date(f.goal.createdAt).getTime() / 1000) * 1000,
  );
  await expect
    .poll(() => Math.floor(Date.now() / 1000) * 1000)
    .toBeGreaterThan(start.getTime());
  const end = new Date(Math.floor(Date.now() / 1000) * 1000);
  const local = (d: Date) =>
    new Date(d.getTime() - d.getTimezoneOffset() * 60000)
      .toISOString()
      .slice(0, 19)
      .replace(/:00$/, "");
  expect(end.getTime()).toBeGreaterThan(start.getTime());
  await dialog.getByLabel("开始时间", { exact: true }).fill(local(start));
  await dialog.getByLabel("结束时间", { exact: true }).fill(local(end));
  await fillBasis("owner-effort-1");
  await dialog.getByRole("button", { name: "保存观测", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(page.getByRole("group", { name: "人工投入" })).toContainText(
    "1 个有效区间",
  );
  await page.getByRole("button", { name: "登记上线缺陷", exact: true }).click();
  await dialog
    .getByLabel("候选版本", { exact: true })
    .selectOption(f.release.id);
  await dialog.getByLabel("报告附件", { exact: true }).selectOption(f.proof.id);
  await fillBasis("unpublished-defect");
  await dialog.getByRole("button", { name: "保存观测", exact: true }).click();
  await expect(dialog.getByRole("alert")).toContainText(
    "上线缺陷只能关联已有真实发布观测的版本",
  );
  await page.keyboard.press("Escape");
  const screenshots = process.env.IS_QUALITY_SCREENSHOTS;
  if (screenshots) {
    await mkdir(screenshots, { recursive: true });
    await page.screenshot({
      path: path.join(
        screenshots,
        "delivery-metrics-" + info.project.name + ".png",
      ),
      fullPage: true,
    });
  }
  await row.getByRole("button", { name: "作废观测", exact: true }).click();
  await dialog
    .getByLabel("作废原因", { exact: true })
    .fill("来源标识填写错误，保留原始判断与更正轨迹");
  await dialog.getByRole("button", { name: "确认作废", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(businessCard).toContainText("100.0%");
  await expect(
    page.getByText("已作废观测（1）", { exact: true }),
  ).toBeVisible();
  await page.getByText("已作废观测（1）", { exact: true }).click();
  await expect(row).toContainText("来源标识填写错误");
  await recordBusiness("failed", "review-1");
  await expect(
    page.getByText("更正自 " + failure.id, { exact: true }),
  ).toBeVisible();
  // The correction is recorded after review-2; the first valid recorded result
  // is now review-2. This is explicitly the valid-ledger observation metric.
  await expect(businessCard).toContainText("100.0%");
  await f.command("goals.revise", { id: f.goal.id, reason: "新的业务范围" });
  await openMetrics(
    page,
    f.project.id,
    f.goal.id,
    info.project.name === "mobile",
  );
  await expect(businessCard).toContainText("待采集");
  await page.getByLabel("效能需求版本").selectOption("1");
  await expect(businessCard).toContainText("100.0%");
  await expect(
    page.getByText("历史观测不作为当前版本验收", { exact: true }),
  ).toBeVisible();
  await page.getByLabel("选择交付目标").selectOption(f.empty.id);
  await expect(previewCard).toContainText("待采集");
  await expect(page.getByText("review-1", { exact: true })).toHaveCount(0);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth + 1,
    ),
  ).toBeTruthy();
  expect(errors).toEqual([]);
});

test("metrics loading, failure and stale scope never borrow another goal's result", async ({
  page,
  ownerSession,
}, info) => {
  const f = await metricFixture(
    page,
    ownerSession.csrf,
    "states-" + info.project.name + Date.now(),
  );
  await openMetrics(
    page,
    f.project.id,
    f.goal.id,
    info.project.name === "mobile",
  );
  await expect(page.getByRole("group", { name: "首次业务验收" })).toContainText(
    "待采集",
  );
  const pattern = "**/api/v1/projects/" + f.project.id + "/delivery-metrics*";
  await page.route(pattern, (route) =>
    route.fulfill({
      status: 503,
      json: { error: { message: "观测服务暂时不可用" } },
    }),
  );
  await page.getByRole("button", { name: "刷新效能", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("观测服务暂时不可用");
  await page.unroute(pattern);
  let release!: () => void;
  const gate = new Promise<void>((r) => {
    release = r;
  });
  let entered!: () => void;
  const started = new Promise<void>((r) => {
    entered = r;
  });
  await page.route(pattern, async (route) => {
    if (
      new URL(route.request().url()).searchParams.get("goalId") === f.goal.id
    ) {
      entered();
      await gate;
    }
    await route.continue();
  });
  await page.getByRole("button", { name: "刷新效能", exact: true }).click();
  await started;
  await expect(page.getByRole("status")).toContainText("正在加载交付效能");
  await page.getByLabel("选择交付目标").selectOption(f.empty.id);
  release();
  await expect(page.getByRole("group", { name: "首次业务验收" })).toContainText(
    "0 / 1 个需求版本已观察",
  );
  await expect(
    page.getByText("当前观测：尚未观察的应用", { exact: true }),
  ).toBeVisible();
  await page.unroute(pattern);
});
