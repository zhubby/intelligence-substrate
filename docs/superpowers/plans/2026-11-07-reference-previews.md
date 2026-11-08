# 首批完整开发预览实施计划

> **For agentic workers:** Use superpowers:executing-plans in this existing feature workspace. 用户要求主线程顺序执行和评审，不派生 Agent，不提交或合并 Git。

**Goal:** 真实 Flutter/Go 参考应用从 Worker 构建包进入隔离预览，并完成后端业务交互。

**Architecture:** 专用不可变包与私有后端清单；控制面按原权限校验逐请求授权，Unix socket 接入独立 Docker 预览节点。节点用持久化 intent/receipt 和精确标签管理网络隔离的短期 scratch 容器，业务 RPC 不携带平台凭据。

**Tech Stack:** Go、Python、Flutter/Dart、Docker、Chrome/Playwright。

**Spec:** `docs/superpowers/specs/2026-11-07-reference-previews-design.md`

## Global Constraints

- 维持 64 MiB 包/展开、32 MiB 单文件和 10,000 项限制；不放开隐藏路径、外网、Worker、allow-same-origin。
- 正常发布 Web 配置保持原有根 base 与质量适配语义。
- 主线程实现/评审，保留现有分支/暂存区/18080/Docker/AVD/历史输出，不调用模型或发布。
- API 上限 16 KiB 请求、512 KiB 响应，无写入重试。节点租约 60 秒、维护间隔 5 秒，控制面维护 15 秒；容器监督执行绝对到期。

## Task 1 — 可验证专用包

Files: `internal/webpreview/runtime.go`, `runtime_test.go`, `bundle.go`; `scripts/reference_preview.py`, `test_reference_preview.py`, `build-reference-client.py`, `new-app.py`; `blueprints/flutter-go/client/lib/main.dart`。

Interface: `Bundle.Runtime() *Runtime`, `Bundle.Backend() []byte`; Runtime 包含 schemaVersion/profile/mode/goarch/backendSHA256/backendSize；`reference_preview.entries(built, backend, mode, arch)` 生成受限归档项。

- [x] 保留原始包实际拒绝证据，补齐二进制/清单不可公开、错误摘要/未知清单/缺失文件拒绝的测试；验证专用构建去除元数据与根 base，常规发布语义回归。
- [x] 实现专用构建、客户端相对 API 与严格归档校验，运行对应 Go/Python/Flutter 测试。

## Task 2 — 独立执行节点

Files: `internal/previewnode/{protocol,engine,docker,http,client}.go` 与测试，`cmd/preview-node/main.go`, `cmd/preview-relay/main.go` 与测试。

Interface: `Binding{PreviewID,Binding,ArtifactDigest,ExpiresAt,Runtime}`；Client `Ensure/Request/List/Renew/Stop`；Engine 持久化状态，Driver `Start/Request/Stop`。RPC Request 仅 Method/Path/Body，Response 仅 Status/Body。

- [x] 补齐状态/恢复/绑定/错误/请求边界测试；实现并验证持久化 intent/receipt、容量、租约和终态保护。
- [x] 补齐隔离驱动与 relay 边界测试；实现 scratch 镜像、精确资源标签、network none、tmpfs、资源限制、固定 loopback RPC 与绝对期限监督，并以真实Docker核对。

## Task 3 — 接入权威预览与浏览器

Files: `internal/platform/{service,previews}.go`、`internal/httpapi/{server,previews,preview_runtime}.go` 及测试、`cmd/platform/main.go`、`web/src/DevelopmentPreviews.tsx`。

Interface: `Handler.ConfigurePreviewNode(socket)`, `Handler.ReconcilePreviewRuntimes(ctx)`, `Service.PreviewRuntimeCurrent(ctx,id,binding)`。原有 access token 校验继续先于业务执行；新 CSP 只增加准确当前 token 的 api 路径。

- [x] 补齐禁用节点、精确业务路由、请求限额、跨预览与撤权测试；实现独立节点接入、逐请求授权与状态维护。
- [x] 构建临时 Web 工作台，回归静态预览、完整 Go race/PostgreSQL/Python 检查；现有 18080 静态产物不变。

## Task 4 — 真实闭环与交付

Files: `scripts/verify-reference-previews.go`, `scripts/verify-reference-previews.mjs`, `docs/reference-previews.md`, `docs/{development-previews,operations,validation}.md`, README、蓝图说明。

- [x] 准备管理员 relay 与节点，通过两个当前 Git→Worker 实际构建运行真实 Docker/Chrome 多端宽度业务流程；核对容器隔离、数据隔离、服务恢复和停止/到期回收。
- [x] 主线程检查实现、独立证据和四张截图；复验现有静态 HTTPS 入口及 PostgreSQL 集成。初次静态夹具点击失败在相同源码的6条针对性及最终94条流程中未复现，根因未确立，保留原trace。
- [x] 核对源码/暂存区/原服务/AVD/Docker/旧输出；8份脱敏交付物首次独立回读通过后完成029，再导出含完成记录的最终包并独立复读。总体目标仍为 active。
