# Release Remediation Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans in the main thread. User instructions require sequential execution and prohibit Agent delegation and Git commits.

**Goal:** 将准确渠道拒审回流到修复规划，并以重新批准版本的独立发布验收关闭问题。

**Architecture:** 同事务派生 release_issue，人工确认修复要求后冻结到方案与候选。复用现有质量/发布门槛，仅在准确新候选目标 verified 后自动解决。

**Tech Stack:** Go、SQLite/PostgreSQL、React/TypeScript、现有 HTTP/MCP、Playwright。

**Spec:** `docs/superpowers/specs/2026-11-07-release-remediation-design.md`

## Global Constraints

- 主线程顺序实施，不提交、不推送、不操作真实渠道或生产。
- 首批支持需要重新构建的修复；纯资料补交不自动计为已支持或已完成。
- 拒审、提审、发布观察与独立验收分开；不新增直接关闭问题的公共命令。
- 保持无拒审项目的原绑定格式、18080/PID 36063、web/dist、261 条原索引、零提交、原输出与无关文件。
- 所有 shell 命令使用 `rtk proxy`；中间证据写入新 work 目录，交付物写入既定 outputs。

## Task 1: 权威拒审问题与人工修复要求

**Files:** `internal/platform/release_issues.go`、`release_issues_test.go`；修改 `releases.go`、`service.go`。

**Interfaces:** `recordReleaseRejection(tx, release, target, actor)` 原子产生问题；`release_issues.triage` 使用 `id, expectedRevision, expectedBinding, key, instructions, strategy=new-build`。

- [x] 编写服务行为测试，使用 `awaitingChannel` 后真实 `tasks.complete` 报 rejected，再读取 `Store.List(...,"release_issue",...)`，断言只有一条并绑定准确包/外部版本；先运行确认失败。
- [x] 实现来源冻结、重复保护、人工权限、严格字段、CAS/幂等；禁止旧拒审候选 ready/submitting。
- [x] 运行定向 race 测试，检查拒审/核对/取消与错误租约均保持权威状态。

## Task 2: 方案与候选的修复链路

**Files:** `internal/platform/release_issue_plans.go`、`release_issue_plans_test.go`；修改 `goals.go`、`plan_generation.go`、`releases.go`、`release_feedback.go`、`internal/integrations/plans.go`。

**Interfaces:** `releaseIssueSnapshot(tx,g)` 产出稳定数组；`validatePlanReleaseIssues` 比较当前输入；`releaseIssueGate` 校验准确新包与后继提交；`resolveReleaseIssues` 只在独立 verified 钩子执行。

- [x] 先验证问题未确认不能规划、确认后手工与模型输入包含来源、旧生成上下文被新增问题/修复要求/需求变更拒绝。
- [x] 将控制面快照计入方案批准和候选 binding；拒绝调用方伪造快照。
- [x] 测试旧包/同字节/错误仓库/非后继提交/旧验收/旧审批被拒绝，新候选 published 保持未解决，独立发布验收通过才 resolved。
- [x] 校验无问题项目仍兼容原绑定，备份恢复保持完整问题链。

## Task 3: 工作台与协议

**Files:** `web/src/ReleaseIssues.tsx`、发布页、方案页、API 状态文案、HTTP/MCP 测试、浏览器测试。

**Interfaces:** 复用 workspace 的 release_issue 资源和 `/api/v1/commands/release_issues.triage`；不向 MCP 开放人工决定。

- [x] 编写并实际运行桌面/移动流程：拒审可见、来源与外部版本准确、确认修复要求、规划快照、只读角色无决定权限。
- [x] 通过实际浏览器检查窄屏换行、表单和问题状态；构建到新 work 目录。
- [x] 确认 HTTP 拒绝越权/撤权，MCP 可读问题并规划但不能确认或伪造关闭。

## Task 4: 实际执行、回归与交付

**Files:** 新验证脚本、`docs/release-remediation.md`、README/API/validation、todo 036、输出报告/手册/源码归档/证据索引。

- [x] 实际 HTTP/Git/Worker 执行新修复提交、构建、独立测试，记录拒审→新候选→重新批准→独立发布验收全链；外部渠道使用明确标记的本地样本。
- [x] 使用专用 PostgreSQL 两连接验证幂等/CAS与备份恢复；运行完整 Go race、vet/build、受影响浏览器和现有 Python 检查。
- [x] 核对原源码/索引/预览/输出和 Docker 当前基线；完整 Xcode 仅检查许可，不代接受。
- [x] 主线程顺序复核源代码，独立读回交付物、原始测试事件和归档字节。更新日志与 todo 验收结果后才声明本增量完成。

## Validation Ledger

- 完整Go race661顶层/2243项含子场景，30项专用PG，零测试失败/跳过；vet/build/tidy、格式/空白及Linux/Windows Worker交叉编译通过，PG已停止。
- 桌面/移动10条受影响流程、TypeScript/Vite/Prettier和Python111项通过；实际查看4张最终截图。
- HTTP/Git/Worker实际执行修复/新包/独立QA；单独Python核对55条资源/60条审计及PG恢复24条资源/28条审计、源码/制品/报告/备份与绑定。模拟渠道及publication声明不计真实交付。
- Go overlay移除门禁使旧候选/同字节场景按预期失败，生产源码保持。首轮回归证据父目录遗漏已修正，失败记录保留；接受结果来自全新完整回归。

- 原673源码无关字节、319输出、261索引/零提交、原18080/PID36063/静态文件、21份AVD均已核对。以实际捕获文件确认本轮容器基线为38并全部保持，另24镜像引用/18镜像ID/17卷保持；前轮39→37的旧ID销毁记录保留，本轮开始前已有另一个ID的cybervisual-ui-preview。操作主体未核实，本轮没有重建或修改Docker资源。最新Apple两SDK仍许可69。

- 四份交付物首次独立读回通过：686源码/687个ZIP项，全部字节/大小/模式、原始Go事件661/2243及30项PG、浏览器/变异/报告/手册链接一致，319份既有输出保持。只关闭036并纳入完成记录；最终重新导出与读回，整体产品目标/P3仍active。
