import { test, expect } from "./fixtures";

// A real, portable one-file source archive; no test-only server mutation API.
function snapshot() {
  const header = Buffer.alloc(512);
  const octal = (offset: number, size: number, value: number) =>
    header.write(
      value.toString(8).padStart(size - 1, "0") + "\0",
      offset,
      size,
      "ascii",
    );
  header.write("app.txt");
  octal(100, 8, 0o644);
  octal(108, 8, 0);
  octal(116, 8, 0);
  octal(124, 12, 10);
  octal(136, 12, 0);
  header.fill(32, 148, 156);
  header.write("0", 156);
  header.write("ustar\0", 257);
  header.write("00", 263);
  const checksum = header.reduce((total, value) => total + value, 0);
  header.write(checksum.toString(8).padStart(6, "0") + "\0 ", 148, 8, "ascii");
  const body = Buffer.alloc(512);
  body.write("candidate\n");
  return Buffer.concat([header, body, Buffer.alloc(1024)]);
}

for (const decision of ["confirm", "abandon"] as const) {
  test(`source recovery ${decision} preserves candidate and restores governed workflow`, async ({
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
      const value = await response.json();
      expect(response.ok(), JSON.stringify(value)).toBeTruthy();
      return value;
    };
    const project = await command("projects.create", {
      title:
        "源码接管 " + decision + " " + info.project.name + " " + Date.now(),
    });
    const repo = await command("repositories.create", {
      projectId: project.id,
      title: "recovery",
    });
    const goal = await command("goals.create", {
      projectId: project.id,
      title: "中断源码恢复",
      outcome: "采用后继续独立验证",
      targets: ["web"],
      budget: 100,
      criteria: [{ id: "works", text: "完整业务流程可用" }],
    });
    await command("goals.approve", { id: goal.id });
    const plan = await command("plans.propose", {
      goalId: goal.id,
      summary: "源码中断后需核对",
      tasks: [
        {
          id: "code",
          title: "中断代码",
          kind: "code",
          role: "developer",
          runtime: "linux",
          repositoryId: repo.id,
          branch: "feature",
          budgetCents: 10,
        },
        {
          id: "build",
          title: "后续独立构建",
          kind: "build",
          role: "developer",
          runtime: "linux",
          repositoryId: repo.id,
          dependsOn: ["code"],
          budgetCents: 10,
        },
      ],
    });
    await command("plans.approve", { id: plan.id });
    const worker = await command("workers.register", {
      projectId: project.id,
      title: "中断源码节点",
      roles: ["developer"],
      capabilities: ["code", "build"],
      runtime: "linux",
    });
    const issued = await page.request.post("/api/v1/tokens", {
      data: { projectId: project.id, workerId: worker.id },
      headers: { Origin: origin, "X-CSRF-Token": session.csrf },
    });
    const credential = await issued.json();
    const client = await playwright.request.newContext({
      baseURL: origin,
      extraHTTPHeaders: { Authorization: "Bearer " + credential.token },
    });
    try {
      const work = async (name: string, data: unknown) => {
        const response = await client.post("/api/v1/commands/" + name, {
          data,
        });
        const result = await response.json();
        expect(response.ok(), JSON.stringify(result)).toBeTruthy();
        return result;
      };
      const run = await work("tasks.claim", {});
      const binding = run.data.task.sourceBinding;
      const upload = await client.post(
        `/api/v1/repositories/${repo.id}/snapshot`,
        {
          data: snapshot(),
          headers: {
            "Content-Type": "application/x-tar",
            "X-IS-Source": Buffer.from(
              JSON.stringify({
                key: run.id,
                runId: run.id,
                leaseToken: run.data.leaseToken,
                branch: binding.targetBranch,
                baseCommit: binding.commit,
                expectedHead: binding.expectedHead,
                message: "Interrupted real source",
              }),
            ).toString("base64"),
          },
        },
      );
      const op = await upload.json();
      expect(upload.ok(), JSON.stringify(op)).toBeTruthy();
      await work("tasks.complete", {
        id: run.id,
        leaseToken: run.data.leaseToken,
        exitCode: 17,
        result: {
          reason: "执行中断，源码已保存",
          // The actual platform accepted the archive; the worker can still
          // report local_only after losing its acknowledgement.
          ...(decision === "confirm"
            ? {
                stoppedSource: {
                  status: "local_only",
                  incomplete: true,
                  digest: op.data.digest,
                  synchronizationError: "上传回执未确认",
                },
              }
            : {}),
        },
        log: "original exit: 17",
      });
      await page.goto("/");
      if (info.project.name === "mobile")
        await page
          .getByRole("button", { name: "打开导航", exact: true })
          .click();
      await page.getByLabel("选择项目").selectOption(project.id);
      if (info.project.name === "mobile")
        await page
          .getByRole("button", { name: "关闭导航", exact: true })
          .click();
      await page.getByRole("tab", { name: "执行记录", exact: true }).click();
      const open = page.getByRole("button", {
        name: "核对源码：中断代码",
        exact: true,
      });
      await open.focus();
      await expect(page.getByLabel("中断源码保存")).toHaveCount(0);
      await page.keyboard.press("Enter");
      const dialog = page.getByRole("dialog");
      await expect(
        dialog.getByRole("heading", { name: "核对中断源码", exact: true }),
      ).toBeVisible();
      await expect(
        dialog.getByText(op.data.commit, { exact: true }).first(),
      ).toBeVisible();
      await expect(
        dialog.getByText("worker:" + worker.id, { exact: true }),
      ).toBeVisible();
      await page.keyboard.press("Escape");
      await expect(open).toBeFocused();
      await open.click();
      const reason = dialog.getByLabel("核对原因");
      await reason.fill("已检查候选内容与来源，继续受控研发流程");
      if (decision === "confirm") {
        const advanced = await command("repositories.commit", {
          id: repo.id,
          branch: "feature",
          baseCommit: op.data.commit,
          expectedHead: op.data.commit,
          message: "Later branch change",
          files: { "later.txt": "later" },
        });
        await dialog
          .getByRole("button", { name: "采用并继续", exact: true })
          .click();
        await expect(dialog.getByRole("alert")).toContainText("目标分支已变化");
        await expect(reason).toHaveValue(
          "已检查候选内容与来源，继续受控研发流程",
        );
        await dialog
          .getByRole("button", { name: "刷新核对状态", exact: true })
          .click();
        await expect(
          dialog.getByText(advanced.data.lastCommit, { exact: true }),
        ).toBeVisible();
      }
      await dialog
        .getByRole("button", {
          name: decision === "confirm" ? "采用并继续" : "停止采用",
          exact: true,
        })
        .click();
      await expect(
        dialog.getByText("已记录负责人决定", { exact: true }),
      ).toBeVisible();
      await expect(
        dialog.getByText("已检查候选内容与来源，继续受控研发流程", {
          exact: true,
        }),
      ).toBeVisible();
      expect(
        await dialog.evaluate((el) => el.scrollWidth <= el.clientWidth),
      ).toBeTruthy();
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= window.innerWidth,
        ),
      ).toBeTruthy();
      await page.screenshot({
        path: `../work/screenshots/${info.project.name}-source-recovery-${decision}.png`,
        fullPage: true,
      });
      await page.keyboard.press("Escape");
      const next = await work("tasks.claim", {});
      if (decision === "confirm") {
        expect(next.data.task.logicalId).toBe("build");
        expect(next.data.task.commit).toBe(op.data.commit);
      } else expect(next.kind).toBe("worker");
    } finally {
      await client.dispose();
    }
  });
}
