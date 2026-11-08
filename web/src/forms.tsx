import { useState, type FormEvent } from "react";
import { LoaderCircle, Plus, Trash2 } from "lucide-react";
import {
  command,
  methods,
  roles,
  targets,
  upload,
  type Resource,
  type Data,
} from "./api";
import { Button, Form } from "./ui";
import { useWorkspace } from "./context";

export function GoalForm({ goal }: { goal?: Resource }) {
  const w = useWorkspace();
  const [criteria, setCriteria] = useState<Data[]>(
    goal?.data.criteria || [
      { id: "criterion_1", text: "", method: "automated" },
    ],
  );
  const [chosen, setChosen] = useState<string[]>(
    goal?.data.targets || Object.keys(targets),
  );
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setError("");
    const data = new FormData(e.currentTarget);
    setPending(true);
    try {
      const g = await w.run(goal ? "goals.revise" : "goals.create", {
        id: goal?.id,
        reason: data.get("reason"),
        projectId: w.project.id,
        title: data.get("title"),
        outcome: data.get("outcome"),
        expectedRequirementRevision: goal?.revision,
        requirementBrief: data.get("requirementBrief") ?? undefined,
        budget: Number(data.get("budget")),
        criteria:
          !goal && criteria.every((c) => !c.text.trim()) ? undefined : criteria,
        targets: chosen,
      });
      w.setGoal(g.id);
      w.close();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setPending(false);
    }
  }
  return (
    <form className="form" onSubmit={submit}>
      <label className="field">
        <span>交付目标</span>
        <input
          name="title"
          required
          defaultValue={goal?.title}
          disabled={!!goal}
          placeholder="例如：面向客户的多端服务平台"
        />
      </label>
      <label className="field">
        <span>业务结果</span>
        <textarea
          name="outcome"
          required
          defaultValue={goal?.data.outcome}
          rows={4}
          placeholder="谁会使用这个应用？要完成什么业务？怎样判断成功？"
        />
      </label>
      {goal?.data.requirementBrief && (
        <label className="field">
          <span>需求说明</span>
          <textarea
            name="requirementBrief"
            aria-label="需求说明"
            defaultValue={goal.data.requirementBrief}
            rows={5}
            maxLength={16000}
          />
          <small>
            手工变更后的说明由本次编辑负责，原需求稿保留在历史版本。
          </small>
        </label>
      )}
      <div className="field">
        <span>验收标准{!goal && "（可稍后整理）"}</span>
        {!goal && (
          <small>
            可以先记录业务目标，澄清后整理验收标准；批准基线前必须补齐。
          </small>
        )}
        {criteria.map((c, i) => (
          <div className="criterion-editor" key={c.id}>
            <input
              aria-label={"验收标准 " + (i + 1)}
              required={!!goal || criteria.some((c) => c.text.trim())}
              value={c.text}
              placeholder="描述一项可验证的用户行为"
              onChange={(e) =>
                setCriteria(
                  criteria.map((x) =>
                    x.id === c.id ? { ...x, text: e.target.value } : x,
                  ),
                )
              }
            />
            <select
              aria-label={"验证方式 " + (i + 1)}
              value={c.method}
              onChange={(e) =>
                setCriteria(
                  criteria.map((x) =>
                    x.id === c.id ? { ...x, method: e.target.value } : x,
                  ),
                )
              }
            >
              {Object.entries(methods).map(([k, v]) => (
                <option value={k} key={k}>
                  {v}
                </option>
              ))}
            </select>
            <button
              type="button"
              className="icon-button"
              aria-label="移除验收标准"
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
          onClick={() =>
            setCriteria([
              ...criteria,
              {
                id: "criterion_" + crypto.randomUUID().slice(0, 8),
                text: "",
                method: "automated",
              },
            ])
          }
        >
          <Plus size={14} />
          添加验收标准
        </button>
      </div>
      <fieldset className="target-picker">
        <legend>必选交付目标</legend>
        {Object.entries(targets).map(([k, v]) => (
          <label key={k}>
            <input
              type="checkbox"
              checked={chosen.includes(k)}
              onChange={(e) =>
                setChosen(
                  e.target.checked
                    ? [...chosen, k]
                    : chosen.filter((x) => x !== k),
                )
              }
            />
            {v}
          </label>
        ))}
      </fieldset>
      <label className="field">
        <span>执行预算（USD）</span>
        <input
          name="budget"
          aria-label="执行预算（USD）"
          type="number"
          min="0.01"
          max="1000000"
          step="0.01"
          defaultValue={goal ? goal.data.budgetCents / 100 : 50}
          required
        />
        <small>预算由控制面执行，增加预算需要变更需求基线。</small>
      </label>
      {goal && (
        <label className="field">
          <span>变更原因</span>
          <textarea name="reason" aria-label="变更原因" required rows={3} />
          <small>变更后，方案、证据和批准需要重新确认。</small>
        </label>
      )}
      {error && (
        <div role="alert" className="form-error">
          {error}
        </div>
      )}
      <footer>
        <Button type="submit" disabled={pending || !chosen.length}>
          {pending ? (
            <LoaderCircle className="spin" size={16} />
          ) : (
            <Plus size={16} />
          )}{" "}
          {goal ? "创建新需求版本" : "创建交付目标"}
        </Button>
      </footer>
    </form>
  );
}

export function PlanForm({
  design,
  blueprint,
  initializeBlueprint = false,
}: {
  design?: Resource;
  blueprint?: Resource;
  initializeBlueprint?: boolean;
} = {}) {
  const w = useWorkspace();
  const [captured] = useState(() => ({
    goal: w.goal!,
    design,
    blueprint,
    initializeBlueprint,
  }));
  const [tasks, setTasks] = useState<Data[]>([
    {
      id: "design",
      title: "确定应用结构与交互方案",
      role: "architect",
      runtime: "linux",
      kind: "design",
      dependsOn: [],
    },
    {
      id: "implement",
      title: "实现业务功能与多端应用",
      role: "developer",
      runtime: "linux",
      kind: "code",
      dependsOn: ["design"],
    },
  ]);
  const options = Object.fromEntries(
    Object.entries(roles).filter(
      ([k]) => !["owner", "product", "viewer"].includes(k),
    ),
  );
  return (
    <Form
      fields={[
        {
          name: "repositoryId",
          label: "源代码仓库（可选）",
          type: "select",
          options: Object.fromEntries(
            w.select("repository").map((x) => [x.id, x.title]),
          ),
          hint: "编码任务未指定仓库时，执行节点可创建应用仓库。",
        },
        {
          name: "summary",
          label: "方案与交付路径",
          type: "textarea",
          required: true,
          placeholder: "说明应用架构、共享能力、各端差异与验证路径",
        },
        { name: "risk", label: "风险与待澄清事项", type: "textarea" },
        {
          name: "reason",
          label: "新增或调整原因",
          defaultValue: "首次提交交付方案",
        },
      ]}
      submit="提交方案审阅"
      onSubmit={async (data) => {
        await w.run("plans.propose", {
          ...data,
          goalId: captured.goal.id,
          expectedRequirementRevision: captured.goal.revision,
          ...(captured.blueprint
            ? {
                blueprintId: captured.blueprint.id,
                expectedBlueprintBinding: captured.blueprint.data.binding,
                initializeBlueprint: captured.initializeBlueprint,
              }
            : {}),
          ...(captured.design
            ? {
                designId: captured.design.id,
                expectedDesignBinding: captured.design.data.binding,
              }
            : {}),
          tasks: tasks.map(({ writeScopeMode, writePathsText, ...t }) => ({
            ...t,
            repositoryId: data.repositoryId || undefined,
            ...(t.kind === "code" && writeScopeMode === "paths"
              ? {
                  writePaths: String(writePathsText || "")
                    .split("\n")
                    .map((path) => path.trim())
                    .filter(Boolean),
                }
              : {}),
            command: String(t.commandText || "")
              .split("\n")
              .filter(Boolean),
          })),
        });
        w.close();
      }}
    >
      <div className="field">
        <span>工作项与依赖</span>
        {tasks.map((task, i) => (
          <div className="task-editor" key={task.id}>
            <div className="task-editor-heading">
              <span className="mono">{String(i + 1).padStart(2, "0")}</span>
              <input
                aria-label={"工作项 " + (i + 1)}
                required
                value={task.title}
                onChange={(e) =>
                  setTasks(
                    tasks.map((t) =>
                      t.id === task.id ? { ...t, title: e.target.value } : t,
                    ),
                  )
                }
              />
              <button
                type="button"
                className="icon-button"
                aria-label="移除工作项"
                onClick={() =>
                  setTasks(
                    tasks
                      .filter((t) => t.id !== task.id)
                      .map((t) => ({
                        ...t,
                        dependsOn: t.dependsOn.filter(
                          (d: string) => d !== task.id,
                        ),
                      })),
                  )
                }
              >
                <Trash2 size={15} />
              </button>
            </div>
            <div className="task-editor-options">
              <select
                aria-label="执行角色"
                value={task.role}
                onChange={(e) =>
                  setTasks(
                    tasks.map((t) =>
                      t.id === task.id ? { ...t, role: e.target.value } : t,
                    ),
                  )
                }
              >
                {Object.entries(options).map(([k, v]) => (
                  <option key={k} value={k}>
                    {v}
                  </option>
                ))}
              </select>
              <select
                aria-label="任务类型"
                value={task.kind}
                onChange={(e) =>
                  setTasks(
                    tasks.map((t) =>
                      t.id === task.id ? { ...t, kind: e.target.value } : t,
                    ),
                  )
                }
              >
                {Object.entries({
                  analysis: "分析",
                  design: "设计",
                  code: "编码",
                  build: "构建",
                  test: "验证",
                  security: "安全",
                  manual: "人工任务",
                }).map(([k, v]) => (
                  <option key={k} value={k}>
                    {v}
                  </option>
                ))}
              </select>
              <select
                aria-label="执行环境"
                value={task.runtime}
                onChange={(e) =>
                  setTasks(
                    tasks.map((t) =>
                      t.id === task.id ? { ...t, runtime: e.target.value } : t,
                    ),
                  )
                }
              >
                {Object.entries({
                  linux: "Linux",
                  darwin: "macOS",
                  windows: "Windows",
                  device: "设备",
                  manual: "人工",
                }).map(([k, v]) => (
                  <option key={k} value={k}>
                    {v}
                  </option>
                ))}
              </select>
            </div>
            <div className="task-command-editor">
              {task.kind === "code" && (
                <>
                  <label className="field">
                    <span>源码修改范围</span>
                    <select
                      aria-label="源码修改范围"
                      value={task.writeScopeMode || "repository"}
                      onChange={(e) =>
                        setTasks(
                          tasks.map((t) =>
                            t.id === task.id
                              ? { ...t, writeScopeMode: e.target.value }
                              : t,
                          ),
                        )
                      }
                    >
                      <option value="repository">全仓库</option>
                      <option value="paths">指定文件或目录</option>
                    </select>
                    <small>扩大已批准范围需要提交新方案。</small>
                  </label>
                  {task.writeScopeMode === "paths" && (
                    <label className="field">
                      <span>允许修改的路径</span>
                      <textarea
                        aria-label="允许修改的路径"
                        required
                        rows={3}
                        value={task.writePathsText || ""}
                        onChange={(e) =>
                          setTasks(
                            tasks.map((t) =>
                              t.id === task.id
                                ? { ...t, writePathsText: e.target.value }
                                : t,
                            ),
                          )
                        }
                        placeholder={"backend/\napp/[id]/page.tsx"}
                      />
                      <small>
                        每行一项，最多128项。文件精确匹配；目录以 /
                        结尾，包含其下文件。不支持通配符。删除、执行权限和集成带入的变更也受此范围约束。
                      </small>
                    </label>
                  )}
                </>
              )}
              <label className="field">
                <span>执行命令（可选，每行一项参数）</span>
                <textarea
                  aria-label="任务命令"
                  rows={2}
                  value={task.commandText || ""}
                  onChange={(e) =>
                    setTasks(
                      tasks.map((t) =>
                        t.id === task.id
                          ? { ...t, commandText: e.target.value }
                          : t,
                      ),
                    )
                  }
                  placeholder={"例如：\ngo\ntest\n./..."}
                />
              </label>
              {task.kind === "build" && (
                <div className="task-editor-options">
                  <input
                    aria-label="制品输出路径"
                    placeholder="制品输出路径"
                    value={task.outputPath || ""}
                    onChange={(e) =>
                      setTasks(
                        tasks.map((t) =>
                          t.id === task.id
                            ? { ...t, outputPath: e.target.value }
                            : t,
                        ),
                      )
                    }
                  />
                  <select
                    aria-label="构建目标"
                    value={task.target || ""}
                    onChange={(e) =>
                      setTasks(
                        tasks.map((t) =>
                          t.id === task.id
                            ? { ...t, target: e.target.value }
                            : t,
                        ),
                      )
                    }
                  >
                    <option value="">选择交付目标</option>
                    {Object.entries(targets).map(([k, v]) => (
                      <option key={k} value={k}>
                        {v}
                      </option>
                    ))}
                  </select>
                </div>
              )}
            </div>
            <div className="task-dependencies">
              <span>依赖</span>
              {tasks
                .filter((t) => t.id !== task.id)
                .map((other) => (
                  <label key={other.id}>
                    <input
                      type="checkbox"
                      checked={task.dependsOn.includes(other.id)}
                      onChange={(e) =>
                        setTasks(
                          tasks.map((t) =>
                            t.id === task.id
                              ? {
                                  ...t,
                                  dependsOn: e.target.checked
                                    ? [...t.dependsOn, other.id]
                                    : t.dependsOn.filter(
                                        (x: string) => x !== other.id,
                                      ),
                                }
                              : t,
                          ),
                        )
                      }
                    />
                    {other.title}
                  </label>
                ))}
            </div>
          </div>
        ))}
        <button
          className="text-button"
          type="button"
          onClick={() =>
            setTasks([
              ...tasks,
              {
                id: "task_" + crypto.randomUUID().slice(0, 8),
                title: "",
                role: "developer",
                runtime: "linux",
                kind: "code",
                dependsOn: [],
              },
            ])
          }
        >
          <Plus size={14} />
          添加工作项
        </button>
        <small>
          提交后由人审阅。工作项执行、测试验证和正式发布分别受平台策略约束。
        </small>
      </div>
    </Form>
  );
}

export function UploadForm() {
  const w = useWorkspace();
  const repos = w.select("repository");
  return (
    <Form
      fields={[
        {
          name: "goalId",
          label: "关联交付目标",
          type: "select",
          options: Object.fromEntries(
            w.select("goal").map((x) => [x.id, x.title]),
          ),
          defaultValue: w.goal?.id,
          required: true,
        },
        {
          name: "repositoryId",
          label: "源代码仓库",
          type: "select",
          options: Object.fromEntries(repos.map((x) => [x.id, x.title])),
          required: true,
        },
        {
          name: "commit",
          label: "源提交 SHA",
          required: true,
          hint: "使用完整提交，平台会核对仓库中的实际内容。",
        },
        {
          name: "target",
          label: "交付目标",
          type: "select",
          options: targets,
          required: true,
        },
        {
          name: "type",
          label: "制品类型",
          type: "select",
          options: {
            package: "应用制品",
            report: "验证或渠道报告",
            prototype: "原型",
          },
          defaultValue: "package",
        },
        { name: "file", label: "文件", type: "file", required: true },
      ]}
      submit="上传并校验制品"
      onSubmit={async (values) => {
        const file = values.file as File;
        delete values.file;
        await upload({ ...values, name: file.name }, file);
        await w.refresh();
        w.close();
      }}
    />
  );
}

export function EvidenceForm() {
  const w = useWorkspace();
  return (
    <Form
      fields={[
        {
          name: "artifactId",
          label: "被验证的应用制品",
          type: "select",
          options: Object.fromEntries(
            w
              .select("artifact")
              .filter((x) => x.data.type === "package")
              .map((x) => [
                x.id,
                x.title + " · " + (targets[x.data.target] || x.data.target),
              ]),
          ),
          required: true,
        },
        {
          name: "proofArtifactId",
          label: "实际验收报告",
          type: "select",
          options: Object.fromEntries(
            w
              .select("artifact")
              .filter((x) => x.data.type === "report")
              .map((x) => [x.id, x.title]),
          ),
          required: true,
        },
        {
          name: "method",
          label: "验证方式",
          type: "select",
          options: { manual: "人工验收", device: "人工记录的真机验收" },
          defaultValue: "manual",
        },
        {
          name: "criteria",
          label: "关联验收标准 ID",
          hint: "需求验收必填，多个 ID 使用逗号分隔；目标环境验收与发布后验证可留空。",
        },
        {
          name: "purpose",
          label: "验证目的",
          type: "select",
          options: {
            acceptance: "需求验收",
            environment: "目标环境验收",
            publication: "发布后验证",
          },
          defaultValue: "acceptance",
        },
        {
          name: "verificationEnvironment",
          label: "实际验证环境",
          type: "select",
          required: true,
          options: {
            ci: "API / 通用 CI",
            browser: "真实浏览器",
            windows: "Windows 目标系统",
            macos: "macOS 目标系统",
            "ios-device": "iOS 真机",
            "android-device": "Android 真机",
            simulator: "模拟器",
          },
          hint: "按实际过程选择。移动真机同时选择真机验证方式；模拟器和普通 CI 不能替代目标环境验收。",
        },
        {
          name: "externalId",
          label: "外部发布版本标识",
          hint: "发布后验证需要与渠道记录完全一致。",
        },
        {
          name: "statement",
          label: "验证过程与观察结果",
          type: "textarea",
          required: true,
        },
      ]}
      submit="登记独立验收证据"
      onSubmit={async (values) => {
        const publication =
          values.purpose === "publication"
            ? w
                .select("release")
                .flatMap((release) =>
                  release.data.targets
                    .filter(
                      (target: any) =>
                        target.status === "published" &&
                        target.artifactId === values.artifactId &&
                        target.externalId === values.externalId,
                    )
                    .map((target: any) => ({ release, target })),
                )
            : [];
        const materialPublication = publication.filter(
          (x) => x.target.materialRevisionId,
        );
        if (materialPublication.length > 1)
          throw new Error("存在多份相同外部版本的资料批准，请先核对发布候选。");
        await w.run("evidence.record", {
          ...values,
          ...(materialPublication.length === 1
            ? {
                releaseId: materialPublication[0].release.id,
                publicationBinding:
                  materialPublication[0].target.publicationBinding,
              }
            : {}),
          criteria: String(values.criteria)
            .split(",")
            .map((x) => x.trim())
            .filter(Boolean),
        });
        w.close();
      }}
    />
  );
}

export function WorkerForm() {
  const w = useWorkspace();
  return (
    <Form
      fields={[
        { name: "title", label: "节点名称", required: true },
        {
          name: "runtime",
          label: "执行系统",
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
          name: "role",
          label: "执行角色",
          type: "select",
          options: {
            analysis: "需求分析",
            design: "设计",
            developer: "开发",
            quality: "质量",
            architect: "架构",
            security: "安全",
            release: "发布",
          },
          defaultValue: "developer",
        },
        {
          name: "capabilities",
          label: "任务能力",
          required: true,
          defaultValue: "code,build",
          hint: "可选 analysis、design、code、build、test、security、publish，逗号分隔。",
        },
        {
          name: "trustedQuality",
          label: "授权为独立质量节点",
          type: "checkbox",
          hint: "仅为可信、隔离的质量执行环境授权；它产生的结果可用于质量门禁。",
        },
      ]}
      onSubmit={async (values) => {
        await w.run("workers.register", {
          projectId: w.project.id,
          ...values,
          roles: [values.role],
          capabilities: String(values.capabilities)
            .split(",")
            .map((x) => x.trim())
            .filter(Boolean),
        });
        w.close();
      }}
      submit="注册执行节点"
    />
  );
}

export function ReleaseForm() {
  const w = useWorkspace();
  const storeMaterials = w
    .select("artifact")
    .filter(
      (x) =>
        x.data.type === "report" &&
        x.data.target === "ios-appstore" &&
        x.goalId === w.goal?.id &&
        x.data.requirementRevision === w.goal?.revision,
    );
  const artifacts = w
    .select("artifact")
    .filter(
      (x) =>
        x.data.type === "package" &&
        x.goalId === w.goal?.id &&
        x.data.requirementRevision === w.goal.revision,
    );
  const [selected, setSelected] = useState<string[]>(
    artifacts.map((x) => x.id),
  );
  return (
    <Form
      fields={[
        {
          name: "version",
          label: "版本号",
          required: true,
          placeholder: "1.0.0",
        },
        {
          name: "commit",
          label: "候选代码提交 SHA",
          required: true,
          defaultValue: artifacts[0]?.data.commit,
        },
        ...(w.goal?.data.targets.includes("ios-appstore")
          ? [
              {
                name: "storeMaterialId",
                label: "App Store 资料清单",
                type: "select" as const,
                options: {
                  "": "未绑定（使用企业预备资料）",
                  ...Object.fromEntries(
                    storeMaterials.map((a) => [
                      a.id,
                      a.title + " · " + a.data.digest.slice(0, 12),
                    ]),
                  ),
                },
              },
            ]
          : []),
      ]}
      submit="检查质量并创建候选版本"
      onSubmit={async (values) => {
        const { storeMaterialId, ...candidate } = values;
        await w.run("releases.create", {
          ...candidate,
          goalId: w.goal!.id,
          artifactIds: selected,
          storeMetadataArtifactIds: storeMaterialId ? [storeMaterialId] : [],
        });
        w.close();
      }}
    >
      <fieldset className="artifact-picker">
        <legend>绑定不可变制品</legend>
        {artifacts.length ? (
          artifacts.map((a) => (
            <label key={a.id}>
              <input
                type="checkbox"
                checked={selected.includes(a.id)}
                onChange={(e) =>
                  setSelected(
                    e.target.checked
                      ? [...selected, a.id]
                      : selected.filter((x) => x !== a.id),
                  )
                }
              />
              <span>
                {a.title}
                <small>
                  {targets[a.data.target]} · {a.data.digest.slice(0, 12)}
                </small>
              </span>
            </label>
          ))
        ) : (
          <p>当前目标还没有候选制品，请先构建或上传。</p>
        )}
      </fieldset>
      <p className="form-note">
        平台会检查每个必选目标、独立验收证据和当前需求版本，创建候选不触发发布。
      </p>
      {w.goal?.data.targets.includes("ios-appstore") && (
        <p className="form-note">
          先在质量中心上传同一提交的 JSON
          资料清单；启用资料管理的发布节点要求审批中的清单一致。
        </p>
      )}
    </Form>
  );
}

export function ChannelForm({
  release,
  target,
}: {
  release: Resource;
  target: Data;
}) {
  const w = useWorkspace();
  return (
    <Form
      fields={[
        {
          name: "state",
          label: "实际渠道状态",
          type: "select",
          options: {
            ...(target.status === "reconciliation_required" ||
            target.status === "rejected" ||
            target.status === "halted"
              ? { ready: "已核对，可以重新发布" }
              : {}),
            submitting: "正在提交",
            processing: "已上传，等待构建处理",
            awaiting_review: "已提交，等待审核",
            awaiting_release: "审核已处理，等待发布",
            rejected: "审核未通过",
            published: "已正式发布",
            halted: "暂停发布",
            reconciliation_required: "结果不确定，等待核对",
          },
          required: true,
        },
        { name: "externalId", label: "渠道版本或审核编号" },
        {
          name: "proofArtifactId",
          label: "渠道操作报告",
          type: "select",
          options: Object.fromEntries(
            w
              .select("artifact")
              .filter((x) => x.data.type === "report")
              .map((x) => [x.id, x.title]),
          ),
        },
        { name: "reason", label: "说明与阻塞原因", type: "textarea" },
      ]}
      submit="记录渠道实际结果"
      onSubmit={async (values) => {
        await w.run(
          target.status === "reconciliation_required" ||
            values.state === "ready"
            ? "releases.reconcile"
            : "releases.transition",
          { ...values, id: release.id, target: target.target },
        );
        w.close();
      }}
    />
  );
}
