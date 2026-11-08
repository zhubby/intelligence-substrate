import React, {
  useCallback,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { createRoot } from "react-dom/client";
import {
  Activity,
  ArrowRight,
  Box,
  Check,
  ChevronDown,
  CircleHelp,
  FolderGit2,
  Layers3,
  LoaderCircle,
  LogOut,
  Menu,
  Plus,
  RefreshCw,
  ShieldCheck,
  Sparkles,
  Target,
  Workflow,
  X,
} from "lucide-react";
import {
  api,
  APIError,
  command,
  login,
  session,
  type Actor,
  type Audit,
  type Data,
  type Integration,
  type Resource,
} from "./api";
import { Workspace, type View } from "./context";
import { Button, Form, Modal, Empty, Breadcrumb } from "./ui";
import { Delivery } from "./pages/Delivery";
import { Code, Quality } from "./pages/Engineering";
import { Agents, Governance, Releases } from "./pages/Operations";
import "./styles.css";
import "./looseleaf.css";

const navigation: {
  id: View;
  title: string;
  icon: typeof Target;
  group: string;
}[] = [
  { id: "workspace", title: "目标与需求", icon: Target, group: "交付空间" },
  { id: "code", title: "代码与评审", icon: FolderGit2, group: "交付空间" },
  { id: "quality", title: "质量与证据", icon: ShieldCheck, group: "交付空间" },
  { id: "releases", title: "构建与发布", icon: Box, group: "交付空间" },
  { id: "agents", title: "Agent 与执行", icon: Workflow, group: "平台管理" },
  { id: "governance", title: "企业治理", icon: Layers3, group: "平台管理" },
];

function Brand({ compact = false }: { compact?: boolean }) {
  return (
    <div className={"brand " + (compact ? "compact" : "")}>
      <span className="brand-mark">
        <Layers3 size={21} />
      </span>
      <div>
        <strong>Intelligence</strong>
        <span>SUBSTRATE</span>
      </div>
    </div>
  );
}

function Login({ onLogin }: { onLogin: (actor: Actor) => void }) {
  const [oidc, setOIDC] = useState(false);
  useEffect(() => {
    api<{ oidc: boolean }>("auth/options")
      .then((v) => setOIDC(v.oidc))
      .catch(() => {});
  }, []);
  return (
    <div className="login-page">
      <div className="login-story">
        <Brand />
        <div className="login-message">
          <span className="eyebrow">ENTERPRISE DELIVERY INFRASTRUCTURE</span>
          <h1>
            让业务目标，
            <br />
            走向真实交付。
          </h1>
          <p>
            连接需求、Agent、代码和验证。
            <br />
            每一步有依据，每次交付可追溯。
          </p>
          <div className="login-flow">
            {["需求", "方案", "开发", "验证", "交付"].map((x, i) => (
              <React.Fragment key={x}>
                <span>{x}</span>
                {i < 4 && <ArrowRight size={16} />}
              </React.Fragment>
            ))}
          </div>
        </div>
        <p className="login-caption">
          INTELLIGENCE SUBSTRATE · 企业智能研发与交付底座
        </p>
      </div>
      <main className="login-panel">
        <div className="login-form">
          <span className="eyebrow">WORKSPACE ACCESS</span>
          <h2>进入研发工作台</h2>
          <p>使用企业账户，继续你的交付工作。</p>
          <Form
            fields={[
              {
                name: "email",
                label: "企业邮箱",
                required: true,
                placeholder: "name@company.com",
              },
              {
                name: "password",
                label: "密码",
                type: "password",
                required: true,
              },
            ]}
            submit="登录工作台"
            onSubmit={async (d) => onLogin(await login(d.email, d.password))}
          />
          {oidc && (
            <a className="button secondary sso-button" href="/auth/oidc/start">
              <ShieldCheck size={16} />
              使用企业单点登录
            </a>
          )}
          <div className="login-note">
            <ShieldCheck size={16} />
            <span>访问、决策和执行操作均受企业权限与审计策略管理。</span>
          </div>
        </div>
      </main>
    </div>
  );
}

function App() {
  const [actor, setActor] = useState<Actor>();
  const [initializing, setInitializing] = useState(true);
  const [projects, setProjects] = useState<Resource[]>([]);
  const [projectId, setProjectID] = useState("");
  const [items, setItems] = useState<Resource[]>([]);
  const [audits, setAudits] = useState<Audit[]>([]);
  const [integrations, setIntegrations] = useState<Integration[]>([]);
  const [goalId, setGoal] = useState("");
  const [view, setView] = useState<View>("workspace");
  const [mobile, setMobile] = useState(false);
  const [mobileViewport, setMobileViewport] = useState(
    () =>
      typeof window !== "undefined" &&
      window.matchMedia("(max-width: 700px)").matches,
  );
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [dialog, setDialog] = useState<{ title: string; node: ReactNode }>();
  const generation = useRef(0);
  const mobileMenuRef = useRef<HTMLButtonElement>(null);
  const mobileCloseRef = useRef<HTMLButtonElement>(null);
  const workspaceFocusRef = useRef<HTMLButtonElement>(null);
  const mobileWasOpen = useRef(false);
  const dialogOpener = useRef<HTMLElement | null>(null);
  const close = useCallback(() => {
    const opener = dialogOpener.current;
    dialogOpener.current = null;
    setDialog(undefined);
    if (opener) {
      window.requestAnimationFrame(() => {
        if (opener.isConnected && !opener.hasAttribute("disabled")) {
          opener.focus();
        }
      });
    }
  }, []);
  const open = useCallback(
    (title: string, node: ReactNode) => {
      const active = document.activeElement;
      dialogOpener.current = active instanceof HTMLElement ? active : null;
      setDialog({ title, node });
    },
    [],
  );
  useEffect(() => {
    const query = window.matchMedia("(max-width: 700px)");
    const update = () => setMobileViewport(query.matches);
    update();
    query.addEventListener("change", update);
    return () => query.removeEventListener("change", update);
  }, []);
  useEffect(() => {
    if (!mobileViewport) {
      const drawerWasOpen = mobile || mobileWasOpen.current;
      mobileWasOpen.current = false;
      setMobile(false);
      if (drawerWasOpen) {
        window.requestAnimationFrame(() => workspaceFocusRef.current?.focus());
      }
      return;
    }
    if (mobile) {
      mobileWasOpen.current = true;
      window.requestAnimationFrame(() => mobileCloseRef.current?.focus());
    } else if (mobileWasOpen.current) {
      mobileWasOpen.current = false;
      window.requestAnimationFrame(() => mobileMenuRef.current?.focus());
    }
  }, [mobile, mobileViewport]);
  useEffect(() => {
    session()
      .then(setActor)
      .catch(() => {})
      .finally(() => setInitializing(false));
  }, []);
  const loadProjects = useCallback(async () => {
    const list = await api<Resource[]>("projects");
    setProjects(list);
    setProjectID((id) =>
      list.some((p) => p.id === id) ? id : list[0]?.id || "",
    );
    return list;
  }, []);
  useEffect(() => {
    if (actor) loadProjects().catch((e) => setError(e.message));
  }, [actor, loadProjects]);
  const refresh = useCallback(async () => {
    if (!projectId) return;
    const version = ++generation.current;
    try {
      const [resources, history, connections] = await Promise.all([
        api<Resource[]>("projects/" + projectId + "/workspace"),
        api<Audit[]>("audit?projectId=" + projectId),
        api<Integration[]>("integrations"),
      ]);
      if (version !== generation.current) return;
      setItems(resources);
      setAudits(history);
      setIntegrations(connections);
      setError("");
      setGoal((id) =>
        resources.some((x) => x.kind === "goal" && x.id === id)
          ? id
          : resources.find((x) => x.kind === "goal")?.id || "",
      );
    } catch (error) {
      if (version !== generation.current) return;
      if (error instanceof APIError && error.status === 401) {
        generation.current++;
        setActor(undefined);
        setProjects([]);
        setProjectID("");
        setItems([]);
        setAudits([]);
        setGoal("");
        close();
        setError("");
        return;
      }
      if (error instanceof APIError && [403, 404].includes(error.status)) {
        const list = await api<Resource[]>("projects");
        if (version !== generation.current) return;
        if (!list.some((project) => project.id === projectId)) {
          generation.current++;
          setProjects(list);
          setProjectID(list[0]?.id || "");
          setItems([]);
          setAudits([]);
          setGoal("");
          close();
          setError("");
          setNotice("项目访问权限已变更，已更新可访问项目");
          return;
        }
      }
      throw error;
    }
  }, [projectId, close]);
  useEffect(() => {
    generation.current++;
    setItems([]);
    setAudits([]);
    setGoal("");
    if (!projectId) return;
    setLoading(true);
    refresh()
      .catch((e) => setError(e.message))
      .finally(() => setLoading(false));
    const timer = setInterval(() => {
      if (!document.hidden) refresh().catch((e) => setError(e.message));
    }, 6000);
    return () => {
      clearInterval(timer);
      generation.current++;
    };
  }, [projectId, refresh]);
  useEffect(() => {
    if (!notice) return;
    const timer = setTimeout(() => setNotice(""), 4500);
    return () => clearTimeout(timer);
  }, [notice]);
  const run = useCallback(
    async (name: string, data: Data) => {
      const r = await command(name, data);
      await refresh();
      return r;
    },
    [refresh],
  );
  const project =
    items.find((x) => x.kind === "project" && x.id === projectId) ||
    projects.find((x) => x.id === projectId);
  const goal = items.find((x) => x.kind === "goal" && x.id === goalId);
  const createProject = () =>
    open(
      "创建企业项目",
      <Form
        fields={[
          {
            name: "title",
            label: "项目名称",
            required: true,
            placeholder: "例如：客户服务平台",
          },
          {
            name: "description",
            label: "项目背景",
            type: "textarea",
            required: true,
          },
          {
            name: "allowExternalModels",
            label: "允许按企业策略访问外部模型",
            type: "checkbox",
            hint: "未授权时，模型网关会拒绝此项目的外部模型请求。",
          },
        ]}
        submit="创建项目"
        onSubmit={async (d) => {
          const p = await command("projects.create", d);
          await loadProjects();
          setProjectID(p.id);
          setView("workspace");
          close();
          setNotice("项目已创建，可以开始定义交付目标");
        }}
      />,
    );
  const navigate = (next: View) => {
    setView(next);
    setMobile(false);
  };
  if (initializing)
    return (
      <div className="boot-screen">
        <Brand />
        <LoaderCircle className="spin" size={24} />
        <p>正在连接工作台…</p>
      </div>
    );
  if (!actor) return <Login onLogin={setActor} />;
  const current = navigation.find((x) => x.id === view)!;
  const decisions = items.filter(
    (x) =>
      x.status === "awaiting_baseline" ||
      x.status === "proposed" ||
      x.status === "awaiting_approval",
  ).length;
  const body = (
    <div className="app-shell">
      {mobileViewport && mobile && (
        <button
          type="button"
          className="sidebar-scrim"
          aria-label="收起导航遮罩"
          onClick={() => setMobile(false)}
        />
      )}
      <aside
        className={"sidebar " + (mobile ? "mobile-open" : "")}
        aria-hidden={mobileViewport && !mobile ? true : undefined}
        inert={mobileViewport && !mobile ? true : undefined}
      >
        <div className="sidebar-top">
          <Brand compact />
          <button
            type="button"
            className="icon-button mobile-close"
            aria-label="关闭导航"
            ref={mobileCloseRef}
            onClick={() => setMobile(false)}
          >
            <X size={19} />
          </button>
        </div>
        <div className="project-switch">
          <span className="eyebrow">当前项目</span>
          <div>
            <span className="project-symbol">
              {project?.title.slice(0, 1) || "P"}
            </span>
            <select
              aria-label="选择项目"
              value={projectId}
              onChange={(e) => {
                setProjectID(e.target.value);
                close();
              }}
            >
              {projects.length ? (
                projects.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.title}
                  </option>
                ))
              ) : (
                <option value="">尚无项目</option>
              )}
            </select>
            <ChevronDown size={14} />
          </div>
          {actor.admin && (
            <button type="button" className="text-button" onClick={createProject}>
              <Plus size={13} />
              新建项目
            </button>
          )}
        </div>
        <nav aria-label="主导航">
          {["交付空间", "平台管理"].map((group) => (
            <div className="nav-group" key={group}>
              <span className="nav-label">{group}</span>
              {navigation
                .filter((x) => x.group === group)
                .map((x) => (
                  <button
                    type="button"
                    key={x.id}
                    className={view === x.id ? "nav-item active" : "nav-item"}
                    onClick={() => navigate(x.id)}
                  >
                    <x.icon size={18} />
                    <span>{x.title}</span>
                    {x.id === "workspace" && decisions > 0 && (
                      <span className="nav-count">{decisions}</span>
                    )}
                  </button>
                ))}
            </div>
          ))}
        </nav>
        <div className="sidebar-bottom">
          <div className="environment-note">
            <span className="environment-dot" />
            <span>企业内部工作空间</span>
          </div>
          <button
            type="button"
            className="profile-button"
            onClick={() =>
              open(
                "账户与会话",
                <div className="detail-content">
                  <h3>{actor.name}</h3>
                  <p className="muted">
                    {actor.admin ? "平台管理员" : "企业成员"} · {actor.id}
                  </p>
                  <Button
                    secondary
                    onClick={async () => {
                      await api("auth/logout", { method: "POST", body: "{}" });
                      close();
                      setActor(undefined);
                      setProjects([]);
                      setProjectID("");
                      setItems([]);
                    }}
                  >
                    <LogOut size={15} />
                    退出登录
                  </Button>
                </div>,
              )
            }
          >
            <span className="profile-avatar">
              {actor.name.slice(0, 1).toUpperCase()}
            </span>
            <span>
              {actor.name}
              <small>{actor.admin ? "平台管理员" : "企业成员"}</small>
            </span>
            <ChevronDown size={13} />
          </button>
        </div>
      </aside>
      <div className="main-shell">
        <header className="topbar">
          <button
            type="button"
            className="icon-button mobile-menu"
            aria-label="打开导航"
            ref={mobileMenuRef}
            onClick={() => setMobile(true)}
          >
            <Menu size={20} />
          </button>
          <Breadcrumb>
            {project?.title || "项目空间"}
            <span className="breadcrumb-divider">/</span>
            <strong>{current.title}</strong>
          </Breadcrumb>
          <div className="topbar-actions">
            {items.some((x) => x.kind === "goal") && (
              <select
                aria-label="选择交付目标"
                className="goal-select"
                value={goalId}
                onChange={(e) => setGoal(e.target.value)}
              >
                {items
                  .filter((x) => x.kind === "goal")
                  .map((x) => (
                    <option key={x.id} value={x.id}>
                      {x.title}
                    </option>
                  ))}
              </select>
            )}
            <button
              type="button"
              className="icon-button"
              aria-label="刷新工作空间"
            ref={workspaceFocusRef}
              onClick={() => refresh().catch((e) => setError(e.message))}
            >
              <RefreshCw size={16} />
            </button>
            <button
              type="button"
              className="icon-button"
              aria-label="查看操作说明"
              onClick={() =>
                open(
                  "工作台使用说明",
                  <div className="detail-content help-copy">
                    <h3>从目标开始</h3>
                    <p>
                      创建项目和交付目标，定义验收条件与必选渠道。批准需求基线和方案后，授权的执行节点可以领取工作项。
                    </p>
                    <h3>以证据推进</h3>
                    <p>
                      代码提交、制品和验收结果绑定具体版本。人工验收需要独立质量身份和实际报告。正式发布另行批准，每个目标经发布后验证才能完成交付。
                    </p>
                    <h3>连接执行环境</h3>
                    <p>
                      在「Agent
                      与执行」注册节点并签发凭据，按部署文档启动工作进程。未配置模型、构建或发布服务时，平台保留真实阻塞状态。
                    </p>
                  </div>,
                )
              }
            >
              <CircleHelp size={17} />
            </button>
          </div>
        </header>
        {error && (
          <div className="global-error" role="alert">
            <span>{error}</span>
            <button
              type="button"
              className="text-button"
              onClick={() => {
                setError("");
                if (projectId) refresh().catch((e) => setError(e.message));
                else loadProjects().catch((e) => setError(e.message));
              }}
            >
              重试
            </button>
          </div>
        )}
        <main id="main-content" className="main-content" key={projectId}>
          {!project ? (
            <div className="welcome-space">
              <span className="eyebrow">INTELLIGENCE SUBSTRATE</span>
              <h1>每次交付，从这里开始。</h1>
              <p>把业务目标、协作开发和交付证据连接到一个受控的工作空间。</p>
              <Empty
                icon={<Sparkles size={30} />}
                title={actor.admin ? "创建第一个项目" : "等待项目授权"}
                description={
                  actor.admin
                    ? "建立项目范围与模型访问策略，然后定义首个业务目标。"
                    : "请项目负责人为此企业账户授予项目角色。"
                }
                action={
                  actor.admin ? (
                    <Button onClick={createProject}>
                      <Plus size={16} />
                      创建项目
                    </Button>
                  ) : undefined
                }
              />
            </div>
          ) : loading ? (
            <div className="loading-panel">
              <LoaderCircle className="spin" size={24} />
              <p>正在加载项目空间…</p>
            </div>
          ) : view === "workspace" ? (
            <Delivery />
          ) : view === "code" ? (
            <Code />
          ) : view === "quality" ? (
            <Quality />
          ) : view === "releases" ? (
            <Releases />
          ) : view === "agents" ? (
            <Agents />
          ) : (
            <Governance />
          )}
        </main>
        <footer className="app-footer">
          <span>Intelligence Substrate</span>
          <span>
            <Activity size={12} />
            交付状态以验证记录为准
          </span>
        </footer>
      </div>
      {dialog && (
        <Modal title={dialog.title} onClose={close}>
          {dialog.node}
        </Modal>
      )}
      {notice && (
        <div className="toast" role="status">
          <Check size={16} />
          {notice}
        </div>
      )}
    </div>
  );
  return project ? (
    <Workspace.Provider
      value={{
        actor,
        project,
        items,
        audits,
        integrations,
        goal,
        setGoal,
        select: (kind) => items.filter((x) => x.kind === kind),
        refresh,
        run,
        open,
        close,
        navigate,
        notify: setNotice,
        can: (...wanted: string[]) => {
          if (actor.admin) return true;
          const roles =
            project.data.effectiveRoles ||
            actor.roles ||
            items.find(
              (x) =>
                x.kind === "member" &&
                x.status === "active" &&
                x.data.identityId === actor.id,
            )?.data.roles ||
            [];
          return (
            roles.includes("owner") || wanted.some((r) => roles.includes(r))
          );
        },
      }}
    >
      {body}
    </Workspace.Provider>
  ) : (
    body
  );
}

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
