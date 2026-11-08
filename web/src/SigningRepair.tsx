import { useWorkspace } from "./context";
import { targets, type Data } from "./api";
import { Badge } from "./ui";

export function SigningRepair({ defectId }: { defectId: string }) {
  const w = useWorkspace();
  const defect = w.select("defect").find((x) => x.id === defectId);
  const records: Data[] =
    defect?.data.signingRepairs || defect?.data.androidSigningRepairs || [];
  const record = records.at(-1);
  if (!defect || !record) return null;
  const continuation = record.continuation || {};
  const tasks = w.select("task");
  const stages = [
    ["源码修复", record.repairTaskId],
    ["无签名重建", continuation.unsignedTaskId],
    ["受控签名", continuation.signingTaskId],
    ["签后独立复验", continuation.verificationTaskId],
  ].map(([label, id]) => ({ label, id, task: tasks.find((x) => x.id === id) }));
  const current = stages.find((stage) => stage.task?.status !== "succeeded");
  const signing = tasks.find((x) => x.id === record.rootSigningTaskId);
  const target = record.target || signing?.data.target;
  const phase =
    defect.status === "closed"
      ? "独立复验通过，缺陷已关闭"
      : current?.task?.status === "awaiting_signature"
        ? "等待 Apple 公证结果"
        : current?.label || "等待独立复验结论";
  return (
    <section className="source-details" aria-label="签名包修复">
      <h4>签名包修复 · {targets[target] || target}</h4>
      <p>第 {record.round} 轮 / 最多 2 轮</p>
      <p role="status">{phase}</p>
      {current?.task?.data.reason && (
        <p className="blocked-reason">{current.task.data.reason}</p>
      )}
      <dl className="source-inputs">
        {stages
          .filter((stage) => stage.id)
          .map((stage) => (
            <div key={stage.id}>
              <dt>{stage.label}</dt>
              <dd>
                {stage.task ? (
                  <Badge status={stage.task.status} />
                ) : (
                  "工作项暂不可用"
                )}
                <br />
                <code>{stage.id}</code>
              </dd>
            </div>
          ))}
        <div>
          <dt>失败制品</dt>
          <dd>
            <code>{record.failedArtifactId}</code>
          </dd>
        </div>
        {continuation.commit && (
          <div>
            <dt>修复提交</dt>
            <dd>
              <code>{continuation.commit}</code>
            </dd>
          </div>
        )}
      </dl>
      <p>沿用批准的签名身份和质量策略。每轮创建新签名操作，原失败记录保留。</p>
      <p className="muted">正式交付仍需目标环境验收和发布批准。</p>
    </section>
  );
}
