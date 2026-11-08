# 华为 AppGallery 发布

内置 `huawei-appgallery` 驱动连接 `android-huawei` 目标：验证批准的 release APK，上传准确字节、登记包、持久等待解析，在原审批下继续提审，并查询准确版本。当前验收是本地真实 TLS/SQLite 契约和诊断 APK 的 SDK 拒绝场景，没有真实企业账户的提审、审核、上架或真机交付证据。

## 管理员前置条件

在独立 Linux/macOS 可信发布节点安装 publisher、固定版本 Android SDK build-tools 和 JDK。节点仅有 release 角色与 publish 能力，不运行生成的仓库代码。publisher、配置、SDK、JDK、状态目录和凭据均由服务管理员管理；私有状态不可放在公开分发卷或 SMB/NFS。

企业先建立 AppGallery Connect 应用、配置准确包名、发布地区、商店资料/截图、隐私说明、资质与必要授权。发布负责人确认审核后立即发布的全量策略，审批绑定准确 APK、需求、方案与提交。本驱动不代填合规声明，不管理资料/截图、应用初始化、分阶段发布、下载回调、AAB/HarmonyOS 包或 OS 私钥签名。

活跃操作期间由本服务独占该 AGC 应用的发布写入。上游 `app-submit` 没有条件版本参数；即使本服务在提交前重读，外部操作员仍可能在最后一次读与写之间更换包。SQLite AppID 锁协调使用同一 publisher 状态库的进程，不能替代企业对其他 AGC 账号、脚本和另一状态库的权限管理。

## 认证与固定配置

参照 [publisher 配置](../deploy/publisher.huawei.example.json) 和 [worker 配置](../deploy/worker.huawei.example.json)，替换示例 ID、路径、证书摘要和上传域名。示例 signer 占位值不能通过配置校验。

`appId` 为数字，`bundleId` 为 Android 包名，`expectedSigner` 为正式发布证书的 **64 位小写 SHA256**。SDK 使用 `apksigner verify --verbose --print-certs` 和 `aapt dump badging` 检查完整签名、唯一 signer、包名、versionName、正整数 versionCode（1–2100000000）及 debuggable 标记；已知 Android Debug 证书被拒绝。APK 上限沿用平台的 512MiB。`chineseMainland` 必须显式设为 true/false，并与业务发布地区核对。

首版仅支持文档共同明确支持的 **API client**，私有凭据 JSON 为：

```json
{"clientId":"REPLACE_API_CLIENT_ID","clientSecret":"REPLACE_API_CLIENT_SECRET"}
```

用受控秘密配置渠道写入文件，权限为 0600，父目录为私有目录；不要把真实值粘贴进命令行、仓库、任务或日志。新上传接口推荐的 Service Account 尚未在旧 Android 文件登记/提审接口文档中明确列出，本实现不会推断兼容或自动降级。API client 未来被上游停止支持时将阻塞，需要验证后扩展连接器。

API 与 token origin 固定为 `https://connect-api.cloud.huawei.com`。`uploadHosts` 是管理员核验过的精确 OBS HTTPS host，不能是通配符。上传 reservation 必须返回 PUT、正确 Host、完整 SHA256、未过期时间和官方存储授权；Bearer、Cookie、client_id、未知头及夹带控制面令牌被拒绝。上传客户端不继承 Cookie jar、不跟随重定向，不把签名 URL/OBS 凭据写入 journal。

`JAVA_HOME` 由可信服务环境设置。Worker 只显式透传该环境变量，SDK 命令使用受限 PATH、两分钟超时和有限输出；不读取项目提供的 JDK 或 SDK 路径。实际 Windows/macOS/iOS 客户端构建与该 Android 发布节点是不同能力。

## 生命周期

1. 校验批准制品摘要/大小并私有暂存，再校验 APK。用 packageName 查询唯一 AppID，核对发布地区和可编辑状态，拒绝已有 versionCode。
2. 原子预约 AppID 和 operationKey，将 `upload_url_intent` 写入 SQLite。请求上传 URL 时始终传准确 SHA256、字节数、全量 releaseType=1 和明确 mainland 标志。
3. 保存 objectId 与 `byte_upload_intent`，只上传获批字节。获取 URL 的 GET 会分配对象，使用独立 HTTP/1 连接防止标准 HTTP transport 在丢失响应后自动重试。
4. 写入 `file_registration_intent`，以 fileType=5、单个 files 数组登记 objectId。官方字段表将 files 声明为数组，但其示例写成对象；本实现遵循字段表。返回的唯一 `pkgVersion` 是 **包 ID**，不是 versionCode 或业务 versionId。
5. 保存 `processing` 和登记时间，立即结束任务。至少两分钟后由短只读查询核对解析结果，不用 sleep 占用发布节点。平台通常每五分钟查询，失败退避三十分钟，自动跟踪最多三十天。
6. package-list 分页最多 1000 项，必须找到唯一包 ID、versionCode、文件名、versionName、SHA256 和大小。缺少解析摘要继续等待，冲突阻塞。当前 app-info 也须指向该版本并保持可编辑。
7. 只读查询返回 `continueRequired` 后，平台复核原审批、需求、方案、质量与预算，派发独立 `publish_continue`。该任务只使用 `continuationProviders`，不下载另一包、不调用模型。publisher 以原 journal 的条件更新争取唯一写权，保存业务 versionId，提交前再读准确版本，然后调用 app-submit。
8. `ret.code=0` 只表示提审已接受，返回 `awaiting_review`。查询把当前版本、上架版本及历史包信息共同核对；`published` 要求本次 package 仍准确，且已上架 versionId/code/name 全部匹配持久业务版本。旧版本 releaseState=0、缺失字段或其他版本审核状态只返回 unchanged。

只读查询没有上游发布副作用。准确终态（published/rejected/halted）会释放本操作的本地 AppID 锁，绝不释放后续操作的锁。商店 reported published 后仍需要独立质量人员在目标地区和真实设备核对安装版本、权限、业务流程及运行；平台不会只凭渠道响应标记整体 delivered。

## 中断与核对

```sh
rtk proxy ./bin/is-publisher -config /etc/substrate/publisher.huawei.json -inspect OPERATION_KEY
```

journal 保存 stage、objectId、packageId、version/code、业务 versionId 和登记时间，不保存 token、签名 URL、OBS 授权或上游任意错误正文。相同已确认请求重放只返回原回执；绑定、配置或 externalId 不一致被拒绝。

在外部写前已持久化 intent。超时、进程中断、HTTP/API 错误、丢失回执或缺少 package ID 都保留 pending 和 AppID 锁，后续 publish/continue 不盲重放。管理员可使用[国内渠道核对](store-recovery.md)的 `-reconcile-store`：绑定准确原APK/日志和停止声明，只读查验登记包或持久业务版本。证明不足继续阻塞；没有直接解锁、放弃或重提命令。应暂停目的地、保留完整备份，不删除 journal、改数据库或换 key 绕过锁。控制面状态核对不会自动修改发布节点状态。

正常 processing 状态可跨进程重开；等待期间不保留执行租约。继续提审失败或租约失效进入待核对，不能用普通任务 retry 重提。备份需暂停写入并保存整个 publisher 状态目录和单独管理的 API client 凭据；恢复本地数据库不代表外部渠道已回滚。

## 验证边界

Go 测试使用实际 HTTPS 服务与 SQLite，检查准确请求/上传字节、写前状态、断线、重开、并发、分页、错误身份和状态映射。SDK 只在包内契约测试替换；实际 CLI 没有 skip-verification 开关。真实诊断 APK 用于证明拒绝 debug/不匹配签名，不构成正式签名成功证据。

官方来源与适用协议见 [本增量规格](superpowers/specs/2026-11-06-huawei-publisher-design.md#官方来源)。真实 API client、存储 host、完整商店材料、合法 release APK 与地区渠道结果仍须企业环境复验。
