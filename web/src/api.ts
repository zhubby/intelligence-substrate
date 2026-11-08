export type Data = Record<string, any>;
export interface Resource {
  id: string;
  kind: string;
  projectId: string;
  goalId: string;
  title: string;
  status: string;
  revision: number;
  data: Data;
  createdAt: string;
  updatedAt: string;
}
export interface Actor {
  id: string;
  name: string;
  kind: string;
  admin: boolean;
  projectId?: string;
  roles?: string[];
}
export interface Audit {
  id: string;
  actorId: string;
  action: string;
  resourceId: string;
  at: string;
  detail: Data;
  hash: string;
  previous: string;
}
export interface Integration {
  id: string;
  title: string;
  status: string;
  reason: string;
  verified: boolean;
  fullstackConfigured?: boolean;
}
let csrf = "";
export class APIError extends Error {
  constructor(
    public code: string,
    message: string,
    public status: number,
  ) {
    super(message);
  }
}
export async function api<T>(
  path: string,
  options: RequestInit = {},
): Promise<T> {
  const response = await fetch("/api/v1/" + path, {
    credentials: "same-origin",
    signal: AbortSignal.timeout(20000),
    ...options,
    headers: {
      ...(options.body instanceof File
        ? {}
        : { "Content-Type": "application/json" }),
      "X-CSRF-Token": csrf,
      ...options.headers,
    },
  });
  const result = await response.json();
  if (!response.ok)
    throw new APIError(
      result.error?.code || "failed",
      result.error?.message || "请求未完成",
      response.status,
    );
  return result as T;
}
export async function session() {
  const s = await api<{ actor: Actor; csrf: string }>("auth/me");
  csrf = s.csrf;
  return s.actor;
}
export async function login(email: string, password: string) {
  const s = await api<{ actor: Actor; csrf: string }>("auth/login", {
    method: "POST",
    body: JSON.stringify({ email, password }),
  });
  csrf = s.csrf;
  return s.actor;
}
export function command(name: string, input: Data) {
  return api<Resource>("commands/" + name, {
    method: "POST",
    body: JSON.stringify(input),
  });
}
export function post<T>(path: string, input: Data) {
  return api<T>(path, { method: "POST", body: JSON.stringify(input) });
}
export function upload(meta: Data, file: File) {
  return api<Resource>(
    "artifacts/upload?" +
      new URLSearchParams({ ...meta, mediaType: file.type }),
    { method: "POST", body: file },
  );
}
export const targets: Record<string, string> = {
  web: "Web 与后端",
  "ios-appstore": "iOS · App Store",
  "android-google-play": "Android · Google Play",
  "android-huawei": "Android · 华为",
  "android-xiaomi": "Android · 小米",
  "android-oppo": "Android · OPPO",
  "android-vivo": "Android · vivo",
  windows: "Windows",
  macos: "macOS",
};
export const roles: Record<string, string> = {
  owner: "项目负责人",
  product: "产品",
  analysis: "需求分析",
  design: "设计",
  architect: "架构",
  developer: "开发",
  quality: "质量",
  security: "安全",
  release: "发布",
  viewer: "只读",
};
export const states: Record<string, string> = {
  healthy: "运行正常",
  unhealthy: "健康异常",
  unknown: "运行信号待核对",
  resolved: "已恢复",
  active: "已启用",
  draft: "草稿",
  awaiting_baseline: "待确认需求",
  planning: "待制定方案",
  proposed: "待审阅",
  developing: "开发中",
  pending: "待执行",
  running: "执行中",
  succeeded: "已完成",
  completed: "已完成",
  committed: "已提交",
  saved: "已保存",
  ok: "正常",
  recovered: "人工接管确认",
  confirmed: "已核对采用",
  abandoned: "已停止采用",
  failed: "失败",
  cancelled: "已取消",
  blocked: "需处理",
  unresolved: "未解决",
  triaged: "已分诊",
  stopped: "已停止",
  expired: "租约中断",
  stale: "已失效",
  superseded: "已替代",
  open: "待评审",
  prepared: "已准备",
  scheduled: "已排期",
  dispatched: "已派发",
  local_only: "仅本地保留",
  approved: "已批准",
  merged: "已合并",
  passed: "验证通过",
  awaiting_approval: "待批准发布",
  ready: "待发布",
  submitting: "提交中",
  revising_materials: "资料修订中",
  processing: "构建处理中",
  awaiting_review: "等待审核",
  awaiting_release: "等待发布",
  rejected: "审核未通过",
  published: "已发布",
  verified: "已验证",
  delivered: "已交付",
  publishing: "发布中",
  halted: "已暂停",
  reconciliation_required: "外部结果待核对",
  online: "在线",
  offline: "待连接",
  disabled: "已停用",
  revoked: "已移除",
  unconfigured: "待配置",
  configured: "已配置",
  worker_required: "等待执行节点",
  credentials_required: "等待渠道接入",
  settled: "已结算",
  recorded: "已登记",
  answered: "已确认答复",
  voided: "已作废",
  uncertain: "费用待核对",
  reserved: "费用已预留",
  waiting: "等待子工作项",
  waiting_message: "等待协作回复",
  awaiting_signature: "等待 Apple 公证",
  waiting_signature: "已释放签名节点",
  suspended: "已挂起",
  repairing: "修复中",
  awaiting_verification: "等待独立复测",
  closed: "已关闭",
  leased: "投递中",
};
export const methods: Record<string, string> = {
  automated: "自动化验证",
  manual: "人工验收",
  device: "真机验证",
  simulator: "模拟器验证",
};
export const money = (c: number = 0) =>
  new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(
    c / 100,
  );
export const date = (s: string) =>
  s
    ? new Intl.DateTimeFormat("zh-CN", {
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
        hour12: false,
      }).format(new Date(s))
    : "尚无记录";
