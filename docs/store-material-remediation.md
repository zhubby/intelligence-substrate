# App Store 拒审后的文字资料修订

当原构建仅因商店文字或链接被拒时，保留原 IPA、候选批准和其他渠道状态，另行提案并独立批准一份 `store_revision`。可信节点在原版本、原构建和原审核项上修订资料并重新提审。当前资料版本完成独立发布验收后，平台才解决对应拒审问题链。

本手册的 `store-text` 范围仅限 **App Store 文字与 HTTPS 链接**。截图更换应选择另行批准的 [`store-media` 截图修订](store-screenshot-revision.md)，旧文字批准不会获得图片修改权限。语言集合、截图字节、尺寸、规格、顺序及已有 Apple 资源身份均保持。增加语言、审核沟通附件、法律声明、其他渠道资料补交和二进制拒审仍需另外处理。需要改变应用行为时，使用[新构建修复流程](release-remediation.md)。

## 前提与操作

1. 原候选已独立批准、通过当前需求/方案/质量门槛，绑定过商店资料清单，并由持有权威日志的原发布节点提交。准确拒审观察形成 `release_issue`；历史上未绑定清单或缺少原日志的操作不能补造来源。
2. 发布、产品或架构负责人在“构建与发布”确认修复要求，选择“商店文字与链接”。这一步只确认修复策略，不批准新资料。
3. 在原清单上只改文字和链接，将新 JSON 的**准确字节**上传为同目标、同仓库、同提交的 `ios-appstore` / `report` 制品。不得修改语言、图片路径/摘要/大小/规格/顺序。任务不接受本地路径或 Apple ID 选择器。
4. 有 developer/release 权限的人或项目 Agent 提交修订，核查新清单、原包、外部版本及新旧摘要。独立人类 release 负责人批准准确 binding；提案人和资料作者不能自批，原候选批准不授予这次修订权限。
5. 管理员在原专用节点固定的 `metadataFile` 位置安装新清单，更新 profile 的 `metadataDigest` 为已批准摘要，保持其余目的地及图片字节不变。原日志、根版本预约及状态目录保持。暂停门户及其他状态库的并行编辑，备份控制面与发布器。
6. 点击“执行资料修订”，仅派给原可信发布节点。节点用专用 `materialProviders` 执行；不会下载或重传 IPA，也不会预约/上传/删除截图，或创建新的构建、submission 和 item。进入等待审核后释放 Worker 租约。
7. 独立 `statusProviders` 携带同一资料身份查询。重新提审只记录 `awaiting_review`，审核通过不等于公开可用。准确 `published` 后，由独立质量人员对原包、外部版本和当前资料进行实际验证，登记带 `releaseId` 和 `publicationBinding` 的 publication 证据，再执行 `releases.verify`。

整体 release 的原 binding、包和原 `storeMetadata` 保持；目标记录当前生效资料与批准。已经验证的 Web/其他端保留状态。连续两次资料拒审分别产生问题及决定，保留历史；最后一次准确资料的独立验收可解决其来源链。来源要求改变、需求/方案/质量变化会阻止旧任务，不会静默继承批准。

## 控制面 API 与 MCP

命令均为 `POST /api/v1/commands/{name}`，会话写入需要 CSRF。

| 命令 | 准确输入 | 权限与结果 |
|---|---|---|
| `release_issues.triage` | `id,expectedRevision,expectedBinding,key,strategy:"store-text",instructions` | 人工 product/architect/release；仅有原日志和资料的 iOS 来源 |
| `store_revisions.propose` | `issueId,metadataArtifactId,key` | developer/release；返回待独立批准修订 |
| `store_revisions.approve` | `id,expectedBinding` | 独立人类 release；返回已批准修订 |
| `store_revisions.dispatch` | `id,workerId,key` | 人类 release；必须原节点，返回单次 `publish_materials` 工作项 |
| `evidence.record` | 既有 publication 字段，以及 `releaseId,publicationBinding` | 独立质量；从准确已发布目标派生 metadataDigest/materialRevisionId |
| `releases.verify` | `id,target:"ios-appstore",evidenceId` | 独立验收准确资料版本，完成后解决问题链 |

只接受定义字段。`403` 表示人类/角色约束，`409` 表示来源、版本、状态、批准或幂等冲突，`422` 表示输入错误。修订 spec 包括原候选/需求/方案/问题/人工决定、原包/提交、原操作/资料批准及新清单摘要；`data.binding = SHA256(JSON(spec))`，批准固定同一摘要。

相同 key 与输入重放返回原对象，不产生新外部操作；同 key 不同内容冲突。完成后派发重放仍返回原 task，但不授予新的执行权限。切换 key 不能绕过已派发状态或待核对操作。

MCP 的 `get_resource` / `project_workspace` 可读取相同来源，`platform_command` 仅开放 `store_revisions.propose`。人工 triage、approve、dispatch 和 verify 不作为新的 Agent 工具开放。撤销项目权限或凭据后，旧授权不能继续读取/写入。

## 专用 Worker 与发布器协议

参照 [worker.appstore.example.json](../deploy/worker.appstore.example.json)，配置独立入口：

```json
{
  "materialProviders": {
    "ios-appstore": ["/opt/is/bin/is-publisher", "-config", "/opt/is/publisher.json", "-materials-only"]
  }
}
```

没有显式入口时任务阻塞，不回退到普通 publisher 或 status。CLI 的 `-materials-only` 只接受 `action:"materials"`，与 status/continue/recovery 等其他模式互斥。

工作项保留原候选 `binding`，另带 `publicationBinding`、`materialRevisionId`、新 `metadataDigest`、`sourceOperationKey`、`sourcePublicationBinding` 和 `sourceMetadataDigest`。Worker 发给可信发布器时将新 publicationBinding 映射成 `binding`，sourcePublicationBinding 映射成 `sourceBinding`；原包 ID/摘要/大小和提交仍精确传递，`artifactPath` 为空。不要将平台中的 `sourceBinding` 源码对象改写成字符串。

发布器首先只读核对原 processed build、linked version、`METADATA_REJECTED`、原 submission 的 `UNRESOLVED_ISSUES` 和唯一准确 `REJECTED` item，以及旧文字/截图全部身份。按批准清单 PATCH 文字，复核，再 PATCH 原 item `resolved:true`；只有准确 item 成为 `READY_FOR_REVIEW` 才 PATCH 原 submission `submitted:true`。读回原版本 `WAITING_FOR_REVIEW/IN_REVIEW`、原构建和新资料后才确认重新提审。

## 防重、未知结果与恢复

根 `apple_versions` 预约永久属于最初操作。每个 parent 只有一个持久 successor；新 operation、父子关系和 app 写锁同事务预约。每次写前核对当前请求、原日志、app owner、根版本 owner 及父子关系，竞争失败不留部分预约。旧操作被继任后不能再作为当前查询或写入权威。凭据轮换不改变目的地授权。

网络响应丢失、节点/租约中断或外部结果不明时，保留 pending、app 锁与 `reconciliation_required`；不能换 key、直接 retry、删锁或恢复旧备份后重传。等待人工核对不占用执行节点。人工取消/核对不等于授权继续；当前没有“取消前尚未执行”资料任务的一键重新派发流程。

资料写入或resolve中间阶段不自动解锁/重放。文字修订只有处于`material_resubmit_intent`，且管理员已停止准确进程、持有原批准请求/原包与准确journalDigest时，才能经[管理员只读恢复](app-store.md#已接受操作的只读恢复)核实。必须正向证实准确新资料已进入待审/待发布/已发布，仍UNRESOLVED_ISSUES或拒审不能算重提审成功。恢复只GET，不补写文字、不resolve、不重新submit；原快照、来源和预约CAS成功后才完成并释放自己的app锁。截图修订另支持052完整图片提交的processing等待恢复，仍保留app锁并经后续受保护继续；它不能计为重提审。恢复发布器后还须按控制面核对流程记录真实结果，不能跳过独立验收。

053进一步支持[完整新展示与审核项的只读恢复](store-placement-recovery.md)，正向区分尚未resolve与已确认resolve。恢复仍是保留预约的processing，当前状态和审批/质量再次通过后只执行剩余审核动作，不重放展示或已确认resolve。此扩展仅针对有完整展示ID/顺序/图片和原审核证据的截图修订，文字及其他中间未知状态没有自动解锁。

## 验证及正式交付边界

本地验证覆盖真实 TLS 契约/SQLite 日志、双 Engine 唯一继任、二轮修订、原身份/媒体漂移拒绝、未知结果与只读恢复；控制面覆盖独立批准、当前门槛、准确 publication 证据、双 PostgreSQL 连接及跨数据库备份恢复。完整 HTTP Worker 循环实际执行本地固定程序，原包仅首次发布下载一次，后续资料与查询不下载。桌面/移动浏览器验证 MCP 提案、权限/CSRF/撤权、人工批准和其他端保留。

这些样本的包、设备声明及 Apple 回执均明确为 synthetic，不能证明真实签名、Apple 审核/上架、真机或生产交付。正式 P3 仍须两个不同业务应用完成全部声明必选端与渠道的实际交付。Xcode 协议由用户本人阅读接受，SDK 检查通过后方可继续对应原生构建。

官方契约参考：[处理未解决审核问题](https://developer.apple.com/help/app-store-connect/manage-submissions-to-app-review/manage-a-submission-with-unresolved-issues)、[更新审核项](https://developer.apple.com/documentation/appstoreconnectapi/patch-v1-reviewsubmissionitems-_id_)、[更新审核提交](https://developer.apple.com/documentation/appstoreconnectapi/patch-v1-reviewsubmissions-_id_)。API 可用性和账户权限仍须真实企业测试应用验收。
