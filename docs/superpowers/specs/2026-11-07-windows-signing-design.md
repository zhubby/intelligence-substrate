# Windows 准确 MSIX 构建与受控签名

落实已批准产品规划的 Windows 签名安装包和独立验收。现有分发器和更新器可验证预签名 MSIX，缺少批准构建到私钥签名的生产链。本轮提供完整的 unsigned MSIX → 专用节点 → 最终 MSIX → 独立质量路径；真实 Windows、企业代码签名和安装/升级仍须对应环境验收。

## 契约与权限

`target=windows` 的 unsigned 构建必须使用 `runtime=windows`、developer/build；签名必须 release/build、windows、`signingMode=windows-msix`。方案批准同时绑定 `expectedSigner`（证书 DER SHA256）、`expectedWindowsPublisher`（准确证书 Subject）、`expectedWindowsPackage`（MSIX Identity Name）、`expectedWindowsVersion`（四段包版本）。这些字段由控制面进入 signingInput、上传制品及最终回执；发布器原有 SHA1 thumbprint 是证书存储选择器，不替换批准的 SHA256。

签名节点不检出源码、不运行仓库命令、不兼任开发/质量/发布；只执行管理员固定程序。Worker 严格核对准确下载、回执和输出字节，撤权/取消/基线变化拒绝完成。只有最终签名包进入独立质量；CI 不能满足 desktop-native 安装/业务验证或发布审批。通用重试不能重复未知签名。

## 无签名包

构建助手新增 `windows-unsigned-msix`，在原生 Windows 上执行 Flutter release/x64，采用显式固定 MakeAppx、准确发布者/Identity/版本及本地 PNG 标识生成 Windows.Desktop/full-trust MSIX。HTTPS API 必须在构建前校验。manifest 仅声明受支持的单个 Win32 可执行入口、Internet 能力和 runFullTrust；无服务、启动任务、安装脚本或其他扩展。MakeAppx 原生校验不能用 /nv 绕过。

共享解析模块验证 ZIP 上限、规范 Windows 路径、大小写/文件目录歧义、特殊文件、加密/异常压缩、XML DTD/实体、完整 SHA256 BlockMap、Content Types、manifest 及 x64 PE32+ 入口。所有载荷由 BlockMap 覆盖；不允许已有 AppxSignature.p7x。版本化清单记录 SDK/命令/源摘要和实际产物，不将打包成功记成已安装。

## 管理员签名服务

Python CLI 仅在 Windows 接收严格 windows-1 单 JSON；固定项目/目标、DER、公钥摘要、包身份、版本、证书存储位置、SignTool 和 HTTPS RFC3161 时间戳服务。私钥保留在管理员配置的 Windows 证书存储/HSM，不接受 PFX、口令、调用者工具/路径/URL。固定 PowerShell 辅助程序读取证书和 Authenticode、检查代码签名 EKU/期限/私钥/精确 Subject、SHA256 与 SHA1，并验证专用目录 DACL。状态根由管理员预建，只允许专用账户、SYSTEM 和 Administrators；不修改机器信任、导入证书或提升权限。

私有 SQLite DELETE/FULL 日志、跨进程维护锁、签名前 durable intent；同操作成功重放只读原包，变更请求/配置拒绝，未确认 intent 保持待核对。SignTool 固定 `/fd SHA256 /tr <approved HTTPS> /td SHA256` 和准确存储 thumbprint；完成后 SignTool `/pa /all /v` 与 Authenticode 双重检查、准确叶证书和受信任时间戳。重新解析 MSIX，原始所有非签名条目逐字节不变，唯一新增签名必须存在且有界。成功 fsync 后才保存回执。

## 验收与保全

先失败后实现：实际控制面/Git/HTTP Worker/SQLite/归档、双连接 PostgreSQL 争用与恢复、恶意包/错误身份/撤权/取消/回执漂移/重复操作，Python 子进程和进程清理；Windows 提供方成功使用明确标识的替身。非 Windows 原生入口必须拒绝，不伪造 SignTool、证书、时间戳、安装或升级成功。

主线程顺序，不派 Agent、不提交/推送/改索引，不调用付费模型、真实商店或生产。保留734源码无关字节、335历史交付物、261索引/零提交、18080/PID36063/dist、当前Docker/AVD/keychain。中间文件 work；报告/手册/源码/证据仅既定 outputs，逐项读回再关闭本地增量。Apple许可69和完整P3保持开放。
