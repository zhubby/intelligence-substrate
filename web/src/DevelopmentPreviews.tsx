import { useEffect, useState } from "react";
import { ArrowUpRight, Eye, Plus, Square } from "lucide-react";
import { api, date, type Resource } from "./api";
import { useWorkspace } from "./context";
import { Button, Empty, Form, SectionHeading } from "./ui";

function currentStatus(preview: Resource, now: number) {
  return preview.status === "active" &&
    !(Date.parse(preview.data.expiresAt) > now)
    ? "expired"
    : preview.status;
}

function PreviewAccessDialog({ preview }: { preview: Resource }) {
  const w = useWorkspace();
  const [access, setAccess] = useState<{ url: string; expiresAt: string }>();
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const [clock, setClock] = useState(Date.now());
  const current = w.items.find((x) => x.id === preview.id);
  const available =
    w.project.id === preview.projectId &&
    w.goal?.id === preview.goalId &&
    !!current &&
    currentStatus(current, clock) === "active" &&
    current.data.requirementRevision === w.goal.revision;

  useEffect(() => {
    const timer = setInterval(() => setClock(Date.now()), 1000);
    return () => clearInterval(timer);
  }, []);
  useEffect(() => {
    if (!available) return;
    const controller = new AbortController();
    let live = true;
    setAccess(undefined);
    setError("");
    api<{ url: string; expiresAt: string }>(
      "previews/" + preview.id + "/access",
      {
        method: "POST",
        body: "{}",
        signal: AbortSignal.any([
          controller.signal,
          AbortSignal.timeout(20000),
        ]),
      },
    )
      .then((result) => {
        if (live && !controller.signal.aborted) setAccess(result);
      })
      .catch((e) => {
        if (live && !controller.signal.aborted) setError(e.message);
      });
    return () => {
      live = false;
      controller.abort();
    };
  }, [preview.id, attempt, available]);

  const expired = access && !(Date.parse(access.expiresAt) > clock);
  return (
    <div className="form">
      <div className="confirmation-copy">
        <p>
          {preview.title} · 需求 v{preview.data.requirementRevision}
        </p>
        <p>
          短期链接绑定当前登录会话。请勿分享；退出登录、权限变化或停止预览后需要重新打开。
        </p>
        {preview.data.temporaryData && (
          <p>业务数据仅属于本次预览，停止或执行环境回收后会清除。</p>
        )}
      </div>
      {!available ? (
        <p role="alert" className="inline-notice">
          预览已失效，请关闭对话框并刷新当前目标。
        </p>
      ) : error ? (
        <>
          <p role="alert" className="inline-notice">
            {error}
          </p>
          <Button secondary onClick={() => setAttempt((x) => x + 1)}>
            重试准备链接
          </Button>
        </>
      ) : !access ? (
        <p role="status">正在准备短期访问…</p>
      ) : expired ? (
        <>
          <p role="alert">访问链接已到期。</p>
          <Button secondary onClick={() => setAttempt((x) => x + 1)}>
            重新准备链接
          </Button>
        </>
      ) : (
        <>
          <p>链接到期：{date(access.expiresAt)}</p>
          <a
            className="button"
            href={access.url}
            target="_blank"
            rel="noopener noreferrer"
            referrerPolicy="no-referrer"
          >
            <ArrowUpRight size={16} />
            在新窗口体验
          </a>
        </>
      )}
    </div>
  );
}

export function DevelopmentPreviews({
  repositoryId,
}: {
  repositoryId: string;
}) {
  const w = useWorkspace();
  const goal = w.goal;
  const [clock, setClock] = useState(Date.now());
  useEffect(() => {
    const timer = setInterval(() => setClock(Date.now()), 15000);
    return () => clearInterval(timer);
  }, []);
  const integration = w.integrations.find(
    (x) => x.id === "development-previews",
  );
  const configured = integration?.status === "configured";
  const artifacts = w
    .select("artifact")
    .filter(
      (x) =>
        x.goalId === goal?.id &&
        x.data.repositoryId === repositoryId &&
        x.data.target === "web" &&
        x.data.type === "package" &&
        x.data.requirementRevision === goal?.revision,
    );
  const previews = w
    .select("preview")
    .filter(
      (x) => x.goalId === goal?.id && x.data.repositoryId === repositoryId,
    )
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  const enabled =
    configured &&
    w.can("developer") &&
    !!goal &&
    goal.data.approvedRevision === goal.revision &&
    artifacts.length > 0;

  function create() {
    if (!goal || !enabled) return;
    const captured = goal;
    const key = crypto.randomUUID();
    w.open(
      "创建开发预览",
      <Form
        fields={[
          {
            name: "title",
            label: "预览名称",
            required: true,
            defaultValue: captured.title + " · 开发预览",
          },
          {
            name: "artifactId",
            label: "Web 构建包",
            type: "select",
            required: true,
            defaultValue: artifacts[0].id,
            options: Object.fromEntries(
              artifacts.map((x) => [
                x.id,
                x.title + " · " + x.data.commit.slice(0, 12),
              ]),
            ),
          },
          {
            name: "ttlMinutes",
            label: "有效期（分钟）",
            type: "number",
            step: "1",
            required: true,
            defaultValue: 60,
            hint: "5–240 分钟；每项目最多保留 10 个活动预览。",
          },
          {
            name: "reason",
            label: "创建原因",
            type: "textarea",
            required: true,
          },
        ]}
        submit="创建预览"
        onSubmit={async (values) => {
          try {
            await w.run("previews.create", {
              ...values,
              key,
              expectedRequirementRevision: captured.revision,
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
            支持带根 index.html 的 tar / tar.gz
            静态包，资源使用相对路径；首批蓝图可使用专用的完整应用预览包。创建预览不通过质量验收。
          </p>
        </div>
      </Form>,
    );
  }

  function stop(preview: Resource) {
    const key = crypto.randomUUID();
    w.open(
      "停止开发预览",
      <Form
        fields={[
          {
            name: "reason",
            label: "停止原因",
            type: "textarea",
            required: true,
          },
        ]}
        submit="确认停止"
        onSubmit={async (values) => {
          try {
            await w.run("previews.stop", {
              id: preview.id,
              expectedRevision: preview.revision,
              key,
              reason: values.reason,
            });
            w.close();
          } finally {
            await w.refresh();
          }
        }}
      >
        <div className="confirmation-copy">
          <p>{preview.title}</p>
          <p>
            停止后拒绝新的页面和资源请求。已下载的内容仍可能保留在浏览器中。
          </p>
        </div>
      </Form>,
    );
  }

  return (
    <section aria-label="开发预览" className="development-previews">
      <SectionHeading
        title="开发预览"
        description="体验当前提交的 Web 应用。需求变化、到期或停止后需要重新创建。"
        action={
          <Button secondary disabled={!enabled} onClick={create}>
            <Plus size={15} />
            创建开发预览
          </Button>
        }
      />
      {!configured ? (
        <p className="inline-notice">
          开发预览入口尚未配置，请联系平台管理员。
        </p>
      ) : (
        <p className="preview-support">
          {integration?.fullstackConfigured
            ? "支持静态应用与首批蓝图的完整应用预览包。每次预览使用独立临时数据，停止后清除。"
            : "支持多文件静态应用；完整应用预览需要管理员配置独立执行节点。"}
        </p>
      )}
      {!goal ? (
        <p className="inline-notice">请先在目标与需求中选择交付目标。</p>
      ) : !previews.length ? (
        <Empty
          icon={<Eye size={24} />}
          title="还没有开发预览"
          description={
            artifacts.length
              ? "从当前 Web 构建包创建一次限时体验。"
              : "当前需求还没有该仓库的 Web 构建包，请先完成构建。"
          }
        />
      ) : (
        <div className="preview-list">
          {previews.map((preview) => {
            const status = currentStatus(preview, clock);
            const label =
              (
                {
                  active: "可体验",
                  stopped: "已停止",
                  expired: "已到期",
                  stale: "需求已变化",
                } as Record<string, string>
              )[status] || status;
            return (
              <article
                key={preview.id}
                aria-label={preview.title}
                className="preview-row"
              >
                <Eye size={19} aria-hidden="true" />
                <div className="preview-detail">
                  <strong>{preview.title}</strong>
                  <small>
                    {preview.data.sourceKind === "worker-build"
                      ? "Worker 构建"
                      : "登记制品"}{" "}
                    · 需求 v{preview.data.requirementRevision} ·{" "}
                    <code>{preview.data.commit.slice(0, 12)}</code> ·{" "}
                    {preview.data.fileCount} 个文件
                    {preview.data.temporaryData ? " · 独立临时数据" : ""}
                  </small>
                  <small>预览到期 {date(preview.data.expiresAt)}</small>
                </div>
                <span
                  className={
                    "badge status-" + (status === "active" ? "online" : status)
                  }
                >
                  <span />
                  {label}
                </span>
                <div className="button-group">
                  <Button
                    secondary
                    disabled={
                      !configured ||
                      status !== "active" ||
                      w.actor.kind !== "human"
                    }
                    onClick={() =>
                      w.open(
                        "打开开发预览",
                        <PreviewAccessDialog preview={preview} />,
                      )
                    }
                  >
                    <ArrowUpRight size={14} />
                    打开预览
                  </Button>
                  <Button
                    secondary
                    disabled={!w.can("developer") || status !== "active"}
                    onClick={() => stop(preview)}
                  >
                    <Square size={13} />
                    停止预览
                  </Button>
                </div>
              </article>
            );
          })}
        </div>
      )}
    </section>
  );
}
