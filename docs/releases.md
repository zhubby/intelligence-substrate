# 内置发布连接器与应用分发

本批在已有候选、人工批准、制品绑定和独立验收之上补齐可部署的发布执行。控制面不会把连接器返回成功等同于完成交付。

拒审后使用[渠道拒审修复流程](release-remediation.md)：准确来源与人工决定进入新方案，新包独立质量和新发布批准通过后才可发布。原拒审候选不能重置重提，提审或发布回执不能关闭问题；对应替代版本独立发布验收后才解决。需要改变应用行为时使用新构建；App Store 文字/链接另有[原构建资料修订](store-material-remediation.md)，独立批准与验收，不重置旧候选。截图更换及其他渠道资料补交仍待接入。

## 支持范围

| 目标 | 内置实现 | 完成边界 |
|---|---|---|
| Web 静态 | tar.gz 静态站点部署、事务切换当前版本、不可变下载 | is-distribution 只读提供静态应用 |
| Web 后端 | is-runtime 运行批准的 Linux Docker-save 镜像、只读网关、健康事件、批准回滚 | 专用节点、数据兼容、停旧启新；不含迁移、零停机或 SLO，见 [运行手册](runtime.md) |
| Windows | 原生 unsigned MSIX 构建、专用证书存储签名、预签名 exe/msi/msix 校验、公开分发与签名更新索引 | [受控 MSIX 签名](windows-signing.md)绑定准确证书/发布者/包/版本及可信时间戳；真实 Windows 构建、签名、安装和升级仍待验收，不含 MSI/EXE 安装器制作 |
| macOS | 专用双证书签名/公证 PKG 链、安装包校验/分发、签名更新索引和独立单组件 PKG 更新 CLI | [签名节点](macos-signing.md)持久等待公证并系统验证；实际企业签名、公证、安装/升级和业务验收仍待验，不含证书申请 |
| Google Play | 服务账户OAuth、AAB上传、track更新、edit commit、已有版本查询、持久发布/查询互斥与管理员恢复 | 需要已建立的应用、完整商店资料和预签名AAB；同状态库跨key防重，查询清理未确认保持阻塞；提交结果为等待审核，track completed不证明公开可用 |
| App Store | 专用Distribution/profile签名IPA、资料清单审批绑定、本地化/隐私 URL 更新、App Asset Library 截图上传/排序、处理等待、受控提审和查询 | [签名节点](ios-signing.md)与发布器独立，需完整Xcode/真实身份、预建版本/语言与合规资料、团队API key；App Store IPA不能直接真机安装，TestFlight等受控验收需绑定原提交/Apple build，真实签名/设备/上架待验，见 [App Store 手册](app-store.md) |
| 华为 AppGallery | release APK 验证、OBS 上传、异步解析、原审批下提审和准确版本查询 | 本地 TLS/SQLite 契约及诊断 APK 拒绝验证，真实 API client/签名/上架待验，见 [华为手册](huawei-appgallery.md) |
| 小米应用商店 | 受审核资料绑定的新增/更新 APK 推送、RSA/MD5 multipart、持久包锁 | 官方无审核/上架状态 API 或沙盒；推送仅为待审，需人工核对和独立验收，见 [小米手册](xiaomi-market.md) |
| OPPO | 普通应用更新、审核资料上传、异步任务与准确版本上架查询 | 首次创建/发布须门户完成；本地契约已实现，真实账号/签名/上架待验，见 [OPPO 手册](oppo-store.md) |
| vivo | 现有普通应用更新、受审核资料直传、独立沙箱与受限状态查询 | 通用详情不足以确认准确制品在架，须门户证明与独立验收；真实沙箱/账户/签名待验，见 [vivo 手册](vivo-store.md) |

当前Google Play实现为单AAB、一次完整发布到配置track，不实现分阶段rollout、自动停止分阶段分发、商店资料生成或首次账号初始化。同一权威SQLite状态库已按应用互斥发布与查询；其他状态库、旧程序及门户写入仍由发布负责人安排独占编辑窗口。不同Track不能绕过占用，详见[发布防重](google-write-guard.md)及[查询窗口与准确清理](google-query-guard.md)。

## 可信节点部署

构建新增程序：

```sh
rtk proxy go build -trimpath -o bin/is-publisher ./cmd/publisher
rtk proxy go build -trimpath -o bin/is-distribution ./cmd/distribution
```

以管理员身份安装到生成代码不能写入的绝对路径。节点注册 release 角色和 publish 能力；不要兼任开发或独立质量。节点的 `publishers` 配置执行发布，`statusProviders` 单独配置只读状态程序，示例见 [worker.publisher.example.json](../deploy/worker.publisher.example.json)。

`-status-only` 拒绝所有发布输入。没有 statusProviders 时，查询报告未配置并保留上次状态，绝不回退到旧发布脚本。`allowNative` 约束仓库工具任务；可信发布程序始终由独立配置调用。

发布程序读取 [publisher.example.json](../deploy/publisher.example.json) 中固定的项目、目标、客户端与外部应用映射。请求不能选其他凭据、track、部署目录或公钥。桌面使用对应 [macOS](../deploy/publisher.macos.example.json) / [Windows](../deploy/publisher.windows.example.json) 配置；替换全部 ID、域名、路径和实际签名主体。

Windows/macOS 的签名成功路径必须在相应系统实测。Windows 指纹是受信任代码签名证书的 SHA1 thumbprint；更新索引和包完整性使用 Ed25519 / SHA256，这是两种不同用途。macOS 要求预签名且已公证的 Developer ID Installer pkg。

## 独立公开分发服务

发布节点可写入本机私有 distributionRoot，公开服务以只读方式访问同一卷。桌面可各自在对应系统提供独立分发节点及域名，工作台组合各端状态。SQLite 根目录必须使用可靠本地文件系统，不通过 SMB/NFS 共享一个可写数据库。

公开分发使用独立 HTTPS origin，不能与控制面共用 origin 或有权限的 Cookie 域。每个 Web 应用使用独立分发根和 origin，避免生成的应用共享浏览器存储与权限。服务没有上传、发布、登录或控制面管理接口。

先初始化空卷，创建更新索引的签名密钥，再启用服务：

```sh
rtk proxy ./bin/is-distribution -root /var/lib/substrate-distribution -init
rtk proxy ./bin/is-publisher -init-key /private/substrate-secrets/update-key
rtk proxy ./bin/is-distribution -root /var/lib/substrate-distribution -listen 127.0.0.1:8090
```

密钥父目录事先以私有权限创建。初始化拒绝覆盖密钥；stdout 只有公开的 publicKey/keyId，私钥为 0600。分发进程不读取私钥，配置拒绝将私钥放在公开服务卷内。TLS 代理示例见 [distribution.Caddyfile](../deploy/distribution.Caddyfile)。

Web 包根目录必须包含 index.html；最大 512 MiB、展开后 512 MiB / 20,000 个条目。拒绝路径逃逸、符号链接、特殊文件、重复路径和隐藏文件。包被完整校验后，数据库事务一次切换当前版本。版本为数字 major.minor.patch，必须递增；重放旧操作只返回旧收据，不能重新激活旧版本。

接口：

- `/apps/{app}/web/`：当前 stable 静态站点。响应的 X-Substrate-Release-ID / X-Substrate-Artifact-SHA256 供独立检查核对实际版本。
- `/updates/{app}/{target}/{channel}.json`：带 Ed25519 签名的更新索引。
- `/downloads/{app}/{target}/{operationKey}/{filename}`：不可变安装包/归档，支持 HEAD/Range。
- `/healthz`：分发服务可读性检查，不代表应用业务健康。

更新索引的 payload 为 base64 JSON；签名覆盖 `intelligence-substrate-update-v1\n` 加原始 payload 字节。客户端必须独立固定公钥，并持久化最低 sequence，验证 app/target/channel、时效、版本与下载 SHA256/size。`internal/distribution.VerifyFeed` 实现索引校验。不能信任下载响应自行提供的公钥。

索引 7 天过期。私钥留在发布节点；运行 `is-publisher -config ... -renew-feeds` 更新当前索引的签名、时效与 sequence，包和版本保持不变。运维应每日执行并监控失败；不会由公开服务自动持有私钥续期。[桌面更新客户端](desktop-updates.md) 实现索引消费、校验暂存、按具体摘要确认的原生安装和中断核对；应用内接入、真实签名升级与系统业务验收仍需完成。

App Store 单独配置 `continuationProviders` 和 `-continue-only`，执行原审批下的后续提审；查询不会调用这个入口。完整状态、固定目的地、上传主机和未知阶段核对见 [App Store 手册](app-store.md)。

华为也使用独立的 continuationProviders，解析等待至少两分钟，复用原绑定继续提审。API client、AppID 串行锁、上传域名、准确业务版本及未知操作限制见 [华为 AppGallery 手册](huawei-appgallery.md)。

小米通过固定发布程序完成新建/更新推送；没有 continuation。只读入口说明人工核对要求，返回 `MANUAL_VERIFICATION_REQUIRED` 后停止自动轮询。资料绑定、未知写入与锁保留见 [小米应用商店手册](xiaomi-market.md)。

OPPO 更新使用独立 statusProviders，不需要 continuation。异步接受仅为 processing，按任务与准确 APK/版本上架证据观察；创建/首次发布、资料、可信上传主机和未知写入限制见 [OPPO 手册](oppo-store.md)。

## Google Play

服务账户凭据保存在受控文件，不放到任务数据、仓库、制品或日志。当前仅支持 service_account JSON，OAuth token URI 固定为 Google 官方端点，权限 scope 为 androidpublisher；账户还需在 Play Console 授予相应应用和发布权限。

连接器先校验批准制品 SHA256/大小，再依次执行：创建 edit → 上传 AAB → 校验 Google 返回的 SHA256/versionCode → 更新固定 track → commit。关键 edit/versionCode 收据即时写入本地持久日志。

外部调用前，pending日志、永久业务版本/制品预约与应用写入占用同事务创建；已知versionCode也受应用内唯一约束。未知结果保留占用，不允许新key或另一Track再次上传；成功发布或准确管理员恢复才在完成事务中释放自己的占用，永久预约保留。相同key重放返回原结果，新版本且新字节可继续，不同应用独立。旧日志须按原批准请求保守登记，无法归属或历史冲突保持阻塞。

commit 接受后返回 `externalId=packageName:versionCode`、`state=awaiting_review`。状态查询使用临时 edit 读取现有 track，随后删除临时 edit，不上传、不改 track、不 commit。临时 edit 清理失败由 Google 过期回收，不能因此重提发布。

Google track API 未提供本实现可用的独立商店审核/公开上架证明。`completed` 返回 unchanged 观察结果，保留上次已知状态，不能自动变成 published，也不能覆盖人工确认的 published。发布负责人核对真实渠道结果并附实际报告后记录 published；独立质量人员仍需在真实 Android 设备验证准确版本。

官方接口参考：[edits](https://developers.google.com/android-publisher/api-ref/rest/v3/edits)、[bundles.upload](https://developers.google.com/android-publisher/api-ref/rest/v3/edits.bundles/upload)、[tracks.update](https://developers.google.com/android-publisher/api-ref/rest/v3/edits.tracks/update)、[edits.commit](https://developers.google.com/android-publisher/api-ref/rest/v3/edits/commit)。当前自动化测试使用测试 API 服务，尚无真实 Google 账户或正式上架证据。

## 查询、未知结果与恢复

Google Play支持[管理员专用结果核对](google-play-recovery.md)，按原批准文件、准确pending日志、Bundle SHA256/versionCode和固定Track证明补全收据，不上传、改Track或commit。completed只恢复awaiting_review，halted保持halted，仍需真实渠道和独立设备验收。状态查询不能解除应用发布占用，也不能进入已有新写入占用的应用。

华为、小米、OPPO与vivo的已接受未知操作支持[管理员专用证据核对](store-recovery.md)。华为/OPPO必须有准确制品与版本的API观察；小米/vivo保留明确标为人工声明的后台报告及附件，只恢复处理中。该入口不执行上传或重提，不自动修改平台状态，早期或证明不足的操作仍阻塞。

控制面新增 `releases.refresh`，只允许有发布权限的人请求已有外部版本的查询。发布节点回报构建处理、等待审核或等待发布后，控制面持久保存下一次查询时间。节点领取工作时扫描到期查询；通常每 5 分钟一次，失败后 30 分钟，最多自动跟踪 30 天，再由人决定是否继续。发布节点离线时保留队列，没有长时间占用资源的等待进程。

状态任务每次租约 90 秒、超时 120 秒、失联最多 3 次。耗尽后保留已知状态和查询错误；绝不重新执行发布。只有实际状态变化才更新发布时间基准，重复相同查询不会使已有发布证据无故失效。人工状态决定或独立验收会取消旧查询/继续任务及租约，延迟结果不能覆盖新决定。

只读查询不调用模型、不申请新的模型预算；模型费用达到上限后仍允许核查已经提交的外部操作。该例外不能用于构建、模型调用或重新发布。

publisher.db 在调用外部发布之前保留操作。相同 operationKey 的已完成请求返回原收据；不同批准绑定或目的地被拒绝。外部调用中断、超时、非成功响应及日志确认失败都不能自动重试。

```sh
rtk proxy ./bin/is-publisher -config /etc/substrate/publisher.json -inspect OPERATION_KEY
```

检查日志中的 editId/versionCode 和渠道实际结果，使用控制面的人工核对流程。没有确认“外部未接受”前不得重发；确认需要新发布后使用新的操作 key。hosted 驱动可从自身不可变事务中核对已完成激活，但也不会重放或回退当前版本。

控制面备份不包含独立发布/分发节点。备份时暂停发布和索引续期，完整备份 publisher 状态目录、distributionRoot 和单独保管的密钥；公开只读服务可继续运行。恢复到新目录验证包摘要、索引签名和当前版本后再切流。分发服务不实现一键回滚；旧制品可保留下载，重新激活旧版本需要后续独立批准的回滚能力。不能把恢复数据库当作已核对外部渠道。

## 本地验收

```sh
rtk proxy go test -race ./internal/distribution ./internal/publisher ./internal/platform ./internal/worker
rtk proxy env IS_BROWSER_EXECUTABLE='/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' node scripts/verify-release.mjs
```

脚本需要已构建的两个新增二进制、Node、OpenSSL、tar 和已安装 Chrome/Playwright。它创建隔离 work 目录和临时 TLS 证书，实际运行 CLI、只读服务、下载/签名检查、重放/续期及 Chrome 提交反馈；结束后停止本次进程。批准身份是测试样本，不操作工作台试点数据，也不产生商店、原生安装或物理设备验收记录。

vivo 使用独立 statusProviders，无 continuation；明确同步更新成功仅 awaiting_review。通用 saleStatus 不足以证明新包在架，模糊状态返回 MANUAL_VERIFICATION_REQUIRED 并停止轮询；环境、资料、包/版本防重和未知操作边界见 [vivo 手册](vivo-store.md)。
