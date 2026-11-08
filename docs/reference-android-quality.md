# Android同制品自动模拟器质量验收

此流程将实际平台构建的Android诊断APK交给独立质量Worker，在明确指定的专用模拟器运行，并收集同一制品的原始报告。它扩展既有受保护报告和自动调度，不改变真机或正式发布门槛。

真机业务已有单独的[USB设备验证入口](android-device-quality.md)，需要管理员固定设备、准确release包与HTTPS测试后端，实际设备验收仍待执行。两个入口部署时均须安装同目录的`android_quality_common.py`；本页模拟器流程与证据分类保持不变。

## 输入、身份与资源归属

管理员提供 `scripts/verify-reference-android-quality.py`、Android SDK的ADB/aapt/apksigner、Java、Go及RTK。脚本仅支持当前flutter-go-v1的反馈和预约业务、arm64 Android Debug签名包，以及明确的诊断包名 `com.intelligencesubstrate.quality.android_feedback` / `android_booking`。

通过受保护pipeline声明命令、`reportPath:quality/android.tar.gz`、`method:simulator`、`verificationEnvironment:simulator`。质量节点只使用Worker下载的 `IS_ARTIFACT_PATH`，不读取另一轮外部build manifest。真实被测target使用平台现有名称 `android-google-play`；它表示需求目标，不表示此诊断APK满足Play提交条件。

脚本要求显式serial、AVD名称、已经运行的独立loopback ADB端口及空闲API端口。它不启动模拟器或ADB服务器，不自动选择设备。管理员负责在没有敏感凭据的可丢弃节点准备专用模拟器；native执行不构成沙箱。AVD克隆、隔离端口和设备锁不证明多租户隔离。

## 安装前与清理约束

- 校验ZIP完整条目、路径、链接/特殊条目、重复及大小写冲突、父目录关系、加密、CRC和展开上限，不解包APK。最多256MiB压缩包、20000条目、单条100MiB及总展开400MiB；结构检查本身不证明编译或签名。
- 使用真实aapt核对准确包名及arm64 ABI，真实apksigner核对诊断签名；源码application.json必须匹配受保护的准确包名与已知蓝图。
- 空闲API检查和设备独占锁在安装前生效。核对准确AVD和启动完成状态；已安装同名包立即拒绝，不覆盖或卸载。ADB传输失败不能被当作“未安装”。Android的pm path对缺失包返回1且空stdout/stderr，仅此组合被视为不存在。
- 安装意图在执行ADB前记录，即使安装成功后回复丢失，清理仍核对本轮最初不存在的包。安装后从设备内读取base.apk的SHA256，必须等于平台下载包摘要。
- 退出时只清理自己的Go后端、诊断UI文件及本轮包。卸载前再次核对设备内APK摘要；字节变化则保留并报错，不卸载其他来源的应用。不会停止管理员提供的ADB或模拟器。
- SIGINT/SIGTERM会进入失败报告和清理；SIGKILL、节点丢失或ADB不可达时仍可能留下需操作员核对的资源，不能声称无条件恢复。

## 实际业务与报告

反馈检查空标题、实际提交、服务器记录、后端重启持久化、API不可用及恢复刷新。预约检查过期客户端冲突、48位不透明取消凭据、服务端不保存明文、错误凭据拒绝、正确取消、后端重启及API恢复。

报告tar.gz保留result.json、APK badging/签名工具输出、API日志和实际Android截图/UI层级。记录准确包摘要、设备内已安装摘要、完整源码文件SHA256、后端二进制摘要、系统/ABI/指纹、检查和清理结果。成功缺报告仍由现有Worker/控制面阻塞，报告来源及字节按[受保护报告契约](reference-quality.md)复核。

本流程只验证声明的本机API业务；没有实施Android全网络隔离或证明没有其他出站请求，报告明确 `networkIsolationVerified:false`。模拟器运行原生Android代码，与Chrome手机宽度验证不同，也与移动真机验证不同。

## 配置与复验

管理员维护的受保护命令示例，省略部署位置相关的凭据：

```sh
rtk proxy python3 scripts/verify-reference-android-quality.py \
  --application /private/qa/exact-source \
  --adb /private/sdk/platform-tools/adb \
  --aapt /private/sdk/build-tools/36.0.0/aapt \
  --apksigner /private/sdk/build-tools/36.0.0/apksigner \
  --serial emulator-5586 --avd Company_QA \
  --adb-server-port 15039 --api-port 18138 \
  --expected-package com.intelligencesubstrate.quality.android_feedback \
  --output /private/qa/quality/android.tar.gz
```

`IS_ARTIFACT_PATH`由Worker提供；单独运行时须明确传入准确APK路径。输出必须不存在。Android应用编译时API_BASE_URL使用对应的 `http://10.0.2.2:18138`；该HTTP仅用于明确本机诊断。真实产品仍按原HTTPS策略。

实际平台样本入口：

```sh
rtk proxy go run ./scripts/verify-reference-android-quality.go \
  --work-dir /private/qa/new-platform-run \
  --serial emulator-5586 --avd Company_QA --adb-server-port 15039 \
  --android-sdk /private/sdk --native-wrapper /private/qa/run-native.py
```

工作目录必须不存在。包装程序由管理员提供，配置Flutter、Java、缓存和SDK；当前本机复用忽略目录work/run-native.py。参考入口选择蓝图批准的Gradle9.4.1，并使用18138/18139作为两套样本API端口。合成测试身份批准基线和构建方案，真实Git/Worker构建后自动调度质量，不使用pipelines.run或模型调用。

完整源码树和每个文件摘要须与准确Git提交核对。平台证据保持 `method/environment=simulator`。实际调用releases.create必须仍因缺少 `android-device` 独立真机证据被拒绝；模拟器通过不会生成可批准的正式候选。诊断签名、截图和包名也不能替代企业release签名、商店版本或上线验证。

## 本机实际结果与兼容性

最终平台证据为 `work/reference-android-quality-gsvgseac/actual-final/result.json`。反馈、预约分别从新Git提交真实构建，通过Flutter analyze、26项widget测试和Android debug构建，再自动领取同一APK完成原生业务检查。每应用有1条自动调度、2次实际执行、1条独立通过证据及1份准确报告；报告完整139个源码文件及逐文件SHA256与对应提交一致，设备内base.apk摘要与平台APK一致。两次实际releases.create均因缺android-device证据被拒绝，持久候选数为0。

反馈通过3组检查，预约通过4组，包含同包安装核对与API不可用恢复；预约还独立检查取消所有权和重启持久化。57项Python回归、相关Go race 8顶层/33含子场景及8项真实CLI负向检查通过；负向检查覆盖危险APK、错包名/AVD/serial、占用API、设备锁、旧报告和预装包保留。运行代码与工作台没有变更，前轮全量Go及浏览器结果属于前轮证据。

实际工具差异保留原失败记录：隔离HOME下 `adb emu avd name` 成功但返回空值，使用设备只读 `ro.boot.qemu.avd_name` 核对AVD；ADB安装要求文件名带 `.apk`，因此复制并核对实际下载字节；设备安装目录含 `~~` 随机前缀。只处理真实pm缺包的空输出/状态1，不放宽传输失败。专用端口与AVD克隆不证明私有主机ADB密钥隔离；本机服务器仍报告主机既有ADB身份。

报告内清理结果只证明本次成功停止自有后端和卸载自有包。外层测试负责在结束时停止本轮专用模拟器/ADB，核对原AVD、预览、Docker、暂存记录与既有交付物；适配器本身不取得停止管理员设备的权限。

安全回归：

```sh
rtk proxy python3 -m unittest discover -s scripts -p test_reference_android_quality.py -v
```

原生Apple许可、Windows节点、物理设备、企业签名/渠道账户及完整P3仍按原产品验收开放。

## Release 签后离线冒烟

`--release-offline-smoke` 与 `--expected-release-signer SHA256` 必须同时提供。此模式独立检查准确证书、v2/v3、非调试包、设备内 APK 同摘要和原生启动/离线刷新/空标题校验，不启动后端，也不宣称在线 API 或业务全流程通过。报告记录 `businessVerified:false`、`liveAPIVerified:false`、`enterpriseIdentityVerified:false`；method/environment 仍为 simulator，不能替代 android-device。既有 debug 模式的真实 API 业务验收保留。无签名构建到可信签名、签后自动质量的完整操作见 [Android 可信签名](android-signing.md)。
