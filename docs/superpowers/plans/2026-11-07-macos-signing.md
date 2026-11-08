# macOS Signing and Notarization Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans sequentially in the main thread. No subagents or Git commits under the current user instructions.

**Goal:** 从准确无签名macOS应用产生可信Developer ID签名、公证并装订的PKG，等待释放节点，最终仍由独立质量和发布审批验收。

**Architecture:** 复用受控签名输入/租约/制品，增加macos-pkg模式与持久status阶段；管理员Python转换器持有凭据，固定Apple工具与SQLite保存不可重放意图。

**Tech Stack:** Go、SQLite/PostgreSQL、Python、macOS codesign/productbuild/notarytool/stapler、现有HTTP Worker与独立质量。

**Spec:** docs/superpowers/specs/2026-11-07-macos-signing-design.md

## Global constraints

- 仅主线程，不提交/推送，不调用付费模型或真实商店/生产，不代接受Apple协议。
- 原18080/PID36063、dist、700源码中无关字节、327历史输出、261索引/零提交及Docker/AVD保持。
- 不修改用户keychain搜索列表、默认keychain、系统信任或真实签名凭据。
- 真实Developer ID、公证、目标系统业务及整体P3没有证据时保持未验收。
- shell使用rtk proxy，中间文件work，用户交付物仅既定outputs；临时Go覆盖文件用.go.txt。

## Task 1: 准确macOS签名控制契约

**Files:** internal/platform/android_signing.go、goals.go、tasks.go及新macos_signing_test.go；internal/worker/android_signing.go、runner.go与新macos_signing_test.go。

**Interfaces:** signingMode=macos-pkg；expectedSigner/expectedApplicationSigner/expectedTeamId固定到signingInput；unsigned-package仅由darwin开发构建产生；最终回执含应用与安装器证书/团队/准确公证ID。

- [x] 先写实际计划→构建→签名租约→回执→独立QA测试，观察现有仅Android门禁拒绝macOS的red。
- [x] 泛化公共签名身份与输入门禁，保留Android精确行为；macOS仅darwin、双证书与团队，禁止请求选凭据或运行仓库命令。
- [x] Worker显式macOS协议、准确输入下载和固定程序，严格响应与同字节复制/上传，当前租约/来源复核；测试错误系统/模式/证书/来源/质量/超时与撤权。

## Task 2: 持久公证等待

**Files:** internal/platform/macos_signing.go、tasks.go、task_sources等实际调用处；internal/worker/macos_signing.go；平台及Worker状态测试。

**Interfaces:** 首轮processing冻结operation/requestBinding/notary UUID；task awaiting_signature→到期ready/status，后续任务仍同输入与原节点，status不得下载或重新sign。

- [x] 先写等待释放租约、同一节点处理其他工作、到期查询和最终QA的失败行为测试。
- [x] 保存当前签名phase/轮数/下一查询/绝对截止；只允许服务端变更，人工retry拒绝，未知操作阻塞，取消/基线/权限变化停止。
- [x] 双连接PG竞争、过期/重复消息、控制面重开与跨库备份恢复，核对只有一次sign和准确status。

## Task 3: 固定Apple工具与私有日志

**Files:** scripts/macos_signer.py、scripts/sign-macos-pkg.py、scripts/test_macos_signer.py、deploy/signer.macos.example.json、deploy/worker.signer.macos.example.json。

**Interfaces:** 与Worker同一严格版本协议；sign/status/管理员inspect，固定私有配置、证书、keychain、entitlements与notaryprofile；SQLite阶段与内容寻址PKG。

- [x] 以TAR逃逸/重复/链接、错误bundle/证书、提交前意图、未知/已知公证等待、同key重放和状态恢复写失败测试。
- [x] 实现有界归档/私有暂存、固定codesign/productbuild、Developer ID与资源核查，先持久notary_submit_intent再提交一次。
- [x] 准确info/log UUID+PKG摘要、装订意图与stapler/pkgutil/Gatekeeper；未知阶段不盲重试，成功对象fsync后完成，配置/输入漂移拒绝。
- [x] 实际调用本机工具的可用负例；只有合法身份与服务成功证据才可标实际公证通过，许可69与无账户保持明确。

## Task 4: 集成、验证与交付

**Files:** docs/macos-signing.md、docs/workers.md、docs/api.md、docs/validation.md、README.md、原实施ledger及相关工作台状态文案。

- [x] 实际HTTP Worker/本地工具契约循环与独立原始SQLite/制品/归档读回，明确合成回执边界。
- [x] 运行受影响与完整Go race/PG、Python、构建/静态；有UI改动时做实际浏览器与渲染验证。
- [x] 检查原数据、索引、预览、历史输出、Docker/AVD与keychain保全，输出报告/手册/源码/证据并独立读回。
- [x] 只关闭有准确证据的任务；真实Apple/原生/企业P3与未做工作保持开放，不将阶段1当完整签名交付。
