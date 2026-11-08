import { test, expect } from "./fixtures";
import type { Page } from "@playwright/test";
import { Buffer } from "node:buffer";

// Real portable tar bytes, submitted through the authenticated Worker endpoint.
// Browser checks cover the workbench; Go tests execute the actual Agent loops.
function archive() {
  const blocks: Buffer[] = [];
  for (const [name, bytes, mode] of [
    ["asset.bin", Buffer.from([0, 255, 1]), "0000644"],
    ["tool.sh", Buffer.from("#!/bin/sh\ntrue\n"), "0000755"],
  ] as const) {
    const h = Buffer.alloc(512);
    h.write(name, 0);
    h.write(mode, 100);
    h.write("0000000", 108);
    h.write("0000000", 116);
    h.write(bytes.length.toString(8).padStart(11, "0"), 124);
    h.write("00000000000", 136);
    h.fill(32, 148, 156);
    h.write("0", 156);
    h.write("ustar\0", 257);
    h.write("00", 263);
    const checksum = h.reduce((sum, n) => sum + n, 0);
    h.write(checksum.toString(8).padStart(6, "0") + "\0 ", 148);
    blocks.push(h, bytes, Buffer.alloc((512 - (bytes.length % 512)) % 512));
  }
  blocks.push(Buffer.alloc(1024));
  return Buffer.concat(blocks);
}

async function open(
  page: Page,
  project: string,
  goal: string,
  mobile: boolean,
) {
  await page.goto("/");
  if (mobile)
    await page.getByRole("button", { name: "打开导航", exact: true }).click();
  await page.getByLabel("选择项目").selectOption(project);
  await page
    .getByRole("navigation", { name: "主导航" })
    .getByRole("button", { name: /^目标与需求/ })
    .click();
  await page.getByLabel("选择交付目标").selectOption(goal);
  await page.getByRole("tab", { name: "执行记录", exact: true }).click();
  await page
    .getByRole("button", { name: "查看工作项：实现预约接口", exact: true })
    .click();
}

test("code wait shows saved workspace and keeps its exact binding after human reply", async ({
  page,
  ownerSession,
}, info) => {
  const command = async (name: string, data: unknown, token?: string) => {
    const response = await page.request.post("/api/v1/commands/" + name, {
      data,
      headers: token
        ? { Authorization: "Bearer " + token }
        : {
            Origin: "http://127.0.0.1:18082",
            "X-CSRF-Token": ownerSession.csrf,
          },
    });
    expect(response.ok(), await response.text()).toBeTruthy();
    return response.json();
  };
  const project = await command("projects.create", {
    title: "保存开发工作区 " + info.project.name,
  });
  const state = await (
    await page.request.get(`/api/v1/projects/${project.id}/workspace`)
  ).json();
  const owner = state.find((x: any) => x.kind === "member").data.identityId;
  const goal = await command("goals.create", {
    projectId: project.id,
    title: "预约接口",
    outcome: "保留实现并澄清 UTC 约定",
    targets: ["web"],
    criteria: [{ id: "utc", text: "时间使用 UTC" }],
    budget: 100,
  });
  await command("goals.approve", { id: goal.id });
  const repo = await command("repositories.create", {
    projectId: project.id,
    title: "预约应用",
  });
  const base = repo.data.head;
  const plan = await command("plans.propose", {
    goalId: goal.id,
    summary: "实现时澄清接口",
    tasks: [
      {
        id: "code",
        title: "实现预约接口",
        kind: "code",
        role: "developer",
        runtime: "linux",
        repositoryId: repo.id,
        commit: base,
        branch: "workspace-browser",
        budgetCents: 100,
      },
    ],
  });
  await command("plans.approve", { id: plan.id });
  const worker = await command("workers.register", {
    projectId: project.id,
    title: "开发节点",
    roles: ["developer"],
    capabilities: ["code"],
    runtime: "linux",
  });
  const issued = await page.request.post("/api/v1/tokens", {
    data: { projectId: project.id, workerId: worker.id },
    headers: {
      Origin: "http://127.0.0.1:18082",
      "X-CSRF-Token": ownerSession.csrf,
    },
  });
  expect(issued.ok()).toBeTruthy();
  const token = (await issued.json()).token;
  const run = await command("tasks.claim", {}, token);
  const request = await command(
    "messages.send",
    {
      projectId: project.id,
      goalId: goal.id,
      runId: run.id,
      leaseToken: run.data.leaseToken,
      recipient: owner,
      type: "review",
      key: "code-review",
      payload: { question: "已保存接口实现，请确认 UTC 约定。" },
    },
    token,
  );
  const saved = await page.request.post(`/api/v1/runs/${run.id}/checkpoint`, {
    data: archive(),
    headers: {
      Authorization: "Bearer " + token,
      "Content-Type": "application/x-tar",
      "X-IS-Checkpoint": Buffer.from(
        JSON.stringify({
          leaseToken: run.data.leaseToken,
          repositoryId: repo.id,
          commit: base,
        }),
      ).toString("base64"),
    },
  });
  expect(saved.ok(), await saved.text()).toBeTruthy();
  const checkpoint = await saved.json();
  const wait = await command(
    "tasks.wait",
    {
      id: run.id,
      leaseToken: run.data.leaseToken,
      requestMessageId: request.id,
      reason: "需要接口确认",
      summary: "已保存二进制资源和脚本；收到回复后从原始源码继续。",
      timeoutSeconds: 600,
      steps: 4,
      source: { repositoryId: repo.id, commit: base },
      checkpointId: checkpoint.id,
    },
    token,
  );
  await open(page, project.id, goal.id, info.project.name === "mobile");
  const dialog = page.getByRole("dialog");
  const region = dialog.getByRole("region", { name: "协作等待", exact: true });
  await expect(
    region.getByText("工作区已保存 · 2 个文件", { exact: true }),
  ).toBeVisible();
  await expect(
    region.getByText(
      "执行已挂起，已释放节点；回复后由可用节点恢复工作区并继续执行。",
      { exact: true },
    ),
  ).toBeVisible();
  await expect(region.getByText(base, { exact: true })).toBeVisible();
  await expect(
    region.getByText(checkpoint.data.digest, { exact: true }),
  ).toBeVisible();
  await expect(
    region.getByText("工作区保存不代表源码已合入或质量已通过。", {
      exact: true,
    }),
  ).toBeVisible();
  const screenshots = process.env.IS_QUALITY_SCREENSHOTS;
  await region.locator(".workspace-checkpoint").scrollIntoViewIfNeeded();
  if (screenshots)
    await page.screenshot({
      path: screenshots + "/workspace-wait.png",
      fullPage: true,
      animations: "disabled",
    });
  await region
    .getByLabel("回复内容", { exact: true })
    .fill("确认 UTC；独立测试仍需执行。");
  await region
    .getByRole("button", { name: "发送协作回复", exact: true })
    .focus();
  await page.keyboard.press("Enter");
  await expect(
    region.getByText("已收到回复，等待节点继续", { exact: true }),
  ).toBeVisible();
  await expect(
    region.getByText("工作区已保存 · 2 个文件", { exact: true }),
  ).toBeVisible();
  const updated = await (
    await page.request.get(`/api/v1/projects/${project.id}/workspace`)
  ).json();
  const currentWait = updated.find((x: any) => x.id === wait.id);
  expect(currentWait.data.checkpointId).toBe(checkpoint.id);
  expect(currentWait.data.checkpointDigest).toBe(checkpoint.data.digest);
  expect(
    updated.filter((x: any) =>
      ["source_commit", "artifact", "evidence"].includes(x.kind),
    ),
  ).toHaveLength(0);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBeTruthy();
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "查看工作项：实现预约接口", exact: true }),
  ).toBeFocused();
});
