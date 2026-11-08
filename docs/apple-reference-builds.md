# Apple 参考应用诊断构建

参考应用的同一构建入口支持 `ios-simulator`、`ios-unsigned` 和 `macos-unsigned`。它们用于本地编译与产物追溯；后两者可分别交给独立的 [iOS 签名节点](ios-signing.md)和 [macOS 签名公证节点](macos-signing.md)，真机和渠道发布仍按各自验收要求执行。

## 前置条件与当前实测

需要 macOS 节点、完整 Xcode、所需 Apple SDK 和 Flutter。节点管理员本人阅读并接受 Xcode/Apple SDK 许可。脚本不会接受协议、切换系统默认 Xcode、选择开发团队或请求更新 provisioning profile。

本次固定 Flutter 3.47.9 / Dart 3.13.7，源码提交 `c197ca5199e9ee5661e865596c9c07294dfab51c`。从当前蓝图新生成的反馈、预约项目分别通过 analyze、26 项 Flutter 测试和 2 项后端 race 测试；构建助手及生成器的 30 项 Python 测试通过。

早期四次实际 Apple 调用因 Xcode 许可未接受退出 69。039 增量重新核查三个 SDK，并以两套应用的隔离副本各调用上述三个目标，共六次构建均在 SDK 前置检查退出 69，未生成 Apple 归档或成功清单。Python 中替代外部 SDK 的测试覆盖命令、包格式和错误处理，不能据此认定原生构建、签名或目标系统运行成功。

## 运行

以下 `flutter` 须指向节点已核对的 SDK，生成目录必须不存在。默认保留 macOS 沙盒；企业桌面直分发更新另按桌面更新手册选择生成配置。

```sh
rtk proxy python3 scripts/new-app.py work/apple-feedback --name customer_feedback --mode feedback --organization com.company --create-platforms
rtk proxy env DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer python3 work/apple-feedback/scripts/build-reference-client.py --application work/apple-feedback --target ios-simulator --api-url https://api.company.example --output work/apple-artifacts/feedback-ios-simulator.tar.gz
rtk proxy env DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer python3 work/apple-feedback/scripts/build-reference-client.py --application work/apple-feedback --target ios-unsigned --api-url https://api.company.example --output work/apple-artifacts/feedback-ios-unsigned.tar.gz
rtk proxy env DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer python3 work/apple-feedback/scripts/build-reference-client.py --application work/apple-feedback --target macos-unsigned --api-url https://api.company.example --output work/apple-artifacts/feedback-macos-unsigned.tar.gz
```

预约应用使用 `--mode booking --name service_booking` 生成另一个目录。可显式传入 `--flutter /absolute/path/to/flutter` 和 `--xcrun /usr/bin/xcrun`，并在进程环境内指定受控 `PUB_CACHE`。不要为解决 Apple 许可而改写全局 SDK 或自动接受协议。

每个 Apple 目标先执行 `xcrun xcodebuild -version` 和目标 SDK 版本查询；任一失败就原样传播正退出码，尚未运行 Flutter。随后执行依赖解析、analyze、完整 Flutter 测试，再编译：

| 目标 | 编译方式 | 输出 |
|---|---|---|
| ios-simulator | Flutter iOS debug、simulator、no-codesign | `Runner.app` 为归档根；仅适用模拟器 |
| ios-unsigned | Flutter iOS release、no-codesign | iPhoneOS/iphoneos、仅 arm64 的无签名 `Runner.app`；不得含 profile |
| macos-unsigned | Flutter macOS release config-only，再在新的 derived-data 目录运行明确禁用签名的 xcodebuild | 唯一 macOS `.app` 为归档根；顶层应用必须未签名 |

macOS 构建每次保留独立 derived-data 目录，便于排查，不改写其他已有产品目录。iOS 使用 Flutter 的标准模拟器或 iphoneos 产品路径。`ios-unsigned` 只接受 HTTPS API，显式本地测试也不能改用 HTTP。失败的构建没有发布成功归档；日志和构建目录留在应用内以便诊断。

## 归档与来源清单

输出为 `.tar.gz` 和同名追加 `.json` 的清单。内部保留 executable 位及 framework 的相对符号链接；链接必须存在、最终解析到应用内部，拒绝绝对路径、越界、断链、自循环及特殊文件。平台在使用包前仍需执行其独立制品验证和权限策略。

构建入口读取实际 Info.plist 的 bundle ID、平台、营销版本与构建号，核查主可执行文件与 `lipo -archs`，再用 codesign 明确确认顶层应用未签名。嵌入框架可能有工具链自身的签名；清单不将它们认定为企业分发签名。产物未转换为可提审 IPA、已公证应用或安装器，不能直接交给正式发布连接器。

清单在原字段上增加 `apple` 和 `nativeCommands`：所选 Xcode、SDK 名称/版本、进程内 developer directory、实际 bundle 信息、主程序架构和本地工具调用。原 `sourceDigest` 是排除缓存后的构建完成时文件快照，不是 Git 提交或构建前后不可变证明；可信 Worker 仍负责绑定准确提交、任务和上传制品。

`validation.analyze` 和 `widgetTests` 代表本次前置检查；`browser`、`emulator`、`nativeApplication`、`physicalDevice`、`storePublished` 初始均为 false。编译出模拟器包不等于已经启动模拟器或完成业务验收。后续独立验收保存新记录，不覆盖构建清单。

输出和清单均拒绝覆盖；若清单被并发创建，撤回本次已创建的输出，保留另一写入者文件。两文件不是一个跨文件原子事务，进程在发布窗口被强制终止时可能只留下其中一个。遇到不完整对，先核对现有字节，再使用新的输出路径，不能自动覆盖审批绑定的制品。

## 许可生效后的接续验收

先运行上面的三个目标，检查实际 `.app`、SHA256/大小、bundle/版本/架构及未签名观察。依次完成专用模拟器启动与业务流程、macOS 目标系统运行与业务流程，再用企业账户和合法签名身份完成正式包、安装、更新及渠道流程。App Store IPA 应通过企业授权 TestFlight 等渠道建立原提交、Apple build ID 和实际安装版本的映射，不能直接当作可本地安装的真机包。生产要求使用可访问的实际 HTTPS API。

本机当前需要用户本人完成许可，之后才能判断 SDK 缺失、依赖下载、编译或运行的下一项真实条件。当前没有原生 Apple UI、物理设备、企业签名、公证、正式提审或上架证据。
