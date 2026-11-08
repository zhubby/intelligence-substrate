import { useEffect, useRef, useState } from "react";
import { type Resource } from "./api";
import { useWorkspace } from "./context";
import { Button } from "./ui";

export function SandboxRecovery({ goalId }: { goalId: string }) {
  const w = useWorkspace();
  const pending = w
    .select("sandbox")
    .filter((s) => s.goalId === goalId && s.status !== "stopped");
  if (!pending.length) return null;
  return (
    <section className="stopped-source-note" aria-label="AX 客体待核对">
      <strong>AX 客体尚未确认停止</strong>
      <p>
        原执行结束或失联后，相关工作将等待核对。此处包含该目标所有方案的待处理客体。
      </p>
      <div className="button-group">
        {pending.map((s) => (
          <Button
            secondary
            key={s.id}
            onClick={() => w.open("核对 AX 客体", <SandboxDetails id={s.id} />)}
          >
            核对客体：{s.data.name}
          </Button>
        ))}
      </div>
    </section>
  );
}

function SandboxDetails({ id }: { id: string }) {
  const w = useWorkspace();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [observation, setObservation] = useState("");
  const [checked, setChecked] = useState(false);
  const [clock, setClock] = useState(Date.now());
  const request = useRef<{ fingerprint: string; key: string } | undefined>(
    undefined,
  );
  useEffect(() => {
    const timer = setInterval(() => setClock(Date.now()), 1000);
    return () => clearInterval(timer);
  }, []);
  const sandbox = w.items.find((x) => x.id === id) as Resource | undefined;
  const worker = w.items.find((x) => x.id === sandbox?.data.workerId);
  const run = w.items.find((x) => x.id === sandbox?.data.runId);
  if (!sandbox) return <p role="status">客体记录不可用，请关闭后刷新。</p>;
  const lease = Date.parse(run?.data.leaseUntil || "");
  const inactive =
    Boolean(run) &&
    (run?.status !== "running" || (Number.isFinite(lease) && lease < clock));
  const owner = w.actor.kind === "human" && w.can("owner");
  const ready =
    owner &&
    worker?.status === "disabled" &&
    inactive &&
    sandbox.status !== "stopped";
  const transport = sandbox.data.backend?.transport;
  const act = async (disable: boolean) => {
    setBusy(true);
    setError("");
    try {
      if (disable) await w.run("workers.disable", { id: worker!.id });
      else {
        const input = {
          id,
          name: sandbox.data.name,
          backendDigest: sandbox.data.backendDigest,
          observation: observation.trim(),
        };
        const fingerprint = JSON.stringify(input);
        if (request.current?.fingerprint !== fingerprint)
          request.current = { fingerprint, key: crypto.randomUUID() };
        await w.run("sandboxes.reconcile", {
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
        先停止或隔离原节点并撤销其 AX 访问，核实准确 Task、Workspace 及 Actor
        已停止。平台停用节点只阻止后续平台访问；确认按钮记录核对决定。
      </p>
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      {transport?.version === "mtls-v1" ? (
        <section className="source-details" aria-label="AX 认证连接">
          <p>执行节点固定的双向 TLS 连接身份</p>
          <dl>
            {(
              [
                ["control", "控制面"],
                ["guest", "客体路由"],
              ] as const
            ).map(([role, label]) => (
              <div key={role}>
                <dt>{label}</dt>
                <dd>
                  <code>{transport[role].endpoint}</code>
                  <br />
                  TLS 服务名：<code>{transport[role].serverName}</code>
                  <br />
                  服务端 SPKI：<code>{transport[role].serverSPKI}</code>
                  <br />
                  CA 摘要：<code>{transport[role].caDigest}</code>
                  <br />
                  客户端证书摘要：
                  <code>{transport[role].clientCertDigest}</code>
                </dd>
              </div>
            ))}
          </dl>
        </section>
      ) : (
        <p className="form-note">旧版 CLI 诊断连接，未固定服务认证身份。</p>
      )}
      <section className="source-details" aria-label="AX 客体信息">
        <dl>
          <div>
            <dt>原节点</dt>
            <dd>
              {worker?.title || sandbox.data.workerId} ·{" "}
              {worker?.status === "disabled" ? "已停用" : "未停用"}
            </dd>
          </div>
          <div>
            <dt>准确资源名</dt>
            <dd>
              <code>{sandbox.data.name}</code>
            </dd>
          </div>
          <div>
            <dt>后端</dt>
            <dd>
              <code>{JSON.stringify(sandbox.data.backend)}</code>
            </dd>
          </div>
          <div>
            <dt>后端摘要</dt>
            <dd>
              <code>{sandbox.data.backendDigest}</code>
            </dd>
          </div>
          <div>
            <dt>原执行</dt>
            <dd>{inactive ? "租约已失效" : "租约仍有效或待核实"}</dd>
          </div>
          <div>
            <dt>依据</dt>
            <dd>{sandbox.data.observation || "尚未收到清理回执"}</dd>
          </div>
        </dl>
      </section>
      {sandbox.status === "stopped" ? (
        <p role="status">已记录停止决定，可按剩余预算重试工作项。</p>
      ) : owner ? (
        <form
          className="recovery-form"
          onSubmit={(e) => {
            e.preventDefault();
            if (ready && checked && observation.trim() && !busy)
              void act(false);
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
              请先停用原节点，并等待原租约失效。停用前请完成节点隔离。
            </p>
          )}
          <label className="field" htmlFor="sandbox-observation">
            <span>核对依据</span>
            <textarea
              id="sandbox-observation"
              required
              maxLength={2000}
              value={observation}
              disabled={busy}
              onChange={(e) => setObservation(e.target.value)}
            />
          </label>
          <label>
            <input
              type="checkbox"
              checked={checked}
              disabled={busy}
              onChange={(e) => setChecked(e.target.checked)}
            />{" "}
            我已隔离原节点、撤销 AX 访问并核实准确客体已停止
          </label>
          <Button
            type="submit"
            disabled={!ready || !checked || !observation.trim() || busy}
          >
            {busy ? "记录中…" : "确认已停止"}
          </Button>
        </form>
      ) : (
        <p role="status">需要项目负责人核对。</p>
      )}
    </div>
  );
}
