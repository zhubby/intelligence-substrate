import { useEffect, useState } from "react";
import {
  ArrowRight,
  Check,
  Code2,
  Download,
  FileCode2,
  FolderGit2,
  GitBranch,
  GitPullRequest,
  Play,
  Plus,
  ShieldCheck,
  Upload,
} from "lucide-react";
import { api, date, methods, targets, type Data, type Resource } from "../api";
import { Badge, Button, Confirm, Empty, Form, SectionHeading } from "../ui";
import { useWorkspace } from "../context";
import { EvidenceForm, UploadForm } from "../forms";
import { QualityBindingButton, QualityScheduling } from "../QualityScheduling";
import { DevelopmentPreviews } from "../DevelopmentPreviews";
import { SigningRepair } from "../SigningRepair";
import { CodeOverview, QualityOverview } from "../Overview";

export function Code() {
  const w = useWorkspace();
  const repos = w.select("repository");
  const [repoId, setRepo] = useState("");
  const [branch, setBranch] = useState("main");
  const [path, setPath] = useState("");
  const [content, setContent] = useState<Data>();
  const [error, setError] = useState("");
  const current = repos.find((x) => x.id === repoId) || repos[0];
  useEffect(() => {
    if (!current) {
      setContent(undefined);
      return;
    }
    let live = true;
    api<Data>(
      "repositories/" +
        current.id +
        "?" +
        new URLSearchParams({ branch, path }),
    )
      .then((x) => {
        if (live) {
          setContent(x);
          setError("");
        }
      })
      .catch((e) => {
        if (live) setError(e.message);
      });
    return () => {
      live = false;
    };
  }, [current?.id, current?.updatedAt, branch, path]);
  const newRepo = () =>
    w.open(
      "创建项目仓库",
      <Form
        fields={[
          { name: "title", label: "仓库名称", required: true },
          { name: "description", label: "用途", type: "textarea" },
        ]}
        submit="创建 Git 仓库"
        onSubmit={async (values) => {
          const r = await w.run("repositories.create", {
            ...values,
            projectId: w.project.id,
          });
          setRepo(r.id);
          setBranch("main");
          w.close();
        }}
      />,
    );
  return (
    <>
      <SectionHeading
        eyebrow="CODE & REVIEW"
        title="代码与评审"
        description="实际 Git 仓库、提交与评审记录。主分支合并需要独立评审和 CI 验证。"
        action={
          <Button disabled={!w.can("developer")} onClick={newRepo}>
            <Plus size={16} />
            创建仓库
          </Button>
        }
      />
      <CodeOverview repositories={repos} reviews={w.select("review")} />
      {!current ? (
        <Empty
          icon={<FolderGit2 size={30} />}
          title="项目还没有代码仓库"
          description="创建仓库后，可以浏览代码、提交变更，或使用项目令牌进行 Git clone 和 push。"
          action={
            <Button disabled={!w.can("developer")} onClick={newRepo}>
              创建第一个仓库
            </Button>
          }
        />
      ) : (
        <>
          <div className="repo-toolbar">
            <select
              aria-label="选择代码仓库"
              value={current.id}
              onChange={(e) => {
                setRepo(e.target.value);
                setPath("");
                setBranch("main");
              }}
            >
              {repos.map((x) => (
                <option key={x.id} value={x.id}>
                  {x.title}
                </option>
              ))}
            </select>
            <GitBranch size={15} />
            <select
              aria-label="选择代码分支"
              value={branch}
              onChange={(e) => {
                setBranch(e.target.value);
                setPath("");
              }}
            >
              {(content?.branches || ["main"]).map((b: string) => (
                <option key={b}>{b}</option>
              ))}
            </select>
            <div className="toolbar-spacer" />
            <Button
              secondary
              disabled={!w.can("developer")}
              onClick={() =>
                w.open(
                  "提交代码变更",
                  <Form
                    fields={[
                      {
                        name: "branch",
                        label: "工作分支",
                        required: true,
                        defaultValue:
                          branch === "main" ? "feature/application" : branch,
                      },
                      {
                        name: "path",
                        label: "文件路径",
                        required: true,
                        defaultValue: path || "README.md",
                      },
                      {
                        name: "content",
                        label: "文件内容",
                        type: "textarea",
                        required: true,
                        defaultValue: content?.content || "",
                      },
                      { name: "message", label: "提交说明", required: true },
                    ]}
                    submit="提交到工作分支"
                    onSubmit={async (values) => {
                      await w.run("repositories.commit", {
                        id: current.id,
                        branch: values.branch,
                        expectedHead:
                          values.branch === branch
                            ? String(content?.log || "").split("\t")[0]
                            : "",
                        message: values.message,
                        files: { [values.path]: values.content },
                      });
                      setBranch(values.branch);
                      setPath(values.path);
                      w.close();
                    }}
                  />,
                )
              }
            >
              <Code2 size={15} />
              提交变更
            </Button>
            <Button
              onClick={() =>
                w.open(
                  "发起代码评审",
                  <Form
                    fields={[
                      { name: "title", label: "评审标题", required: true },
                      {
                        name: "branch",
                        label: "源分支",
                        required: true,
                        defaultValue: branch === "main" ? "" : branch,
                      },
                    ]}
                    submit="创建评审"
                    onSubmit={async (values) => {
                      await w.run("reviews.create", {
                        ...values,
                        repositoryId: current.id,
                        goalId: w.goal?.id || "",
                      });
                      w.close();
                    }}
                  />,
                )
              }
            >
              <GitPullRequest size={15} />
              发起评审
            </Button>
          </div>
          <div className="clone-address">
            <span>Clone</span>
            <code>
              {window.location.origin}/git/{current.id}.git
            </code>
          </div>
          {error && <div className="inline-notice">{error}</div>}
          <div className="code-browser">
            <div className="file-list">
              <div className="file-list-heading">文件</div>
              {content?.files.filter(Boolean).map((f: string) => (
                <button
                  type="button"
                  key={f}
                  className={f === path ? "active" : ""}
                  onClick={() => setPath(f)}
                >
                  <FileCode2 size={15} />
                  {f}
                </button>
              ))}
              {content?.files.filter(Boolean).length === 0 && (
                <p>仓库为空，可提交首个文件。</p>
              )}
            </div>
            <div className="file-preview">
              {path ? (
                <>
                  <header>
                    <FileCode2 size={14} />
                    {path}
                  </header>
                  <pre>{content?.content ?? "读取文件…"}</pre>
                </>
              ) : (
                <div className="commit-log">
                  <h3>最近提交</h3>
                  {String(content?.log || "")
                    .split("\n")
                    .filter(Boolean)
                    .map((line) => {
                      const [sha, ...message] = line.split("\t");
                      return (
                        <div key={sha}>
                          <span className="mono">{sha.slice(0, 8)}</span>
                          <span>{message.join("\t")}</span>
                        </div>
                      );
                    })}
                </div>
              )}
            </div>
          </div>
          <DevelopmentPreviews repositoryId={current.id} />
          <SectionHeading
            title="合并评审"
            description="评审绑定具体提交；源分支变化后需要重新审阅。"
          />
          {w.select("review").length ? (
            w.select("review").map((review) => (
              <div className="review-row" key={review.id}>
                <GitPullRequest size={20} />
                <div>
                  <strong>{review.title}</strong>
                  <small>
                    {review.data.branch} → main ·{" "}
                    <span className="mono">{review.data.head.slice(0, 8)}</span>
                  </small>
                </div>
                <Badge status={review.status} />
                <div className="button-group">
                  {review.status === "open" && (
                    <Button
                      secondary
                      onClick={() =>
                        w.open(
                          "批准代码评审",
                          <Confirm
                            description={
                              <p>
                                批准提交 <code>{review.data.head}</code>
                                。提交生产者不能批准自己的代码。
                              </p>
                            }
                            onConfirm={async () => {
                              await w.run("reviews.approve", { id: review.id });
                              w.close();
                            }}
                          />,
                        )
                      }
                    >
                      批准评审
                    </Button>
                  )}
                  {review.status === "approved" && (
                    <Button
                      onClick={() =>
                        w.open(
                          "合并到受保护主分支",
                          <Confirm
                            label="检查并合并"
                            description={
                              <p>
                                平台将重新检查源提交、主分支状态及独立 CI 验证。
                              </p>
                            }
                            onConfirm={async () => {
                              await w.run("reviews.merge", { id: review.id });
                              w.close();
                            }}
                          />,
                        )
                      }
                    >
                      合并
                      <ArrowRight size={14} />
                    </Button>
                  )}
                </div>
              </div>
            ))
          ) : (
            <div className="inline-empty">暂无合并评审</div>
          )}
        </>
      )}
    </>
  );
}

export function Quality() {
  const w = useWorkspace();
  const artifacts = w
    .select("artifact")
    .filter((x) => !w.goal || x.goalId === w.goal.id);
  const evidence = w
    .select("evidence")
    .filter((x) => !w.goal || x.goalId === w.goal.id);
  return (
    <>
      <SectionHeading
        eyebrow="QUALITY & EVIDENCE"
        title="质量与交付证据"
        description="验证结果绑定需求版本、提交和制品，保留实际执行环境与验证方式。"
        action={
          <div className="button-group">
            <Button
              secondary
              disabled={!w.goal || !w.can("developer", "quality", "release")}
              onClick={() => w.open("上传实际制品或报告", <UploadForm />)}
            >
              <Upload size={15} />
              上传制品
            </Button>
            <Button
              disabled={!w.can("quality")}
              onClick={() => w.open("记录独立验收", <EvidenceForm />)}
            >
              <ShieldCheck size={15} />
              记录验收
            </Button>
          </div>
        }
      />
      <QualityOverview
        tasks={w
          .select("task")
          .filter(
            (task) =>
              !w.goal ||
              (task.goalId === w.goal.id &&
                task.data.requirementRevision === w.goal.revision),
          )}
        evidence={evidence.filter(
          (record) =>
            !w.goal || record.data.requirementRevision === w.goal.revision,
        )}
        defects={w
          .select("defect")
          .filter((defect) => !w.goal || defect.goalId === w.goal.id)}
      />
      <QualityScheduling />
      <div className="section-line">
        <h3>
          构建制品与报告 <span>{artifacts.length}</span>
        </h3>
      </div>
      {artifacts.length ? (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>制品</th>
                <th>交付目标</th>
                <th>源提交 / 摘要</th>
                <th>需求版本</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {artifacts.map((x) => (
                <tr key={x.id}>
                  <td>
                    <strong>{x.title}</strong>
                    <small>
                      {x.data.type === "report"
                        ? "报告"
                        : x.data.type === "prototype"
                          ? "交互原型"
                          : x.data.type === "unsigned-package"
                            ? "待签名制品"
                            : "应用制品"}{" "}
                      · {(x.data.size / 1024).toFixed(1)} KB
                    </small>
                  </td>
                  <td>{targets[x.data.target]}</td>
                  <td className="mono">
                    {x.data.commit.slice(0, 8)}
                    <small>{x.data.digest.slice(0, 16)}</small>
                  </td>
                  <td>v{x.data.requirementRevision}</td>
                  <td>
                    {x.data.type === "prototype" && (
                      <a
                        className="text-button"
                        href={"/api/v1/artifacts/" + x.id + "/preview"}
                        target="_blank"
                        rel="noopener noreferrer"
                      >
                        预览
                      </a>
                    )}
                    <a
                      className="icon-button"
                      aria-label={"下载 " + x.title}
                      href={"/api/v1/artifacts/" + x.id + "/content"}
                    >
                      <Download size={16} />
                    </a>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <Empty
          icon={<Upload size={28} />}
          title="暂无实际制品"
          description="构建完成后登记制品，或上传已有应用包与报告。平台会计算内容摘要，并核对代码提交。"
        />
      )}
      <SectionHeading title="独立验证证据" />
      {evidence.length ? (
        evidence.map((x) => (
          <div className="evidence-row" key={x.id}>
            <div className="evidence-icon">
              <ShieldCheck size={20} />
            </div>
            <div>
              <strong>{x.title}</strong>
              <small>
                {targets[x.data.target]} · {methods[x.data.method]} ·{" "}
                {x.data.source === "human-attested"
                  ? "人工记录"
                  : "可信质量节点"}
              </small>
              {x.data.statement && <p>{x.data.statement}</p>}
              <small className="mono">
                {x.data.commit.slice(0, 8)} · {date(x.createdAt)}
              </small>
              {x.data.proofArtifactId && (
                <a
                  className="text-button"
                  href={
                    "/api/v1/artifacts/" + x.data.proofArtifactId + "/content"
                  }
                >
                  <Download size={14} /> 下载验证报告
                </a>
              )}
            </div>
            <Badge status={x.status} />
          </div>
        ))
      ) : (
        <div className="inline-empty">
          没有验证记录。Agent 的完成声明不会自动成为质量证据。
        </div>
      )}
      <SectionHeading
        title="缺陷与独立复测"
        description="失败验证回流至批准范围内的开发工作，修复后重新构建并独立验证。"
      />
      {w
        .select("defect")
        .filter((x) => !w.goal || x.goalId === w.goal.id)
        .map((x) => (
          <button
            type="button"
            key={x.id}
            className="resource-row clickable"
            onClick={() =>
              w.open(
                x.title,
                <div className="detail-content">
                  <Badge status={x.status} />
                  <p>{x.data.reason || "保留原失败记录，复测通过后关闭。"}</p>
                  <p>
                    自动修复 {x.data.repairAttempts}/2 次 · 需求 v
                    {x.data.requirementRevision}
                  </p>
                  <pre className="run-log">{x.data.log || "没有输出日志"}</pre>
                  {x.data.repairCommit && (
                    <p className="mono">修复提交 {x.data.repairCommit}</p>
                  )}
                  <SigningRepair defectId={x.id} />
                </div>,
              )
            }
          >
            <ShieldCheck size={17} />
            <span>
              {x.title}
              <small>
                {x.data.reason || `修复 ${x.data.repairAttempts}/2 次`}
              </small>
            </span>
            <Badge status={x.status} />
          </button>
        ))}
      {!w.select("defect").filter((x) => !w.goal || x.goalId === w.goal.id)
        .length && <div className="inline-empty">暂无独立验证发现的缺陷</div>}
      <SectionHeading
        title="受保护的验证策略"
        description="由质量负责人维护，独立质量节点执行。"
        action={
          <Button
            secondary
            disabled={!w.can("quality")}
            onClick={() =>
              w.open(
                "新增验证策略",
                <Form
                  fields={[
                    { name: "title", label: "策略名称", required: true },
                    {
                      name: "executable",
                      label: "执行程序",
                      required: true,
                      placeholder: "例如 go",
                    },
                    {
                      name: "arguments",
                      label: "参数（每行一项）",
                      type: "textarea",
                      defaultValue: "test\n./...",
                    },
                    {
                      name: "runtime",
                      label: "执行环境",
                      type: "select",
                      options: {
                        linux: "Linux",
                        darwin: "macOS",
                        windows: "Windows",
                        device: "设备",
                      },
                      defaultValue: "linux",
                    },
                    {
                      name: "method",
                      label: "验证方式",
                      type: "select",
                      options: {
                        automated: "自动化",
                        simulator: "模拟器",
                        device: "真机",
                      },
                      defaultValue: "automated",
                    },
                    {
                      name: "verificationEnvironment",
                      label: "实际验证环境",
                      type: "select",
                      defaultValue: "ci",
                      options: {
                        ci: "API / 通用 CI",
                        browser: "真实浏览器",
                        windows: "Windows 目标系统",
                        macos: "macOS 目标系统",
                        "ios-device": "iOS 真机",
                        "android-device": "Android 真机",
                        simulator: "模拟器",
                      },
                      hint: "由受保护的实际测试命令完成相应检查，标记本身不产生验证。",
                    },
                    {
                      name: "reportPath",
                      label: "报告文件路径（可选）",
                      placeholder: "quality/browser.tar.gz",
                      hint: "填写命令产生的相对文件路径。成功时必须有本次报告，失败时保留已产生的报告。",
                    },
                  ]}
                  onSubmit={async (values) => {
                    await w.run("pipelines.create", {
                      projectId: w.project.id,
                      title: values.title,
                      command: [
                        values.executable,
                        ...String(values.arguments).split("\n").filter(Boolean),
                      ],
                      runtime: values.runtime,
                      method: values.method,
                      verificationEnvironment: values.verificationEnvironment,
                      reportPath: values.reportPath || "",
                    });
                    w.close();
                  }}
                />,
              )
            }
          >
            <Plus size={15} />
            新增策略
          </Button>
        }
      />
      {w.select("pipeline").map((x) => (
        <div className="resource-row member-row" key={x.id}>
          <ShieldCheck size={17} />
          <div className="member-identity">
            <strong>{x.title}</strong>
            <small>
              {x.data.runtime} · {methods[x.data.method]} ·{" "}
              <code>{x.data.command.join(" ")}</code>
            </small>
            {x.data.reportPath && <small>报告 {x.data.reportPath}</small>}
          </div>
          <div className="button-group member-actions">
            <QualityBindingButton pipeline={x} />
            <Button
              secondary
              disabled={!w.can("quality")}
              onClick={() =>
                w.open(
                  "发起独立验证",
                  <Form
                    fields={[
                      {
                        name: "artifactId",
                        label: "被验证制品",
                        type: "select",
                        options: Object.fromEntries(
                          artifacts
                            .filter((a) => a.data.type === "package")
                            .map((a) => [
                              a.id,
                              a.title + " · " + targets[a.data.target],
                            ]),
                        ),
                        required: true,
                      },
                      {
                        name: "criteria",
                        label: "验收标准 ID（逗号分隔）",
                        required: true,
                        defaultValue: w.goal?.data.criteria
                          .filter((c: Data) => c.method === x.data.method)
                          .map((c: Data) => c.id)
                          .join(","),
                      },
                    ]}
                    submit="创建验证工作项"
                    onSubmit={async (values) => {
                      await w.run("pipelines.run", {
                        pipelineId: x.id,
                        artifactId: values.artifactId,
                        criteria: String(values.criteria)
                          .split(",")
                          .map((c) => c.trim())
                          .filter(Boolean),
                      });
                      w.close();
                    }}
                  />,
                )
              }
            >
              <Play size={14} />
              发起验证
            </Button>
          </div>
        </div>
      ))}
      {!w.select("pipeline").length && (
        <div className="inline-empty">还没有验证策略</div>
      )}
    </>
  );
}
