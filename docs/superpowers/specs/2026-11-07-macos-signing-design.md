# macOS 可信签名、安装包与公证

本设计落实已批准产品规划中的 macOS 正式签名安装包、受控执行和外部等待。完整目标是无签名应用经专用节点产生 Developer ID 签名、公证并装订的 PKG，再交独立质量和既有发布审批。当前没有企业证书或公证账户；本地替代提供方不能构成真实正式交付证明。

## 当前事实与方案选择

现有 Android 签名已经提供当前需求/方案、准确无签名制品、专用 release/build 节点、租约、成功原始执行和签后独立验证。macOS publisher 只接受通过 pkgutil 与 Gatekeeper 的 Notarized Developer ID PKG；尚无对应签名生产链。Apple 构建助手可产生 macos-unsigned 应用归档，但本机 SDK 许可仍69。

复用上述控制面与隔离 Worker 协议，扩展为 macos-pkg 受控转换。采用固定 codesign/productbuild/notarytool 工具和管理员凭据；不允许运行仓库打包脚本。不将未公证的 APP 当成最终 package，也不允许普通构建把 unsigned-package 升级成可信签后制品。Android 现有回执和幂等定义保持兼容。

## 绑定与权限

- 无签名输入：target=macos、runtime=darwin、developer/build、artifactStage=unsigned。最终输入来自同一批准方案中成功依赖祖先的唯一 unsigned-package，具有准确仓库、提交、字节、生产run和源码binding。
- 签名任务：release/build、runtime=darwin、signingMode=macos-pkg、inputArtifactFrom、expectedSigner（安装器证书SHA256）、expectedApplicationSigner（应用证书SHA256）、expectedTeamId（10位Developer ID团队）。证书与团队进入方案批准和signingInput摘要。
- 签名任务没有command/outputPath/branch或调用者选择的密钥、entitlements、本地路径和外部ID。专用节点不能兼任开发/质量/发布，也不能领取普通仓库任务。
- 管理员配置固定项目/目标、bundle ID、应用与安装器证书/团队、专用keychain、entitlements、notarytool凭据profile与Developer目录。请求不能覆盖这些值；不导入证书、不自动解锁用户keychain、不接受Apple协议、不写全局信任或搜索列表。
- 输出回执保留原输入、操作与请求摘要，包含准确应用/安装器证书、团队、公证ID及公证结果。只有最终公证/装订/系统验证全部通过才能上传最终package；控制面验证实际原始成功run后才能产生QA与候选。

## 签名程序与输入

管理员安装的Python程序只接收版本化单JSON，严格字段、大小、重复/大小写别名检查。路径、大小和摘要在私有副本上再次检查。TAR仅允许唯一APP根、普通文件、目录与最终解析在APP内部的相对符号链接；拒绝重复/大小写冲突、父路径歧义、逃逸、链接循环、特殊文件及膨胀上限。提取过程不执行应用或仓库代码。

核对Info.plist的MacOSX/APPL、固定bundle ID、版本/构建号及主可执行文件；枚举实际Mach-O与嵌套bundle，自内向外签名。已有顶层分发签名拒绝，工具链内嵌签名须明确识别后由固定工具处理。签名使用固定证书、hardened runtime、安全时间戳及管理员entitlements。校验准确叶证书、团队、资源和嵌套签名后，用productbuild的固定component到/Applications生成签名PKG，不执行安装或任意scripts。

签名状态SQLite DELETE journal / synchronous=FULL先持久意图，所有临时/最终文件在私有目录，最终对象和目录fsync后完成。重复成功只核对原对象并重放；不同请求/配置冲突，未确认阶段不重签。payloadDigest记录被授权输入的树摘要；不能将签名会合法修改的Mach-O字节声称为原始逐字节不变。普通资源、模式、链接和身份另行比较，独立质量仍验证最终安装包。

## 公证等待与恢复

sign首次执行本地签名/PKG并在notary_submit_intent持久后提交一次。拿到准确UUID才保存notary_submitted与原PKG摘要。返回processing后控制面保存同一签名任务的status阶段与下一查询时间，释放Worker租约；后续领取只执行只读status，不下载原包、不重复签名或submit。

每轮查询使用同一任务、输入binding、原操作和原节点。status的runId和临时路径不改变私有操作binding，但当前控制面租约/权限必须有效。等待不占节点或增加模型调用；查询次数和绝对截止时间有界，达到上限明确阻塞。通用retry不授予重新签名权限。失联或结果不明确时保留状态，不因换key或恢复旧快照盲重放。

accepted必须通过notarytool info与log的准确UUID和原PKG SHA256正向关联，不能仅凭名称或状态。Rejected/Invalid保留诊断摘要且不发布。accepted后在持久staple_intent下装订并执行stapler validate、pkgutil证书检查与Gatekeeper Notarized Developer ID。中断后只能以准确本地包和正向只读验证确认完成，不重新提交公证；无ID的submit intent保持待核对，当前不提供强制解锁。

## 独立质量与正式交付

最终PKG仍须独立macOS节点检查准确制品、原输入来源、签名/团队、公证装订、实际安装和业务验收。CI或本地合成回执不满足desktop-native目标环境门槛。发布继续需要原候选审批及既有hosted验证器；开发者不能降低质量或将processing记成交付。

## 验收

1. 原生macOS任务可批准、专用领取、准确输入派生；跨目标/系统/权限、伪造字段、旧基线/输入/租约和回执漂移均拒绝。Android回归保持。
2. 实际HTTP Worker循环在本地协议提供方执行首次签名、等待、释放节点、到期只读查询与最终制品上传；准确任务不重复签名，撤权/取消/截止停止后续动作。
3. 真实SQLite、并发进程和独立PostgreSQL/备份恢复保留请求/阶段/输入/审计与幂等；重复消息不能生成第二次签名或公证提交。
4. TAR与工具/证书/公证契约有真实失败与独立读回。运行本机codesign等工具的可用负例不证明企业成功；合法Developer ID和真实Apple服务成功路径明确保持待验。
5. 原18080/PID36063、web/dist、700源码基线无关字节、327历史输出、261索引/零提交、Docker/AVD及用户keychain状态保全。交付报告、手册、源码与证据独立读回；整体P3保持开放。
