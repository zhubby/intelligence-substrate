# 中断源码人工接管 Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans task-by-task in the current thread. User instructions prohibit subagents. Preserve the existing uncommitted branch and all prior outputs.

**Goal:** 中断的真实源码可以经负责人核对后恢复下游工作，同时保留原来源、旧租约失效和独立质量门槛。

**Architecture:** 在现有 Resource/Store 和快照事务之上增加 source_recovery 决定。检查接口读取实际 Git，决定接口只提交 SQL 状态，绝不更新分支。任务调度显式阻塞未核对源码，下游校验人工恢复的证据链。

**Tech Stack:** Go、Git plumbing、SQLite/PostgreSQL、React/TypeScript、Playwright。

**Spec:** [设计](../specs/2026-11-06-source-recovery-design.md)

## Global Constraints

- shell 命令与脚本子进程使用 rtk；不运行子 Agent、不提交/推送、不重启 18080。
- 保留原 65 个交付文件和既有服务；验证数据库使用新建隔离集群。
- 采用来源不等于测试通过；不伪造退出码、不恢复旧租约、不修改 Git 分支。
- 新决定的参数、身份、需求/方案、固定源码、原作者和实际候选都需校验。

### Task 1: 可信核对及来源恢复

**Files:** Create `internal/platform/source_recovery.go`, `internal/platform/source_recovery_test.go`; modify `service.go`, `task_source.go`, `tasks.go`.

**Interfaces:** `InspectSourceOperation(ctx,a,id) (M,error)`; `sources.reconcile` 返回 `source_recovery`；`recovered` run 通过可信决定被选择为源码生产者。

- [x] 先写真实 Git/SQL 故障测试：快照提交后人为打断，过期租约，负责人确认候选后下游构建绑定相同 SHA，原作者、退出码和失效租约保留。
- [x] 运行 `rtk proxy go test ./internal/platform -run 'TestSourceRecovery' -count=1`，记录缺失功能导致的失败。
- [x] 实现检查、严格确认/停止采用、幂等、状态及来源验证；prepared 与 committed 两种中断均支持。
- [x] 补充权限、活跃/其他租约、分支/候选变化、需求/方案变化、集成决定、重复决定及事务回滚测试并逐项验证。

### Task 2: 调度与隔离边界

**Files:** Modify `tasks.go`, `source_recovery.go`, `source_recovery_test.go`, add PostgreSQL recovery test.

**Interfaces:** 未正常完成且含源码候选的任务阻塞，通用 retry 不消耗尝试；旧 Worker 请求不能绕过人工决定。

- [x] 先写过期/失败/重试不得重复执行、原 Worker 不能覆盖恢复结果测试并观察失败。
- [x] 在 claim/expiry/failure/retry 路径接入源码待核对状态；保留取消和需求过期语义。
- [x] PostgreSQL 双客户端同时决定只有一次成功，注入 SQL 失败后无半成品；备份/恢复保留决定和候选。

### Task 3: API、MCP 与工作台

**Files:** Modify `internal/httpapi/server.go`, `mcp.go`; create recovery HTTP test, `web/src/SourceRecovery.tsx`, `web/tests/source-recovery.spec.ts`; modify `Delivery.tsx`, `api.ts`.

**Interfaces:** GET source-operations/id、MCP inspect_source_operation；workbench 通过同一 sources.reconcile 提交人工决定。

- [x] 先写 HTTP/MCP 授权与无令牌暴露测试、桌面/手机采用/停止采用/刷新/错误测试并观察失败。
- [x] 实现只读检查端点及 MCP 工具；Agent 只读，决定仍要求人类身份。
- [x] 在既有执行页接入弹窗，使用实时状态和本地错误，保存幂等键及理由；完整 SHA、历史决定和下载链接可见。
- [x] TypeScript/Vite 构建与浏览器测试通过，核对截图、键盘、Escape、手机滚动。

### Task 4: 回归、文档与交付

**Files:** Update README, API/architecture/task/source/worker/validation docs; create `docs/source-recovery.md`; verification/export scripts live in `work/`.

- [x] 运行 go vet、完整 race（真实隔离 PostgreSQL）、三个系统共 21 项命令编译、前端构建、全部浏览器测试及 diff 检查。
- [x] 主线程复查正确性、权限和简化，明确没有独立审查者。
- [x] 更新手册、边界、实测证据及本计划；停止本轮隔离数据库，确认 18080 及历史输出保留。
- [x] 只在用户输出目录新增报告、完整源码 ZIP、操作手册、证据和截图；逐文件校验工作区/ZIP/摘要及本地链接。

## 验证证据

`work/source-recovery-8fw3z9da/result.json`：171 个顶层测试、351 项含子场景，无失败/测试跳过，21 项编译、12 个 Chrome 桌面/手机场景及 PostgreSQL 双客户端核对和恢复通过。浏览器初次 429 与真实会话复用后的通过记录均保留。主线程自行复查，未委派审查；最终导出由 `work/export-source-recovery.py` 逐字节和摘要核验并生成 `export-verification.json`。全产品目标未完成。
