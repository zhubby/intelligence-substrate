import { useRef, useState, type FormEvent } from "react";
import { LoaderCircle, Send } from "lucide-react";
import { APIError, date, type Resource } from "./api";
import { useWorkspace } from "./context";
import { Button } from "./ui";

const states: Record<string, string> = {
  waiting: "等待协作者回复",
  ready: "已收到回复，等待节点继续",
  resumed: "已由节点继续执行",
  expired: "等待已超时",
  cancelled: "等待已取消",
  stale: "等待已失效",
  blocked: "等待需要人工处理",
};

function Reply({ wait }: { wait: Resource }) {
  const w = useWorkspace();
  const [text, setText] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  // Freeze a submitted body/key together: an uncertain response must not turn a
  // text edit into another reply before the original outcome is known.
  const submitted = useRef<{ key: string; answer: string } | undefined>(
    undefined,
  );
  async function send(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    setError("");
    const answer = text.trim();
    if (
      !answer ||
      new TextEncoder().encode(JSON.stringify({ answer })).length > 8192
    ) {
      setError("请填写回复，内容最多 8 KiB。");
      return;
    }
    submitted.current ??= { key: crypto.randomUUID(), answer };
    setPending(true);
    try {
      await w.run("messages.send", {
        projectId: wait.projectId,
        goalId: wait.goalId,
        recipient: wait.data.requester,
        recipientTaskId: wait.data.taskId,
        type: "result",
        key: submitted.current.key,
        causationId: wait.data.requestMessageId,
        payload: { answer: submitted.current.answer },
      });
    } catch (e) {
      if (e instanceof APIError && [400, 413, 422].includes(e.status))
        submitted.current = undefined;
      setError((e as Error).message);
    } finally {
      setPending(false);
    }
  }
  return (
    <form className="form message-wait-reply" onSubmit={send}>
      <label className="field">
        <span>回复内容</span>
        <textarea
          aria-label="回复内容"
          required
          value={text}
          rows={3}
          disabled={pending || !!submitted.current}
          onChange={(e) => setText(e.target.value)}
        />
        <small>回复用于协作；需求、方案和正式发布仍需分别批准。</small>
      </label>
      {error && (
        <p role="alert" className="form-error">
          {error}
        </p>
      )}
      <Button type="submit" disabled={pending}>
        {pending ? (
          <LoaderCircle className="spin" size={15} />
        ) : (
          <Send size={15} />
        )}
        {pending ? "正在发送…" : "发送协作回复"}
      </Button>
    </form>
  );
}

export function MessageWait({ taskId }: { taskId: string }) {
  const w = useWorkspace();
  const task = w.items.find(
    (item) => item.kind === "task" && item.id === taskId,
  );
  const waits = w
    .select("message_wait")
    .filter((item) => item.data.taskId === taskId)
    .sort((a, b) => a.createdAt.localeCompare(b.createdAt));
  if (!waits.length) return null;
  const name = (id: string) =>
    w.items.find(
      (x) =>
        "worker:" + x.id === id ||
        (x.kind === "member" && x.data.identityId === id),
    )?.title || id;
  return (
    <section className="source-details message-wait" aria-label="协作等待">
      <h4>协作等待 · {waits.length} / 3</h4>
      <ol className="message-context-list">
        {waits.map((wait) => {
          const checkpoint = w.items.find(
            (item) =>
              item.kind === "workspace_checkpoint" &&
              item.id === wait.data.checkpointId,
          );
          const integration = checkpoint?.data.integration;
          const request = w.items.find(
            (item) =>
              item.id === wait.data.requestMessageId && item.kind === "message",
          );
          const live =
            wait.status === "waiting" && task?.status === "waiting_message";
          const canReply =
            live &&
            w.actor.kind === "human" &&
            w.actor.id === wait.data.recipient;
          return (
            <li key={wait.id}>
              <strong>{states[wait.status] || wait.status}</strong>
              {live && (
                <p className="muted">
                  {wait.data.checkpointId
                    ? "执行已挂起，已释放节点；回复后由可用节点恢复工作区并继续执行。"
                    : "执行已挂起，已释放节点；回复后由可用节点继续分析。"}
                </p>
              )}
              <dl>
                <div>
                  <dt>指定协作者</dt>
                  <dd>{name(wait.data.recipient)}</dd>
                </div>
                <div>
                  <dt>需求基线</dt>
                  <dd>v{wait.data.requirementRevision}</dd>
                </div>
                <div>
                  <dt>等待截止</dt>
                  <dd>{date(wait.data.expiresAt)}</dd>
                </div>
                <div>
                  <dt>等待原因</dt>
                  <dd>{wait.data.reason}</dd>
                </div>
                <div>
                  <dt>工作摘要</dt>
                  <dd>{wait.data.summary}</dd>
                </div>
                <div>
                  <dt>原执行</dt>
                  <dd>
                    <code>{wait.data.runId}</code>
                  </dd>
                </div>
                <div>
                  <dt>原请求</dt>
                  <dd>
                    <code>{wait.data.requestMessageId}</code>
                  </dd>
                </div>
                {wait.data.replyMessageId && (
                  <div>
                    <dt>唤醒回复</dt>
                    <dd>
                      <code>{wait.data.replyMessageId}</code>
                    </dd>
                  </div>
                )}
                {wait.data.resumedRunId && (
                  <div>
                    <dt>继续执行</dt>
                    <dd>
                      <code>{wait.data.resumedRunId}</code>
                    </dd>
                  </div>
                )}
              </dl>
              {checkpoint && (
                <div className="workspace-checkpoint" aria-label="保存的工作区">
                  <strong>
                    工作区已保存 · {checkpoint.data.fileCount} 个文件
                  </strong>
                  <dl>
                    <div>
                      <dt>源码提交</dt>
                      <dd>
                        <code>
                          {checkpoint.data.source?.commit || "无原始源码"}
                        </code>
                      </dd>
                    </div>
                    <div>
                      <dt>工作区摘要</dt>
                      <dd>
                        <code>{checkpoint.data.digest}</code>
                      </dd>
                    </div>
                    {integration && (
                      <div>
                        <dt>集成冲突决定</dt>
                        <dd>
                          已记录{" "}
                          {Object.keys(integration.resolutions || {}).length} /{" "}
                          {integration.conflicts?.length || 0}{" "}
                          项，完成时重新核对
                        </dd>
                      </div>
                    )}
                  </dl>
                  <p className="muted">
                    工作区保存不代表源码已合入或质量已通过。
                  </p>
                </div>
              )}
              {wait.data.checkpointId && !checkpoint && (
                <p className="blocked-reason">工作区记录缺失，需要核对。</p>
              )}
              {request && (
                <pre className="code-block message-context-payload">
                  {JSON.stringify(request.data.payload, null, 2)}
                </pre>
              )}
              {wait.data.closeReason && (
                <p className="blocked-reason">{wait.data.closeReason}</p>
              )}
              {canReply ? (
                <Reply key={wait.id} wait={wait} />
              ) : (
                live && <p className="muted">仅指定接收人可回复此请求。</p>
              )}
            </li>
          );
        })}
      </ol>
    </section>
  );
}
