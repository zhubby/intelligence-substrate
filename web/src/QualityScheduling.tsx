import { Plus, ShieldCheck } from "lucide-react";
import { date, money, targets, type Data, type Resource } from "./api";
import { useWorkspace } from "./context";
import { Badge, Button, Form, SectionHeading, type Field } from "./ui";

export function QualityBindingButton({ pipeline }: { pipeline: Resource }) {
  const w = useWorkspace();
  const goal = w.goal;
  const enabled =
    w.actor.kind === "human" &&
    w.can("quality") &&
    goal?.data.approvedRevision === goal?.revision &&
    !!goal?.data.activePlanId;
  function bind() {
    if (!goal) return;
    const captured = goal;
    const key = crypto.randomUUID();
    const criteria: Data[] = captured.data.criteria.filter(
      (c: Data) => c.method === pipeline.data.method,
    );
    const fields: Field[] = [
      {
        name: "target",
        label: "交付目标",
        type: "select",
        options: Object.fromEntries(
          captured.data.targets.map((id: string) => [id, targets[id]]),
        ),
        defaultValue: captured.data.targets[0],
        required: true,
      },
      {
        name: "budget",
        label: "单次验证预算上限（USD）",
        type: "number",
        defaultValue: 1,
        required: true,
        hint: "领取时预留执行预算；实际基础设施费用仍需核对。",
      },
      ...criteria.map(
        (c): Field => ({
          name: "criterion_" + c.id,
          label: c.text,
          type: "checkbox",
          defaultValue: true,
        }),
      ),
    ];
    w.open(
      "绑定自动质量验证",
      <Form
        fields={fields}
        submit="确认自动绑定"
        onSubmit={async (values) => {
          try {
            await w.run("quality.bind", {
              goalId: captured.id,
              expectedRequirementRevision: captured.revision,
              expectedPlanId: captured.data.activePlanId,
              pipelineId: pipeline.id,
              target: values.target,
              criteria: criteria
                .filter((c) => values["criterion_" + c.id])
                .map((c) => c.id),
              budgetCents: Math.round(Number(values.budget) * 100),
              key,
            });
            w.close();
          } finally {
            await w.refresh();
          }
        }}
      >
        <div className="confirmation-copy">
          <p>
            {captured.title} · 需求 v{captured.revision}
          </p>
          <p>
            {pipeline.title}：独立质量节点将自动核验当前批准方案的成功构建制品。
          </p>
          <p>
            方案或需求变化后需重新绑定。仅检查目标环境时可以不勾选业务标准。
          </p>
        </div>
      </Form>,
    );
  }
  return (
    <Button secondary disabled={!enabled} onClick={bind}>
      <Plus size={14} />
      自动绑定
    </Button>
  );
}

export function QualityScheduling() {
  const w = useWorkspace();
  const goal = w.goal;
  if (!goal) return null;
  const policies = w
    .select("quality_policy")
    .filter((r) => r.goalId === goal.id)
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  const dispatches = w
    .select("quality_dispatch")
    .filter((r) => r.goalId === goal.id)
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  function revoke(policy: Resource) {
    const key = crypto.randomUUID();
    w.open(
      "撤销自动验证绑定",
      <Form
        fields={[
          {
            name: "reason",
            label: "撤销原因",
            type: "textarea",
            required: true,
          },
        ]}
        submit="确认撤销"
        onSubmit={async (values) => {
          await w.run("quality.revoke", {
            id: policy.id,
            reason: values.reason,
            key,
          });
          w.close();
        }}
      >
        <div className="confirmation-copy">
          <p>{policy.title}</p>
          <p>
            撤销将取消尚未结束的关联验证；现有发布候选需要重新创建与批准。历史验证记录保留。
          </p>
        </div>
      </Form>,
    );
  }
  return (
    <section aria-label="自动质量验证">
      <SectionHeading
        title="自动质量验证"
        description="在下方验证策略中选择“自动绑定”。成功构建后，由独立质量节点自动领取；失败进入受控修复与复测。"
      />
      {!policies.length && (
        <div className="inline-empty">当前目标尚未绑定自动验证策略</div>
      )}
      {policies.map((policy) => (
        <div className="resource-row member-row" key={policy.id}>
          <ShieldCheck size={17} />
          <div className="member-identity">
            <strong>{policy.title}</strong>
            <small>
              {targets[policy.data.target]} · 需求 v
              {policy.data.requirementRevision} ·{" "}
              {policy.data.criteria.length
                ? `${policy.data.criteria.length} 项业务标准`
                : "目标环境验证"}
              {" · "}单次上限 {money(policy.data.budgetCents)}
            </small>
            <small>
              {policy.data.runtime} · {policy.data.verificationEnvironment}
            </small>
            <small>
              {policy.status === "active"
                ? dispatches.some((r) => r.data.policyId === policy.id)
                  ? "自动验证已启用"
                  : "已绑定，等待独立质量节点轮询"
                : policy.status === "revoked"
                  ? "已撤销"
                  : "需求或方案已变化，需要重新绑定"}
            </small>
          </div>
          <Badge status={policy.status} />
          {policy.status === "active" && (
            <Button
              secondary
              disabled={w.actor.kind !== "human" || !w.can("quality")}
              onClick={() => revoke(policy)}
            >
              撤销绑定
            </Button>
          )}
        </div>
      ))}
      {dispatches.length > 0 && (
        <div className="section-line">
          <h3>
            自动验证调度 <span>{dispatches.length}</span>
          </h3>
        </div>
      )}
      {dispatches.map((dispatch) => {
        const task = w.items.find((r) => r.id === dispatch.data.taskId);
        const status = task?.status || "blocked";
        const label =
          status === "succeeded"
            ? "验证通过"
            : status === "running"
              ? "验证中"
              : status === "pending"
                ? "等待质量节点执行"
                : status === "failed"
                  ? "验证失败"
                  : status === "cancelled"
                    ? "验证已取消"
                    : "验证阻塞";
        const runs = w
          .select("run")
          .filter((r) => r.data.taskId === task?.id)
          .sort((a, b) => b.createdAt.localeCompare(a.createdAt));
        return (
          <div className="resource-row member-row" key={dispatch.id}>
            <ShieldCheck size={17} />
            <div className="member-identity">
              <strong>{label}</strong>
              <small>
                {dispatch.title} · {targets[dispatch.data.target]}
              </small>
              <small>
                {date(dispatch.createdAt)} · 提交{" "}
                {dispatch.data.commit.slice(0, 8)}
              </small>
              {(task?.data.reason || dispatch.data.reason) && (
                <small className="blocked-reason">
                  {task?.data.reason || dispatch.data.reason}
                </small>
              )}
            </div>
            <Badge status={status} />
            <Button
              secondary
              onClick={() =>
                w.open(
                  "自动验证记录",
                  <div className="detail-content">
                    <p>
                      {dispatch.title} · 需求 v
                      {dispatch.data.requirementRevision}
                    </p>
                    <p>
                      {label} · {targets[dispatch.data.target]}
                    </p>
                    <p className="mono">
                      制品摘要 {dispatch.data.artifactDigest}
                    </p>
                    <pre className="run-log">
                      {runs[0]?.data.log || "暂无执行日志"}
                    </pre>
                    {runs[0]?.data.result?.reportArtifactId && (
                      <a
                        className="text-button"
                        href={
                          "/api/v1/artifacts/" +
                          runs[0].data.result.reportArtifactId +
                          "/content"
                        }
                      >
                        下载验证报告
                      </a>
                    )}
                    <details>
                      <summary>追溯信息</summary>
                      <p className="mono">{dispatch.id}</p>
                      <p className="mono">{dispatch.data.policyId}</p>
                      <p className="mono">{dispatch.data.artifactId}</p>
                      <p className="mono">{dispatch.data.buildRunId}</p>
                    </details>
                  </div>,
                )
              }
            >
              查看验证记录
            </Button>
          </div>
        );
      })}
    </section>
  );
}
