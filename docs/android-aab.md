# Google Play AAB 的受控构建、签名与验证

平台将 Google Play 的无签名 App Bundle、固定上传签名、准确制品独立验证连接起来。目标为 `android-google-play` 时使用 `signingMode:android-aab`；华为、小米、OPPO、vivo 仍使用 `android-apk`。两种格式不能交换目标。控制面、Worker 和固定签名程序分别检查映射。

本地构建和上传签名不会证明 Google Play 已分发该版本。上传证书、Play App Signing 的分发证书、本地派生 APK 的安装证书分别记录。完整产品 P3 仍要求真实渠道、设备、企业身份和正式交付结果。

## 管理员工具和身份

使用 [Android 可信签名](android-signing.md) 的专用 release/build Worker、私有配置、权限和持久意图。签名节点不检出源码，不执行仓库命令，不向开发进程传递私钥。当前 Python 实现依赖 POSIX，Windows 签名服务需单独验证。

将已审查的 `sign-android-apk.py`、`android_bundle.py` 和 `VerifyAndroidBundle.java` 安装到管理员维护、仓库任务不可写的位置。该转换器文件名沿用既有入口，支持 APK 与 AAB 两种固定 profile。AAB 使用 JDK 17+ 的 Java、keytool 和 jarsigner；SDK 的 APK 签名工具仍用于本地派生 APK 检查。

固定 bundletool 1.18.3，来源是 [Google 官方 release](https://github.com/google/bundletool/releases/tag/1.18.3)，资产 `bundletool-all-1.18.3.jar`，32,520,401 字节，SHA256：

```text
a099cfa1543f55593bc2ed16a70a7c67fe54b1747bb7301f37fdfd6d91028e29
```

参考 `deploy/signer.android-aab.example.json`。profile 固定项目、目标、应用 ID、上传证书、私钥、密码文件和工具路径。批准证书为 DER 编码 X.509 证书的 SHA256。内置命令使用 RSA/SHA256 上传密钥；需提前确认 keystore 的密钥类型。示例证书占位符必须更换。

将签名 Worker 的 `signers["android-google-play"]` 配置为固定绝对程序和 AAB 配置文件。配套模块与 Java 源码需一并部署，不能从待签名仓库加载。bundletool 在每次 Java 检查前按配置摘要核对。配置和凭据为进程用户拥有的 0600 普通文件，状态目录为 0700；密钥与密码位于持久状态目录外。

同一 OS 身份的 native 进程隔离和本地 keystore 不构成 HSM 或企业多租户安全证明。企业节点必须独立部署并验证实际身份、网络、文件与备份边界。

## 构建无签名 bundle

先使用 `new-app.py --create-platforms --android-unsigned-release` 创建工程；release 配置明确取消 debug 签名。工程携带构建助手与 `android_bundle.py`，验证器和 bundletool 由管理员在固定路径提供。

```sh
rtk proxy python3 scripts/build-reference-client.py \
  --application /private/build/new-app \
  --target android-unsigned-aab \
  --api-url https://api.company.example \
  --java-home /opt/java/21 \
  --bundletool /opt/intelligence-substrate/trusted/bundletool-all-1.18.3.jar \
  --bundletool-sha256 a099cfa1543f55593bc2ed16a70a7c67fe54b1747bb7301f37fdfd6d91028e29 \
  --bundle-verifier /opt/intelligence-substrate/trusted/VerifyAndroidBundle.java \
  --output /private/build/artifacts/unsigned.aab
```

构建只接受 HTTPS API。执行 Flutter analyze、widget 测试以及 `build appbundle --release --target-platform=android-arm64`，随后核查有界 ZIP、Flutter 结构、准确应用 ID/版本/minSDK≥24、非 debuggable、仅 arm64 与真实无 JAR 签名状态。bundletool validate/dump 和 Java 全条目读取同时参与检查。任一失败都不发布产物；成功清单绑定准确字节和构建输入，不覆盖现有文件。

计划延续已有签名转换协议：

```json
[
  {
    "id": "unsigned",
    "title": "Build unsigned AAB",
    "kind": "build",
    "role": "developer",
    "runtime": "linux",
    "repositoryId": "repository_example",
    "commit": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "target": "android-google-play",
    "artifactStage": "unsigned",
    "command": ["/opt/company/build-unsigned-aab"],
    "outputPath": "artifacts/unsigned.aab"
  },
  {
    "id": "sign",
    "title": "Sign exact upload bundle",
    "kind": "build",
    "role": "release",
    "runtime": "linux",
    "target": "android-google-play",
    "signingMode": "android-aab",
    "inputArtifactFrom": "unsigned",
    "dependsOn": ["unsigned"],
    "expectedSigner": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
  }
]
```

`/opt/company/build-unsigned-aab` 是企业固定构建入口示例，需自行部署。来源、权限、同目标成功依赖、需求/方案版本、准确源码与制品摘要均由原协议检查；外部请求不能填写权威 `signingInput`。无签名包只能上传为 `unsigned-package`，不能用于最终 QA 或发布候选。

## 签名与中断恢复

签名前检查 ZIP 展开边界、重复/大小写冲突、路径/链接、Flutter 结构和无既存签名。SQLite WAL/FULL 持久保存原 `signing` 意图后，才调用 jarsigner，以 `SHA256withRSA` 对准确输入签名。密码经临时私有文件传入，不进入公开 argv、环境或 journal。

签后 Java JarFile 实际读完每个 payload 条目，要求恰好一个批准的有效上传证书，拒绝未签条目、内容篡改、多签名者、错误证书或 Android Debug 身份。签前/签后的 payload 文件名和 SHA256 必须完全一致；只有 JAR 签名元数据可以变化。再次核查 bundletool 与应用身份。

成功输出以 `.aab` 内容寻址存储并同步，再持久登记回执。同 key、原输入与配置重放返回原字节，不重签。失败、已开始或结果未知的意图继续阻塞；不能删除 journal、重置 key 或借普通 retry 绕过核对。管理员可沿用 `--inspect <operationKey>` 只读查询。

受保护质量失败可复用最多两轮的[签名包修复](android-signing-repair.md)：源码修复后生成新 unsigned AAB 和新签名任务，使用原批准证书与质量策略。每轮新 operationKey，原意图不变。协议/数据库测试覆盖 AAB 修复；本轮真实 AAB 验收未执行模型修复。

## 独立质量与本地安装

质量流水线使用 `method:automated, verificationEnvironment:ci`。CI 是执行环境，验收标准的 method 应使用 automated。固定管理员程序 `verify-reference-bundle-quality.py` 接收准确 `IS_ARTIFACT_PATH`，使用公开工具与证书，无私钥访问，生成不可覆盖的 tar 报告。参数为 application、output、java-home、bundletool、bundletool-sha256、bundle-verifier、expected-package 和 expected-signer。

报告包含准确 AAB 摘要、体积、上传证书、结构与身份、77 个本轮 payload 条目摘要和完整源码快照。条目数由实际包决定。独立验证运行成功后才有平台质量证据；无签名输入不调度。设备门槛仍为 `android-device`，CI 报告不会满足它。

本地安装使用 bundletool 从准确 AAB 生成 universal APK，使用另一临时安装证书，并记录 AAB → APKS → APK → 设备内 base.apk 的摘要关系。设备安装/界面报告只属于该派生 APK，不能称其为 Play 下发包、Play 的分发签名或 AAB 的直接安装。正式 Play 版本必须通过真实渠道、实际分发证书和目标设备另外验收。

## 现场入口与验收范围

```sh
rtk proxy go run scripts/verify-reference-bundle.go \
  --work-dir /private/qa/new-bundle-acceptance \
  --signer-config-template /private/qa/temporary-upload-signer.json
```

这是受限的本机诊断入口，限定 Google Play、feedback 示例包名和显式临时证书；依赖当前项目 `work/run-native.py` 的隔离工具链配置。它用合成测试人审建立需求与方案，通过真实 HTTP/Git Worker 执行构建、签名和自动独立质量，验证正式候选因缺真机证据拒绝。没有模型、商店或生产调用。企业 CI 应按上面的公开参数配置自己的工具路径与可信节点，不依赖本机 work 目录。

本轮完成真实 Flutter/Java 构建、准确 140 文件 Git 树核对、77 payload 文件不变、持久成功重放与 5 类真实 Java 拒绝检查。独立本地派生 APK 在专用 Android16/API36 arm64 模拟器完成同字节安装、启动、刷新失败提示与空标题验证；使用 HTTPS 示例 API，只作离线 UI 冒烟，不证明在线业务。

真实企业上传身份、Play App Signing 的分发签名、物理设备、真实提交/审核/上架、企业密钥灾备和完整 P3 仍开放。既有 Google Play 连接器的真实账号验收须另行完成。本机 Apple 两个 SDK 的最新检查仍返回许可错误 69，继续依赖用户本人接受 Xcode/SDK 许可；Windows 继续依赖对应执行节点。
