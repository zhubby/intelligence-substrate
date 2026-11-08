# macOS 受控签名与公证

平台将准确的无签名 APP 归档交给专用 macOS 签名节点，生成 Developer ID Installer PKG、提交一次 Apple 公证、等待后查询原操作，确认成功后装订并执行系统验证。签名成功仍需独立质量、真实目标系统验收和正式发布批准。

当前成功路径使用本地工具契约提供方，没有企业 Developer ID 身份、真实 Apple 公证或安装成功证据。Command Line Tools 的 macOS SDK 查询可用；显式完整 Xcode 的 macosx/iphoneos SDK 查询仍返回许可 69。用户本人阅读并接受协议后才能继续完整 Xcode 构建。平台不接受协议、申请证书、导入私钥或修改系统信任。

## 输入和批准

先在 darwin developer/build 节点产生 `artifactStage:unsigned` 的 `unsigned-package`。内容是唯一 `.app` 根的 tar/tar.gz，来自准确成功构建。不能把它登记为最终 `package` 或独立质量的最终输入。

签名任务示例（ID、证书和团队都是占位值）：

```json
{
  "id": "sign",
  "title": "macOS 签名与公证",
  "kind": "build",
  "role": "release",
  "runtime": "darwin",
  "target": "macos",
  "signingMode": "macos-pkg",
  "inputArtifactFrom": "unsigned",
  "dependsOn": ["unsigned"],
  "expectedSigner": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "expectedApplicationSigner": "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
  "expectedTeamId": "ABCD123456"
}
```

`expectedSigner` 是安装器叶证书 DER 的小写 SHA256；`expectedApplicationSigner` 是应用叶证书 DER 的小写 SHA256。两者均不是证书名称、SHA1 或团队 ID。`expectedTeamId` 是10位大写字母数字。现有 publisher/updater 配置的 macOS `expectedSigner` 则表示团队 ID，各协议字段用途不同。

签名输入由控制面从当前批准方案、当前需求和唯一成功无签名依赖推导。证书、团队、仓库、提交、原制品字节、生产执行和源码绑定都进入摘要。调用者不能提供 `signingInput`、阶段、轮数、等待时间、公证编号或持久操作，也不能指定仓库命令、输出位置或凭据。更改批准身份须新方案。已绑定独立质量失败后可进入[受控签名包修复](native-signing-repair.md)，重新构建并以新操作签名，保持原身份与质量策略；未知签名结果仍阻塞。

## 专用节点与私有配置

节点只能注册 `roles:[release]`、`capabilities:[build]`、`runtime:darwin`、`trustedQuality:false`，并只安装 `signers.macos`。它不运行仓库命令、不下载源码、不兼任质量或渠道发布。配置示例见 [Worker](../deploy/worker.signer.macos.example.json) 和 [签名服务](../deploy/signer.macos.example.json)。

管理员在独立 OS 身份和机器中安装 `sign-macos-pkg.py`、`macos_signer.py`、`android_signer_state.py`，使用经验证的 Python 3.11+。程序、配置与固定工具不位于可被 Agent 修改的仓库/工作区。`/usr/bin/python3` 仅为占位路径，部署前替换为已验证的版本。配置绑定准确 project/goal、bundle ID、双证书、团队及全部工具绝对路径。

管理员预先提供独立私有 keychain、DER 公证书、已审核 XML ASCII entitlements 和该 keychain 内的 notarytool profile；程序不持有明文密码、不查找默认凭据、不解锁 keychain、不改变搜索列表。配置、证书、entitlements、keychain 为所属运行身份的单链接普通私有文件，权限0600；状态根0700。私钥/凭据放在状态根之外。管理员负责 Keychain ACL、非交互签名权限、可信证书链、证书续期和网络出口，程序不会自动配置这些事项。

entitlements 首版支持最多32个 `com.apple.*` 布尔项，禁止调试授权。示例 [entitlements](../deploy/signer.macos.entitlements.example.plist) 只开启客户端网络；Flutter JIT、插件或业务权限必须先由管理员单独审核和固定。输入应用携带的授权不会被采用。

子进程只得到固定 PATH、隔离 HOME/TMPDIR 与 C locale。IS_*、平台令牌和宿主环境不传入；工具标准输出/错误只进入私有有界日志，公共 Worker 仅返回受限回执或通用未确认说明。每个工具调用120秒、输出每路2MiB。Apple 工具继承 Worker 的专用任务进程组，取消会结束整组；工具超时先结束直接子进程，Worker 在签名程序退出后清理剩余后代。管理员命令不会向共享 shell 进程组发信号。不能据停止本地进程推断 Apple 没收到请求。

## APP 和安装包处理

- 输入最多256MiB、展开最多768MiB、最多50,000项、单文件最多256MiB。流式手工提取，不执行应用，不使用 `extractall`。
- 只接受唯一 APP 根、普通文件/目录和最终解析在 APP 内的相对符号链接。拒绝路径逃逸、绝对/循环/缺失链接、硬链接、稀疏/特殊文件、大小写冲突、父路径歧义和特殊权限。首版路径字符范围为 ASCII 字母数字及空格、下划线、点、@、+、括号、等号和短横线；不支持任意名称的外部应用。
- 顶层应用必须由 codesign 正向确认为无签名；核对 MacOSX/APPL、固定 bundle ID、版本/构建号、实际 Mach-O 主程序和嵌套代码，不接受可执行脚本或外部平台二进制。SDK 嵌套框架已有签名须先由固定 codesign 验证，再改用批准证书。
- 从内到外固定 `codesign --options runtime --timestamp`，只使用管理员 entitlements 和私有 keychain。逐项提取签后叶证书，核对应用证书 SHA256、团队、时间戳和 hardened runtime，再执行深度严格签名验证。
- `payloadDigest` 是授权输入树的摘要。Mach-O 会被签名合法修改，普通资源、模式和链接另行核对。签名不证明业务正确性。
- 固定 `productbuild --component APP /Applications --sign ... --keychain ... --timestamp`，不允许 installer scripts 或仓库打包器。pkgutil 必须确认可信 Developer ID Installer 叶证书和批准团队。

## 一次提交、持久等待和恢复

本地 SQLite 使用 DELETE journal、synchronous=FULL，修改前持久状态并 fsync，进程级私有维护锁防并发。阶段为 `signing_intent → notary_submit_intent → notary_submitted → staple_intent → signed`。`notary_rejected` 保留拒绝结果；未知阶段不得盲目重新签名或提交。

`notary_submit_intent` 在真实 submit 之前提交。只有拿到合法公证 UUID 才保存 `notary_submitted` 和原未装订 PKG 的摘要。无 UUID、超时、丢失响应均留待管理员核对；不提供强制清空或换 key 重放。

处理中的回执使同一任务变为 `awaiting_signature`、原 run 变为 `waiting_signature`，清除租约而不生成成功构建、最终包或 QA。平台每5分钟以原节点创建一个新的 status run；status 不下载原 APP、不重新 sign 或 submit。原节点可在等待期间处理其他应用；查询运行才占用节点。

最多288轮查询、首次领取起24小时绝对截止。等待时间不计活动执行，签名及查询共享任务原批准 `timeoutSeconds`（默认900秒）。查询不增加签名 `attempts`。预算、原输入、需求、方案、节点身份和权限继续检查，取消/撤权/变更/截止会停止继续。失联或未知执行阻塞，通用 `tasks.retry` 不重新授权签名。

info 必须返回准确 UUID 和 Accepted；log 必须以同一 jobId、Accepted 与原 PKG SHA256 证明结果。log 中错误拒绝，警告保留私有附件。只凭文件名、上传成功、info 状态或其他任务的公证不能完成。Accepted 后先保存 `staple_intent`，在单独副本装订，保留未装订原包；再执行 stapler validate、pkgutil 和 Gatekeeper `source=Notarized Developer ID`。最终对象与目录落盘后才保存成功回执。

装订中断后只允许读取原接受日志、原 PKG、装订副本和系统验证；不会再次 staple 或 submit。无法正向验证时保持待核对。成功重放只校验原最终对象字节，不调用工具。更换请求、证书、entitlements 或配置拒绝重用原操作。

管理员本机检查（只读业务观察；首次空状态会创建私有空日志）：

```sh
rtk proxy /path/to/python3 /Library/IntelligenceSubstrate/trusted/sign-macos-pkg.py --config /Library/IntelligenceSubstrate/private/signer.macos.json --inspect sign-task_example
```

控制面备份不包含签名节点。备份前停止该节点派发及全部签名进程，完整复制状态根，并单独安全保管凭据和原配置；恢复先隔离并检查原操作、原包/接受日志/最终对象及 Apple 状态。控制面等待备份和签名节点已知操作的离线复制已验证；未提供自动加密 Mac keychain 备份或恢复激活器。旧快照可能缺少后续外部提交，禁止直接启用为活跃节点。控制面新任务也不能替代原未知操作的外部核对。

## 完成和验收

最终回执包含双证书、团队、公证 UUID、输入/操作/请求/载荷摘要，以及准确最终 PKG。控制面复核当前租约、来源和等待时冻结的操作，成功后才调度独立 QA。独立节点验证的必须是最终包；CI 检查不能代替 macOS 安装和业务验收。发布端仍独立做系统签名/团队/公证检查，并需要原候选批准和发布后证据。

本地验证命令：

```sh
rtk proxy python3 scripts/test_macos_signer.py -v
rtk proxy go test -race ./internal/platform ./internal/worker -run 'TestMacOSSigning|TestPostgresMacOSSigning|TestWorkerMacOSSigning' -count=1
rtk proxy python3 scripts/verify-workbench.py --suite macos-signing
```

PostgreSQL 场景需要专用 `IS_TEST_POSTGRES_URL`。测试中的 Apple 工具、证书和 PKG 为明确合成契约；真实 HTTP、子进程、数据库、归档、摘要和浏览器操作分别记录，不能据此声称真实公证或企业交付。

官方契约参考：[Apple 公证要求](https://developer.apple.com/documentation/security/notarizing-macos-software-before-distribution)、[自定义公证流程](https://developer.apple.com/documentation/security/customizing-the-notarization-workflow)，以及目标节点本机 codesign/productbuild/pkgutil/notarytool/stapler 手册。官方资料不能替代真实企业账户和目标系统验收。

<!-- END INTELLIGENCE SUBSTRATE MACOS SIGNING RUNBOOK -->
