# App Store 完整截图展示与审核项恢复

053补齐截图已经完整替换之后的未知响应处理。管理员只读核对全部展示、图片、原版本/构建和审核项，恢复为受保护的处理等待；后续继续只执行剩余审核动作。沿用[管理员恢复入口](app-store.md#已接受操作的只读恢复)，不新增Worker/MCP恢复权限，不重传IPA/图片或重复已确认的展示操作。

本地验证实际执行SQLite、ES256与TLS，Apple协议提供方、签名成功条件和规格为替身。真实Apple身份、原生/真机业务、企业签名和正式发布分别验收，完整P3尚未完成。

## 支持条件

必须持有原批准Request、准确pending journalDigest、真实停止操作的确认和原因、原IPA、当前管理员清单及原始PNG/JPEG。恢复重新核对本地字节、远端父文件/MD5和原构建。全部图片和展示项的准确ID必须已经持久保存，不猜测或发现缺失ID。

每个语言的完整展示集合/顺序、placement→image关联、图片库身份/大小/规格/尺寸及新文字必须符合批准清单，所有图片就绪。版本仍为METADATA_REJECTED/AFTER_APPROVAL，原submission为UNRESOLVED_ISSUES，唯一原item和原build保持。多余、缺失、重复、未处理或漂移的展示拒绝恢复。

| pending阶段 | 原item正向状态 | 恢复后的receipt阶段 | 剩余动作 |
|---|---|---|---|
| image_order_intent / materials_placed | REJECTED | material_placement_confirmed | 受保护resolve与重新提审 |
| material_resolve_intent | REJECTED | material_placement_confirmed | 同上 |
| material_resolve_intent | READY_FOR_REVIEW | material_resolution_confirmed | 受保护重新提审，跳过已确认resolve |
| material_placement_confirmed | REJECTED | material_placement_confirmed | 恢复领取后在首个写入前中断的继续任务 |
| material_resolution_confirmed | READY_FOR_REVIEW | material_resolution_confirmed | 同上，保留已确认resolve，不降级 |

最后两行仅用于pending的已领取继续操作。恢复均返回processing且不直接给出continueRequired，保留app/version/继任预约。complete仅指本次本地操作完成，不是重新提审或版本交付完成。

## 操作步骤

1. 实际停止准确发布进程、确认在途写请求结束，暂停其他状态库和门户的并行编辑。取消租约、输入声明或状态文件本身不能证明程序已停止。
2. 分别备份控制面与发布节点私有状态/凭据。保留原请求、IPA、资料和原图，不删除pending日志或只换key重传。
3. 构建当前可信发布器，inspect准确操作，将完整Operation保存为0600私有文件。

```sh
rtk proxy go build -trimpath -o ./bin/is-publisher ./cmd/publisher
rtk proxy ./bin/is-publisher -config /etc/substrate/publisher.appstore.json -inspect OPERATION_KEY
```

4. 按[完整恢复输入示例](app-store.md#已接受操作的只读恢复)，从真实原Request对象生成新的0600文件，包含request、准确journalDigest、confirmStoppedOperationKey和真实reason。仅artifactPath可调整为恢复后的原字节路径；版本、提交、制品、来源、资料修订、scope和binding保持。拒绝覆盖旧输入，凭据不进入Agent任务、聊天或公开工件。

```sh
rtk proxy ./bin/is-publisher -config /etc/substrate/publisher.appstore.json -reconcile-apple < /private/recovery/recovery-input.json
```

5. 私有保存返回Operation，核对processing、receipt阶段、恢复历史和保留预约。事务同时比较当前日志、来源父日志及全部预约，竞争或失败不留下部分记录。有人类权限的负责人将报告登记为同需求/提交/ios-appstore的report制品，经releases.reconcile记录准确processing、externalId、reason、proofArtifactId；发布器恢复不会自动更新控制面或给予批准。
6. 经原statusProviders只读观察，当前完整展示和审核项仍准确才返回processing/continueRequired:true。平台按准确operationKey、当前资料批准和独立质量派发publish_continue；continuationProviders再次验证并CAS领取当前日志/来源/预约的唯一写权。
7. material_placement_confirmed执行剩余resolve和submit；material_resolution_confirmed只submit。两者不再删除/创建/排序展示或重传IPA/图片。awaiting_review后仍需查询实际审核/上架并独立验收准确版本。

重启后同一准确恢复输入可本地幂等返回，即使原IPA/凭据暂不可读也不访问Apple。修改原因、绑定、制品或快照不能借用旧历史，已继续提审后旧输入拒绝。继续领取后再次中断，重新停止并inspect，使用新的pending快照和原因，不复用旧快照覆盖新状态。

## 仍保留阻塞

图片创建/字节上传、非完整图片集合、展示删除/创建后缺少ID、部分展示/错误顺序、未知版本/审核项和没有正向提交结果的重新提审，不由本功能修补。完整顺序是当前状态证据，不是额外的远端图片字节校验。跨状态库、门户和外部账户的独占仍由管理员保证。

恢复后可能变化，每次继续重新验证。已确认resolve回退、资料/来源变化、预约丢失、并发领取和SQL失败均拒绝继续。手工登记控制面事实不会解锁本地未知写入，所有必选目标仍须正式发布并独立验证。

## 验证及原生接续

```sh
rtk proxy go test -race -count=1 ./internal/publisher ./cmd/publisher
rtk proxy go test -race -count=1 -run 'Test(StoreRevision|StoreScreenshotRevision|WorkerStoreMaterialRemediation|WorkerStoreScreenshotRevision)' ./internal/platform ./internal/worker
```

本轮未重跑完整平台Go/PostgreSQL/Python/Flutter/浏览器套件，结果见[验证记录](validation.md#2026-11-07-053-完整截图展示与审核项只读恢复)。本地协议替身不证明真实Apple接收、企业签名或商店成功。

Apple构建按[原生接续手册](apple-reference-builds.md)执行；当前三个SDK仍许可退出69，由用户本人处理。后续分别完成两应用Apple实际构建/业务、Windows节点、移动真机、企业签名、生产与渠道、同一应用真实模型多端闭环。单项恢复能力通过不会关闭完整产品/P3。

<!-- END INTELLIGENCE SUBSTRATE PLACEMENT RECOVERY RUNBOOK 053 -->
