# Flutter + Go 多端蓝图

共享 Flutter 客户端源码，Go 单实例参考 API。支持 feedback（客户反馈）与 booking（服务预约）两种业务起点。它们用于检验不同数据/交互形态；当前不是两个已正式发布的应用。

`web-preview` 专用构建会同时编译 Flutter 与 Linux Go 后端，以当前平台预览路径连接独立临时数据。需要单独的受控执行节点；普通 Web 发布配置不变。构建、节点部署与验收范围见[完整应用预览手册](../../docs/reference-previews.md)。

## 生成

在平台仓库根目录运行：

```sh
rtk proxy python3 scripts/new-app.py work/customer-feedback --name customer_feedback --mode feedback --organization com.company
rtk proxy python3 scripts/new-app.py work/service-booking --name service_booking --mode booking --organization com.company
```

目录存在时拒绝覆盖。已安装 Flutter SDK 可加 `--create-platforms`，生成 Web、iOS、Android、Windows、macOS 平台工程；脚本不安装 SDK。生成器保留自定义客户端、测试、锁文件和网络权限，并将测试的包名改为新项目包名。`application.json` 记录业务模式和生成配置；应用自带 `scripts/build-reference-client.py`，不依赖平台仓库的目录位置。

2026-11-06 本地验收使用 Flutter 3.47.9 / Dart 3.13.7，Flutter 提交 `c197ca5199e9ee5661e865596c9c07294dfab51c`。固定依赖锁文件随蓝图保存。已生成两套完整平台工程并构建 Web 发布包与 Android arm64 调试 APK；模拟器、签名与商店结果分别见 [验证记录](../../docs/validation.md)。

## 本地后端

```sh
rtk proxy go -C work/customer-feedback/backend test -race ./...
rtk proxy env APP_MODE=feedback APP_STATE_FILE=work/feedback-state.json APP_WEB_ORIGIN=http://localhost:3000 go -C work/customer-feedback/backend run .
```

后端默认 `127.0.0.1:8081`（部署地址见 main.go），提供 `/api/health`。Feedback：GET/POST `/api/feedback`；Booking：GET `/api/slots`、POST `/api/bookings`、POST `/api/bookings/cancel`。预约成功返回不可预测的取消凭据，服务端仅保存其摘要；用户须保存凭据。

预约模式需要明确配置可用时段，`APP_SLOTS` 为 Slot 对象 JSON 数组，示例见生成的 `application.env.example`。未配置则展示空列表，不会捏造可预约数据。启动进程需显式传入这些环境变量，示例文件不会自动加载。

持久化为原子 JSON 文件，适合单实例验证，不是多实例生产数据库；反馈无用户账户和内容管理，生产需增加访问策略、隐私、限流、数据生命周期、监控与数据库。预约不是支付/医疗/实名业务系统。

## 客户端与构建

安装 SDK、生成平台工程后，进入生成的 client 目录运行（将 URL 换为实际可达 API）：

```sh
rtk proxy flutter pub get
rtk proxy flutter analyze
rtk proxy flutter run -d chrome --dart-define=API_BASE_URL=http://127.0.0.1:8081 --dart-define=APP_MODE=feedback
rtk proxy flutter build web --release --dart-define=API_BASE_URL=https://api.company.example
rtk proxy flutter build ipa --release --dart-define=API_BASE_URL=https://api.company.example
rtk proxy flutter build appbundle --release --dart-define=API_BASE_URL=https://api.company.example
rtk proxy flutter build windows --release --dart-define=API_BASE_URL=https://api.company.example
rtk proxy flutter build macos --release --dart-define=API_BASE_URL=https://api.company.example
```

iOS/macOS 使用 macOS；Windows 使用 Windows。Android 各商店的包格式、证书、隐私材料、SDK 政策可能不同，AAB 构建不代表国内商店已支持；按连接器生成相应 APK/包并独立验收。桌面发布需要额外签名、notarization/安装包装、公开分发与更新服务。源码没有预设这些配置或伪造签名。

`blueprint.json` 提供命令元数据，不会自动调用构建，也不会替代批准后的平台任务。平台任务的 command 工作目录是仓库根，需要用明确脚本进入 client/backend，打包输出目录为一个制品文件。

## 受检查的单文件构建

从生成应用的根目录运行：

```sh
rtk proxy python3 scripts/build-reference-client.py --application . --target web --api-url https://api.company.example --output artifacts/web.tar.gz
rtk proxy python3 scripts/build-reference-client.py --application . --target android-debug --api-url https://api.company.example --output artifacts/android-arm64-debug.apk
rtk proxy env DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer python3 scripts/build-reference-client.py --application . --target ios-simulator --api-url https://api.company.example --output artifacts/ios-simulator.tar.gz
rtk proxy env DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer python3 scripts/build-reference-client.py --application . --target macos-unsigned --api-url https://api.company.example --output artifacts/macos-unsigned.tar.gz
```

构建入口依次执行依赖解析、analyze、完整 Flutter 测试（当前 26 项，含业务 widget、桌面更新界面和实际进程桥接）和实际构建；任一检查失败就停止，不输出成功制品。Web 包以站点根目录打包，CanvasKit 等 Web 资源随包交付。APK 仅含 arm64 诊断构建，使用开发调试密钥，不能用于正式渠道交付。每个输出旁生成同名 `.json`，记录 SDK、参数、客户端文件摘要、实际制品 SHA256/大小及验证类别。拒绝覆盖现有包或清单。进程测试用当前 Flutter SDK 编译不含安装功能的 Dart 样本，不需要全局 Go 或 Flutter PATH。

Apple 目标额外先核查 Xcode/目标 SDK，再生成包含一个 `.app` 的诊断归档：iOS 仅模拟器 debug；macOS 明确禁用顶层签名、保留独立构建目录。清单记录实际 bundle、版本、架构与未签名观察，保留合法内部 framework 链接。当前本机实际调用仍被未接受许可阻断，没有 Apple 成功构建或运行证据。流程与接续条件见 [Apple 诊断构建手册](../../docs/apple-reference-builds.md)。

中文/拉丁字体使用随包分发的 Noto Sans SC 与 Roboto，许可证和 [来源与摘要](client/assets/fonts/provenance.json) 一并保存。自定义 Web bootstrap 将缺字回退 URL 限定到本站，避免 Flutter 默认静默请求 Google 字体 CDN。浏览器验收从冷上下文禁止所有未声明 origin；其他语言和特殊字形需要另补本地字体及验证，缺字不会触发公共 CDN 回退。

`sourceDigest` 是本机构建后的客户端文件快照，排除缓存、构建目录及部分本地配置；它不是 Git 提交，也不证明不同机器生成的包逐字节相同。平台 worker 另行绑定实际仓库提交、运行和上传制品摘要。构建清单的 browser/emulator/nativeApplication/physicalDevice/storePublished 初始均为 false；后续验收单独留证，不改写构建记录。

API 默认要求 HTTPS，并拒绝包含账户、密码、查询参数或片段的 URL。本地验收才可显式追加 `--local-testing`，允许 loopback HTTP；Android 诊断包还允许模拟器宿主地址 `http://10.0.2.2:18110`。Android 仅 debug manifest 放行明文，release 不增加例外。生成的 macOS entitlements 允许客户端出站网络。

Flutter 默认 Gradle 分发下载受网络条件影响时，可在生成时显式选择蓝图登记的版本：

```sh
rtk proxy python3 scripts/new-app.py work/customer-feedback-native --name customer_feedback --mode feedback --organization com.company --create-platforms --gradle-version 9.4.1
```

该选项固定官方 Gradle binary URL 和 SHA256，不更改 Android/Kotlin 插件或 NDK 版本。没有指定时保留 Flutter 默认模板。当前本机复用了已有 Gradle 9.4.1 安装的副本并逐文件记录摘要，未重新校验缺失的原始 ZIP；不能把它声称为已下载并验过完整官方归档。正式执行节点应预装和验证固定工具链及离线依赖，不能依赖本机 `work/` 缓存。

## 桌面应用内更新

蓝图包含 `lib/updates/`：更新状态、外置可信 `is-updater` 进程桥接和用户确认对话框。构建时固定 `UPDATE_EXECUTABLE` 与 `UPDATE_PROFILE` 的绝对路径才能在 Windows/macOS 显示“应用更新”；Web/移动端不启用。检查、下载及按准确 SHA256 确认安装分开执行，结果不明时只允许刷新与管理员核对，不自动重装。

默认 macOS 工程保持沙盒。企业直分发版本需要外部更新程序时，可在 `--create-platforms` 生成时显式添加 `--macos-direct-distribution`，只改变所生成工程的沙盒 entitlement，并记录在应用元数据中；不适用于 Mac App Store，也不授予 PKG 安装权限。可信程序和 profile 由管理员单独部署，普通 GUI 不会自动提权。配置、限制与只读桥接探针见 [桌面更新手册](../../docs/desktop-updates.md)。

目前是经过本地测试的集成源码；原生应用构建、实际 OS 签名/公证/打包、合法包升级、应用重启和目标系统业务验收仍需独立完成。

## 独立浏览器验收

在平台仓库根目录，准备两套应用与指定本地 API 地址的 Web 包后运行：

```sh
rtk proxy node scripts/verify-reference-apps.mjs --feedback work/customer-feedback --booking work/service-booking --feedback-manifest work/artifacts/feedback-web.tar.gz.json --booking-manifest work/artifacts/booking-web.tar.gz.json
```

清单需分别声明 `http://127.0.0.1:18110`、`http://127.0.0.1:18111`；端口被占用时拒绝启动。脚本校验包摘要、解包实际站点，编译并启动真实 Go API，使用独立状态文件测试提交、普通文本、重复点击、离线恢复、预约冲突、取消授权和进程重启持久性。输出保存在 `work/reference-browser-*`，包括 JSON 和截图。仅通过 Flutter 的无障碍启用按钮开启语义树；业务请求仍由真实界面发出。390px Chrome 视口属于浏览器检查。

Android 验收入口为平台仓库的 `scripts/verify-reference-android.py`：显式指定两套应用与 APK 清单、adb/aapt/apksigner、专用 `--serial emulator-PORT` 和 `--avd NAME`。脚本核对 AVD 名称、签名和 ABI 后安装，仅针对 `com.intelligencesubstrate.reference.*` 的诊断包；通过 UIAutomator 的实际点击/输入测试与隔离 Go API 的业务流程。API 不可达后创建一条新服务端记录，客户端刷新必须真正读到新记录才能通过，不用旧界面内容冒充恢复。测试输出在 `work/reference-android-*`，不会选择已有用户设备或接受许可。

Android release 可在生成时显式使用 `--android-unsigned-release`，随后由 `build-reference-client.py --target android-unsigned-apk --aapt ABS --apksigner ABS` 生成无签名 arm64 包。构建环境不配置私钥，平台将其记录为 unsigned-package；独立 release/build 节点再调用管理员固定程序签名，最终包须重新验证。当前真实样本仅证明临时测试证书与模拟器离线冒烟，未完成企业身份、真机业务或商店发布。配置、命令和中断限制见 [Android 可信签名](../../docs/android-signing.md)。

## 参考验收规格

- [客户反馈应用](../../docs/reference-apps/customer-feedback.md)
- [服务预约应用](../../docs/reference-apps/service-booking.md)

每个参考项目必须独立确认必选端、渠道、业务验收和验证方式。CI 单元测试、模拟器、真机、目标系统安装和真实商店上线分别留证，全部声明目标达标后才满足 P3。
