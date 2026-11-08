# App Store 原构建截图修订

原版本因截图或商店说明被拒时，选择 `store-media`，对新的完整资料清单单独提案和独立批准。可信发布器保留原 IPA、构建、App Store 版本和审核项，先上传新图片，持久等待图片就绪，再替换本版本展示项并重新提审。新资料公开发布并通过独立验收后，问题才能解决。

此能力是本地实现和协议验证。真实 Apple 账户、合法签名 IPA、真机、渠道审核及正式上架尚未验收，不能将协议替身结果记为企业正式交付。

## 批准范围

| 选择 | 批准范围 | 必须保持 |
|---|---|---|
| `store-text` | 文字与 HTTPS 链接 | 所有截图字节、数量、规格、顺序 |
| `store-media` | 截图字节、数量、规格、顺序，以及文字/链接 | 语言集合及顺序、原包、提交、构建、版本、审核项 |

`store-media` 由人工 product/architect/release 负责人在准确拒审问题上确认。服务端派生 `materialScope:"screenshots"`，纳入修订 spec 和 binding；调用方不能通过任务字段扩大批准范围。提案人或资料作者不能批准自己的修订。旧文字记录省略此字段，原摘要格式保持兼容。

新语言、视频预览、自定义产品页、审核沟通附件、法律声明及其他渠道补交不在本范围。应用行为或二进制需要修改时，执行新构建修复。截图仍受完整 PNG/JPEG、单张/总大小、组数和实时 Apple 规格目录约束，见源码内 `docs/app-store.md`。

## 操作步骤

1. 原发布已绑定资料清单，准确拒审观察已形成 `release_issue`；保留原发布器状态库、根版本预约和原可信节点。缺少原日志不能补造截图来源。
2. 在“构建与发布 → 拒审问题与修复”确认修复方式为“修订 App Store 截图与资料”，填写准确修复要求。此决定不等于批准新清单。
3. 在原 JSON 清单上修改截图和说明。保留 appId、bundleId、version、语言集合/顺序；计算每张图片 SHA256 与大小，再计算完整清单准确字节摘要。将清单作为同需求、同仓库、同提交的 `ios-appstore` / `report` 制品上传。
4. 提交资料修订，独立 release 负责人审阅清单和图片，批准准确 binding。原候选批准不能代替本次截图批准。
5. 管理员在原专用发布节点固定的 `metadataFile` 安装新清单及准确图片，更新 profile 的 `metadataDigest`；其他目的地与配置保持。暂停门户和其他状态库的并行编辑，分别备份控制面和发布器。
6. 执行资料修订。Worker 使用 `materialProviders`，不下载 IPA；发布器核对原包身份、版本、拒审审核项和旧资料后修订文字、上传新图片。旧展示项保持，进入 `processing` 后释放 Worker 租约。
7. 显式只读 `statusProviders` 查询原操作。新截图全部完成且规格、旧展示项和新文字仍准确时，结果为 `processing` / `continueRequired:true`。平台以同一 operationKey、资料 binding 和 scope 排入单个 `publish_continue`，通过显式 `continuationProviders` 执行。
8. 继续操作取得唯一写权限，逐项删除准确旧 placement，创建并重排新 placement，读回核对后 resolve 原审核 item、提交原 submission。进入 `awaiting_review` 仍未交付；不要提前关闭问题。
9. 继续查询真实审核和发布状态。准确 `published` 后，独立质量人员核对公开版本、业务和本次截图，登记带 `releaseId,publicationBinding` 的 publication 证据，执行 `releases.verify`。其他端已验证状态、原候选 binding 和原包保持。

只调整顺序时，可以复用图片库内已绑定且摘要、大小、规格、生成文件名等身份完全相同的图片；本版本 placement 仍重新建立。不会删除共享的旧 image 资源。

## API 与节点配置

```json
{"id":"release_issue_ID","expectedRevision":2,"expectedBinding":"<当前问题摘要>","key":"<幂等键>","strategy":"store-media","instructions":"替换不准确截图，保留原构建"}
```

以上用于 `release_issues.triage`。后续仍使用 `store_revisions.propose {issueId,metadataArtifactId,key}`、独立人工 `store_revisions.approve {id,expectedBinding}` 和 `store_revisions.dispatch {id,workerId,key}`。会话写入需要 CSRF。MCP 可提案及读取，不能执行人工确认、批准、派发或验收。

```json
{
  "materialProviders": {"ios-appstore": ["/opt/is/bin/is-publisher", "-config", "/opt/is/publisher.json", "-materials-only"]},
  "statusProviders": {"ios-appstore": ["/opt/is/bin/is-publisher", "-config", "/opt/is/publisher.json", "-status-only"]},
  "continuationProviders": {"ios-appstore": ["/opt/is/bin/is-publisher", "-config", "/opt/is/publisher.json", "-continue-only"]}
}
```

各入口必须由管理员明确配置，没有回退。CLI 模式互斥。资料、状态与继续请求均携带 `materialScope:"screenshots"`、新资料批准 binding、metadataDigest、materialRevisionId，以及准确 sourceOperationKey/sourceBinding/sourceMetadataDigest；原包摘要、大小和提交保持，`artifactPath` 为空。

## 状态、并发与恢复

| 状态 | 实际含义 | 允许动作 |
|---|---|---|
| `revising_materials` | 正在核对/上传资料 | 等待本次执行 |
| `processing` | 新图待处理，原展示项保持，尚未重新提审 | 只读查询；全就绪后自动派准确继续任务 |
| `awaiting_review` | 原审核项已重新提交 | 查询实际渠道结果 |
| `reconciliation_required` | 中间写结果不明确 | 停止准确进程并人工核对，禁止盲重试 |
| `published` | 渠道报告公开发布 | 独立验收准确资料版本 |

图片处理状态持久保存在发布器 SQLite 与控制面，进程重启可继续；等待保留 app 预约，防止其他发布修改同一版本。继续操作对准确日志、更新时间、app 锁、根版本 owner 和唯一 successor 使用原子比较，两个 Engine 竞争时只有一个取得写权限。每次外部写前仍重新核对所有权。

新图处理失败或旧展示项/图片/规格/父日志漂移会停止继续，不会把旧证据复用于新资料。需求、方案、人工修复决定或批准范围改变后，旧任务不再执行。备份恢复必须保留原资料批准、任务、操作与来源，不重建或重传已确认外部动作。

**当前恢复限制：** 图片创建、未完整提交、字节上传、placement 删除/创建/排序，以及文字或 resolve 的中间写响应不明确时，保留pending和app预约，禁止换key、删锁、直接retry或猜测补写。旧展示项已部分替换时，不能保证自动回滚展示内容。

052补充[完整图片提交的只读核对](app-store.md#截图提交已接受后的处理等待恢复)：仅`image_commit_intent`或`material_images_processing`，且全部图片ID已经持久化、提交已被远端正向确认、新批准资料/本地原图/原IPA准确、原拒审构建/审核项和旧展示项保持时，管理员可恢复为processing等待。事务保留app锁并绑定来源父日志和预约，不执行任何远程写。随后由status重新判断就绪，再通过现有受保护continue；恢复本身不重新提审。丢失首张提交且后续图片未上传等不完整集合仍无法恢复。

仅最终 `material_resubmit_intent` 丢失已接受响应时，管理员在停止准确进程、持有原批准请求、原 IPA 和准确 journalDigest 后，可走现有只读恢复。必须 GET 正向证实新资料、原构建/审核项及已提交状态，不能把仍拒审/未解决状态当成功。恢复不执行任何远程写，平台核对后仍需独立发布验收。

## 完整替换后的恢复

截图已完整替换、准确placement ID已持久保存后，最后排序或原审核item处理的响应丢失可按[完整展示与审核项恢复](store-placement-recovery.md)核对。恢复只读保存完整展示/已确认resolve的processing等待，继续只执行剩余审核动作；既有审批、质量和准确operationKey仍生效。领取后在首次写入前中断也须新pending快照核对。部分展示、缺ID、错误顺序或没有正向提交结果的重新提审继续阻塞，不能称所有媒体中间状态自动恢复。

## 已验证与待验收

本地测试实际执行 Go race、SQLite/PostgreSQL、HTTP Worker 租约与固定子进程、真实 PNG 编解码和 TLS API 契约；覆盖二轮修订、顺序复用、重启、并发继续、漂移、未知写和最终只读恢复。桌面/手机浏览器覆盖批准、等待、提审及独立验收。独立读回直接核对原始数据库、PNG、请求、清单、审计和备份，不导入产品代码。

测试中的 IPA、设备声明和 Apple 服务都是明确合成样本，100×200 图片和 test-spec 仅用于契约测试。未验证真实商店图片规格、企业账户权限、渠道接口映射或实际审核行为。正式环境还需企业发布主体、授权签名身份、真实应用/设备，以及两个业务应用所有必选端和渠道的实际验收。Xcode/SDK 协议由用户本人阅读接受。

官方接口：[删除展示项](https://developer.apple.com/documentation/appstoreconnectapi/delete-v1-appassetlibraryplacements-_id_)、[创建展示项](https://developer.apple.com/documentation/appstoreconnectapi/post-v1-appassetlibraryplacements)、[创建排序请求](https://developer.apple.com/documentation/appstoreconnectapi/post-v1-appassetlibraryplacementorderingrequests)、[创建图片](https://developer.apple.com/documentation/appstoreconnectapi/post-v1-appassetlibraryimages)。当前未使用不存在的 placement PATCH 接口。

<!-- intelligence-substrate-store-screenshot-revision-runbook-end -->
