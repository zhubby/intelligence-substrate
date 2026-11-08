import { useEffect, useRef, useState } from "react";
import { type Resource } from "./api";
import { useWorkspace } from "./context";
import { Button } from "./ui";

export function AgentProgressRecovery({ goalId }: { goalId: string }) {
  const w = useWorkspace();
  const pending = w
    .select("agent_step")
    .filter((s) => s.goalId === goalId && s.status === "prepared");
  if (!pending.length) return null;
  return (
    <section className="stopped-source-note" aria-label="Agent 操作待核对">
      <strong>有待确认的 Agent 操作</strong>
      <p>
        执行中断时，先核对原进程与外部副作用，再使用最后确认的进度继续。历史方案的未明操作也会保留。
      </p>
      <div className="button-group">
        {pending.map((step) => (
          <Button
            secondary
            key={step.id}
            onClick={() =>
              w.open("核对 Agent 操作", <AgentStepDetails id={step.id} />)
            }
          >
            核对操作：{step.title} · 步骤 {step.data.step}
          </Button>
        ))}
      </div>
    </section>
  );
}

export function AgentProgressStatus({ task }: { task: Resource }) {
  const w = useWorkspace();
  const step = w.items.find(
    (s) =>
      s.id === task.data.agentProgressId &&
      s.kind === "agent_step" &&
      s.status === "completed",
  );
  if (!step) return null;
  return (
    <div className="button-group">
      <small>已保存步骤 {step.data.step}</small>
      <Button
        secondary
        onClick={() =>
          w.open("已确认的 Agent 进度", <AgentStepDetails id={step.id} />)
        }
      >
        查看已确认进度
      </Button>
    </div>
  );
}

function AgentStepDetails({ id }: { id: string }) {
  const w = useWorkspace();
  const [reason, setReason] = useState("");
  const [checked, setChecked] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [clock, setClock] = useState(Date.now());
  const request = useRef<{ fingerprint: string; key: string } | undefined>(
    undefined,
  );
  useEffect(() => {
    const timer = setInterval(() => setClock(Date.now()), 1000);
    return () => clearInterval(timer);
  }, []);
  const step = w.items.find((s) => s.id === id);
  const worker = w.items.find((s) => s.id === step?.data.workerId);
  const run = w.items.find((s) => s.id === step?.data.runId);
  const checkpoint = w.items.find((s) => s.id === step?.data.checkpointId);
  if (!step) return <p role="status">操作记录不可用，请关闭后刷新。</p>;
  const lease = Date.parse(run?.data.leaseUntil || "");
  const inactive =
    Boolean(run) &&
    (run?.status !== "running" || (Number.isFinite(lease) && lease < clock));
  const owner = w.actor.kind === "human" && w.can("owner");
  const ready =
    owner &&
    worker?.status === "disabled" &&
    inactive &&
    step.status === "prepared";
  const act = async (disable: boolean) => {
    setBusy(true);
    setError("");
    try {
      if (disable) await w.run("workers.disable", { id: worker!.id });
      else {
        const input = {
          id,
          actionDigest: step.data.actionDigest,
          externalReconciled: true,
          reason: reason.trim(),
        };
        const fingerprint = JSON.stringify(input);
        if (request.current?.fingerprint !== fingerprint)
          request.current = { fingerprint, key: crypto.randomUUID() };
        await w.run("agent_steps.reconcile", {
          ...input,
          key: request.current.key,
        });
      }
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="detail-content source-recovery">
      <p className="form-note">
        已确认的文件和工具观察可用于继续工作，仍须独立测试与验收。核对未明操作时，请先独立停止原进程，核查外部副作用，并确认放弃此次尚未保存的文件改动。
      </p>
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      <section className="source-details" aria-label="Agent 操作信息">
        <dl>
          <div>
            <dt>原节点</dt>
            <dd>
              {worker?.title || step.data.workerId} ·{" "}
              {worker?.status === "disabled" ? "已停用" : "未停用"}
            </dd>
          </div>
          <div>
            <dt>原执行</dt>
            <dd>{inactive ? "原租约已失效" : "原租约仍有效或待核实"}</dd>
          </div>
          <div>
            <dt>步骤</dt>
            <dd>
              {step.data.step} ·{" "}
              {step.status === "completed"
                ? "已确认"
                : step.status === "abandoned"
                  ? "已核对并放弃"
                  : "结果待确认"}
            </dd>
          </div>
          <div>
            <dt>操作</dt>
            <dd>
              <code>{JSON.stringify(step.data.action)}</code>
            </dd>
          </div>
          <div>
            <dt>操作摘要</dt>
            <dd>
              <code>{step.data.actionDigest}</code>
            </dd>
          </div>
          {checkpoint && (
            <div>
              <dt>工作区</dt>
              <dd>
                {checkpoint.data.fileCount} 个文件 ·{" "}
                <code>{checkpoint.data.digest}</code>
              </dd>
            </div>
          )}
          {step.data.observation && (
            <div>
              <dt>已确认的工具观察</dt>
              <dd style={{ whiteSpace: "pre-wrap" }}>
                {step.data.observation}
              </dd>
            </div>
          )}
          {step.data.reason && (
            <div>
              <dt>核对依据</dt>
              <dd>{step.data.reason}</dd>
            </div>
          )}
        </dl>
      </section>
      {step.status === "abandoned" ? (
        <p role="status">
          已记录核对决定。重试将使用最后确认的进度与剩余预算。
        </p>
      ) : step.status === "completed" ? (
        <p role="status">
          已保存确认的进度。替换执行会重新检查需求、源码和剩余预算。
        </p>
      ) : owner ? (
        <form
          className="recovery-form"
          onSubmit={(e) => {
            e.preventDefault();
            if (ready && checked && reason.trim() && !busy) void act(false);
          }}
        >
          {worker && worker.status !== "disabled" && (
            <Button
              type="button"
              secondary
              disabled={busy}
              onClick={() => void act(true)}
            >
              停用原执行节点
            </Button>
          )}
          {!ready && (
            <p role="status">
              请先独立停止原进程、停用原节点，并等待原租约失效。
            </p>
          )}
          <label className="field" htmlFor="agent-reconcile-reason">
            <span>核对依据</span>
            <textarea
              id="agent-reconcile-reason"
              required
              maxLength={2000}
              value={reason}
              disabled={busy}
              onChange={(e) => setReason(e.target.value)}
            />
          </label>
          <label>
            <input
              type="checkbox"
              checked={checked}
              disabled={busy}
              onChange={(e) => setChecked(e.target.checked)}
            />{" "}
            我已独立停止原进程、核查外部副作用，并同意放弃本次未明进度
          </label>
          <Button
            type="submit"
            disabled={!ready || !checked || !reason.trim() || busy}
          >
            {busy ? "记录中…" : "确认核对并放弃未明进度"}
          </Button>
        </form>
      ) : (
        <p role="status">需要项目负责人核对。</p>
      )}
    </div>
  );
}
