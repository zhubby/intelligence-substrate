# 批准源码修改范围 Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans sequentially in the main thread. The user's AGENTS.md prohibits subagents. Do not stage, commit or push this platform checkout.

**Goal:** 让负责人批准代码工作项允许修改的路径，并在 Worker 与控制面的真实 Git 提交入口强制执行，保留旧配方和恢复语义。

**Architecture:** 代码任务可选 `writePaths` 数组（1–128 项）。文件精确匹配，末尾 `/` 按目录边界递归匹配，不解释通配符；`[id]` 是合法字面路径。缺省为全仓库并在审批界面明确展示。范围冻结进源码绑定，控制面比较最终树与绑定基础树的全部变化，包括合并带入的变更、删除、二进制、模式和重命名两端。越界时不创建提交、prepared 操作或候选分支。

**Tech Stack:** Go 1.27.1、SQLite/PostgreSQL、原生 Git、现有 React/TypeScript、Python/Playwright。

**Spec:** [产品规格](../specs/2026-11-05-intelligence-substrate-design.md)；[054 真实失败](../../local-model-multiplatform.md)。

## Global Constraints

- 保留 887 份继承源码、396 份历史输出、261 个索引条目及 flags、零平台提交、原分支、18080/PID36063/dist、Docker/AVD/钥匙串与默认 Ollama。
- 只在本轮独立 work 根保存服务、构建、浏览器、回归与证据；不改变原预览。
- 不调用付费模型、不重新运行本地7B试验、不接受 Apple 许可、不安装真机、不操作真实商店/生产。
- 无 `writePaths` 的旧 selectionDigest 和 messageTaskRecipe 算法保持准确原值；范围不能通过 split/reorder/retry/wait/progress/reconcile 丢失或扩大。
- 范围约束是代码产物门禁；任意命令的执行隔离继续由既有 driver 决定，不宣称此字段是文件系统沙箱。
- 完整五端 P2/P3 目标仍未完成；固定模型动作夹具仅证明平台协议和执行行为。

### Task 1: 真实边界回归与范围冻结

**Files:** 新增 `internal/platform/source_write_scope_test.go`；修改 `goals.go`、`task_source.go`、`adaptive.go`；新增共享 `internal/source/write_scope.go` 并复用 `path.go` 的路径语法。

**Interfaces:** `source.ParseWritePaths(value any) ([]string, error)`；`source.AllowsWrite(paths []string, name string) bool`。缺省由调用方判定，显式 null/空数组不等于缺省。

- [x] 通过既有 `validateTasks` 与真实 claim API 添加可编译回归，实际观察缺失门禁导致失败。非法输入包括 null、非数组、非字符串、空项、绝对路径、穿越、保留名、重复与大小写/NFC别名；合法 Next 路由按字面匹配。
- [x] 在 `validateTasks` 严格验证并规范化范围；新绑定仅存在该字段时纳入 selectionDigest、保存范围并验证准确一致。旧缺省哈希不改变。
- [x] split 继承范围，拒绝 child/update 显式覆盖；reorder 仅改变依赖。通过实际待执行任务和恢复/重试路径核对冻结不丢失。

验证命令：`rtk proxy go test ./internal/platform -run 'TestWriteScope' -count=1 -v`。

### Task 2: 控制面提交与恢复门禁

**Files:** 新增 `internal/platform/source_write_scope.go`；修改 `source_commit.go`、`git.go`、`source_recovery.go`；扩展上述真实 Git/SQLite 回归。

**Interfaces:** `(*Service).sourceWriteScope(ctx context.Context, repo Resource, task M, base, tree string) error` 比较实际 Git 树；`(*Service).snapshotWriteScope(ctx context.Context, t *Tx, a Actor, repo Resource, in M, tree string) error` 从当前租约任务读权威范围。

- [x] 写并实际运行完整快照和旧文本入口的越界回归。拒绝新增、修改、删除、binary、chmod、rename出/入范围，以及 `backend/` 对 `backend-extra/` 的前缀误匹配；断言分支、提交对象、change/source_commit 数量不变且有拒绝审计。
- [x] 在 commit-tree/prepared/ref 之前执行 `git --no-replace-objects diff-tree --no-commit-id --name-only -r -z --no-renames BASE TREE --`；文本入口在 add 后、commit 前检查实际 write-tree。
- [x] 完成 prepared 的第二事务和人工确认恢复时重新验证范围。实际注入 finalize 故障，重试和人工核对均保留原范围。
- [x] 集成任务按绑定基础提交比较整个最终树；范围必须覆盖合并带入的所有实际变化。测试遗漏合并路径被拒以及完整范围正常完成。

### Task 3: Worker、模型契约与可审查界面

**Files:** 修改 `internal/worker/agent.go`、`internal/integrations/plans.go`、`web/src/forms.tsx`、`web/src/pages/Delivery.tsx`；新增 `web/src/SourceWriteScope.tsx`、`internal/worker/source_write_scope_test.go`、`web/tests/source-write-scope.spec.ts`。浏览器复用既有 `scripts/verify-workbench.py`，无须新增专用Python runner。

- [x] 固定动作模型通过真实认证 HTTP 驱动 Worker：越界 write/delete 先被拒，随后合法修改可提交；任意 run 越界后 finish 被控制面拒，私有失败源码保留但不进入下游构建。
- [x] 将精确 `writePaths` 语义加入规划和执行契约；不把提示当权限实现。messageTaskRecipe 已哈希完整任务，保留原函数并验证新范围自然覆盖等待/进度。
- [x] 手工方案的代码任务显式选择全仓库/指定路径，每行一项；指定路径空白不能提交；切换非 code 类型移除范围输入。方案、批准对话框和任务详情展示范围，缺省显示全仓库。
- [x] 隔离浏览器实际操作表单、提交和审批，再查看执行详情，核对 API 保存值和显示一致，记录截图/控制台。

### Task 4: 回归、独立读回和交付

**Files:** 新增 `docs/source-write-scope.md`；更新 `docs/api.md`、`docs/product-acceptance.md`、`docs/validation.md`、本计划与055 todo。私有验收器放 `work/`。

- [x] 完成受影响 Go/HTTP/Worker/规划回归、Go vet/race 和 TypeScript/隔离 Vite 构建；PostgreSQL 复核准确说明覆盖。Python 用本轮输入摘要关联结果。
- [x] 独立 Python/Git/SQLite 与 HTTP 读回成功/拒绝记录、绑定、准确源码和无候选/发布状态，不复用实现的判定函数。
- [x] 导出源码ZIP、中文报告/接续手册和脱敏证据至约定 outputs；独立核对所有源码/ZIP项、历史输出字节和基线资源。
- [x] 收尾只关闭本项范围门禁，不将054模型失败、未验收的业务或P2/P3标为通过。

## Execution Record

- 2026-11-07: 保存真实red日志后完成实现。完整Go race826顶层/2955含子场景，44项PostgreSQL顶层、59项WriteScope命名含子场景，零失败/测试跳过；438份Go/module输入冻结，vet/tidy-diff/gofmt/diff-check通过。TypeScript和隔离Vite构建、6条桌面/390px浏览器流程通过，4张范围截图人工查看。
- 真实认证HTTP/Git/Worker三场景及独立Python/SQLite/Git读回通过。7个固定模型动作，不是真实推理；成功的22/27字节文本包仅为协议夹具。命令越界场景零候选/新增提交/构建，实际失败字节私有保留。专用PostgreSQL和浏览器服务已停止；三个AppleSDK复探仍69。
- 首轮6份交付物通过独立897源码/898ZIP项字节/大小/模式、438冻结输入与原始测试计数、8处输出链接/56处源码文档链接、两张准确PNG和结束标记核对。396份旧输出、872份未改继承源码、261索引/flags/零提交/分支、原预览及宿主资源保持。仅关闭055，最终重导读回纳入完成记录，完整P2/P3继续开放。
