# 交付效能与观测依据

企业治理 → 交付效能展示当前项目/目标的需求版本观测。平台状态与明确的人工登记提供依据；指标不改变需求、质量、预算、审批或发布门槛。

## 指标定义

| 指标 | 分子与分母 | 未采集与边界 |
|---|---|---|
| 需求到预览 | 首次有效预览确认的平台记录时间减对应 requirement.CreatedAt；展示已观察版本的均值 | 无样本为 null；不是原型或构建时间，也不倒推更早实际发生时间 |
| 首次业务验收 | 首次有效结果为 passed 的需求版本 / 有有效业务验收的需求版本 | 0/0 为 null；未观察版本列出、不计失败；技术测试不自动计入 |
| 人工投入 | 本人有效已结束区间之和，单位人秒 | 无区间为 null；未登记投入仍未知，不是交付日历时长 |
| 上线缺陷 | 与已发布版本关联的有效登记数量 | 零条只表示未登记，尚无完整生产观察窗口，不能声称无缺陷 |
| 发布执行失败 | publish/publish_continue/publish_materials 的 failed / succeeded+failed run | 0/0 为 null；排除状态轮询、运行中、过期与取消；不是版本最终失败率 |
| 渠道等待 | processing、awaiting_review、awaiting_release 的观测区间之和 | 单位渠道秒，并行目标相加，不能当交付日历等待或模型活跃时间 |
| 成功交付完整成本 | 尚不可测，返回 null | 既有费用账本为已登记部分，不能升级为完整真实总成本 |
| 人工研发对照 | 尚未采集 | 不计算效率提升比例，需要企业真实人工基线和可比样本 |

默认项目报告只选择各目标当前需求版本；指定 goalId 可选择历史 revision。旧事实不会填补新需求版本缺口。来源附件丢失、损坏或绑定错误时，记录保留并显示待核对，从有效统计排除。

「首次」指有效账本中的首次记录。仍有效的首次失败不会被后续通过抹掉；作废会按有效记录重新计算，原内容、原因和更正链接全部可见。该口径不能声称穷尽真实业务历史，必须结合样本数和缺失范围解读。

## 受控登记

人类 owner/product/quality 可以登记与作废；Agent/Worker 即使有这些角色也不能写人的判断。事务内重新核对当前凭据、身份和成员权限；旧请求成功不延长已撤销授权。

所有类型提交 `POST /api/v1/commands/measurements.record`，共有字段为 goalId、type、reference、key、note。可选 requirementRevision 必须匹配平台从观测来源派生的版本；preview/business_acceptance/human_effort仍要求当前需求，production_defect可以选择所引用候选的历史版本。时间、人员、摘要与审批不由客户端声明。

| type | 额外输入 | 核验规则 |
|---|---|---|
| preview | artifactId、evidenceId、proofArtifactId? | 当前批准需求的 package 与独立通过证据；实际环境 browser/simulator/ios-device/android-device/windows/macos，排除 CI、原型和 publication；必须有真实报告；核对包与报告字节 |
| business_acceptance | releaseId、proofArtifactId、result=passed/failed | 当前目标、需求版本、准确候选提交与报告；保留 release binding；人的通过不会触发独立质量通过或发布批准 |
| human_effort | startedAt、endedAt | RFC3339，当前需求版本创建后、已经结束、不超过24小时；只能登记本人，跨目标/项目也不能重叠；界面支持秒精度并转 UTC |
| production_defect | releaseId、proofArtifactId | 报告对应的准确目标已有 published/verified 观测，或旧数据明确 delivered；仅 halted 或 externalId 不足以证明发布；报告绑定准确版本和提交 |

既有可信 Worker 缺省 purpose 代表验收，预览仍完整复核原任务/执行、策略和报告。模拟器保持模拟器标签，不能作为真机证明。人工投入不从 labor 费用金额推算。

晚到上线缺陷归属所引用候选的原需求版本。通过[上线问题报告](release-feedback.md)入口，可为候选清单内已有发布观测的准确制品上传新文件；即使需求改版或移除该端，报告仍绑定原版本。历史页允许缺陷登记、作废与更正，其他观测保持只读。问题报告不能用于业务验收或独立质量/发布证明。完整生产缺陷观察系统尚未接入；测试缺陷和运行探针告警不自动计入。

示例（ID 为说明占位值）：

```json
{"goalId":"goal_example","requirementRevision":1,"type":"business_acceptance","releaseId":"release_example","proofArtifactId":"artifact_report_example","result":"failed","reference":"acceptance-attempt-1","key":"stable-client-request-key","note":"本次实际业务验收依据及范围"}
```

记录时间由平台生成，不能回填旧时间改变顺序。不同验收尝试使用不同 reference；同来源不能通过不同登记人或目标重复计数。登记人/项目同 key 同输入重放返回原记录，修改输入复用 key 返回409。

`measurements.void {id,key,reason}` 保留原内容、作废人/时间/原因。重新登记沿用 reference、使用新 key，保存 replacesMeasurementId。重放旧请求返回原作废记录，不复活；登记、幂等、去重和审计在同一事务，审计失败全部回滚。

## 读取、渠道历史与恢复

`GET /api/v1/projects/{id}/delivery-metrics?goalId=...&revision=...`，参数均可省略。revision 仅在指定 goalId 时可用，范围1至10亿；重复、空值或未知参数400，不存在版本404。响应包含聚合、versions、observations、gaps，不包含任意 run/tool/provider 数据或租约密钥。

MCP `/mcp` 的只读 `delivery_metrics {projectId,goalId?,revision?}` 使用相同服务和当前项目权限。人工写命令不向 Agent 的 platform_command 开放。

新候选每个目标从 ready 开始采集 observedHistory；所有受控转换追加 from/to/at/actorId，时间来自平台。相同外部状态的查询不追加转换、不重置开始时间。旧候选 updatedAt 无法证明历史，仅从首次新观测开始计时，channelHistoryComplete=false；没有候选也不宣称已完整采集。历史需求在下一需求版本创建时截止等待。

备份保留事实、幂等、渠道历史、审计及真实代码/报告文件；不恢复访问凭据。新模块使用原资源表，不新增分析服务或数据库迁移。

## 验收边界

本地合成记录及浏览器测试证明接口、统计和交互行为。真实业务质量、生产缺陷窗口、渠道审核等待、完整成本和人工收益对照仍需企业样本。P3 的真实多端系统/真机、签名、商店和企业验收仍独立开放。
