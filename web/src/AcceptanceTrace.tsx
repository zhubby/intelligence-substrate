import { useEffect, useState } from "react";
import { RefreshCw } from "lucide-react";
import { api, date, methods, targets } from "./api";
import { useWorkspace } from "./context";
import { Badge, Button } from "./ui";

interface Evidence {
  id: string;
  status: string;
  source: string;
  method: string;
  purpose: string;
  actorId: string;
  criteria: string[];
  verificationEnvironment: string;
  runId?: string;
  taskId?: string;
  reportId?: string;
  reportDigest?: string;
  reportVerified: boolean;
  usable: boolean;
  issues: string[];
}
interface Artifact {
  id: string;
  title: string;
  target: string;
  digest: string;
  repositoryId: string;
  commit: string;
  producerId: string;
  integrityVerified: boolean;
  sourceVerified: boolean;
  requiredEnvironment: string;
  environmentVerified: boolean;
  issues: string[];
  build?: {
    runId: string;
    taskId: string;
    planId: string;
    approvalId?: string;
    designId?: string;
    designDigest?: string;
    prototypeIds: string[];
    verified: boolean;
    issues: string[];
  };
  evidence: Evidence[];
}
interface Report {
  requirementRevision: number;
  currentRevision: number;
  revisions: number[];
  current: boolean;
  baselineApproved: boolean;
  generatedAt: string;
  criteria: {
    id: string;
    text: string;
    method: string;
    targets: {
      target: string;
      covered: boolean;
      artifactIds: string[];
      evidenceIds: string[];
      gaps: string[];
    }[];
  }[];
  artifacts: Artifact[];
  releases: {
    id: string;
    version: string;
    status: string;
    planId: string;
    approvalId?: string;
    approved: boolean;
    gatePassed: boolean;
    issues: string[];
    targets: {
      target: string;
      status: string;
      artifactId: string;
      externalId: string;
      reason: string;
    }[];
  }[];
}
const environments: Record<string, string> = {
  browser: "真实浏览器",
  "ios-device": "iOS 真机",
  "android-device": "Android 真机",
  windows: "Windows 系统",
  macos: "macOS 系统",
  ci: "CI 环境",
  simulator: "模拟器",
};
function Issues({ items }: { items: string[] }) {
  return items.length ? (
    <ul className="trace-issues">
      {items.map((text, i) => (
        <li key={i}>{text}</li>
      ))}
    </ul>
  ) : null;
}
function ArtifactTrace({ artifact: a }: { artifact: Artifact }) {
  return (
    <article
      className="trace-artifact"
      id={"trace-artifact-" + a.id}
      aria-label={"制品 " + a.id}
    >
      <div className="trace-heading">
        <h4>{a.title}</h4>
        <span>{targets[a.target] || a.target}</span>
      </div>
      <dl className="trace-meta">
        <div>
          <dt>包完整性</dt>
          <dd>{a.integrityVerified ? "字节已核验" : "校验未通过"}</dd>
        </div>
        <div>
          <dt>源码仓库</dt>
          <dd>
            <code>{a.repositoryId}</code>
          </dd>
        </div>
        <div>
          <dt>源码提交</dt>
          <dd>
            <code>{a.commit}</code>
            {!a.sourceVerified && " · 未核验"}
          </dd>
        </div>
        <div>
          <dt>包摘要</dt>
          <dd>
            <code>{a.digest}</code>
          </dd>
        </div>
        <div>
          <dt>生产者</dt>
          <dd>{a.producerId}</dd>
        </div>
        <div>
          <dt>目标环境</dt>
          <dd>
            {environments[a.requiredEnvironment] || a.requiredEnvironment} ·{" "}
            {a.environmentVerified ? "独立验证通过" : "待补验证"}
          </dd>
        </div>
      </dl>
      <Issues items={a.issues} />
      {a.build ? (
        <div className="trace-build">
          <h4>构建来源</h4>
          <dl className="trace-meta">
            <div>
              <dt>构建执行</dt>
              <dd>
                <code>{a.build.runId}</code>
              </dd>
            </div>
            <div>
              <dt>工作项</dt>
              <dd>
                <code>{a.build.taskId}</code>
              </dd>
            </div>
            <div>
              <dt>交付方案</dt>
              <dd>
                <code>{a.build.planId}</code>
              </dd>
            </div>
            {a.build.approvalId && (
              <div>
                <dt>方案批准</dt>
                <dd>
                  <code>{a.build.approvalId}</code>
                </dd>
              </div>
            )}
            {a.build.designId && (
              <div>
                <dt>批准的设计</dt>
                <dd>
                  <code>{a.build.designId}</code>
                </dd>
              </div>
            )}
          </dl>
          <Issues items={a.build.issues} />
        </div>
      ) : (
        <p className="form-note">
          人工上传，未记录构建执行。源码关联不代表已在平台构建。
        </p>
      )}
      <div className="trace-evidence-list">
        <h4>独立验证记录</h4>
        {a.evidence.map((e) => (
          <section
            className="trace-evidence"
            key={e.id}
            aria-label={"证据 " + e.id}
          >
            <div className="trace-heading">
              <strong>{methods[e.method] || e.method}</strong>
              <Badge status={e.status} />
            </div>
            <p className="muted">
              {e.source === "trusted-worker"
                ? "独立质量节点"
                : e.source === "human-attested"
                  ? "人工验收记录"
                  : "来源待核对"}{" "}
              · {environments[e.verificationEnvironment] || "未声明验证环境"} ·{" "}
              {e.purpose === "publication"
                ? "发布后验证"
                : e.purpose === "environment"
                  ? "环境验收"
                  : "标准验收"}
            </p>
            <dl className="trace-meta">
              <div>
                <dt>证据</dt>
                <dd>
                  <code>{e.id}</code>
                </dd>
              </div>
              <div>
                <dt>验证人</dt>
                <dd>{e.actorId}</dd>
              </div>
              {e.runId && (
                <div>
                  <dt>验证执行</dt>
                  <dd>
                    <code>{e.runId}</code>
                  </dd>
                </div>
              )}
              <div>
                <dt>关联标准</dt>
                <dd>{e.criteria.join("、") || "未关联标准"}</dd>
              </div>
            </dl>
            <p className={e.usable ? "trace-valid" : "muted"}>
              {e.usable
                ? e.purpose === "publication"
                  ? "发布后验证记录"
                  : "可用于匹配标准或环境的独立验证"
                : "不计入当前验收"}
            </p>
            {e.reportId ? (
              <div className="trace-report">
                <span>
                  {e.reportVerified ? "报告附件已核验" : "报告附件未通过核验"}
                </span>
                {e.reportVerified && (
                  <a
                    className="text-button"
                    href={
                      "/api/v1/artifacts/" +
                      encodeURIComponent(e.reportId) +
                      "/content"
                    }
                    target="_blank"
                    rel="noopener noreferrer"
                  >
                    下载验证报告
                  </a>
                )}
              </div>
            ) : (
              <p className="form-note">仅有执行记录，未附报告</p>
            )}
            <Issues items={e.issues} />
          </section>
        ))}
        {!a.evidence.length && (
          <p className="inline-empty">尚无关联此制品的独立验证记录</p>
        )}
      </div>
    </article>
  );
}

export function AcceptanceTrace() {
  const w = useWorkspace();
  const [revision, setRevision] = useState("0");
  const [refresh, setRefresh] = useState(0);
  const scope = [w.project.id, w.goal?.id, w.goal?.revision, revision].join(
    "/",
  );
  const [snapshot, setSnapshot] = useState<{
    scope: string;
    report?: Report;
    error?: string;
  }>();
  const report = snapshot?.scope === scope ? snapshot.report : undefined;
  const error = snapshot?.scope === scope ? snapshot.error : undefined;
  useEffect(() => {
    if (!w.goal) return;
    const controller = new AbortController();
    let current = true;
    setSnapshot(undefined);
    const query = new URLSearchParams({ goalId: w.goal.id });
    if (revision !== "0") query.set("revision", revision);
    api<Report>(
      "projects/" +
        encodeURIComponent(w.project.id) +
        "/acceptance-trace?" +
        query,
      {
        signal: AbortSignal.any([
          controller.signal,
          AbortSignal.timeout(20000),
        ]),
      },
    )
      .then((report) => {
        if (current) setSnapshot({ scope, report });
      })
      .catch((e: Error) => {
        if (current) setSnapshot({ scope, error: e.message });
      });
    return () => {
      current = false;
      controller.abort();
    };
  }, [scope, revision, w.project.id, w.goal?.id, w.items, refresh]);
  const cells = report?.criteria.flatMap((c) => c.targets) || [];
  const versions =
    report?.revisions ||
    Array.from({ length: w.goal?.revision || 1 }, (_, i) => i + 1);
  return (
    <section className="acceptance-trace detail-content" aria-label="验收追溯">
      <div className="trace-toolbar">
        <label>
          追溯需求版本
          <select
            aria-label="追溯需求版本"
            value={revision}
            onChange={(e) => setRevision(e.target.value)}
          >
            <option value="0">当前 · v{w.goal?.revision}</option>
            {[...versions]
              .reverse()
              .filter((v) => v !== w.goal?.revision)
              .map((v) => (
                <option value={v} key={v}>
                  历史 · v{v}
                </option>
              ))}
          </select>
        </label>
        <Button secondary onClick={() => setRefresh((n) => n + 1)}>
          <RefreshCw size={14} />
          刷新追溯
        </Button>
      </div>
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      {!report && !error && <p role="status">正在读取验收链路…</p>}
      {report && (
        <>
          <div className="trace-summary">
            <div>
              <strong>
                {cells.filter((c) => c.covered).length} / {cells.length}
              </strong>
              <span>标准与必选端具备匹配证据</span>
            </div>
            <p>
              {!report.current
                ? "历史版本不计入当前验收。"
                : !report.baselineApproved
                  ? "当前需求基线尚未批准。"
                  : "需求基线已批准。"}
              <br />
              证据覆盖后仍需满足目标环境、发布审批与渠道验证。
            </p>
          </div>
          <p className="muted">
            需求 v{report.requirementRevision} · 核验时间{" "}
            {date(report.generatedAt)}
          </p>
          <section className="trace-criteria" aria-label="验收标准覆盖">
            {report.criteria.map((c) => (
              <article className="trace-criterion" key={c.id}>
                <div className="trace-heading">
                  <h3>{c.text}</h3>
                  <span>{methods[c.method] || c.method}</span>
                </div>
                <small className="mono muted">{c.id}</small>
                {c.targets.map((t) => (
                  <div className="trace-target" key={t.target}>
                    <div className="trace-heading">
                      <strong>{targets[t.target] || t.target}</strong>
                      <span className={t.covered ? "trace-valid" : "muted"}>
                        {t.covered
                          ? "证据已覆盖"
                          : report.current
                            ? "待补证据"
                            : "历史记录"}
                      </span>
                    </div>
                    <Issues items={t.gaps} />
                    {report.artifacts
                      .filter((a) => a.target === t.target)
                      .map((a) => (
                        <a
                          className="text-button"
                          href={"#trace-artifact-" + a.id}
                          key={a.id}
                        >
                          查看制品 {a.title}
                          {t.artifactIds.includes(a.id) ? " · 已匹配" : ""}
                        </a>
                      ))}
                  </div>
                ))}
              </article>
            ))}
          </section>
          <section className="trace-packages" aria-label="制品与验证来源">
            <h3>制品与验证来源</h3>
            {report.artifacts.map((a) => (
              <ArtifactTrace artifact={a} key={a.id} />
            ))}
            {!report.artifacts.length && (
              <p className="inline-empty">此需求版本尚无可追溯的应用包</p>
            )}
          </section>
          <section className="trace-releases" aria-label="候选与发布">
            <h3>候选与发布</h3>
            {report.releases.map((r) => (
              <article className="trace-release" key={r.id}>
                <div className="trace-heading">
                  <h4>{r.version}</h4>
                  <Badge status={r.status} />
                </div>
                <p>
                  {r.approved
                    ? "已记录当前绑定的发布批准"
                    : "尚无当前绑定的发布批准"}
                </p>
                <p className="muted">
                  {r.gatePassed
                    ? "当前候选通过发布前质量校验"
                    : "当前候选未通过发布前质量校验"}
                </p>
                {r.approvalId && (
                  <code className="trace-reference">{r.approvalId}</code>
                )}
                <Issues items={r.issues} />
                {r.targets.map((t) => (
                  <div className="trace-target" key={t.target}>
                    <div className="trace-heading">
                      <strong>{targets[t.target] || t.target}</strong>
                      <Badge status={t.status} />
                    </div>
                    <code className="trace-reference">{t.artifactId}</code>
                    {t.externalId && (
                      <p className="trace-reference">
                        外部版本：{t.externalId}
                      </p>
                    )}
                    {t.reason && <p>{t.reason}</p>}
                  </div>
                ))}
              </article>
            ))}
            {!report.releases.length && (
              <p className="inline-empty">此需求版本尚无发布候选</p>
            )}
          </section>
        </>
      )}
    </section>
  );
}
