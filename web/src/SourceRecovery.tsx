import { useEffect, useRef, useState } from "react";
import { api, states, type Data, type Resource } from "./api";
import { useWorkspace } from "./context";
import { Badge, Button } from "./ui";

export function SourceRecoveryActions({ task }: { task: Resource }) {
  const w = useWorkspace();
  const latest = w
    .select("run")
    .filter((run) => run.data.taskId === task.id)
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt))[0];
  const local = latest?.data.result?.stoppedSource;
  const node = w.items.find((item) => item.id === latest?.data.workerId);
  const operations = w.select("source_commit").filter((op) => {
    const run = w.items.find((item) => item.id === op.data.runId);
    return (
      run?.data.taskId === task.id &&
      (op.data.sourceRecoveryId || run.status !== "succeeded")
    );
  });
  const recorded = operations.some((op) => op.data.runId === latest?.id);
  const localOnly = local?.status === "local_only" && !recorded;
  const notSaved = Boolean(latest?.data.result?.sourcePreservationError) && !recorded;
  if (!operations.length && !localOnly && !notSaved) return null;
  return (
    <>
      {(localOnly || notSaved) && (
        <section
          role="status"
          aria-label="中断源码保存"
          className="stopped-source-note"
        >
          <strong>
            {localOnly
              ? "执行节点报告源码已保存在本地，尚未同步到平台"
              : "执行节点报告未能保存中断源码"}
          </strong>
          <p>节点：{node?.title || latest?.data.workerId}</p>
          {localOnly ? (
            <>
              <p>请由执行节点管理员核对并取回；继续交付仍需独立验证。</p>
              <code aria-label="本地源码摘要">{local.digest}</code>
            </>
          ) : (
            <p>请检查原执行日志，核实可恢复的源码。</p>
          )}
        </section>
      )}
      {operations.length > 0 && (
        <div className="source-recovery-actions">
          {operations.map((op) => (
            <Button
              key={op.id}
              secondary
              aria-label={"核对源码：" + task.title}
              onClick={() =>
                w.open("核对中断源码", <SourceRecovery operationId={op.id} />)
              }
            >
              {op.data.sourceRecoveryId ? "查看源码决定" : "核对源码"}
              {operations.length > 1 ? " · " + op.data.commit.slice(0, 8) : ""}
            </Button>
          ))}
        </div>
      )}
    </>
  );
}

function SourceRecovery({ operationId }: { operationId: string }) {
  const w = useWorkspace();
  const [view, setView] = useState<Data>();
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [reason, setReason] = useState("");
  // A lost response can be retried exactly. A refreshed observation or changed
  // decision gets a new key, rather than mutating an already bound request.
  const request = useRef<{ fingerprint: string; key: string } | undefined>(
    undefined,
  );
  const endpoint = "source-operations/" + encodeURIComponent(operationId);
  useEffect(() => {
    let live = true;
    api<Data>(endpoint)
      .then((value) => {
        if (live) setView(value);
      })
      .catch((e) => {
        if (live) setError(e.message);
      })
      .finally(() => {
        if (live) setLoading(false);
      });
    return () => {
      live = false;
    };
  }, [endpoint]);
  const refresh = async () => {
    setLoading(true);
    setError("");
    try {
      setView(await api<Data>(endpoint));
    } catch (e) {
      setView(undefined);
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  };
  const decide = async (decision: "confirm" | "abandon") => {
    if (!view || busy || loading || !reason.trim()) return;
    const input = {
      id: operationId,
      commit: view.operation.data.commit,
      observedHead: view.observedHead,
      decision,
      reason: reason.trim(),
    };
    const fingerprint = JSON.stringify(input);
    if (request.current?.fingerprint !== fingerprint)
      request.current = { fingerprint, key: crypto.randomUUID() };
    setBusy(true);
    setError("");
    try {
      await w.run("sources.reconcile", { ...input, key: request.current.key });
      setView(await api<Data>(endpoint));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  const op = view?.operation as Resource | undefined;
  const history = view?.recovery as Resource | undefined;
  return (
    <div className="detail-content source-recovery">
      <p id="source-recovery-scope" className="muted">
        确认已有源码后继续研发，构建、独立测试与发布按各自流程执行。
      </p>
      <Button secondary disabled={loading || busy} onClick={refresh}>
        刷新核对状态
      </Button>
      {loading && <p role="status">正在读取候选与当前分支…</p>}
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      {op && (
        <>
          <section className="source-details" aria-label="源码核对信息">
            <dl>
              <div>
                <dt>候选提交</dt>
                <dd>
                  <code>{op.data.commit}</code>
                </dd>
              </div>
              <div>
                <dt>工作分支</dt>
                <dd>{op.data.branch}</dd>
              </div>
              <div>
                <dt>当前提交</dt>
                <dd>
                  <code>{view?.observedHead || "分支不存在"}</code>
                </dd>
              </div>
              <div>
                <dt>原生产者</dt>
                <dd>
                  <code>{op.data.authorId}</code>
                </dd>
              </div>
              {op.data.runId && (
                <div>
                  <dt>原执行</dt>
                  <dd>{states[view?.runStatus] || view?.runStatus}</dd>
                </div>
              )}
              {view?.workerStatus && (
                <div>
                  <dt>原节点</dt>
                  <dd>{states[view.workerStatus] || view.workerStatus}</dd>
                </div>
              )}
            </dl>
            <a
              className="text-button"
              href={`/api/v1/repositories/${op.data.repositoryId}/archive?commit=${op.data.commit}`}
              download
            >
              下载候选源码
            </a>
          </section>
          {history ? (
            <section aria-label="负责人决定">
              <h4>已记录负责人决定</h4>
              <Badge status={history.status} />
              <p>{history.data.reason}</p>
              <p className="muted">核对人：{history.data.actorId}</p>
            </section>
          ) : (
            <form
              className="recovery-form"
              aria-describedby="source-recovery-scope"
              onSubmit={(event) => {
                event.preventDefault();
                void decide("confirm");
              }}
            >
              <p className="muted">{view?.reason}</p>
              {(view?.canConfirm || view?.canAbandon) && (
                <>
                  <label className="field" htmlFor="source-recovery-reason">
                    <span>核对原因</span>
                    <textarea
                      id="source-recovery-reason"
                      required
                      maxLength={2000}
                      value={reason}
                      onChange={(event) => setReason(event.target.value)}
                      disabled={busy}
                    />
                  </label>
                  <div className="button-group">
                    <Button
                      type="submit"
                      disabled={
                        !view?.canConfirm || !reason.trim() || busy || loading
                      }
                    >
                      {busy ? "记录中…" : "采用并继续"}
                    </Button>
                    <Button
                      type="button"
                      secondary
                      disabled={
                        !view?.canAbandon || !reason.trim() || busy || loading
                      }
                      onClick={() => void decide("abandon")}
                    >
                      停止采用
                    </Button>
                  </div>
                </>
              )}
            </form>
          )}
        </>
      )}
    </div>
  );
}
