# 发布前 Web 开发预览 Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans task-by-task. User AGENTS requires sequential execution and review in the main thread. Do not use subagents or commit Git.

**Goal:** 从准确 Git/Worker Web 制品提供可追溯、可撤销的发布前静态应用预览。

**Architecture:** 受控 preview 资源和独立 credentials 类型维持业务权威；可选独立 HTTP listener 只读取已验证包。工作台沿用现有代码页与表单、权限、状态组件。

**Tech Stack:** Go、SQLite/PostgreSQL、React/TypeScript、Playwright/Chrome。

**Spec:** `docs/superpowers/specs/2026-11-07-development-previews-design.md`

## Global Constraints

- 每条 shell 命令以 rtk 开头，私有脚本/构建/日志仅写 work/。
- 保留现有分支和 261 索引条目、原 18080/PID36063 与 web/dist、既有 275 输出、Docker 与 AVD。
- 不调用付费模型，不代签协议，不写外部生产/商店。
- 归档和展开上限均 64 MiB，单文件 32 MiB，10,000 条目，index.html 入口，静态相对资源。
- 预览 5–240 分钟默认 60 分钟，每项目最多 10 活动项；访问最多 10 分钟且受会话/预览期限约束，每身份最多 20 条。
- P3、原生/真机/真实渠道状态按实际证据保留开放。

## Task 1: 不可变多文件静态包与生命周期

**Files:** 新建 `internal/webpreview/bundle.go`、`bundle_test.go`、`internal/platform/previews.go`、`previews_test.go`；修改 `internal/platform/service.go`、`goals.go`。

**Interfaces:** `webpreview.Read(filename, digest string, size int64) (*Bundle,error)`；`Bundle.File(name string) ([]byte,bool)`；`previews.create/stop` 使用现有 Command。

- [x] 失败测试用真实 tar/gzip 字节，创建 `previews.create` 并断言准确来源、文件数、幂等重放；命令尚不存在时得到 unknown_command。
- [x] 用真实恶意 tar 验证 traversal/link/duplicate/size/entry/trailer/missing-index；实现摘要验证和只读内存包。
- [x] 实现当前权限、需求/制品绑定、限额、到期投影、停止与需求失效；重放使用当前 resource，审计沿用命令事务。
- [x] 执行 `rtk proxy go test -race ./internal/webpreview ./internal/platform -run 'Preview|WebBundle'` 并检查结果。

## Task 2: 短期访问与独立入口

**Files:** 新建 `internal/platform/preview_access.go`、`internal/httpapi/previews.go` 及各测试；修改 `memberships.go`、`server.go`、`cmd/platform/main.go`。

**Interfaces:** `Service.IssuePreviewAccess(ctx,actor,id)` 返回临时 Token/ExpiresAt；`Service.ResolvePreviewAccess(ctx,token)` 返回准确 preview/artifact；`Handler.ConfigurePreviews(listen,publicURL,dev)` 验证独立入口；`Handler.PreviewGateway()` 提供只读静态请求。

- [x] 测试 POST 领取、CSRF、独立入口配置错误、准确 headers/文件、拒绝控制面 API 和平台凭据、停止/过期/基线变化和退出登录。
- [x] 新 preview 凭据与原会话绑定，成员/团队撤权永久删除对应项目 preview 凭据；审计失败完整回滚。
- [x] 实现独立 listener、精确路径/CSP/sandbox、短期无缓存文件与定期失效检查；仅最近一个已验证 bundle 缓存。
- [x] 真实 PostgreSQL 并发领取/撤权和备份→SQLite恢复，验证旧链接失效、准确绑定与无凭据恢复。

## Task 3: 工作台与实际 Git→Worker→Chrome 闭环

**Files:** 新建 `web/src/DevelopmentPreviews.tsx`、`web/tests/development-previews.spec.ts`；修改 `Engineering.tsx`、`styles.css`、`scripts/verify-workbench.py`。

**Interfaces:** Code 页传入当前 repositoryId，按当前目标/仓库显示 preview；受 CSRF 的 `POST previews/{id}/access` 返回仅留在内存的可打开链接。

- [x] 浏览器先断言“开发预览”入口、真实多文件应用体验、停止拒绝、权限和错误/加载、键盘及响应式；先记录 UI 缺失失败。
- [x] 沿用现有样式与表单，展示当前源版本/期限，异步状态有卸载/项目切换保护，避免失效链接长期留在 UI。
- [x] Chrome 桌面/移动验证 ES modules、CSS、JSON 请求、应用交互与受阻网络/导航；用实际 Worker 进程构建已提交源并打开。
- [x] 保存截图与私有过程证据；原 18080 不更新。

## Task 4: 回归、审查与交付

**Files:** 新建 `docs/development-previews.md`；修改 README/API/架构/运维/验证；canonical todo 027。

- [x] 运行完整 Go race（实际 PostgreSQL）、vet/build/tidy、TypeScript/Vite、全部浏览器，零未解释失败/跳过。
- [x] 顺序主线程审查权限、隔离、状态、完整性、复杂度，修复实际问题并验证；核查两种宽度截图。
- [x] 核对测试源码摘要和原有资源，停止本轮服务，重查 Apple SDK 真实条件。
- [x] 向 outputs 导出独立命名前缀的源码 ZIP、报告、runbook、证据和截图，独立读回归档/摘要后标记本增量完成；总体 P3 保持开放。
