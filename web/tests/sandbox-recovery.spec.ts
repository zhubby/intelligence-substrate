import { test, expect } from "./fixtures";
import type { Page } from "@playwright/test";

async function fixture(
  page: Page,
  csrf: string,
  active = false,
  secure = false,
) {
  const command = async (name: string, data: unknown, token?: string) => {
    const r = await page.request.post("/api/v1/commands/" + name, {
      data,
      headers: token
        ? { Authorization: "Bearer " + token }
        : { Origin: "http://127.0.0.1:18082", "X-CSRF-Token": csrf },
    });
    expect(r.ok(), await r.text()).toBeTruthy();
    return r.json();
  };
  const project = await command("projects.create", { title: "AX 客体核对" });
  const goal = await command("goals.create", {
    projectId: project.id,
    title: "预约交付",
    outcome: "保留中断执行边界",
    targets: ["web"],
    criteria: [{ id: "lease", text: "原客体停止后才继续" }],
    budget: 100,
  });
  await command("goals.approve", { id: goal.id });
  const plan = await command("plans.propose", {
    goalId: goal.id,
    summary: "原节点执行",
    tasks: [
      {
        id: "analysis",
        title: "核对预约契约",
        kind: "analysis",
        role: "developer",
        runtime: "linux",
      },
    ],
  });
  await command("plans.approve", { id: plan.id });
  const worker = await command("workers.register", {
    projectId: project.id,
    title: "原 AX 节点",
    roles: ["developer"],
    runtime: "linux",
    capabilities: ["analysis"],
  });
  const issued = await page.request.post("/api/v1/tokens", {
    data: { projectId: project.id, workerId: worker.id },
    headers: { Origin: "http://127.0.0.1:18082", "X-CSRF-Token": csrf },
  });
  expect(issued.ok()).toBeTruthy();
  const token = (await issued.json()).token;
  const run = await command("tasks.claim", {}, token);
  const sandbox = await command(
    "sandboxes.prepare",
    {
      runId: run.id,
      leaseToken: run.data.leaseToken,
      key: "browser-owned",
      command: ["/bin/true"],
      backend: {
        context: "gke_enterprise/asia:cluster",
        namespace: "ax-system",
        atespace: "browser-test",
        image: "runner@sha256:" + "a".repeat(64),
        ...(secure
          ? {
              transport: {
                version: "mtls-v1",
                control: {
                  endpoint: "control.ax.example:443",
                  serverName: "control.ax.example",
                  serverSPKI: "b".repeat(64),
                  caDigest: "c".repeat(64),
                  clientCertDigest: "d".repeat(64),
                },
                guest: {
                  endpoint: "router.ax.example:443",
                  serverName: "router.ax.example",
                  serverSPKI: "e".repeat(64),
                  caDigest: "f".repeat(64),
                  clientCertDigest: "1".repeat(64),
                },
              },
            }
          : {}),
      },
    },
    token,
  );
  if (!active) {
    await command(
      "sandboxes.finish",
      {
        id: sandbox.id,
        leaseToken: run.data.leaseToken,
        name: sandbox.data.name,
        backendDigest: sandbox.data.backendDigest,
        stopped: false,
        observation: "合成浏览器回执：Task 删除结果不明",
      },
      token,
    );
    await command(
      "tasks.complete",
      {
        id: run.id,
        leaseToken: run.data.leaseToken,
        exitCode: 1,
        result: { blocked: true },
      },
      token,
    );
    // Replacing the plan must not hide an unfinished sandbox from this goal.
    const next = await command("plans.propose", {
      goalId: goal.id,
      summary: "新方案仍需核对原客体",
      tasks: [
        {
          id: "next",
          title: "继续分析",
          kind: "analysis",
          role: "developer",
          runtime: "linux",
        },
      ],
    });
    await command("plans.approve", { id: next.id });
  }
  return { project, goal, sandbox, worker, command };
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
    .getByRole("region", { name: "AX 客体待核对", exact: true })
    .getByRole("button", { name: /^核对客体：/ })
    .click();
}

test("owner reconciles exact old-plan sandbox after node disable and explicit observation", async ({
  page,
  ownerSession,
}, info) => {
  const f = await fixture(page, ownerSession.csrf, false, true);
  await open(page, f.project.id, f.goal.id, info.project.name === "mobile");
  const dialog = page.getByRole("dialog");
  const identity = dialog.getByRole("region", {
    name: "AX 认证连接",
    exact: true,
  });
  await expect(
    identity.getByText("control.ax.example:443", { exact: true }),
  ).toBeVisible();
  await expect(
    identity.getByText("router.ax.example:443", { exact: true }),
  ).toBeVisible();
  await expect(
    identity.getByText("b".repeat(64), { exact: true }),
  ).toBeVisible();
  const identityShot = process.env.IS_QUALITY_SCREENSHOTS;
  if (identityShot)
    await page.screenshot({
      path: identityShot + "/sandbox-identity.png",
      fullPage: true,
      animations: "disabled",
    });
  await expect(
    dialog.getByText(f.sandbox.data.name, { exact: true }),
  ).toBeVisible();
  const confirm = dialog.getByRole("button", {
    name: "确认已停止",
    exact: true,
  });
  await expect(confirm).toBeDisabled();
  await dialog
    .getByRole("button", { name: "停用原执行节点", exact: true })
    .click();
  await expect(
    dialog.getByText("原 AX 节点 · 已停用", { exact: true }),
  ).toBeVisible();
  await expect(confirm).toBeDisabled();
  await dialog
    .getByLabel("核对依据", { exact: true })
    .fill(
      "浏览器合成核对：已停止原节点并撤销 AX 凭据，准确 Task、Workspace、Actor 均已停止。",
    );
  await dialog.getByRole("checkbox").check();
  const shot = process.env.IS_QUALITY_SCREENSHOTS;
  if (shot)
    await page.screenshot({
      path: shot + "/sandbox-before.png",
      fullPage: true,
      animations: "disabled",
    });
  await confirm.focus();
  await page.keyboard.press("Enter");
  await expect(
    dialog.getByText("已记录停止决定，可按剩余预算重试工作项。", {
      exact: true,
    }),
  ).toBeVisible();
  const state = await (
    await page.request.get(`/api/v1/projects/${f.project.id}/workspace`)
  ).json();
  const record = state.find((x: any) => x.id === f.sandbox.id);
  expect(record.status).toBe("stopped");
  expect(record.data.name).toBe(f.sandbox.data.name);
  expect(record.data.backendDigest).toBe(f.sandbox.data.backendDigest);
  expect(state.filter((x: any) => x.kind === "run")).toHaveLength(1);
  if (shot)
    await page.screenshot({
      path: shot + "/sandbox-after.png",
      fullPage: true,
      animations: "disabled",
    });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBeTruthy();
});

test("disabling a node cannot reconcile its still-active lease", async ({
  page,
  ownerSession,
}, info) => {
  const f = await fixture(page, ownerSession.csrf, true);
  await open(page, f.project.id, f.goal.id, info.project.name === "mobile");
  const dialog = page.getByRole("dialog");
  await expect(
    dialog.getByText("旧版 CLI 诊断连接，未固定服务认证身份。", {
      exact: true,
    }),
  ).toBeVisible();
  await dialog
    .getByRole("button", { name: "停用原执行节点", exact: true })
    .click();
  await dialog
    .getByLabel("核对依据", { exact: true })
    .fill("尚在等待原租约失效，不能提前放行。");
  await dialog.getByRole("checkbox").check();
  await expect(
    dialog.getByRole("button", { name: "确认已停止", exact: true }),
  ).toBeDisabled();
  await expect(
    dialog.getByText("租约仍有效或待核实", { exact: true }),
  ).toBeVisible();
  const r = await page.request.get("/api/v1/resources/" + f.sandbox.id);
  expect((await r.json()).status).toBe("prepared");
});
