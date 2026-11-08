# Store Material Remediation Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans in the main thread. User requires sequential execution and no Git commits.

**Goal:** 原 IPA 下独立批准 App Store 文字资料修订、安全重新提审，并以准确资料版本的独立验收关闭拒审问题。

**Architecture:** 独立 store_revision 与渠道有效资料 binding；publish_materials 显式协议；根版本预约与父子操作 CAS 日志。

**Tech Stack:** Go、SQLite/PostgreSQL、React/TypeScript、HTTP/MCP、Playwright。

**Spec:** docs/superpowers/specs/2026-11-07-store-material-remediation-design.md

## Global Constraints

- 主线程顺序执行，不提交/推送，不操作真实渠道/生产，不代接受 Apple 协议。
- 仅 App Store 文字/链接修订，保持原 IPA、locale 与截图。
- 保留原候选批准；新资料需独立批准及准确资料版本的 publication evidence。
- 原18080/PID36063、web/dist、261索引、零提交、323历史输出及Docker/AVD保持。
- shell 使用 rtk proxy；中间证据 work，交付物在既定 outputs。

## Task 1: 安全资料修订发布原语

**Files:** publisher/apple_material_revision*.go、apple_material_journal.go、apple_package.go、publisher.go、apple_recovery.go。

- [x] 写真实失败行为测试，核对同包修文案、精确原审核项及零新 IPA/媒体上传。
- [x] 实现请求字段、严格 action、父子日志、原版本预约、写前核对与 CAS、幂等与只读恢复。
- [x] 验证二轮修订、身份漂移、未知结果/重试、错误状态和跨引擎竞争；保留 red/green 证据。

## Task 2: 控制面、Worker 与独立验收

**Files:** platform/store_revisions*.go、release_issues.go、channels.go、tasks.go、releases.go、service.go；worker/runner.go 与协议测试。

- [x] 先写失败测试：人工修复策略、提案/批准/派发、旧绑定拦截。
- [x] 实现独立修订、实际权限、幂等/CAS、质量/需求/方案门禁，单次显式 provider 和原包保留。
- [x] 新资料 epoch 查询与 publication evidence 准确绑定；多轮拒审保留并由独立验收关闭问题链。
- [x] HTTP/MCP 权限、Worker、备份恢复及专用 PostgreSQL 竞争验证。

## Task 3: 工作台与交付

**Files:** web/src/StoreRevisions.tsx、ReleaseIssues.tsx、Operations.tsx、web/tests/store-material-remediation.spec.ts、docs/store-material-remediation.md。

- [x] 工作台修复策略、来源/新资料、独立批准与原节点派发；只读用户无写入口。
- [x] 实际桌面/移动浏览器与渲染核对，新 work 输出构建，不触碰原 dist。
- [x] 运行相应完整回归、静态与构建；独立读回日志/归档、原索引/预览/历史输出保全。
- [x] 输出报告/手册/源码/证据并独立核对；准确列出真实 Apple、截图修订与其他渠道的未验收范围。

## Completion record

037的本地实现与验收通过：最终Go race672顶层/2274项含子场景、31项PG、14条浏览器、111项Python、19次CLI边界调用；原始日志/归档/制品/publisher快照与首批四份输出独立读回通过。源码700文件/701ZIP项，原323输出及环境基线保全；完成记录将纳入最终重导和再次读回。主线程自查不代表第三方审查。真实Apple/原生/设备/签名/企业生产及整体P3保持开放，无Git提交或真实渠道动作。
