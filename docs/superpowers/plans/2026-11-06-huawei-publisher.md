# Huawei Publisher Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans in the main thread. Repository instructions require sequential execution, no subagents and no commit/push.

**Goal:** 从已批准 APK 到准确版本提审和状态查询，新增受治理华为渠道连接器。

**Architecture:** 复用可信 publisher stdin/stdout、SQLite journal 与平台 processing/status/continue 工作流。Huawei 驱动分别负责管理员配置、SDK 包验证、固定 API 调用、OBS 上传与持久生命周期，不执行仓库代码。

**Tech Stack:** Go、net/http TLS、database/sql SQLite、Android SDK aapt/apksigner；无新依赖。

**Spec:** `docs/superpowers/specs/2026-11-06-huawei-publisher-design.md`

## Global Constraints

- Shell 与验收脚本子进程全部使用 rtk；主线程顺序执行；不提交推送。
- 18080/PID 36063、原数据库/凭据和 outputs 原 109 份摘要保留。
- 无新增付费模型；无真实渠道提审授权/账户时只做本地契约与 SDK 拒绝验收。
- API client 是本次唯一认证方式；APK 上限沿用 512MiB；package-list 最多 1000 项。
- 真实 SDK 不可绕过签名；上架与独立设备验收分开；未知写入不可重放。

### Task 1: 固定配置与 APK 验证

**Files:** `internal/publisher/publisher.go`, `huawei_auth.go`, `huawei_package.go`, `huawei_test.go`, `huawei_package_test.go`。

**Interfaces:** `validateHuaweiProfile(Profile) error`; `verifyAndroidPackage(context.Context, Profile, Request, string) (androidPackage,error)`；包结果固定 name/version/code/signer。

- [x] 先写测试：JSON profile 的 huawei-appgallery/android-huawei 目的地可打开，签名/host/路径/大陆选择缺失被拒绝。运行并记录 unsupported driver red。
- [x] 实现配置与 SDK 输出验证；SDK runner 仅在测试包内部替换，生产使用绝对路径、受限环境、有限超时。
- [x] 验证错包名、签名、版本、debuggable、多 signer、SDK 失败全部在外部 HTTP 写之前拒绝，记录 green。

### Task 2: 准确上传与持久处理

**Files:** `huawei.go`, `huawei_auth.go`, `huawei_upload.go`, `huawei_test.go`, `publisher.go`。

**Interfaces:** `executeHuawei(context.Context,Profile,Request,string) (Result,error)`；Receipt.Huawei 保存 stage、object/package/version 身份与等待时间。`Engine.huaweiBase` 和时间函数只供包内测试替换。

- [x] 写真实 TLS/SQLite fixture：token、appid-list、app-info、分页 package-list、upload-url、OBS PUT、file-info。先通过既有 Execute 观察未实现 red。
- [x] 实现 API client 私有凭据与固定 origin；响应必须有成功 ret.code；错误只输出固定信息/数字，不回显上游正文。
- [x] 实现目的地锁、写前 intent、准确上传和 file-info 数组；OBS 仅允许官方头、独立客户端、HTTPS allowlist、正确 Host/长度/摘要，禁止重定向/Cookie。
- [x] 验证成功只返回 processing、无 submit、回执无凭据；丢失响应/重开/并发不重放，记录 green。

### Task 3: 持久等待、单次提审与准确状态

**Files:** `huawei.go`, `huawei_test.go`；如平台/Worker 已通用则不重复修改。

**Interfaces:** status 返回 processing + continueRequired；continue 保持原 key/binding/externalId，只争取一次 journal 写权；提交返回 awaiting_review。

- [x] 测试重开后两分钟以内无继续；超过两分钟但包未解析仍 processing；任意 SHA/大小/版本冲突拒绝。
- [x] 测试两个 Engine 同时 continue 只有一次 app-submit；失败后再次调用不能重提。以字面字段断言 query/body 与准确制品关联。
- [x] 实现当前业务 versionId 的保存、提审前重读、单次继续与状态映射。published 必须匹配当前已上架业务 ID/版本及唯一准确 package。
- [x] 测试旧版本 released、同名不同包、缺失 code、分页异常、错误 externalId/binding、凭据重定向/恶意上传头被拒绝；终态只释放自身锁。

### Task 4: 文档、真实拒绝与回归交付

**Files:** `deploy/publisher.huawei.example.json`, `deploy/worker.huawei.example.json`, `docs/huawei-appgallery.md`, `docs/releases.md`, `docs/validation.md`, `README.md`；私有脚本放 work。

- [x] 写管理员配置及手册，明确企业资料/独占写入前提、API client 选择、文件数组文档差异、未知操作锁、APK/上架/验收边界。
- [x] 运行真实 SDK/CLI 诊断 APK 拒绝验收；不启动新模型调用、不接触真实发布账户。
- [x] 新专用 PostgreSQL，执行 `rtk proxy go vet ./...`、`rtk proxy go test -count=1 -race -json ./...`、7 命令 × 3 平台编译，保存结果并停止本轮集群。
- [x] 主线程复查正确性/权限/恢复边界；更新验收事实，导出新源码 ZIP/报告/手册/证据，逐字节读回，复核原 109 个摘要与预览 PID。整体目标保持 active。
