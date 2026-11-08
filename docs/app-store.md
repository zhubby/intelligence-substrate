# App Store 发布与恢复

内置 `app-store` 驱动覆盖已签名 IPA 上传、受审批资料清单、本地化/隐私链接、App Asset Library 截图、Apple 处理等待、准确版本选择、提交审核、只读状态跟踪及已接受操作的受控只读恢复。当前验证使用本地 HTTPS 契约服务；没有真实 Apple 账户、合法签名 IPA 或上架成功证据。

## 接入条件

企业提供独立 macOS 发布节点、Apple Developer 团队、已建立的 App Store 应用、App Store Connect 团队 API 密钥，以及经过独立质量验收的 iOS 真机包。构建和签名由受控构建/签名服务完成，本连接器不持有签名证书私钥。上传采用 Apple Build Uploads API，不依赖 altool；生产构建仍需完整 Xcode 与应用工具链。

每次发布前，在 App Store Connect 准备唯一的 IOS / versionString 版本，状态为 PREPARE_FOR_SUBMISSION、未绑定构建、releaseType 为 AFTER_APPROVAL。企业预建应用、可编辑 app-info 与需要的语言，完成年龄分级、隐私数据声明、出口合规、审核联系方式、演示账户和合同资质。资料管理只处理下述文字和普通截图，不会自动填写法律声明或创建应用/语言。首次发布不迁移旧媒体；已批准截图修订只替换本版本的准确展示项，保留共享图片。文字/链接拒审后可经[独立资料修订](store-material-remediation.md)保留原构建重新提审；不自动重提二进制拒审。

发布控制器不能运行生成代码。注册 release + publish、runtime=darwin 的专用节点，不兼任 developer 或 quality。`allowNative:false` 禁止仓库原生工具任务，仍允许管理员固定的发布程序调用系统验证工具。

## 管理员配置

使用 [publisher.appstore.example.json](../deploy/publisher.appstore.example.json) 和 [worker.appstore.example.json](../deploy/worker.appstore.example.json)，将 ID、bundle、Team ID 和路径替换为真实值。一个 App Store appId 只能归属一个目标，不能在不同交付目标中复用同一发布目的地。

凭据文件格式：

```json
{
  "issuerId": "12345678-1234-1234-1234-123456789012",
  "keyId": "ABC1234567",
  "privateKey": "-----BEGIN PRIVATE KEY-----\n...PKCS8 P256 key from Apple...\n-----END PRIVATE KEY-----\n"
}
```

凭据仅服务所有者可读（0600），不进入仓库、制品、任务输入或日志。当前只支持团队 issuer API key，不支持个人密钥。JWT 使用 ES256，aud=appstoreconnect-v1，iat 预留 30 秒时钟偏差、exp 在生成后 9 分钟；每次官方 API 请求单独签发。API origin 固定为 https://api.appstoreconnect.apple.com；任务不能指定 API 地址或凭据。应为密钥授予该应用提审所需的最小角色权限；App Asset Library 当前要求 ACCOUNT_HOLDER、ADMIN 或 APP_MANAGER，不能假设仅上传构建的角色即可管理资料。

`uploadHosts` 是管理员核实的**精确 HTTPS host:port**列表，默认端口可省略。示例主机有意不可用。先通过企业可审查的 Apple 测试应用/官方网络资料确定实际上传主机，再填写；禁止通配域名或为排除故障动态信任服务响应中的任意主机。允许列表变化视为发布目的地配置变化，不能继续借用原 journal。上游可能使用 Apple 委托的存储域名；当前没有实际账号回执来确认生产主机列表。

上传文件的 operation 必须为 PUT、精确覆盖批准文件的每个字节，不能重叠、遗漏、越界或过期。请求不携带 ASC JWT/Cookie，不跟随重定向；只允许内容头和明确的 x-amz/x-apple/x-ms 上传头。服务器指定的 Host/Authorization/Cookie/Content-Length 等被拒绝。上传 URL 的签名参数不写入 journal 或错误日志。

## 制品门禁

先复制并验证已批准 SHA256/大小（当前上限 512 MiB），再检查 IPA 的路径、重复项、单一 Payload/*.app、实际 bundle ID、iphoneos 平台、版本和构建号。拒绝符号链接和特殊条目；解包总量不超过 2 GiB。

macOS 原生 codesign 校验 Apple 锚定的分发签名、固定团队及非 debug entitlement；application-identifier 必须为 TeamID.bundleID。没有给测试环境开跳过验证的 CLI 开关。API 契约测试内部替换签名验证依赖，不能作为合法签名成功证据。合法 App Store IPA 的证书链、嵌入式 profile、复杂 framework/extension 包与 Apple 服务接受情况仍需企业实包集成验收。

## 受审批的商店资料与截图

管理员可启用 `metadataFile` 与 `metadataDigest`，使用 [appstore-metadata.example.json](../deploy/appstore-metadata.example.json) 准备私有 JSON。示例路径、摘要、大小及规格都须替换，不能直接用于发布。清单绑定准确 appId、bundleId、version；每张图片包含绝对路径、SHA256、字节数和运行时规格 specId。截图只能是完整 PNG/JPEG，最多 40 张、合计 128 MiB、单张 20 MiB，尺寸不超过 8192×8192 或 3200 万像素。每个语言包含 1–4 个设备组、每组 1–10 张；实际 Apple 目录可以进一步收紧这些限制。

首次接入步骤：

1. 在企业节点准备实际文字与截图，保护清单为 0600 普通文件。清单不包含账户密钥；注意其中的本地路径会对有权查看该项目制品的人可见。先计算所有图片的 SHA256/大小，再计算完整清单的 SHA256。
2. 用 App Asset Library 只读 reference data 查询当前 `APP_STORE_VERSIONS`、`APP_SCREENSHOT`、设备组及 imageSpecs。填写真实 specId；支持 iPhone/iPad 的 IOS 组，不能将本地测试的 100×200 图片或 `test-spec` 当成生产规格。
3. 在质量中心上传**完全相同的 JSON 字节**为 `ios-appstore` / `report` 制品，关联目标需求版本、仓库和准确提交。创建候选时选择“App Store 资料清单”，或通过 `releases.create` 提供 `storeMetadataArtifactIds: ["artifact_ID"]`。当前只支持一份清单；全部所选 IPA 和清单必须在同一提交。
4. 独立发布负责人下载并审阅清单及对应截图，通过正常候选审批。清单生产者不能批准自己参与的发布。审批绑定清单制品摘要；图片摘要包含在清单内。修改文字、图片、路径或格式都会改变清单，首次发布须新建候选并重新批准；原构建资料拒审按独立资料修订申请新的准确批准。
5. 发布节点配置中的 `metadataDigest` 必须等于已批准的清单摘要；`metadataFile` 指向其私有副本。控制面在 publish/status/continue 任务及可信 publisher 请求中传递同一摘要。节点在任何网络写入前核对清单和所有图片，按批准字节暂存，再验证 IPA 签名。

清单完整声明 versionText 的 description、keywords、supportUrl、marketingUrl、promotionalText、whatsNew，以及 appInfo 的 name、subtitle、privacyPolicyUrl、privacyChoicesUrl、privacyPolicyText。空的可选字段以 JSON `null` 清除门户旧值，不会保留未审阅的旧文案；支持和隐私链接必须为 HTTPS。法律声明、隐私数据收集问卷、年龄评级、演示账号和视频仍由企业预备。

企业须预先建立准确的应用、未关联构建的待提交版本、唯一可编辑 app-info 和语言。两个本地化集合必须与清单完全相同；额外语言、分页、旧截图集/预览集或已有现代 placements 都会阻塞。本增量不删除或迁移旧媒体，不创建语言，不管理自定义产品页。首次发布要求空展示项；被拒原版本可通过下述独立资料批准修订。当前保守地读取旧媒体集合并要求为空；旧 API 与新 Asset Library 是否在真实后台互相映射尚未实测，需在企业测试应用核对。

执行流程为文字 PATCH → 图片 reserve → 按范围 PUT → PATCH `uploaded:true` → 保存 processing。使用当前 **App Asset Library** 写接口，不写已弃用的 appScreenshots，也不向图片提交旧的 MD5/sourceFileChecksum。图片与 IPA 都就绪后，独立 continuation 建立 placements、申请排序，重新读取全部文字、图片 ID/尺寸/规格和准确显示顺序，最后才提审。查询与等待释放执行节点。

Apple 当前图片接口不提供供本平台核对的远端 SHA256。保证范围是：本地 SHA256/大小校验、实际范围上传、不可修改的远端 image ID、所属 library、文件名/大小/类别、处理后的尺寸/规格，以及精确 placements/顺序；不能将它表述为远端文件哈希校验。企业须排他管理同一应用的门户/API 写操作。Apple 没有跨资源事务，最后读取与提交间仍存在外部修改窗口。

旧 profile 同时省略两项 metadata 配置时继续使用企业预备资料；候选界面明确显示“未绑定资料清单”，这不提供上述文字与截图审批保证。启用管理的 profile 会拒绝无摘要的旧请求。示例 profile 默认启用管理。

## 资料写入的锁与恢复边界

启用资料管理后，在同一 SQLite 事务中预约 operation、appId 和 version/build。每次文字更新、图片预约、每个分段上传、确认、placement、排序和提审写入前都检查当前请求摘要、pending 状态及预约。已完成 IPA 上传仍保留应用锁，直到确认提审并写入本地 complete 才原子释放；version/build 的占用永久保留，避免换 operationKey 重传同一未知构建。

资料中间阶段任何不确定写入均保留 pending 和应用锁；不要删 journal/锁表、换 key 或恢复旧备份后盲传。下面既有上传/提审恢复阶段、资料修订的 `material_resubmit_intent`，以及满足完整图片正向证据的截图提交阶段可由管理员只读核对。上传或截图处理等待的恢复保留应用锁，确认已提审的恢复才在同一事务释放它。图片创建、未完整提交、展示项变更、文字及 resolve 中间阶段没有自动补写或解锁流程，应停止准确进程、核实实际结果并保留记录。

文字/HTTPS 链接拒审后的[独立资料修订](store-material-remediation.md)使用原构建和原审核项，需要新的 `store_revision` 批准、专用 `materialProviders` / `-materials-only` 和当前资料版本独立发布验收。根版本预约仍属于最初操作，每个原操作只允许一个持久后继。其他端及原候选批准保持，状态查询改为绑定准确新资料；截图更换可经[独立截图修订](store-screenshot-revision.md)选择 `store-media`：先上传并释放 Worker 租约持久等待，再由显式 continuation 入口替换准确展示项和重新提审。旧 `store-text` 不能更换截图，其他渠道资料补交不在此范围。

## 持久状态与资源释放

```mermaid
flowchart LR
  A[批准的签名 IPA] --> B[上传 processing]
  B --> C[只读查询构建处理]
  C -->|尚在处理| B
  C -->|处理完成且构建身份一致| D[独立 publish_continue 任务]
  D -->|原审批及当前门禁有效| E[版本绑定与审核提交]
  E --> F[awaiting_review]
  F --> G[awaiting_release]
  G --> H[published]
  H --> I[真实设备与地区独立验证]
```

POST buildUploads → POST buildUploadFiles → 按范围 PUT IPA → PATCH uploaded/checksums 只表示上传被接受，返回 processing。上传使用 Apple 协议的 MD5 文件 checksum；平台批准、制品存储和追溯仍使用 SHA256。

Apple upload=COMPLETE 后，继续核查关联 build 的 VALID、appId、buildUpload ID、build number、preReleaseVersion 的营销版本和 IOS 平台。仅通过这些检查才请求继续提审。

只读查询返回 continueRequired 后，控制面派发独立任务，复用原 operationKey、绑定、制品、externalId 和发布节点，重新检查需求、当前方案、审批、质量门禁及预算。`continuationProviders` 独立配置，绝不回退到 publishers/statusProviders。继续任务不下载另一份包、不执行仓库代码、不调用模型，超时 900 秒、最多一次。预算暂停发生在执行前；其他任务释放预算预留后可以继续领取。通过 goals.revise 提高预算会生成新需求基线、取消旧继续任务并要求重新审批，不能借用旧发布操作；外部执行后的失败只能核对。

提审依次绑定准确 build、创建 reviewSubmission、加入准确 appStoreVersion item、PATCH submitted=true。只有 Apple 返回 WAITING_FOR_REVIEW/IN_REVIEW 才记录 awaiting_review。审核后的 PENDING_* / PROCESSING_FOR_APP_STORE / ACCEPTED 展示 awaiting_release；不会自动执行手动发布请求。READY_FOR_SALE 记录 published，但不证明每个地区和实际设备均已可用。拒审、移除、替换分别保留 rejected/halted 与上游状态；未知状态保留原结论。

处理、审核和发布等待都不占节点租约。持久查询通常间隔 5 分钟，失败后 30 分钟，自动跟踪上限 30 天。当前不接收 Apple webhook；节点离线时保留业务状态，恢复后领取到期短任务。

## 中断、核对与恢复

publisher.db 在任何外部写之前记录 pending 和 intent stage，每个成功步骤立即保存 uploadId/fileId/buildId/versionId/submissionId/itemId。已完成操作重放返回既有收据，不重复上传/提审。两个发布进程使用 SQLite 原子 reservation 争抢，最多一方发起继续写。

外部超时、非 2xx、响应身份不一致或本地收据未确认都保留 pending；没有自动清除/重试开关。查询也不会修改外部状态，未知写结果不会变成可继续信号。`-inspect` 额外输出 `journalDigest`，绑定原请求、状态、收据、结果和更新时间；它是快照指纹，不是对本地数据库管理员的签章。

```sh
rtk proxy ./bin/is-publisher -config /etc/substrate/publisher.appstore.json -inspect OPERATION_KEY
```

### 已接受操作的只读恢复

`-reconcile-apple` 是发布节点管理员专用模式，与 status-only、continue-only、materials-only、inspect、renew-feeds、init-key 互斥。普通 `Request.action`、Worker 和 MCP 没有恢复动作。它只向固定 Apple API 发送 GET，不接受替代 uploadId/submissionId/itemId，不上传、重提、撤销或修改商店资料。

只支持以下 pending 阶段：

| 本地阶段 | 必需的 Apple 正向证据 | 本地恢复结果 |
|---|---|---|
| file_commit_intent / processing | 原文件完整且校验和匹配；准确 upload/build 已 COMPLETE/VALID；原版本仍为未关联构建的 PREPARE_FOR_SUBMISSION / AFTER_APPROVAL | processing；后续当前审批和质量门禁仍需通过 |
| review_submit_intent / submitted | 上述文件和构建证明；准确 submission 的 app/IOS/版本/提交时间；其唯一 item 为原已接受/批准/拒绝项目；原版本仍绑定原 build | 按真实版本状态恢复 awaiting_review、awaiting_release、published、rejected 或 halted |
| image_commit_intent / material_images_processing，仅截图修订 | 原文件/构建证明、原拒审版本/审核项、新批准文字和原展示项不变；所有批准图片均已有持久ID且远端确认提交已接受或已就绪；原清单和PNG/JPEG一致，来源日志与全部预约保持 | material_images_processing / processing，保留app锁；不直接发出continueRequired，不重提审 |
| image_order_intent / materials_placed / material_resolve_intent，以及pending的已确认继续检查点，仅截图修订 | 全部准确新placement ID已保存，完整新展示/顺序/图片/文字就绪，原拒审构建/submission/item保持；resolve意图可正向区分REJECTED与READY_FOR_REVIEW | material_placement_confirmed或material_resolution_confirmed / processing，保留预约；后续受保护继续仅执行剩余审核动作 |
| material_resubmit_intent | 原包/原构建/原 submission/item 和新批准资料全部一致；正向确认已进入审核或发布；根预约、父子关系、app owner 及准确日志快照保持 | 仅 awaiting_review / awaiting_release / published；仍未解决或拒审不得认作重新提审成功 |

原 SHA256 制品必须仍可读取，恢复会重新验证大小/SHA256，再与 Apple 的协议 MD5 比较。文件和审核项目通过准确父资源的完整集合核实；分页、多个条目、草稿、移除项目、处理未完成、未知状态或身份漂移都保持原 journal。

操作步骤：

1. 停止**这个操作**的实际发布程序，并确认没有仍在飞行的 Apple 写请求。取消平台租约不能单独证明程序已停止。备份发布节点私有状态目录，保留原批准请求与制品。
2. 用 `-inspect` 保存当前快照到私有文件。按原请求填 `request`，只将 `artifactPath` 调整为恢复后的原制品路径；不能改变目标、审批绑定、版本、提交、制品摘要或大小。
3. 构造 0600 的恢复输入，例如用已有的原请求与快照生成：

```sh
rtk proxy python3 - <<'PY'
import json, os
from pathlib import Path
request = json.loads(Path('/private/recovery/approved-request.json').read_text())
journal = json.loads(Path('/private/recovery/inspection.json').read_text())
request['artifactPath'] = '/private/recovery/approved.ipa'
data = {
    'request': request,
    'journalDigest': journal['journalDigest'],
    'confirmStoppedOperationKey': request['operationKey'],
    'reason': '写明实际停止的程序、如何确认请求已结束，以及本次核对原因'
}
fd = os.open('/private/recovery/recovery-input.json', os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
with os.fdopen(fd, 'w') as f:
    json.dump(data, f, ensure_ascii=False, indent=2)
PY
rtk proxy ./bin/is-publisher -config /etc/substrate/publisher.appstore.json -reconcile-apple < /private/recovery/recovery-input.json
```

`confirmStoppedOperationKey` 记录管理员对准确操作的确认，程序不能据此自动证明外部进程已停止。不要机械复制声明代替实际核实。

4. 保存返回的完整 Operation 为报告。receipt 中追加原因、原快照、前后阶段、观测时间及输入摘要；一次 SQL 比较并交换同时写入 receipt/result/complete。期间任何本地状态变化都会使本次更新失败，不能覆盖新结果。
5. 由有权限的人将报告登记为同需求版本、提交及 ios-appstore 的 report 制品，在现有 `releases.reconcile` 中填写准确 state、externalId、reason 和 proofArtifactId。恢复发布节点 journal 不会授予平台审批，也不会自动更新控制面事实。之后刷新渠道查询；processing 的继续提审仍检查当前审批、方案、预算及独立质量。

相同恢复输入在后续操作尚未改变阶段时，可从持久记录幂等返回，无需读取原制品或调用 Apple。开始后续提审后，旧恢复输入被拒绝；修改原因或绑定不能借用既有记录。恢复结果是一次已记录的观察，后续状态由正常只读渠道查询更新。

### 继续保留阻塞的情况

操作员根据 stage 和已保存 ID 在 Apple 侧核对，并保留报告：

- 已提审/上架：优先使用上述受控恢复核实准确 ID；不满足恢复条件时，在工作台人工核对准确 externalId 和真实报告，继续跟踪/独立验收，不新建上传。
- 已创建草稿但尚未提交：在 App Store Connect 人工完成或撤销，记录结果；当前不自动修补不确定的中间阶段。
- 确认未提交且需要重建：保留原 journal，使用新的已批准构建号、制品和发布操作。不能仅换 key 再传同一未知构建。
- 无 uploadId 的创建中断：按管理员应用、版本和构建号核对；未能确定时保持阻塞。

上述支持阶段之外，人工核对控制面事实仍不会解锁 publisher 的未知操作。此时自动查询可能继续报告本地 journal 待核对，由人根据 Apple 结果完成渠道报告和独立验收。没有跨阶段自动修补草稿、重传、重提或猜测丢失 ID 的功能；当前能力不是完全自动恢复。

### 截图提交已接受后的处理等待恢复

052沿用同一`-reconcile-apple`、原批准Request、准确pending journalDigest、停止确认和原因，不增加Worker或MCP恢复权限。管理员须实际停止原进程并核对在途请求结束；输入中的确认本身不能证明外部进程已停止。

仅支持截图修订的`image_commit_intent`或`material_images_processing`。再次核对原IPA大小/SHA256及Apple父文件关联/MD5、准确原构建、当前管理员清单与原始PNG/JPEG。每张预期图片必须已在日志内持久保存准确ID，且远端同一app library返回`UPLOAD_COMPLETE`或可识别就绪状态；已处理图片还核对规格和尺寸。只查已知ID，不按文件名、摘要标签或最新图片猜测补全。这里是协议接受状态和身份核对，不是额外的渠道字节证明。

只读核对还要求原拒审版本、原审核项与构建不变，当前文字等于新批准资料，旧展示项仍完整且顺序准确。恢复事务同时绑定当前日志、来源父日志、app/version/继任预约；竞争、来源变化或数据库失败不会形成部分完成。图片集合不完整、尚未提交、处理失败、资料/展示漂移或不支持阶段均保留pending。

成功仅将本地记录恢复为`processing` / `material_images_processing`并保留app锁。即使新图已就绪，恢复结果也不直接给出继续信号；由后续只读status重新观察，再经平台当前审批、质量门禁和准确operationKey继续替换/重新提审。恢复不上传、改文字、删展示项、排序、resolve或submit。精确重放可本地幂等读回；已经继续提审后，旧恢复输入拒绝。控制面仍按前述人工核对流程登记processing与证据，不能登记为awaiting_review或published。

备份控制面外，还需单独备份发布节点私有状态目录及凭据。外部发布与本地备份不能做分布式原子回滚；恢复后先核对 Apple 实际状态。

### 完整展示与审核项的只读恢复

053补齐最后排序、完整展示检查及resolve响应丢失后的正向核对，详见[完整展示恢复手册](store-placement-recovery.md)。恢复核对已持久的全部新展示ID与完整顺序，不重放删除/创建/排序或审核写入。原item仍REJECTED时保存material_placement_confirmed；仅material_resolve_intent或原已确认resolved检查点可正向接受READY_FOR_REVIEW并保存material_resolution_confirmed。后续status和当前审批/质量门禁再允许唯一继续，已确认resolve不再重复。

继续刚领取但在首个写入前中断时，对pending的两个已确认检查点使用新的快照/原因重新核对；来源和全部预约仍须保持。部分展示、缺ID、错误顺序和未确认重新提审等未知阶段继续阻塞。恢复不是awaiting_review或published，不代表真实Apple验证。

## 验证与官方来源

```sh
rtk proxy go test -race ./internal/publisher ./internal/platform ./internal/worker ./cmd/publisher
```

测试使用真实 SQLite、ES256 签名验证、本地 TLS 服务和实际范围字节传输，覆盖进程重开、并发争抢、未知写不重放、错误版本/批准拦截、状态语义、IPA 路径及实际未签名包拒绝。浏览器检查合成渠道回执，只证明工作台与权限流程。

显式本地恢复验收：

```sh
rtk proxy go build -o work/is-publisher ./cmd/publisher
rtk proxy go run scripts/verify-apple-recovery.go -publisher work/is-publisher -state work/apple-recovery-proof
```

state 目录须不存在。脚本运行本地 TLS 丢失响应契约，复制真实恢复后的测试 journal，再用实际二进制检查/幂等读取并验证拒绝路径。二进制不连接本地模拟 Apple；首次远程恢复由 Engine 的契约测试验证。包签名依赖仅在测试内替换，不能用此证明真实签名、Apple 账号或真实 CLI 远程恢复成功。

本轮在线查阅 Apple 官方 JSON 文档并逐项核对 payload 字段，原始缓存保存在忽略的 work/apple-*.json。权威入口：

- [Build Uploads](https://developer.apple.com/documentation/appstoreconnectapi/build-uploads)、[创建上传](https://developer.apple.com/documentation/appstoreconnectapi/post-v1-builduploads)、[创建上传文件](https://developer.apple.com/documentation/appstoreconnectapi/post-v1-builduploadfiles)、[确认文件上传](https://developer.apple.com/documentation/appstoreconnectapi/patch-v1-builduploadfiles-_id_)。
- [Review Submissions](https://developer.apple.com/documentation/appstoreconnectapi/review-submissions)、[Review Submission Items](https://developer.apple.com/documentation/appstoreconnectapi/post-v1-reviewsubmissionitems)、[提交审核](https://developer.apple.com/documentation/appstoreconnectapi/patch-v1-reviewsubmissions-_id_)。
- [JWT](https://developer.apple.com/documentation/appstoreconnectapi/generating-tokens-for-api-requests)、[App Store Version](https://developer.apple.com/documentation/appstoreconnectapi/appstoreversion)、[版本状态](https://developer.apple.com/documentation/appstoreconnectapi/appstoreversionstate)。
- [读取指定上传的文件集合](https://developer.apple.com/documentation/appstoreconnectapi/get-v1-builduploads-_id_-builduploadfiles)、[文件属性与校验和](https://developer.apple.com/documentation/appstoreconnectapi/builduploadfile/attributes-data.dictionary)、[读取准确审核](https://developer.apple.com/documentation/appstoreconnectapi/get-v1-reviewsubmissions-_id_)、[读取审核项目集合](https://developer.apple.com/documentation/appstoreconnectapi/get-v1-reviewsubmissions-_id_-items)。本轮重新获取这些官方 JSON 并核对字段，没有假设文件存在反向 upload 关系或单项 GET 接口。
- [图片上传与管理](https://developer.apple.com/documentation/appstoreconnectapi/uploading-and-managing-image-assets)、[放置与排序](https://developer.apple.com/documentation/appstoreconnectapi/placing-assets-on-your-app-store-surfaces)、[运行时规格](https://developer.apple.com/documentation/appstoreconnectapi/discovering-asset-specifications)、[迁移到 App Asset Library](https://developer.apple.com/documentation/appstoreconnectapi/migrating-to-the-app-asset-library)。资料管理增量的成功 JSON 缓存和摘要见 `work/appstore-materials-rtnb8y1m/official/` 及交付证据；探索中的 404 不作为依据。
