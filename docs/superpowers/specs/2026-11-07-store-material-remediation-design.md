# App Store 拒审资料修订设计

## 范围与依据

落实已批准产品规划的“拒审后修复或补充材料”。采用独立 `store_revision`，保留原候选、原 IPA、其他渠道状态与原发布批准。首批只接受 App Store 文字和 HTTPS 链接修订；locale 集合、截图字节、规格、显示顺序和 Apple 媒体身份保持不变。更换截图、审核沟通附件和其他渠道资料修订不在本增量支持范围。

在原发布操作中替换资料会破坏批准与幂等语义；另建完整多端候选会重复分发其他已发布端。独立修订在明确授权后，仅改准确原版本的资料，并解决准确原审核项。

Apple 官方 API 定义支持 PATCH reviewSubmissionItems 的 resolved 和 PATCH reviewSubmissions 的 submitted。本地 TLS 契约测试不能证明真实 Apple 账号、审核规则或正式上架；实际渠道验收继续开放。

## 控制面契约

`release_issues.triage` 增加 `store-text` 策略，仅适用于有准确原 operationKey、externalId、受批准资料清单的 ios-appstore 拒审。决定与当前需求、来源及 CAS 绑定。

`store_revisions.propose` 接受 issueId、metadataArtifactId、key；派生不可变来源与清单摘要。清单必须为同需求、仓库、提交、渠道的真实 JSON 报告，并与前次资料字节不同。`approve` 为独立人工发布权限，使用 expectedBinding；生产者/提案者不能自批。`dispatch` 仅原可信发布节点可接手，复查原候选质量、当前需求/方案、问题决定、批准与无在途操作。

新 publish_materials 任务使用单独显式 provider，单次尝试，无 IPA 下载或上传。原候选 binding 保持；publicationBinding、metadataDigest、materialRevisionId 标识当前渠道资料批准。查询、完成、租约及 publication evidence 绑定这组准确身份。旧查询、旧独立验收、旧批准均不能覆盖新资料。

拒审历史保留。每个资料 epoch 可产生一个新问题，不覆盖原问题。多轮资料修订串联原操作；最终只在准确当前 epoch 的独立 verified 后解决该链的问题。若需新构建，可重新确认 new-build 并使用既有重新规划流程。需求/方案/决定变化使未执行修订失效。

## 发布节点契约

请求 action=materials，携带 materialRevisionId、sourceOperationKey、sourceBinding、sourceMetadataDigest。管理员原固定 metadataFile 路径提供新批准字节；凭据与目标不得由任务更改。源日志须完整 submitted，核对同一项目、目标、候选、包、提交、版本及固定管理员 profile。多轮通过持久化 baseHash 和根操作关联，版本永久预约仍由原根操作持有。

写前只读核实原上传 processed build、version 仍选中该 build，版本为 METADATA_REJECTED，submission 为 UNRESOLVED_ISSUES，唯一 item 为准确原 item 且 REJECTED；locale、旧文字与所有截图保持原快照。拒绝 INVALID_BINARY 或不明状态。

同事务预约 app 写锁、唯一 parent→child 和新 operation。每次外部写前 CAS 持久 intent，检查原根 version 预约与当前 app 锁。仅 PATCH 已批准文字、PATCH 原 item resolved=true、PATCH 原 submission submitted=true；不创建或删除媒体、审核项或构建。正向读回相同 build/item/submission 和批准资料后记 awaiting_review 并释放 app 锁。失败/中断保留 pending，禁止盲重试和创建旁路修订。

只读 status 不执行上述写入。资料修订待核对状态仅允许管理员对 exact journalDigest、停止确认与正向已重新提审证明做本地只读恢复；无法证明时保留阻塞。

## 验收与保全

测试覆盖修改文字、两轮拒审、旧批准/旧查询/旧证据、错误构建/审核项/locale/截图、竞争预约、失去锁与外部不明恢复；HTTP/MCP、真实 Worker 协议、SQLite/PostgreSQL 与桌面/移动浏览器核对。实际 Apple 发布、真机及签名资质继续单列。

主线程顺序执行，不提交/推送，不操作真实渠道/生产，不代接受 Apple 协议；保留 261 索引、零提交、18080/PID36063、web/dist、686 原源码无关内容、323 历史输出及当前 Docker/AVD 基线。证据 work/store-material-remediation-7krtq535；交付物只在既定 outputs。
