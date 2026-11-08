# iOS App Store Signing Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans sequentially in this thread. User instructions require no subagents, commits, pushes or staging changes.

**Goal:** 将准确无签名 iPhoneOS 应用转换为绑定批准证书/团队/profile、可独立验证的 App Store IPA。

**Architecture:** 复用受控签名与来源契约，增加 ios-ipa；构建器产生 arm64 真机 APP TAR，管理员固定 Python CLI 验证 provisioning CMS/授权并在私有日志保护下签名、归档，完成后交独立质量。

**Tech Stack:** Go/SQLite/PostgreSQL、Python3.11+、Flutter/Xcode、codesign、OpenSSL3 CMS、React/Playwright。

**Spec:** ../specs/2026-11-07-ios-signing-design.md

## Global constraints

- 保留完整产品和P3验收；合成Apple成功不能算真实签名/真机/上架。
- 主线程顺序，不提交/推送/改索引，不调用付费模型或真实商店/生产，不接受Apple协议。
- 保留原716源码的无关字节、331历史输出、原18080/PID36063/dist、261索引/零提交及Docker/AVD/keychain。
- 不修改keychain搜索列表、默认keychain、系统信任、真实凭据；中间文件work，交付物仅既定outputs。

## Task 1: 准确无签名真机应用

**Files:** scripts/build-reference-client.py、scripts/test_build_reference_client.py及独立iOS载荷检查模块/测试。

**Interfaces:** `--target ios-unsigned` 使用iphoneos、Flutter `ios --release --no-codesign`，输出APP TAR/manifest；原始CFBundleSupportedPlatforms=iPhoneOS、DTPlatformName=iphoneos、arm64。

- [x] 写成功构建/真实产物清单及许可69、错误平台/架构、已有签名/profile、HTTP API拒绝测试，先观察不支持target的失败。
- [x] 新增目标、SDK前置和准确平台/归档检查，运行原构建器全套回归。

## Task 2: 批准身份与专用Worker

**Files:** internal/platform/android_signing.go及新ios_signing_test.go/ios_signing_postgres_test.go；internal/worker/android_signing.go、runner.go及新ios_signing.go/ios_signing_test.go。

**Interfaces:** `signingMode=ios-ipa`；`expectedSigner`/`expectedTeamId`/`expectedProvisioningProfile`绑定signingInput、制品和回执。CLI协议ios-1，只允许sign、固定完整来源身份；回执带profileSHA256、teamId、bundleId/version/build和准确IPA。

- [x] 写实际批准方案→unsigned→专用签名→准确回执→独立QA失败测试，验证当前拒绝iOS。
- [x] 扩展公共身份校验与绑定；iOS仅darwin、App Store profile/团队，错误来源/租约/权限/回执拒绝；无签名包不派发最终QA。
- [x] Worker准确下载和固定程序、受限JSON、专用组清理、读取校验/上传；实际HTTP链覆盖拒绝和取消。
- [x] 专用PG双连接争用与备份恢复，独立读取准确来源/审计/归档；不重复签名或放宽真机发布门槛。

## Task 3: 固定App Store签名服务

**Files:** scripts/ios_signer.py、sign-ios-ipa.py、test_ios_signer.py、ios载荷模块与deploy签名配置例。

**Interfaces:** 管理员配置固定三项身份、bundle、证书/profile/entitlements/keychain/CA和codesign/OpenSSL3；请求不能更换路径。私有日志为签名前intent→signed，完成重放只校验原IPA；中断需人工核对。

- [x] 先写原生载荷/CMS/profile/授权和未完成intent拒绝测试；测试仅替换Apple提供方，真实归档/SQLite/子进程。
- [x] 实现有界APP提取、arm64 iOS Mach-O平台检查、固定CMS证书链/授权/期限验证、批准entitlements子集与准确签后DER/profile/授权检查。
- [x] 固定由内向外codesign，资源读回、唯一Payload IPA、持久防重和fsync；敏感输出仅私有日志。
- [x] 使用本机真实OpenSSL3测试CA验证CMS签名/篡改/错误CA及实际Apple工具负例，明确没有企业凭据或真实Apple签名成功。

## Task 4: 工作台、回归与交付

**Files:** web/src/pages/Delivery.tsx、api.ts、web/tests/ios-signing.spec.ts、docs/ios-signing.md及API/Worker/发布/validation/主ledger。

- [x] 先写团队/profile与独立真机验收显示的浏览器失败，再实现桌面/手机状态。
- [x] 完整Go race/专用PG、Python、Go vet/build/交叉编译、TypeScript/隔离Vite及受影响浏览器验证；检查实际渲染。
- [x] 原始SQLite/审计/备份/私有日志/IPA独立读回，核对原数据、预览、输出、索引和Docker/AVD/keychain保全。
- [x] 四份脱敏交付物读回后才关闭039本地增量；完成记录重导/读回，原生/真机/正式渠道与完整P3继续开放。


## Local increment acceptance

2026-11-07：四份交付物首次独立读回完成，734源码/735ZIP项、Go688/2378含34PG、Python146、终版浏览器16流程、7张实际截图及原始数据库/私有日志/IPA/备份一致。原索引、源码无关字节、历史输出、预览/Docker/AVD/keychain保持；完成记录继续重导并读回最终包。只完成本地实现与契约验证，三SDK及六构建许可69、真实Distribution/profile、TestFlight真机、实际上架和完整产品/P3继续开放。
