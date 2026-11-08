# 桌面更新客户端

`is-updater` 消费既有分发服务的 Ed25519 签名索引，提供检查、校验下载、按具体包确认安装和中断核对。它不持有签名私钥或控制面凭据，也不会替发布版本新增独立质量证据。

当前安装格式为 **Windows MSIX** 和 **macOS 已签名、公证的单组件 PKG**。Windows 的 EXE/MSI、macOS product/distribution PKG 尚不支持此客户端安装；现有分发服务仍可分发这些格式。Flutter 蓝图已包含应用内更新界面和外置 CLI 桥接，默认关闭；正式桌面打包、签名与真实升级尚需目标系统验收。

## 固定信任配置

```sh
rtk proxy go build -trimpath -o bin/is-updater ./cmd/updater
```

管理员将二进制与配置安装在应用代码不可写的位置。为每个应用、系统、渠道分别配置 [macOS](../deploy/updater.macos.example.json) 或 [Windows](../deploy/updater.windows.example.json) profile，替换全部示例值。

| 字段 | 约束 |
|---|---|
| `origin` | 公开分发服务的固定 HTTPS origin；没有路径、账户信息、查询参数或跳转 |
| `app` / `target` / `channel` | 精确匹配已发布索引；每个 profile 只更新一个应用/端/渠道 |
| `publicKey` | 经可信安装渠道提供的 Ed25519 公钥 base64；不能从待验证响应接受新公钥 |
| `expectedSigner` | Windows 代码签名证书 SHA1 thumbprint，或 macOS Developer ID team ID |
| `packageId` | MSIX Identity Name 或 PKG receipt identifier；须与原安装包一致 |
| `windowsPublisher` | Windows 必填：MSIX manifest 中完整、精确的 Publisher；安装记录也核对该发布者，避免借用同名应用的版本 |
| `stateDir` | 固定绝对路径，保存 updater.db 与私有下载目录；Unix 要求 0700，Windows 由管理员配置仅该用户/管理上下文可写的 ACL |
| `caFile` | 可选企业 CA PEM 的绝对路径，仅附加可信 CA；没有关闭 TLS 验证的选项 |

配置不能夹带未知字段。应用/渠道/密钥/原生签名者/包身份/源站/CA 路径等配置与持久状态绑定，变更后拒绝复用数据库。本版没有自动密钥或 profile 迁移命令；管理员需要在保留已确认 sequence/version 下完成受控迁移，不能通过删库重置防回退状态。

## 检查与安装

以 macOS profile 为例：

```sh
rtk proxy ./bin/is-updater -config /etc/substrate/updater-feedback.json -action check
rtk proxy ./bin/is-updater -config /etc/substrate/updater-feedback.json -action stage
rtk proxy ./bin/is-updater -config /etc/substrate/updater-feedback.json -action status
```

命令 stdout 为 JSON，错误写入 stderr 并以非零状态退出。`status` 只读本地状态；`check` 读取索引和系统安装记录，不安装；`stage` 下载并验证，不调用安装器。

`stage` 成功返回 `state=staged`、`feed.version`、`feed.sha256` 和 `packagePath`。应用界面或企业运维应展示这一具体版本和包，让用户/已授权管理策略决定安装，然后执行：

```sh
rtk proxy ./bin/is-updater -config /etc/substrate/updater-feedback.json -action install -approve-sha256 EXACT_STAGED_SHA256
```

没有 SHA256、摘要不符、索引过期、包被替换、系统签名或包身份不符，均在安装预约前拒绝。已运行的更高版本不会被较低候选覆盖。安装前再次读取/校验同一包和原生身份；索引只允许同一 origin 的精确不可变下载 URL，不跟随重定向，不发送平台身份或 Cookie，下载大小上限 512 MiB。

Windows MSIX 要求 manifest Identity Name、Publisher、架构和版本匹配，`major.minor.patch` 对应 `major.minor.patch.0`；不接受其他 revision。验证 Authenticode 与指定证书后，使用固定系统 PowerShell 的 `Add-AppxPackage -ForceApplicationShutdown`。这会关闭该应用的运行实例，调用方应提前提示和保存工作。执行范围为调用用户的 MSIX 上下文。

macOS 使用 pkgutil 检查 Developer ID Installer 团队、spctl 检查公证，并核对单组件 PackageInfo 的 identifier/version。安装调用固定 `/usr/sbin/installer`。客户端不会调用 sudo、弹出自制提权命令、接受协议或改系统策略；无人值守安装须由企业已经授权的管理上下文运行。普通用户缺少安装权限会失败并进入核对流程，不能承诺静默提权。

## 状态与恢复

| 状态 | 含义 |
|---|---|
| `unchecked` | 尚无通过验证的索引 |
| `available` | 有经过验证的可更新候选，尚无可安装的本地包 |
| `staged` | 本地包已通过字节、原生签名和身份检查 |
| `current` | 系统已有候选或更高版本，无需安装该候选 |
| `installing` | 已持久预约安装；进程可能仍运行，也可能已中断，不能再发起安装 |
| `reconciliation_required` | 安装结果或系统版本不明确，需要核对 |
| `installed` | 操作后的系统安装记录确认了准确候选版本，或已确认该版本原先就已安装 |

SQLite 在不同进程之间串行化检查、暂存和安装预约。持久化最高 sequence、版本及具体发布身份；低 sequence、相同 sequence 的不同签名内容、版本回退和同版本替换包会被拒绝。合法索引续期可以提高 sequence，保留相同包与原有暂存结果。

预约在调用原生安装器前提交。崩溃、超时、返回失败或安装器返回 0 但系统版本不符，都不能自动重装。再次调用 `install` 不会重放该操作。

```sh
rtk proxy ./bin/is-updater -config /etc/substrate/updater-feedback.json -action reconcile -confirm-stopped-attempt EXACT_ATTEMPT_ID
```

收据出现不证明安装器或后台安装服务已停止。管理员必须先通过原生进程/安装日志确认该次安装已终止，再用 `status` 返回的准确 `attemptId` 作上述确认；不能让客户端盲目自动填写该参数。核对仅读取 PKG/MSIX 安装记录，准确候选版本存在才转为 `installed`。其他结果继续保持显式待核对。本版没有“强制重试/清除未知结果”命令；需管理员检查原生安装日志、进程和系统状态后作后续恢复决策。不要删库或新建 stateDir 来绕开未确认操作。

`installed` 只代表系统安装记录。应用启动/自动重启、数据兼容、真实交互、目标系统业务验收和平台独立质量仍需各自的检查与证据。不能把 CLI 成功回报当作发布已验证或 P3 已完成。

下载失败会清理该次临时目录；以前成功暂存的包及中断时留下的目录保留。没有自动清理/保留配额工具，运维清理前须确认没有安装进行、保留数据库与恢复所需包。

## Flutter 应用内接入

反馈与预约蓝图共用应用内更新对话框。管理员先部署可信 `is-updater` 和固定 profile，再在桌面构建时提供两个绝对路径：

```sh
rtk proxy flutter build macos --release --dart-define=API_BASE_URL=https://api.company.example '--dart-define=UPDATE_EXECUTABLE=/Library/Application Support/CompanyUpdater/is-updater' '--dart-define=UPDATE_PROFILE=/Library/Application Support/CompanyUpdater/feedback.json'
```

Windows 同样使用 `UPDATE_EXECUTABLE` 和 `UPDATE_PROFILE`，程序必须是 `.exe`。路径是受控构建输入，不从环境变量、下载索引、链接或应用文件选择器获取；配置缺失、相对路径或不支持的操作系统不会显示入口。二进制、profile 和 CA 文件须由管理员安装在应用及普通用户不能修改的位置；状态目录按该用户/管理上下文单独配置，`stateDir` 不展开环境变量。桥接不负责安装或签名这个外部程序，也不提供 IPC 提权服务。

macOS 默认模板保留 App Sandbox。需要外部程序/配置的企业直分发版本必须显式生成：

```sh
rtk proxy python3 scripts/new-app.py work/feedback-direct --name customer_feedback --mode feedback --create-platforms --macos-direct-distribution
```

此选项仅在新生成的 macOS DebugProfile/Release entitlements 中关闭应用沙盒，保留网络权限，并在 `application.json` 记录 `macosDirectDistribution=true`。它面向经企业评估、签名与公证的直接分发，不能用于 Mac App Store；不会改变主机权限、提升进程身份、设置 setuid 或接受 Apple 协议。不提供该选项时保持模板沙盒。普通用户启动的 GUI/CLI 仍无法静默安装需要管理员权限的 PKG；需要企业已经授权的安装上下文或后续管理服务接入，不能只靠编译路径承诺安装成功。

应用入口的行为：

- 打开时只读取本地持久状态；用户点击“检查更新”才访问分发服务。
- “下载并校验”只暂存包；“安装更新”另弹出版本、文件名及 SHA256 确认，并提醒保存工作与应用可能关闭。
- 确认绑定弹窗显示的准确摘要；期间其他进程换了候选时，由 Go 更新器拒绝旧确认，不会安装新候选。
- 一个应用控制器同时只允许一个进程操作；界面等待期间阻止重复操作和普通返回，实际进程结束后才释放锁。应用退出不会自动重放安装。
- 非零退出或无法解析的结果先要求刷新权威状态；`installing`/`reconciliation_required` 只显示刷新和管理员核对编号。界面没有 reconcile、重试安装器、重置数据库或自动重启入口。
- 子进程不使用 shell、不搜索 PATH、不继承平台/模型凭据；只传必要的系统临时目录及 Windows SystemRoot。stdout/stderr 有内存上限，超限后继续排空并等待退出，不通过终止/再试制造重复安装。

Web 通过条件导入排除原生进程入口；Android/iOS/Linux 工厂也不启用桌面更新。应用页面不显示内部配置路径、原始 stderr 或假定的升级百分比。

生成的客户端含 `tool/probe_updater.dart`，可在专用 profile/state 上测试真实 Go 进程桥接：

```sh
rtk proxy dart run tool/probe_updater.dart /absolute/is-updater /absolute/status-only-profile.json
```

该工具只连续执行两次 `status`，验证不同 CLI 进程能读取相同本地状态；不能用于证明索引验签、真实下载、安装成功或桌面应用业务通过。profile 应使用独立验收目录，不复用生产状态。

## 验证

```sh
rtk proxy go test -race ./internal/updater ./cmd/updater ./internal/publisher ./internal/distribution
rtk proxy go run scripts/verify-updater.go -updater work/bin/is-updater
```

第二条仅在 macOS 使用已经构建的明确二进制。它创建专用目录和 TLS 服务，实际检查签名索引、跨 CLI 重启保留防回退状态、接受合法续期，并由真实 pkgutil 拒绝未签名包；不执行安装。测试中的发布者签名身份为合成证明，不能作为真实桌面发布证据。

Go 自动化测试使用实际 TLS/Ed25519/SHA256、SQLite、文件和 MSIX ZIP/XML；只有成功的原生安装/信任依赖被替换。Dart 桥接测试编译并运行独立进程样本，界面测试替换外部 CLI 边界；真实 Go 桥接另以本地 status 验证。实际 Windows、合法签名/公证 PKG 与 MSIX 的安装升级成功路径，以及 Flutter 桌面应用实际运行，仍需对应节点、证书和真实应用验收。
