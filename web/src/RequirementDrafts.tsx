import { useState } from "react";
import { FilePenLine, Plus, Sparkles, Trash2 } from "lucide-react";
import { date, methods, post, type Data, type Resource } from "./api";
import { useWorkspace } from "./context";
import { Button, Confirm, Form } from "./ui";

function DraftForm({
  goal,
  proposal,
  requestKey,
}: {
  goal: Resource;
  proposal?: Resource;
  requestKey: string;
}) {
  const w = useWorkspace();
  const content = proposal?.data.content;
  const [criteria, setCriteria] = useState<Data[]>(
    content?.criteria ||
      (goal.data.criteria.length
        ? goal.data.criteria
        : [{ id: crypto.randomUUID(), text: "", method: "automated" }]),
  );
  return (
    <Form
      fields={[
        {
          name: "outcome",
          label: "业务结果",
          type: "textarea",
          required: true,
          defaultValue: content?.outcome || goal.data.outcome,
        },
        {
          name: "brief",
          label: "需求说明",
          type: "textarea",
          required: true,
          defaultValue: content?.brief || goal.data.requirementBrief || "",
          hint: "说明使用者、业务场景、范围、排除项与关键约束。",
        },
        ...(proposal
          ? [
              {
                name: "reason",
                label: "采纳原因",
                type: "textarea" as const,
                required: true,
              },
            ]
          : []),
      ]}
      submit={proposal ? "采纳并创建新版本" : "保存待审阅需求稿"}
      onSubmit={async (values) => {
        const reviewed = {
          outcome: values.outcome,
          brief: values.brief,
          criteria: criteria.map((c) => ({ text: c.text, method: c.method })),
        };
        await w.run(
          proposal ? "requirements.apply" : "requirements.propose",
          proposal
            ? {
                id: proposal.id,
                expectedRequirementRevision: goal.revision,
                key: requestKey,
                reason: values.reason,
                content: reviewed,
              }
            : {
                goalId: goal.id,
                expectedRequirementRevision: goal.revision,
                key: requestKey,
                content: reviewed,
              },
        );
        w.close();
      }}
    >
      <p className="form-note">
        {goal.title} · 需求 v{goal.revision}
      </p>
      {proposal && (
        <section className="requirement-comparison" aria-label="采纳前对照">
          <h3>当前业务结果</h3>
          <p>{goal.data.outcome}</p>
          <h3>当前验收标准</h3>
          {goal.data.criteria.length ? (
            <ol>
              {goal.data.criteria.map((c: Data) => (
                <li key={c.id}>
                  {c.text} <span className="muted">{methods[c.method]}</span>
                </li>
              ))}
            </ol>
          ) : (
            <p>尚未整理验收标准</p>
          )}
          <p className="form-note">
            下方为拟采纳的内容。采纳会创建新版本，保留本稿引用的人工需求决定，并重新等待基线批准。
          </p>
        </section>
      )}
      <fieldset className="requirement-criteria">
        <legend>拟议验收标准</legend>
        {criteria.map((c, index) => (
          <div className="criterion-editor" key={c.id}>
            <input
              aria-label={"验收标准 " + (index + 1)}
              required
              maxLength={2000}
              value={c.text}
              onChange={(e) =>
                setCriteria(
                  criteria.map((x) =>
                    x.id === c.id ? { ...x, text: e.target.value } : x,
                  ),
                )
              }
            />
            <select
              aria-label={"验证方式 " + (index + 1)}
              value={c.method}
              onChange={(e) =>
                setCriteria(
                  criteria.map((x) =>
                    x.id === c.id ? { ...x, method: e.target.value } : x,
                  ),
                )
              }
            >
              {Object.entries(methods).map(([key, label]) => (
                <option key={key} value={key}>
                  {label}
                </option>
              ))}
            </select>
            <button
              type="button"
              className="icon-button"
              aria-label={"移除验收标准 " + (index + 1)}
              disabled={criteria.length === 1}
              onClick={() => setCriteria(criteria.filter((x) => x.id !== c.id))}
            >
              <Trash2 size={15} />
            </button>
          </div>
        ))}
        <button
          type="button"
          className="text-button"
          disabled={criteria.length >= 100}
          onClick={() =>
            setCriteria([
              ...criteria,
              { id: crypto.randomUUID(), text: "", method: "automated" },
            ])
          }
        >
          <Plus size={14} />
          添加验收标准
        </button>
      </fieldset>
    </Form>
  );
}

export function RequirementDrafts() {
  const w = useWorkspace(),
    goal = w.goal;
  if (!goal) return null;
  const unapproved =
    goal.status === "awaiting_baseline" &&
    goal.data.approvedRevision !== goal.revision;
  const unresolved = w
    .select("clarification")
    .some(
      (b) =>
        b.goalId === goal.id &&
        b.data.requirementRevision === goal.revision &&
        b.data.questions.some((q: Data) => q.status !== "answered"),
    );
  const canPropose =
    unapproved &&
    !unresolved &&
    w.actor.kind !== "worker" &&
    w.can("product", "architect", "analysis", "design", "developer");
  const canAdopt =
    unapproved && !unresolved && w.actor.kind === "human" && w.can("product");
  const proposals = w
    .select("requirement_proposal")
    .filter((p) => p.goalId === goal.id)
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  const current = proposals.filter(
    (p) =>
      p.status === "proposed" && p.data.requirementRevision === goal.revision,
  );
  const history = proposals.filter((p) => !current.includes(p));
  const requests = w
    .select("requirement_generation")
    .filter((p) => p.goalId === goal.id)
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  function generate() {
    const key = crypto.randomUUID();
    w.open(
      "辅助整理需求",
      <Confirm
        label="生成需求稿"
        description={
          <>
            <p>
              {goal!.title} · 需求 v{goal!.revision}
            </p>
            <p>
              根据当前业务目标、已确认答复和项目知识整理待审阅需求。此次调用计入目标模型预算。
            </p>
            <p>
              返回后请核对业务范围与验收标准。生成未完成时，先核对费用与请求记录再决定是否重新发起。
            </p>
          </>
        }
        onConfirm={async () => {
          try {
            await post("requirements/generate", {
              goalId: goal!.id,
              expectedRequirementRevision: goal!.revision,
              key,
            });
            w.close();
          } finally {
            await w.refresh();
          }
        }}
      />,
    );
  }
  function row(proposal: Resource) {
    const content = proposal.data.content;
    const input = proposal.data.input;
    return (
      <article
        className="requirement-draft"
        aria-label="需求稿"
        key={proposal.id}
      >
        <div className="clarification-question-heading">
          <strong>
            {proposal.status === "applied" ? "已采纳需求稿" : "待审阅需求稿"}
          </strong>
          <span className="method-tag">
            {proposal.data.origin === "model"
              ? "模型整理"
              : "人工 / Agent 起草"}
          </span>
        </div>
        <small className="muted">
          基于需求 v{proposal.data.requirementRevision} ·{" "}
          {proposal.data.proposedName || proposal.data.proposedBy} ·{" "}
          {date(proposal.createdAt)}
        </small>
        <p>{content.outcome}</p>
        <p className="requirement-brief">{content.brief}</p>
        <ol>
          {content.criteria.map((c: Data) => (
            <li key={c.id}>
              {c.text} <span className="muted">{methods[c.method]}</span>
            </li>
          ))}
        </ol>
        {input.clarifications.length > 0 && (
          <details>
            <summary>引用的人工需求决定</summary>
            {input.clarifications.flatMap((batch: Resource) =>
              batch.data.questions.map((q: Data) => (
                <div className="clarification-answer" key={batch.id + q.id}>
                  <p>{q.question}</p>
                  <p>{q.answer}</p>
                  <small>
                    {q.answeredName || q.answeredBy} · {date(q.answeredAt)}
                  </small>
                </div>
              )),
            )}
          </details>
        )}
        {current.includes(proposal) && (
          <Button
            secondary
            disabled={!canAdopt}
            onClick={() =>
              w.open(
                "审阅需求稿",
                <DraftForm
                  goal={goal!}
                  proposal={proposal}
                  requestKey={crypto.randomUUID()}
                />,
              )
            }
          >
            审阅并采纳
          </Button>
        )}
        {proposal.status === "applied" && (
          <p className="form-note">
            {proposal.data.appliedName || proposal.data.appliedBy} 采纳为需求 v
            {proposal.data.resultRevision} · {proposal.data.reason}
          </p>
        )}
      </article>
    );
  }
  return (
    <section className="requirement-drafts" aria-label="需求稿与人工审阅">
      <div className="content-heading spaced clarification-heading">
        <div>
          <h3>需求稿与人工审阅</h3>
          <p>整理业务范围和可验证的验收标准，逐项审阅后采纳。</p>
        </div>
        <div className="inline-actions">
          <Button
            secondary
            disabled={!canPropose}
            onClick={() =>
              w.open(
                "人工起草需求",
                <DraftForm goal={goal} requestKey={crypto.randomUUID()} />,
              )
            }
          >
            <FilePenLine size={15} />
            人工起草需求
          </Button>
          <Button secondary disabled={!canAdopt} onClick={generate}>
            <Sparkles size={15} />
            辅助整理需求
          </Button>
        </div>
      </div>
      {unresolved && (
        <p className="clarification-pending">
          请先确认当前版本的澄清答复，再整理需求稿。
        </p>
      )}
      {current.map(row)}
      {!current.length && (
        <p className="inline-empty">当前版本暂无待审阅需求稿</p>
      )}
      {history.length > 0 && (
        <details className="clarification-history">
          <summary>历史需求稿（{history.length}）</summary>
          {history.map(row)}
        </details>
      )}
      {requests.length > 0 && (
        <details className="clarification-history">
          <summary>需求整理记录（{requests.length}）</summary>
          <div className="clarification-generations">
            {requests.map((r) => (
              <div key={r.id}>
                <strong>
                  {r.status === "succeeded"
                    ? "需求稿已生成"
                    : r.status === "failed"
                      ? "生成未完成"
                      : "待返回或待核对"}
                </strong>
                <small>
                  需求 v{r.data.requirementRevision} · {date(r.createdAt)}
                </small>
                {r.status !== "succeeded" && (
                  <p>请核对模型费用与请求记录后，再决定是否重新发起。</p>
                )}
                <code>{r.id}</code>
              </div>
            ))}
          </div>
        </details>
      )}
    </section>
  );
}
