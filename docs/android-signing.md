# Android APK 可信签名

本增量将无签名 release 构建、管理员固定签名程序、签后独立验证连成一条受控链路。私钥与密码由签名节点持有；开发任务不接收凭据，签名工作项不能执行仓库命令。覆盖华为、小米、OPPO、vivo 的 APK 转换接口，当前实际样本使用 `android-huawei`，未向任何商店提交。

Google Play 使用独立的 android-aab profile 和 JAR 签名路径，配置与验收见 [Google Play AAB](android-aab.md)。iOS、macOS 和 Windows 签名不在这个 Android 转换器的支持范围内。临时测试证书只验证签名技术链路，企业身份、真机、在线业务和正式上架仍须分别验收，完整 P3 保持开放。

## 管理员配置

部署 Python 3.10+、Android SDK build-tools 36.0.0 与 Java。将已审查的 `scripts/sign-android-apk.py` 及配套 `scripts/android_bundle.py`、`scripts/android_signer_state.py` 安装到管理员维护、仓库任务不可写的固定位置，不从待构建仓库加载签名代码。SDK 和 Java 也必须由管理员维护。AAB 还需要固定 Java 验证器和 bundletool。

内置 Python 签名程序依赖 POSIX 所有权和 O_NOFOLLOW，面向 Linux/macOS；本轮真实 SDK 与模拟器验收运行于本机 macOS。平台的固定程序协议可适配其他系统，但 Windows 需另行提供和验证管理员签名程序，不能直接将此脚本当作 Windows 签名服务。

参考 `deploy/signer.android.example.json`：每个 profile 固定 projectId、goalId、target、bundleId、证书 SHA256、keystore/alias、密码文件与 SDK/Java 路径。示例证书为占位值，必须换成实际批准证书的小写 SHA256。请求无法选择这些路径或证书。

配置、keystore 和密码文件必须为当前签名进程用户拥有的普通文件，不得使用末级符号链接，权限为 0600；状态目录为该用户拥有的 0700 目录。密钥与密码必须位于状态目录之外。密码文件供 SDK 的 `file:` 输入使用，内容不进入 argv、环境变量、SQLite、公开日志或响应。程序在意图持久化后创建两份短期私有密码副本，以避免 apksigner 对同一密码文件路径重复读流导致 EOF；正常退出会清除副本。

注册专用 Worker：`roles:["release"]`、`capabilities:["build"]`、`trustedQuality:false`，runtime 与实际系统一致。不能兼任 developer、quality、publish 或其他能力。`deploy/worker.signer.example.json` 的 `signers[target]` 指向固定绝对程序和管理员配置；未配置时阻塞，禁止回退到仓库构建命令。仅配置目标映射不会注册节点或授权发布。

控制面将仅 release/build 的节点保留给签名任务，不向其派发普通仓库构建；配置了非空 `signers` 的 Worker 也在获取工作区之前拒绝其他执行和发布任务。两侧限制同时生效，避免通过普通 release/build 工作项在签名节点运行仓库代码。

平台角色分离和不检出源码属于执行协议边界。同一 OS 用户下的 native 进程仍可能互相读取文件；目录权限、本地 keystore、AVD 克隆不构成企业多租户或 HSM 隔离。企业部署应将签名节点置于独立身份和主机/隔离域，限制网络与文件访问，并单独验证隔离措施。当前实现不提供远程签名服务、HSM、证书轮换或商店操作。

## 无私钥构建

新工程必须显式选择无签名 release 配置：

```sh
rtk proxy python3 scripts/new-app.py /private/build/new-app \
  --name application --mode feedback --organization com.company \
  --create-platforms --android-unsigned-release

rtk proxy python3 scripts/build-reference-client.py \
  --application /private/build/new-app --target android-unsigned-apk \
  --api-url https://api.company.example \
  --aapt /opt/android-sdk/build-tools/36.0.0/aapt \
  --apksigner /opt/android-sdk/build-tools/36.0.0/apksigner \
  --output /private/build/artifacts/unsigned.apk
```

生成器只接受已识别的 Gradle 模板，将 release 的调试签名替换为 `signingConfig = null`，保留 debug 配置。构建不修改源码，执行 Flutter analyze、widget 测试、arm64 release 构建，独立核对实际包名、版本、minSDK、ABI、非 debuggable 和无签名状态。输出及 manifest 不覆盖已有文件。工程脚本调用 SDK/Flutter 不依赖 RTK；本机命令示例使用 RTK 代理。

## 计划、租约与产物

计划中的构建/签名片段：

```json
[
  {
    "id": "unsigned",
    "title": "Build unsigned release",
    "kind": "build",
    "role": "developer",
    "runtime": "linux",
    "repositoryId": "repository_example",
    "commit": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "target": "android-huawei",
    "artifactStage": "unsigned",
    "command": ["/opt/company/build-unsigned-apk"],
    "outputPath": "artifacts/unsigned.apk"
  },
  {
    "id": "sign",
    "title": "Sign exact approved APK",
    "kind": "build",
    "role": "release",
    "runtime": "linux",
    "target": "android-huawei",
    "dependsOn": ["unsigned"],
    "signingMode": "android-apk",
    "inputArtifactFrom": "unsigned",
    "expectedSigner": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
  }
]
```

上述命令是管理员部署的构建入口示例，并非仓库内置文件；完整方案仍需正常需求与方案批准。当前通过 API 计划配置签名，没有独立的签名向导。

无签名构建只能上传 `unsigned-package`，界面显示“待签名制品”，不能用于最终质量证据或发布候选。签名输入必须是同方案、同需求版本、同目标的成功依赖祖先，并有唯一实际构建 run 与准确仓库/提交。控制面检查实际字节后派生 `signingInput`，包含输入 ID/摘要/大小、源码绑定、生产任务/run、批准证书和稳定 `operationKey=sign-<taskId>`。

外部方案禁止传入 `signingInput/signingInputDigest`。签名工作项禁止 command、outputPath、artifactId、artifactStage、sourceMode、sourceFrom、branch。上传时由有效租约派生签名来源字段；完成时复核当前基线、方案、权限、输入字节、源码绑定与签名回执。最终输出为新的 `package`，不能复用输入摘要。未确认成功的签后上传包不能用于质量或发布；验证入口会重查原始成功签名 run、回执与当前权限。

Worker 仅下载绑定的无签名制品，核对摘要和大小，不检出源码，不运行仓库命令，也不传递 IS_* 控制面凭据。固定程序响应限 64KiB，必须为单个 JSON 对象及精确字段集，拒绝重复键、额外字段、不匹配证书、输入、输出摘要和末级链接文件。核对后复制同字节包再上传，公开结果仅保存签名绑定和摘要，不保存私有路径或 SDK 诊断。

## 签名程序与恢复

程序通过 stdin 接受版本 1 的 sign 请求，协议字段为 protocolVersion、action、operationKey、projectId、goalId、runId、repositoryId、commit、target、artifactId、artifactDigest、artifactSize、artifactPath、expectedSigner。拒绝未知字段、重复/大小写别名、额外 JSON、非法身份与路径；仅接受当前蓝图 arm64、minSDK≥24 的原生 Flutter APK。

先核对输入摘要/大小、ZIP 路径/父目录/重复/CRC/链接与展开边界、Flutter 结构、无既存 v1/v2/v3 签名及真实 aapt 非调试包信息。SQLite WAL/FULL 持久记录 `signing` 意图后才读取密码内容、执行 zipalign 与 apksigner。签后独立检查准确证书、非 Android Debug、v2/v3、16KiB 原生页对齐和完全一致的 ZIP payload 文件摘要。

私有状态目录保存内容寻址的 APK 与签名回执。对象与目录 fsync 完成后持久登记成功。同一 operationKey、同输入和配置的成功重放只核对原对象/回执并返回，不重签；更改输入、run 或配置会冲突。started/failed 意图进入待核对状态，程序不自动重新签名。

管理员只读查询：

```sh
rtk proxy python3 /opt/intelligence-substrate/trusted/sign-android-apk.py \
  --config /etc/intelligence-substrate/signer.android.json \
  --inspect sign-task_example
```

签名失败、租约中断或无法确认完成时，控制面阻塞。普通 `tasks.retry` 拒绝签名任务；Worker 已持久保存的完成上报可以按原摘要重放，不能借此重新执行签名。当前没有自动“接纳已签包”或清除意图的恢复 API。操作员须保留并核对原请求、意图、包、回执和外部状态，再决定是否批准新方案/新任务。不得通过删除日志或改 operationKey 隐藏不明结果。

SIGKILL 可能留下私有临时文件，恢复须由管理员核对并清理确属本次操作的资源。备份应覆盖控制面与私有签名状态，并按企业密钥制度单独保管凭据；只备份控制面不能恢复私钥。独立的[签名库备份恢复工具](signer-recovery.md)支持认证加密的一致快照、同路径独占恢复及成功重放围栏；旧快照不能自动恢复为活跃签名节点。它不代表企业异地密钥托管或完整灾备验收。PostgreSQL 归档回归仍仅证明控制面的制品/任务/run 来源恢复。

## 签后独立质量

人类质量人员用既有 `quality.bind` 绑定受保护策略，最终签名 package 成功后才自动调度。开发者不能借用无签名证据或降低真机门槛。SDK 签名真实性由可信签名程序及独立质量程序检查；控制面自身不解析 APK 签名。

`scripts/verify-reference-android-quality.py` 增加成对参数：

```text
--release-offline-smoke --expected-release-signer <approved-lowercase-sha256>
```

保留原有显式 serial/AVD、独立 ADB 端口、包名、输出及准确 `IS_ARTIFACT_PATH` 配置。该模式检查准确证书、v2/v3、非调试、安装包和设备内 base.apk 同摘要、启动及离线刷新/空标题校验，不启动后端。报告明确 `businessVerified:false`、`liveAPIVerified:false`、`enterpriseIdentityVerified:false`；不得将其算作在线 API 或业务全流程通过。debug 诊断模式仍保留原来的真实本机 API 业务验收，见 `docs/reference-android-quality.md`。

已绑定自动质量策略的签后失败可按原批准配方进入“源码修复 → 新 unsigned 构建 → 新签名任务 → 原策略复验”，最多两轮。各轮使用新任务和签名 operationKey，保留原意图、证书与独立质量门槛；策略/配方变化和未知签名结果阻塞。手工临时测试不能自行授权此链路，具体记录、权限和中断恢复见 [签名包自动修复](android-signing-repair.md)。没有证书轮换或续期。

## 本机复验入口与验收边界

```sh
rtk proxy go run scripts/verify-trusted-android.go \
  --work-dir /private/qa/new-signing-acceptance \
  --signer-config-template /private/qa/temporary-test-signer.json \
  --serial emulator-5588 --avd Company_Signing_QA \
  --adb-server-port 15041 --android-sdk /private/android-sdk
```

入口仅用于本机临时测试证书，限定诊断包名 `com.intelligencesubstrate.quality.android_feedback` 与 android-huawei；需要本机管理员工具包装 `work/run-native.py`、专用已启动模拟器与新目录。它批准合成测试基线和方案，实际生成并导入 Git、运行 Worker release 构建/签名/自动质量，核对完整 139 文件 Git 树、最终报告及设备内 APK 摘要。没有模型调用、商店或生产动作；构建使用 HTTPS 示例 API，质量仅作离线冒烟。

加上 `--exercise-repair` 可验证真实 release-only UI 错误、确定性源码修复、新构建/签名和最终复验；同时保留失败与通过的原始报告。它验证平台链路，不证明模型自主修复能力。

正式发布的 android-device 真机证据仍缺失时，实际 releases.create 必须失败且持久候选数为零。真实企业签名身份、真机业务、四商店/Play 上架、Apple/Windows、企业隔离与生产交付均需独立证明。AAB 的本地构建与签名验证见独立手册，不能替代上述正式验收。原 18080 预览不由验收入口更新。
