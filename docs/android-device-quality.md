# Android USB真机业务验证

051为原产品的Android真机门槛补齐受保护脚本入口：读取Worker下载的准确release APK，核对指定USB设备、证书和版本，再执行Flutter/Go反馈或预约业务。**本轮成功USB/Android UI与签名SDK提供方为固定测试进程，不是实际真机、原生编译、企业签名或正式渠道证据。**实际真机验收仍待设备接入。

## 管理员部署与信任边界

在独立Linux或macOS质量节点安装受信Python3、RTK及Android SDK的ADB/aapt/apksigner，配置验证签名所需JDK。将`verify-reference-android-device.py`、`android_device_quality.py`、`android_quality_common.py`一起安装在Agent不可修改的管理员目录。现有模拟器入口`verify-reference-android-quality.py`现在也依赖同目录的`android_quality_common.py`，升级部署时须一并安装；模拟器参数和证据分类保持不变。

使用者预先授权USB调试并独占专用设备。脚本不会选择第一台设备、接受授权提示、覆盖已有包、改网络/证书信任或停止管理员ADB。通过显式`-H 127.0.0.1 -P PORT`连接已运行服务器；真实ADB负向探测已确认缺少目标服务器时返回失败，不启动daemon。

设备锁按本机serial共享，不随ADB端口改变，不能约束未遵守锁的外部工具。管理员须在运行期间保持设备独占，不人工安装、替换或操作同一包。USB清单和Android属性核对不构成抗伪造硬件证明；可信质量节点、工具和管理员配置仍是信任边界。首版此入口不支持Windows宿主，Android测试可由独立Linux/macOS节点执行。

## 配置与准确输入

复制[配置示例](../deploy/quality.android-device.example.json)到生成应用之外的管理员私有目录，替换全部占位项，设为当前运行身份拥有、0600、单链接普通文件。配置固定USB serial、ADB端口、准确manufacturer/model/build fingerprint/SDK、包名、versionCode/versionName、release证书SHA256、业务模式和专用HTTPS API。

预约使用`mode:booking`并增加明确`slotId`；后端初始须恰好只有这一个未预约测试时段。反馈初始后端须为空。`dedicatedTestAPI:true`是管理员对专用测试环境的声明，脚本另查健康模式和初始数据，不能由空列表推断企业授权。测试反馈会保留，下一次准备新的空状态；不删除其他或生产数据。

API只支持完整HTTPS origin，拒绝userinfo、额外路径、query和fragment。默认验证系统证书链；可选绝对`apiCAFile`只为本质量进程指定CA，不改系统或手机信任。应用构建时使用同一API_BASE_URL并已信任此服务，脚本不会向手机安装CA。请求不跟随重定向、不使用环境代理、不重试写入；单请求8秒，JSON响应最多2MiB。需要代理的环境由节点网络部署提供可达路径。

首版支持flutter-go-v1反馈/预约、单arm64 release APK、标准`.MainActivity`、明确非调试v2/v3单证书签名。源码application.json须与批准包名和业务一致。aapt/apksigner核对准确版本及证书后，安装到设备的base.apk SHA256还须等于平台下载包。摘要是批准身份选择器，本工具不证明企业证书所有权或商店分发来源。

## 受保护流水线

质量负责人安装固定脚本、配置和工具，注册仅承担独立质量的可信节点。替换以下路径为实际批准部署位置：

```sh
rtk proxy python3 /private/quality/verify-reference-android-device.py --config /private/quality/android-device.json --application . --adb /private/sdk/platform-tools/adb --aapt /private/sdk/build-tools/36.0.0/aapt --apksigner /private/sdk/build-tools/36.0.0/apksigner --output quality/android-device.tar.gz
```

`IS_ARTIFACT_PATH`由Worker提供。创建流水线时固定`method:device`、`verificationEnvironment:android-device`、宿主`runtime:darwin`或`linux`及`reportPath:quality/android-device.tar.gz`，仅绑定本工具实际覆盖的业务标准。通过[独立质量调度](quality-scheduling.md)绑定批准方案与Android目标；发布规则保持原要求。

准确运行、源码提交、制品、原始报告与候选继续由[受保护报告机制](reference-quality.md)绑定。该入口没有改控制面权限、证据有效性或正式发布门禁，模拟器结果仍不能改标为真机。

## 业务、清理和报告

反馈验证空标题不写入、原生提交唯一测试标题/内容、HTTPS API独立读回及应用重启后的同一服务器记录。预约验证过期界面冲突、48位取消凭据、错误凭据不能取消、正确凭据恢复时段和应用重启状态。UI只查找批准包名的控件，截图前再核对前台应用，不点击其他应用的同名按钮。

安装前核对设备及包初始缺失，派发ADB前持久保存私有ownership意图。安装回复丢失时，受控退出仅核对并删除最初不存在且摘要准确的包。预装包、变化后的设备身份或不同安装字节保留，运行失败并要求核对；清理失败也使验证失败。只删除本次随机命名的UI dump，不重启ADB或手机，不清其他应用数据。

SIGINT/SIGTERM进入失败报告与清理；SIGKILL、节点丢失、ADB不可达或外部同时操作仍可能留下包、诊断和测试数据。重跑会拒绝已存在包，不自动覆盖或重放未知安装。管理员先核对ownership、日志、设备与准确APK，再处理遗留。预约结果不明时，私有报告保留API意图及已取得的原取消凭据，不自动重试写入或清除未知预约。

0600报告tar.gz拒绝覆盖，保存result.json、源码逐文件摘要、APK/版本/证书、USB观察、API历史、截图/UI层级、ownership和清理结果。设备标识和测试取消凭据按项目质量证据权限保存，不纳入本轮公开原始导出。强制终止可能来不及生成报告，启动日志会给出私有诊断目录。

## 实际验收仍需完成

本入口覆盖安装、指定业务及应用重启。**后端进程重启、离线恢复、旧版升级、商店分发、企业身份、硬件证明与全网络隔离另行验收。**独立质量策略须用其他测试或人工证据覆盖相应要求；单次工具成功不是完整Android交付。

Google Play将AAB转换为渠道APK，原AAB、Play版本、实际安装包和设备报告的对应关系另行取得。本地APK测试不能计为Play实际分发；国内商店也需真实审核、上架及准确版本验证。两个业务的全部必选端/渠道正式交付后，P3才完成。

实际设备缺口见[验证矩阵](validation.md)和[产品验收清单](product-acceptance.md)。本地契约回归：

```sh
rtk proxy python3 -m unittest discover -s scripts -p 'test_android_device_*.py' -v
rtk proxy python3 -m unittest discover -s scripts -p test_reference_android_quality.py -v
```

测试运行真实本地TLS和参考Go API，USB、UI、APK结构与成功签名提供方为测试替身；绿色结果不放行真实设备或商店。

<!-- END INTELLIGENCE SUBSTRATE ANDROID DEVICE QUALITY RUNBOOK 051 -->
