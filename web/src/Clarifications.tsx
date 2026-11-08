import { MessageSquarePlus, Sparkles } from "lucide-react";
import { date, post, type Data, type Resource } from "./api";
import { useWorkspace } from "./context";
import { Button, Confirm, Form } from "./ui";

export function Clarifications() {
  const w = useWorkspace();
  const goal = w.goal;
  if (!goal) return null;
  const draft =
    goal.status === "awaiting_baseline" &&
    goal.data.approvedRevision !== goal.revision;
  const canAsk =
    draft &&
    w.actor.kind !== "worker" &&
    w.can("product", "architect", "developer", "analysis", "design");
  const canAnswer = draft && w.actor.kind === "human" && w.can("product");
  const batches = w
    .select("clarification")
    .filter((batch) => batch.goalId === goal.id)
    .sort((a, b) => a.createdAt.localeCompare(b.createdAt));
  const current = batches.filter(
    (batch) => batch.data.requirementRevision === goal.revision,
  );
  const history = batches.filter(
    (batch) => batch.data.requirementRevision !== goal.revision,
  );
  const pending = current.reduce(
    (count, batch) =>
      count +
      batch.data.questions.filter(
        (question: Data) => question.status !== "answered",
      ).length,
    0,
  );
  const generations = w
    .select("clarification_generation")
    .filter((request) => request.goalId === goal.id)
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt));

  function ask() {
    const key = crypto.randomUUID();
    w.open(
      "提出需求澄清",
      <Form
        fields={[
          {
            name: "question",
            label: "需要确认的问题",
            type: "textarea",
            required: true,
          },
          {
            name: "context",
            label: "业务影响",
            type: "textarea",
            hint: "说明这个决定会影响哪些用户、流程或验收条件。",
          },
        ]}
        submit="提交问题"
        onSubmit={async (values) => {
          await w.run("clarifications.propose", {
            goalId: goal!.id,
            expectedRequirementRevision: goal!.revision,
            key,
            questions: [{ question: values.question, context: values.context }],
          });
          w.close();
        }}
      >
        <p className="form-note">
          {goal!.title} · 需求 v{goal!.revision}
        </p>
      </Form>,
    );
  }
  function answer(batch: Resource, question: Data) {
    const key = crypto.randomUUID();
    w.open(
      "确认需求答复",
      <Form
        fields={[
          {
            name: "answer",
            label: "产品确认答复",
            type: "textarea",
            required: true,
            hint: "记录明确的业务决定，批准前请核对它与验收标准是否一致。确认后的答复保留为本版本记录。",
          },
        ]}
        submit="保存确认答复"
        onSubmit={async (values) => {
          await w.run("clarifications.answer", {
            id: batch.id,
            questionId: question.id,
            expectedRequirementRevision: goal!.revision,
            key,
            answer: values.answer,
          });
          w.close();
        }}
      >
        <p className="form-note">
          {goal!.title} · 需求 v{goal!.revision}
        </p>
        <p className="clarification-question-copy">{question.question}</p>
      </Form>,
    );
  }
  function generate() {
    const key = crypto.randomUUID();
    w.open(
      "辅助澄清业务需求",
      <Confirm
        label="生成澄清问题"
        description={
          <>
            <p className="form-note">
              {goal!.title} · 需求 v{goal!.revision}
            </p>
            <p>根据当前业务目标和项目知识生成待确认问题。</p>
            <p>
              此次调用计入目标模型预算。返回后请逐项审阅，并由产品负责人确认业务答案。
            </p>
            <p className="form-note">
              同一请求的重复确认读取原结果或待核对状态。生成未完成时，请先核对费用记录，再决定是否重新发起。
            </p>
          </>
        }
        onConfirm={async () => {
          try {
            await post("clarifications/generate", {
              goalId: goal!.id,
              expectedRequirementRevision: goal!.revision,
              key,
            });
          } finally {
            await w.refresh();
          }
          w.close();
        }}
      />,
    );
  }
  function rows(batch: Resource, historical = false) {
    return batch.data.questions.map((question: Data) => (
      <article
        className="clarification-question"
        aria-label={"澄清问题 " + question.id}
        key={question.id}
      >
        <div className="clarification-question-heading">
          <strong>{question.question}</strong>
          <span
            className={
              question.status === "answered"
                ? "clarification-state answered"
                : "clarification-state"
            }
          >
            {question.status === "answered"
              ? "已确认答复"
              : historical
                ? "历史未决"
                : "待产品确认"}
          </span>
        </div>
        {question.context && (
          <p className="clarification-context">{question.context}</p>
        )}
        <small>
          {batch.data.origin === "model"
            ? "模型辅助提问"
            : batch.data.proposedKind === "agent"
              ? "Agent 提问"
              : "产品提问"}{" "}
          · {batch.data.proposedName || batch.data.proposedBy} · 需求 v
          {batch.data.requirementRevision}
        </small>
        {question.status === "answered" ? (
          <div className="clarification-answer">
            <p>{question.answer}</p>
            <small>
              {question.answeredName || question.answeredBy} ·{" "}
              {date(question.answeredAt)}
            </small>
          </div>
        ) : (
          !historical && (
            <div className="clarification-question-actions">
              <Button
                secondary
                disabled={!canAnswer}
                onClick={() => answer(batch, question)}
              >
                确认答复
              </Button>
            </div>
          )
        )}
      </article>
    ));
  }
  return (
    <section className="clarifications" aria-label="需求澄清">
      <div className="content-heading spaced clarification-heading">
        <div>
          <h3>需求澄清</h3>
          <p className="muted">
            把影响业务范围与验收的决定，留在对应需求版本中。
          </p>
        </div>
        <div className="button-group">
          <Button secondary disabled={!canAsk} onClick={ask}>
            <MessageSquarePlus size={14} />
            提出澄清
          </Button>
          <Button secondary disabled={!canAnswer} onClick={generate}>
            <Sparkles size={14} />
            辅助生成问题
          </Button>
        </div>
      </div>
      <p
        className={pending ? "clarification-pending" : "form-note"}
        role="status"
      >
        {pending
          ? `还有 ${pending} 项澄清待确认，完成后才能批准基线。`
          : draft
            ? "当前没有未决问题。请核对业务结果、验收标准与已确认答复，再批准基线。"
            : "本版本的问答已随基线批准冻结。后续决定请通过需求变更记录。"}
      </p>
      <div className="clarification-list">
        {current.map((batch) => rows(batch))}
      </div>
      {!current.length && <p className="inline-empty">当前版本尚无澄清记录</p>}
      {history.length > 0 && (
        <details className="clarification-history">
          <summary>
            历史澄清（
            {history.reduce(
              (count, batch) => count + batch.data.questions.length,
              0,
            )}
            ）
          </summary>
          <p className="form-note">
            旧版本保留原问题和答复；当前版本按新的业务范围重新确认。
          </p>
          <div className="clarification-list">
            {history.map((batch) => rows(batch, true))}
          </div>
        </details>
      )}
      {generations.length > 0 && (
        <details className="clarification-history">
          <summary>辅助生成记录（{generations.length}）</summary>
          <div className="clarification-generations">
            {generations.map((request) => (
              <div key={request.id}>
                <strong>
                  {request.status === "succeeded"
                    ? "问题已生成"
                    : request.status === "failed"
                      ? "生成未完成"
                      : "待返回或待核对"}
                </strong>
                <small>
                  需求 v{request.data.requirementRevision} ·{" "}
                  {request.data.requestedName || request.data.requestedBy} ·{" "}
                  {date(request.createdAt)}
                </small>
                {request.status !== "succeeded" && (
                  <p>请核对模型费用与请求记录后，再决定是否重新发起。</p>
                )}
                <code>{request.id}</code>
              </div>
            ))}
          </div>
        </details>
      )}
    </section>
  );
}
