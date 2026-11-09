import { date, type Data } from "./api";
import { useWorkspace } from "./context";
import { MessageBubble } from "./Specialist";

const types: Record<string, string> = {
  request: "请求",
  progress: "进展",
  result: "回复",
  blocked: "阻塞",
  clarification: "澄清",
  review: "审查",
  cancel: "取消请求",
};

export function MessageContext({ taskId }: { taskId: string }) {
  const w = useWorkspace();
  const receipts = w
    .select("message_receipt")
    .filter((item) => item.data.taskId === taskId)
    .sort((a, b) => a.createdAt.localeCompare(b.createdAt));
  if (!receipts.length) return null;
  const senderName = (id: string) => {
    const worker = w.items.find((item) => "worker:" + item.id === id);
    const member = w.items.find(
      (item) => item.kind === "member" && item.data.identityId === id,
    );
    return worker?.title || member?.title || id;
  };
  return (
    <section className="message-context source-details" aria-label="协作上下文">
      <h4>协作上下文 · {receipts.length}</h4>
      <p className="muted">已进入工作项上下文；不表示模型已理解或执行。</p>
      <ol className="message-context-list">
        {receipts.map((receipt) => {
          const message = receipt.data.message as Data;
          return (
            <li key={receipt.id}>
              <MessageBubble>
                <div className="message-context-heading">
                  <strong>{types[message.type] || message.type}</strong>
                  <small className="muted">{date(receipt.createdAt)}</small>
                </div>
                <dl>
                  <div>
                    <dt>发送者</dt>
                    <dd>{senderName(message.sender)}</dd>
                  </div>
                  <div>
                    <dt>需求基线</dt>
                    <dd>v{receipt.data.requirementRevision}</dd>
                  </div>
                  <div>
                    <dt>原消息</dt>
                    <dd>
                      <code>{message.id}</code>
                    </dd>
                  </div>
                  <div>
                    <dt>首次接收</dt>
                    <dd>
                      <code>{receipt.data.firstRunId}</code>
                    </dd>
                  </div>
                  {message.causationId && (
                    <div>
                      <dt>回复来源</dt>
                      <dd>
                        <code>{message.causationId}</code>
                      </dd>
                    </div>
                  )}
                </dl>
                <pre className="code-block message-context-payload">
                  {JSON.stringify(message.payload, null, 2)}
                </pre>
              </MessageBubble>
            </li>
          );
        })}
      </ol>
    </section>
  );
}
