# iOS 准确应用构建与受控 App Store 签名

按已批准的完整产品规划推进 iOS 正式交付主路径。当前缺少无签名真机应用和专用 IPA 转换；模拟器包、预签名上传器及合成 Apple 回执不能填补这个缺口。

## 行为与边界

构建器新增 `ios-unsigned`：先检查完整 Xcode/iphoneos 许可，再执行 Flutter release/no-codesign，确认 iPhoneOS、arm64、无顶层签名和无嵌入 provisioning profile，输出准确 APP TAR 与来源清单。公开 API 必须 HTTPS。许可失败不运行 Flutter、不产出成功包。

批准方案声明开发角色/darwin/ios-appstore/unsigned 构建，随后 release/darwin/ios-ipa 签名转换。`expectedSigner` 是 Apple Distribution DER SHA256，`expectedTeamId` 为10位大写团队，`expectedProvisioningProfile` 为批准 App Store profile 原始 CMS SHA256。输入、来源与三项身份进入不可变 signingInput、上传制品与完成回执；不接受调用者指定凭据、命令、输出或 profile 路径。开发节点与质量节点不能签名，签名节点不能执行仓库命令。失败或失联阻塞，不以通用重试重新授权。

管理员私有 Python CLI 固定项目/目标、bundle、证书、profile、entitlements、keychain、OpenSSL3/codesign 和 profile 信任 CA。凭据不进入源码/Worker配置/任务。工具只使用隔离环境和私有日志，继承 Worker 专用进程组；程序结束后立即清理后代。

App Store profile 必须由固定 CA 验证 CMS 签名，原始摘要正确、未过期、UUID合法、团队/应用前缀准确、批准 DER 出现在 DeveloperCertificates，且没有 ProvisionedDevices/ProvisionsAllDevices、get-task-allow为false。签名 entitlement 由管理员批准文件给出，必须是 profile 授权的子集；application-identifier 和团队精确，keychain组和能力受 profile 约束。首批采用明确注册的同团队 App ID，不支持 wildcard App ID、app extension、Watch app 或多 profile 嵌套应用。

沿用有界 APP TAR 解析，iOS 使用平面 Info.plist；拒绝逃逸、歧义、脚本、特殊文件和顶层签名/profile。原生二进制必须是 arm64 iOS executable/dylib，拒绝模拟器、macOS、跨平台或损坏载荷。固定从内到外签名；framework/dylib不赋应用entitlements，主应用嵌入准确profile并使用批准entitlements。代码签后提取DER并核对SHA256/团队/Apple distribution requirement，实际entitlements和profile精确读回；普通资源和路径/模式不能变化。

输出唯一 `Payload/APP.app` 的有界IPA，原始无签名包不变。私有 DELETE/FULL SQLite、维护锁、签名前持久 intent 和最终 fsync 保证同 operationKey 完成重放只读准确IPA；中途失败不自动重签。profile、entitlements、配置或请求漂移拒绝。签名本身无Apple上传；商店上传仍由现有独立 publisher 和发布审批执行。

## 验收

实际控制面/HTTP Worker/CLI/SQLite/归档执行，Apple成功边界明确使用合成提供方。覆盖原生构建许可阻塞、错误平台/签名/profile，计划/Worker/凭据权限、准确输入/回执/字节、防重、撤权/取消和独立质量；PostgreSQL争用与备份恢复。Python使用真实归档与独立CMS本地测试CA证据，不修改系统信任；合成证书不能算企业Apple签名。工作台显示团队/profile与真机独立验收边界并做实际浏览器检查。

Xcode许可69、真实Apple Distribution/profile/keychain、真机业务验收和商店上架均保持实际状态。原18080/PID36063/dist、716份原源码无关字节、331份历史输出、261索引/零提交、Docker/AVD/keychain保持。只在既定outputs交付脱敏报告、手册、源码与证据。顺序实施，不派Agent、不提交/推送，不调用付费模型或真实商店/生产，不代接受Apple协议。
