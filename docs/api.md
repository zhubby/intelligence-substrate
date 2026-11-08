# API、协作消息与 MCP

## 约定

基础路径 `/api/v1`。JSON 请求最多 1 MiB、必须恰好一个对象；失败返回 `{"error":{"code":"...","message":"..."}}` 和相应 HTTP 状态。`403` 为权限/人类决策约束，`409` 为版本/租约/状态冲突，`422` 为参数无效，`424` 为未接入执行条件。写接口在服务端检查权限，前端按钮状态不构成授权。

浏览器先 `POST /auth/login`，使用会话 Cookie 和返回的 `csrf` 作为 `X-CSRF-Token`；`GET /auth/me` 可恢复。Agent/worker 使用 `Authorization: Bearer <project-token>`。令牌只在签发时返回明文，不把令牌放入 URL 或业务 JSON。所有生产调用使用 HTTPS。

`GET /auth/options` 返回 SSO 是否配置。OIDC 的浏览器入口位于基础路径之外：`/auth/oidc/start` 与 `/auth/oidc/callback`。准确 state Cookie、一次性 state、nonce、PKCE、已验证邮箱及唯一稳定主体共同约束登录；无效状态在 token exchange 前被拒绝，重复绑定不自动迁移账户。重启、备份与企业权限边界见[SSO 身份手册](sso-identity.md)。

生产 OIDC 要求完整、安全的 HTTPS issuer/授权/令牌/JWKS URL，并在发现、交换、取钥的网络重定向前重新检查。配置的信任和较短超时延续到回调，令牌交换及验证共用15秒时限。失败后重新开始登录，不能重放已消费 state；浏览器离开初始授权端点后的导航另由 IdP 管理，见[传输手册](oidc-transport.md)。

响应 Resource：

```json
{"id":"goal_...","kind":"goal","projectId":"project_...","goalId":"goal_...","title":"客户反馈","status":"planning","revision":1,"data":{},"createdAt":"...","updatedAt":"..."}
```

## 读与集成接口

Agent 执行的 retry、claim 与 `POST /model/chat` 共同约束原任务累计金额、活动时间和 24 次调用；新 run 的 `data.agentAllowance` 是领取快照，费用 reserved/uncertain 需要先按原接口人工核对。无固定命令的 analysis/code/design 适用，固定命令及签名/发布保留既有规则；模型 API 拒绝以固定命令任务直接推理。快照字段、费用核对与恢复边界见[Agent 累计配额](agent-allowance.md)。

普通 Agent 领取的 `agentProgressVersion:1` 要求先保存非终结操作意图，再确认观察和私有文件检查点。替换执行通过 `agentResume` 使用最后确认状态和全新模型会话；prepared 操作需要负责人独立核查进程/外部影响后放弃并显式重试，不能借新方案绕过。工具确认不产生源码合入、质量或发布权限，完整字段与边界见[Agent 持久进度](agent-progress.md)。

代码任务可声明 `writePaths`（1–128个规范相对文件名或末尾`/`的目录，无通配符；`[id]`按字面匹配）。只允许`kind=code`，显式null/空数组无效422；缺省保留全仓库兼容并在审批时显示。范围冻结进源码选择/绑定，完整快照与旧文本提交按真实树核对新增、修改、删除、binary、模式及rename两端，越界409发生在commit/prepared/ref之前。集成带入的变更同样检查；拆分、重排、重试、等待与恢复不能扩大范围。详细规则见[源码修改范围](source-write-scope.md)。

完整快照 `POST /repositories/{id}/snapshot` 仅在第一事务实际范围检查拒绝、尚未创建候选时返回 `409/error.code=source_write_scope_rejected`，允许原有效执行纠正后生成新快照。同键被拒绝的请求不记成功幂等结果。旧文本入口、prepared重放/第二事务、权限或普通分支冲突不提供该标记；未知响应只按原准确字节重放或停止核对。普通代码工具 `restore_base {path}` 只能恢复领取时准确基础提交的一个文件字节/执行位或其不存在状态，按普通工具持久登记。不得传任意版本、仓库或内容。继续和等待均保留原范围及累计额度，详见[源码纠正契约](source-write-correction.md)。

组织团队使用 `teams.create/update/disable/enable/grant/revoke`，成员生命周期由人工企业管理员负责，项目绑定由人工项目负责人负责。`GET /teams` 为管理员目录，`GET /teams?projectId=...` 为人工负责人选择团队的元数据目录，均不返回全局成员名单。版本、稳定请求 key、历史、动态角色并集及凭据撤销见 [组织团队手册](organization-teams.md)。工作区项目附有 `effectiveRoles`，团队绑定附有当前团队状态/版本/启用人数及当前用户来源标记，均为服务端读取投影。

项目成员治理：`members.grant {projectId,identityId,roles,expectedRevision?}` 要求已启用的注册身份及严格的完整角色列表；`members.revoke {id,expectedRevision,reason}` 要求人工项目负责人、当前成员版本及非空原因。角色变化或移除在同一事务中删除该成员在此项目签发的 Agent／Worker token，其他项目和签发人保留；相同角色集合不改变成员版本。被移除记录保留，可显式重新授权，但旧凭据不会复活。最后一位有效注册负责人不能被降权、移除或全局停用。MCP 不暴露成员治理。身份停用、缓存认证 Actor 的事务内复核和重新签发说明见 [项目成员与委托凭据](memberships.md)。

| 方法与路径 | 用途 |
|---|---|
| `GET /projects` | 当前主体可访问的项目 |
| `GET /projects/{id}/workspace` | 授权项目全部业务对象，隐藏租约密钥 |
| `GET /projects/{id}/blueprints` | 授权项目可用的管理员安装蓝图目录，包含准确目录摘要；关闭时为空数组 |
| `POST /runs/{id}/blueprint` | 仅当前蓝图初始化 Worker，以 JSON `{leaseToken}` 读取冻结源码 tar，no-store，无查询参数 |
| `GET /projects/{id}/costs?goalId=...` | 一致的目标/项目成本小计、估算/核对/预留、缺失类别和费用来源 |
| `GET /projects/{id}/delivery-metrics?goalId=...&revision=...` | 需求版本的人工观测、预览/首次业务验收/投入、渠道等待及样本缺口；参数可选，历史 revision 须带 goalId |
| `GET /projects/{id}/acceptance-trace?goalId=...&revision=...` | 需求快照的标准/端覆盖、准确包/源码/构建/独立证据和候选状态；revision 可省略 |
| `GET /resources/{id}` | 读取授权对象 |
| `GET /repositories/{id}?branch=main&path=...` | 分支、日志、文件树与文本 |
| `GET /repositories/{id}/refs` | 授权只读分支头映射 `{repositoryId,heads:{branch:fullSha}}`，用于 code 目标预检 |
| `GET /repositories/{id}/archive?commit={sha}` | 固定提交的受控源码 tar |
| `POST /repositories/{id}/snapshot` | 完整源码 tar，64 MiB / 20,000 条目；元数据在 base64 JSON X-IS-Source 头，幂等与有效 code 租约受控 |
| `POST /runs/{id}/checkpoint` | code/design 当前租约上传完整私有工作区 tar，64 MiB / 20,000 条目；唯一严格 X-IS-Checkpoint 头，无查询参数 |
| `GET /runs/{id}/checkpoint` | 当前 Worker 与 X-IS-Lease 仅下载本执行准确继续记录的工作区；no-store，不开放任意检查点或制品下载 |
| `POST /agent-steps/{id}/checkpoint` | 普通 code/design 操作的准确完整私有工作区 tar，唯一 X-IS-Lease，前后事务重新鉴权，无查询参数 |
| `GET /runs/{id}/agent-checkpoint` | 仅准确活动恢复执行可下载最后确认工作区，唯一 X-IS-Lease、无查询参数、no-store/nosniff |
| `GET /source-operations/{id}` | 源码候选、实际目标分支、原执行状态和可核对条件；不返回租约令牌 |
| `POST /artifacts/upload?...` | 原始文件流；查询参数绑定目标、提交与仓库 |
| `POST /releases/{id}/reports?artifactId=...&name=...&key=...` | 人类owner/product/quality上传1 B–16 MiB上线问题报告；三个参数唯一且必填，来源由准确已发布制品派生 |
| `GET /artifacts/{id}/content` | 校验摘要后下载 |
| `GET /artifacts/{id}/preview` | 小于 2 MiB 的独立 HTML 原型，隔离脚本、禁止网络 |
| `GET /audit?projectId=...` | 项目审计链 |
| `GET /events?projectId=...` | SSE，支持 `Last-Event-ID`，定期重新鉴权 |
| `GET /metrics?projectId=...` | 对象/状态计数、模型费用 |
| `GET /integrations` | 配置状态；`verified:false` 不表示真实执行通过 |
| `POST /plans/generate` | `{goalId,expectedRequirementRevision,key,designId?,expectedDesignBinding?}`，授权人/项目Agent生成待批准方案；持久请求，同键不重发 |
| `POST /clarifications/generate` | `{goalId,expectedRequirementRevision,key}`，人类产品负责人受控生成草稿问题，同请求不重发 |
| `POST /requirements/generate` | `{goalId,expectedRequirementRevision,key}`，人类产品负责人整理待审阅需求稿；同请求不重发 |
| `POST /model/chat` | 受策略、上下文、用量和执行预算约束的兼容模型接口 |
| `GET /healthz`（根路径） | 数据库连接健康，不证明设备或渠道健康 |

`plans.propose` 可传 `expectedRequirementRevision`，控制面在写入方案的同一事务中验证当前批准基线。`plans/generate`必须显式提供当前版本与key，并在网络前持久化plan_generation输入快照；仅goalId的旧调用需升级。成功同键读取原方案，running/failed同键409，不再次调用模型；更换请求内容同键冲突。授权人/Agent须有product/architect/developer角色，Worker不能脱离租约生成方案。结果提交重新检查当前权限、知识/仓库/节点/活动方案和需求批准，费用保留，批准仍独立。生命周期方法不向HTTP命令/MCP开放，见[方案生成与恢复](plan-generation.md)。

模型接口的 `max_tokens` 为可选整数 1–8192；省略时使用受信配置的默认值。截断/拒绝和用量不明均不会返回可执行内容，实际费用仍分别结算或等待核对，见 [模型手册](live-model.md)。

`plans.propose` 与 `plans/generate` 可带 `designId,expectedDesignBinding` 选择准确设计，省略时继承当前方案已批准设计（如有）。服务端派生快照并纳入方案摘要，人工批准前复核原型字节；模型不能返回设计选择或批准字段。Worker 只在有效 design 租约中提案，不能自行批准。原型上传限定 2 MiB；product/architect/design 对原型的上传权限不会扩大到安装包。见[设计基线手册](design-baselines.md)。

两类规划接口还可带 `blueprintId,expectedBlueprintBinding,initializeBlueprint`；这里 ID 指准确蓝图提案。省略时沿用当前需求已批准蓝图而不重复初始化，新需求不自动继承。控制面派生快照和保留初始化工作项；人工批准后仅允许在空源码上提交与冻结归档逐字节一致的内容。`blueprints.propose` 要求 `goalId,expectedRequirementRevision,key,blueprintId,expectedCatalogDigest,applicationName,organization,mode,reason`，可选同项目空仓库 `repositoryId`；此处 blueprintId 为目录 ID `flutter-go-v1`。MCP `blueprint_catalog` 和同名提案命令使用相同授权，详见[应用蓝图手册](application-blueprints.md)。

准确 `rejected` 观察原子创建 `release_issue`。人工 `release_issues.triage {id,expectedRevision,expectedBinding,key,strategy:"new-build"、"store-text"或"store-media",instructions}` 要求当前 product/architect/release 权限（owner/admin 沿用权限并集），只接受这些字段；问题来源、当前需求、CAS 与幂等均在事务内复核。问题可通过 workspace/resource 和 MCP 读取，Agent 不能确认或关闭。未确认的当前必选渠道问题阻塞规划；控制面将来源及决定派生为 `releaseIssues`，纳入手工/模型方案和新候选绑定，调用方不能提供该字段。旧拒审候选不能重置重提，新包须满足原仓库后继及独立质量/新发布批准；仅对应新渠道 `releases.verify` 通过才解决。App Store 文字/链接可经 `store-text` 进入[独立资料修订](store-material-remediation.md)；截图修订选择 `store-media`，由服务端派生 `materialScope:"screenshots"` 纳入修订 spec/binding，处理等待后使用准确原操作继续，详见[截图修订](store-screenshot-revision.md)。其余渠道资料补交仍待接入，详见[拒审修复手册](release-remediation.md)。

只读 MCP 工具 `acceptance_trace {projectId,goalId,revision?}` 与 REST 使用相同事务内现行授权。显式版本须为正整数；HTTP 格式错误 400，不存在快照 404。标准覆盖、报告完整性、目标环境和候选质量校验分别返回，历史记录不计入当前验收，见[验收追溯手册](acceptance-traceability.md)。发布门槛重新检查人工报告真实字节；日志型自动证据也必须保留原始成功工作项、执行和可信质量节点。

只读 `delivery_metrics {projectId,goalId?,revision?}` 与 REST 返回相同有界报告，历史版本需要指定目标，缺失指标返回 null。人工 `measurements.record {goalId,requirementRevision?,type,reference,key,note,...}` 支持 preview/business_acceptance/human_effort/production_defect；`measurements.void {id,key,reason}` 保留原始观测。只有当前有权的人类 owner/product/quality 能写入，这两个命令不向 Agent MCP 开放；技术测试与业务结论严格区分。精确输入、单位、样本分母与更正口径见[交付效能手册](delivery-metrics.md)。

上线问题报告使用原始文件体与会话CSRF，返回准确历史版本的report artifact，purpose=release-feedback。同项目/登记人同key同内容返回原报告，内容或来源变化返回409；未知、重复、缺失及编码错误查询参数400。上传前及注册时核对当前权限、候选绑定、发布目标、原制品字节和真实源码。production_defect的显式requirementRevision与原候选派生版本核对，其他类型仍限当前版本。问题报告不能成为业务验收、独立质量/发布、商店资料或运行证明，见[上线反馈手册](release-feedback.md)。

身份：`POST/GET /identities` 仅管理员；`POST /identities/disable` 撤销身份及其签发凭据。项目负责人通过 `POST /tokens` 签发限定角色的 Agent 令牌，或指定 `workerId` 签发节点令牌；`GET /tokens?projectId=...` 查询摘要与到期，`POST /tokens/revoke` 用 `{projectId,id}` 撤销。

## 命令接口

开发预览：`previews.create {artifactId,expectedRequirementRevision,title,ttlMinutes?,reason,key}` 和 `previews.stop {id,expectedRevision,reason,key}` 要求项目 developer 权限，向 scoped Agent MCP 开放，Worker 不得直接调用。人工会话通过受 CSRF 保护的 `POST /previews/{id}/access {}` 获取短期访问链接，Agent 不可领取。准确绑定、状态、独立入口、包限制和撤权见[开发预览手册](development-previews.md)。

`POST /commands/{name}`，响应为受影响对象。金额预算的 `budget` 单位 USD；内部 `budgetCents` 和费用核对 `chargedCents` 为美分整数。完整输入校验以 `internal/platform` 的命令处理器为准。

| 命令组 | 主要输入/规则 |
|---|---|
| `projects.create` | `title, description, allowExternalModels`；管理员 |
| `members.grant` | `projectId, identityId, roles, name`；人类项目负责人 |
| `projects.policy` | `id, allowExternalModels`；人类项目负责人 |
| `goals.create` | `projectId, title, outcome, criteria[]?, targets[], budget`；省略标准可先澄清，空标准不能批准 |
| `goals.revise` | `id, reason, expectedRequirementRevision?` 与新基线字段；可同步requirementBrief，手工改写范围解除旧稿绑定；保留历史、失效下游 |
| `requirements.propose` | `goalId,expectedRequirementRevision,key,content`；当前已答草稿上下文，角色与字段见[需求稿](requirement-drafts.md) |
| `requirements.apply` | `id,expectedRequirementRevision,key,reason,content`；仅人类product/owner，精确上下文校验、保存人审内容、新版本与人工答复来源；单独批准 |
| `goals.approve` | `id,expectedRequirementRevision?`；人类产品/负责人，未决澄清必须答复，冻结准确快照 |
| `clarifications.propose` | `goalId,expectedRequirementRevision,key,questions[{question,context?}]`；当前草稿，1–10项/批、100项/版本 |
| `clarifications.answer` | `id,questionId,expectedRequirementRevision,key,answer`；人类产品/负责人不可覆盖的首次答复 |
| `plans.propose` | `goalId, summary, risk, reason, tasks[], designId?, expectedDesignBinding?`；不会自行启动 |
| `designs.propose` | `goalId,expectedRequirementRevision,key,title,summary,decisions,criteria[],prototypeArtifactIds[]?,reason`；版本化提案，随方案批准冻结，见[设计基线](design-baselines.md) |
| `plans.approve` | `id`；人类架构/负责人 |
| `tasks.split` | `id, children[{title,instructions}], reason, impact` |
| `plans.reorder` | `goalId, tasks[{id,dependsOn}], reason, impact`；id 为 logicalId |
| `tasks.retry / cancel` | `id`；人类接管，发布不能直接 retry |
| `workers.register / update` | `projectId,title,roles,capabilities,runtime,trustedQuality`；update 另需 id |
| `workers.disable` | `id`；人类负责人 |
| `tasks.claim` | `{}`；节点身份从凭据读取，空闲返回 worker 对象 |
| `tasks.progress` | `id,leaseToken,message`；续租不超任务截止时间 |
| `tasks.complete` | `id,leaseToken,exitCode,log,result`；必须是真实退出码 |
| `agent_steps.prepare` | `runId,leaseToken,step,action,source`；当前普通 Worker 的非终结工具意图，准确幂等，未知字段拒绝 |
| `agent_steps.complete` | `id,runId,leaseToken,observation,checkpointId`；准确原意图、实际观察及文件检查点，analysis 的checkpointId为空 |
| `agent_steps.reconcile` | `id,key,actionDigest,externalReconciled:true,reason`；仅当前人类owner，原Worker disabled、原租约inactive，保留放弃决定，不自动retry；不向MCP开放 |
| `repositories.create` | `projectId,title,description` |
| `repositories.commit` | `id,branch,expectedHead,baseCommit,message,files`；files 为路径→文本 |
| `reviews.create` | `repositoryId,branch,title,goalId?` |
| `reviews.approve / merge` | `id`；独立人类评审、固定提交和质量检查 |
| `pipelines.create` | `projectId,title,command[],runtime,method,verificationEnvironment?,reportPath?`；人类质量角色 |
| `pipelines.run` | `pipelineId,artifactId,criteria[]`；人类质量角色 |
| `quality.bind` | `goalId,expectedRequirementRevision,expectedPlanId,pipelineId,target,criteria[],budgetCents,key`；人类quality/owner，将受保护策略绑定当前批准基线/方案 |
| `quality.revoke` | `id,reason,key`；人类quality/owner，取消未结束的关联验证并保留历史 |
| `sources.prepare` | runId,leaseToken；当前集成 code 租约，返回含准备记录和工作区 SHA 的 run |
| `sources.reconcile` | id,key,commit,observedHead,decision,reason；仅人类 owner，confirm/abandon，逐次核对固定候选；返回 source_recovery |
| `sources.resolve` | runId,leaseToken,resolutions[{path,digest,executable} 或 {path,deleted:true}]；当前执行的明确冲突决定 |
| `knowledge.create / revise` | create：projectId,title,content,source；revise：id,content,reason,source? |
| `evidence.record` | `artifactId,proofArtifactId,method,criteria[],statement`；独立人工/设备验收；资料修订 publication 另带 `releaseId,publicationBinding`，服务端派生准确资料摘要 |
| `releases.create` | `goalId,version,commit,artifactIds[]`；检查必选目标与验收覆盖 |
| `releases.approve` | `id`；与候选创建者独立的人类发布角色 |
| `releases.dispatch` | `id,target,workerId,key`；幂等创建固定发布操作 |
| `releases.refresh` | `id,target,key,workerId?`；人类发布角色，查询已知外部版本；默认原发布节点 |
| `releases.transition / reconcile` | `id,target,state,externalId,reason,proofArtifactId`；真实结果及附件 |
| `releases.verify` | `id,target,evidenceId`；发布后的独立证据 |
| `store_revisions.propose` | `issueId,metadataArtifactId,key`；同包文字资料提案，developer/release，向MCP开放 |
| `store_revisions.approve` | `id,expectedBinding`；独立人类release，提案人/资料作者不得自批 |
| `store_revisions.dispatch` | `id,workerId,key`；人类release，原节点、单次专用资料操作 |
| `runtime.report` | `externalId,releaseId,binding,sequence,health,statusCode,latencyMs,checkedAt`；仅原可信发布节点，绑定当前激活及递增序号；抵达时须仍在90秒信号有效期内 |
| `runtime.incident.acknowledge` | `id,key,note`；人类 release/quality/owner 首次接手未关闭事件，幂等重试；不关闭事件或授权回滚 |
| `runtime.rollback.propose` | `id,destinationId,reason,key`；当前 deployment 至同应用历史 deployment，仅建议 |
| `runtime.rollback.approve` | `id`；人类发布角色，不得是目标制品生产者，批准一次确定的恢复操作 |
| `runtime.rollback.reconcile` | `id,state,reason,proofArtifactId`；仅未知操作，state=succeeded/failed，先在节点核对 |
| `runtime.verify` | `id,statement,proofArtifactId`；独立人类质量人员、准确部署后的真实报告 |
| `charges.reconcile` | `id,chargedCents,reason`；人类负责人 |
| `expenses.record` | `goalId,title,category,amountCents,currency,incurredAt,reference,note,key,proofArtifactId?`；人类负责人，活动来源在项目内唯一 |
| `expenses.void` | `id,key,reason`；人类负责人，保留原金额和作废历史 |

渠道查询是控制面生成的 `publish_status` 工作项，使用 release/publish 节点，但只调用节点独立配置的 statusProviders。结果绑定原 operationKey/externalId，支持 processing / awaiting_review / awaiting_release / published / rejected / halted；unchanged 只保留观察记录，不改变已知状态或发布时间。不能返回 verified，不能代替独立验收。等待、失败与恢复规则见 [发布连接器](releases.md)。

已确认的 processing 查询可返回 `continueRequired:true`。只有处理中的准确 externalId 才可请求后续 `publish_continue`；该工作项由控制面生成，使用独立 continuationProviders，复用原操作和审批并重新检查需求/方案/质量/预算。它不能由 Agent 创建或通用 retry 重放，失联后进入外部结果核对。见 [App Store](app-store.md)。

`releases.create` 可提供 `storeMetadataArtifactIds: ["artifact_ID"]`，当前只接受一份同项目/目标/需求版本/仓库/提交的 `ios-appstore` JSON report。候选 `data.storeMetadata` 保存 `{id,target,digest}`，参与 approval binding；资料作者不能批准自己参与的发布。控制面生成的 publish、publish_status、publish_continue 任务与可信 publisher 请求携带相同 `metadataDigest`。未选择时字段省略以保留旧操作摘要格式；启用资料管理的发布节点会拒绝未绑定的请求。该字段仅选择不可变制品，不接受任务指定本地文件路径或 Apple 资源 ID。完整私有清单、图片及兼容限制见 [App Store 资料管理](app-store.md#受审批的商店资料与截图)。

运行对象包括 deployment（active/superseded）、incident（open/resolved）与 rollback（awaiting_approval/approved/running/succeeded/reconciliation_required/failed）。90秒无有效信号时，读接口显示 health=unknown；控制面启动时及每15秒扫描，持久创建 monitor_signal_lost 事件。application_unhealthy 表示连续三次失败；两类可同时存在。一次有效新报告关闭信号失联事件，应用故障仍需连续两次健康报告关闭。旧无type事件按应用故障处理。事件保留 acknowledgedBy/acknowledgedAt/acknowledgementNote 和恢复的 recoveredDeploymentId/recoverySequence/resolutionReason。序号和 checkedAt 必须同时推进，重复同内容依旧幂等。接手只向人类命令 API 开放，不在 Agent MCP 命令列表中。需求变更保留已批准恢复和线上运维证据；它们不满足新业务验收。当前仅支持同节点、同数据契约的后端切换。详见 [应用运行](runtime.md)。

`artifacts/upload` 在当前基线已移除 Web 时，允许 quality/release 上传同历史部署提交和源仓库的 report，必须给出 deploymentId。包和原型仍严格检查当前基线，运维报告不能成为新发布制品。`purpose:runtime` 的独立证据由 runtime.verify 生成，不允许通过通用 evidence.record 伪造运行验收。

worker 修改代码、产物、知识和计划必须另传 `runId,leaseToken`，并符合本次租约的项目、目标与指定仓库/提交。worker 不能通过通用 Git push 绕过租约；人类使用项目 Agent 凭据 clone/push 非保护分支：`/git/{repositoryId}`，HTTPS Basic 的用户名任意，密码使用项目令牌。不要把令牌嵌入 remote URL。

代码 worker 完成时使用完整源码快照，支持二进制、删除和执行权限；旧文本 commit 也必须属于 code 任务并符合批准的分支和绑定基础提交。快照元数据、原始字节、prepared 确认与租约失效后的恢复边界见 [完整源码提交](source-snapshots.md)。code/build/security 领取时依据依赖的实际源码结果生成不可变 sourceBinding；可用非空 sourceFrom 数组选择依赖祖先，多个未集成结果在启动前阻塞并记录 sourceConflict。完整选择、重试和完成规则见 [工作项源码输入](task-sources.md)。

方案示例：

```json
{
  "goalId":"goal_...",
  "summary":"实现反馈服务，经独立验证后发布",
  "tasks":[
    {"id":"implement","title":"实现反馈功能","kind":"code","role":"developer","runtime":"linux","repositoryId":"repository_..."},
    {"id":"package","title":"构建 Web 包","kind":"build","role":"developer","runtime":"linux","repositoryId":"repository_...","dependsOn":["implement"],"command":["/bin/sh","-c","./scripts/build-web.sh"],"outputPath":"web-package.tar","target":"web"}
  ]
}
```

每个任务的命令是 argv，不在服务端插值。shell 脚本只在隔离执行环境运行。`planId`、`logicalId`、`defectId`、`pipelineRevision`、`sourceBinding`、`sourceConflict` 及 `qualityPolicyId/qualityPolicyDigest/qualityPipelineDigest/qualityDispatchId` 等控制面字段禁止在外部方案中自填。受保护测试由 `quality.bind` 授权后自动调度实际成功构建，或由人类逐制品 `pipelines.run` 发起，禁止以方案中的普通命令替代质量策略。

Android APK/AAB 开发构建可声明 `artifactStage:"unsigned"`，上传必须为 `unsigned-package`，不可用于最终质量或候选。签名工作项使用 `kind:build,role:release,signingMode,inputArtifactFrom,expectedSigner`；Google Play 固定 `android-aab`，华为/小米/OPPO/vivo 固定 `android-apk`，不得交换目标。输入须为同目标的成功无签名依赖祖先；不得携带仓库命令或 `signingInput/signingInputDigest`。专用 release/build 节点领取时由控制面派生准确输入，上传来源与完成回执由租约推导/复查。失败或租约中断阻塞，`tasks.retry` 禁止自动重签。字段、配置、签后质量及人工核对限制见 [Android 可信签名](android-signing.md) 和 [Google Play AAB](android-aab.md)。

macOS 签名采用 `signingMode:macos-pkg,runtime:darwin`，另需 `expectedApplicationSigner`（应用证书SHA256）与 `expectedTeamId`。`expectedSigner` 为安装器证书SHA256。首次可信处理回执含 `signingPending:true` 与准确原操作后，task 为 `awaiting_signature`、run 为 `waiting_signature`，不产生成功构建。控制面保存只读 status 阶段、到期/截止和查询轮数；这些均是外部计划不可填写的保留字段。到期仍由原专用节点领取，最终回执必须匹配冻结的 UUID/请求/载荷及双证书团队。签后 QA 与正式发布门槛保持独立。详见[macOS 签名与公证](macos-signing.md)。

iOS采用 `signingMode:ios-ipa,target:ios-appstore,runtime:darwin`，`expectedSigner` 为Distribution DER SHA256，`expectedTeamId`和`expectedProvisioningProfile`为批准团队与原始CMS摘要。三项身份进入signingInput、制品绑定和完成校验；回执必须含准确teamId/profileSHA256。无签名原生APP只上传unsigned-package，专用release/build节点仅运行管理员固定程序；最终IPA才派发独立QA。失败/失联不以通用retry重签，CI不能代替ios-device或正式发布证明。详见[iOS签名](ios-signing.md)。

受保护自动质量策略拒绝签后包后，缺陷可派生最多两轮源码修复→新无签名构建→新签名→原策略复验。`signingRepairId` 是外部方案不可填写的保留字段；Android权威记录保存在 defect.data.androidSigningRepairs（version1），Apple/Windows在 defect.data.signingRepairs（version2），同一缺陷不能混用。continuation 关联新任务和准确修复执行。领取、续租、上传、完成、恢复及签后制品使用复核授权。通用拆分和重排不适用于受控修复链；人工临时测试或未知签名结果不能自行授权新签名。见 [Android修复](android-signing-repair.md)和[Apple/Windows修复](native-signing-repair.md)。

两条quality命令严格拒绝额外字段，不向Agent/Worker或MCP开放；相同key/输入读取原对象，换输入同key冲突。自动任务的依赖、取消和重试受独立质量权限保护，结果和发布候选绑定准确策略/制品。完整限制、空标准环境规则和恢复见[自动质量验证](quality-scheduling.md)。

## 强制目标环境证据

`pipelines.create` 和 `evidence.record` 接受 `verificationEnvironment`：`ci, browser, windows, macos, ios-device, android-device, simulator`。pipeline 默认 ci。移动真机必须 method=device；受保护流水线的 windows/macOS/iOS 环境还需对应执行系统。人工通过 `purpose:environment` 和同版本报告补充目标环境验收，可不填写 criteria；业务验收的 criteria 覆盖仍单独检查。`purpose:publication` 也要求实际目标环境证据才能通过 `releases.verify`。

`pipelines.create.reportPath` 是可选的安全工作区相对文件路径，由受保护策略控制。Worker在真实测试后上传该文件，结果返回 `reportArtifactId`；成功缺失报告不能通过。服务器按原测试租约推导报告 `testedArtifactId`，完成事务验证本run/质量身份/需求/仓库/提交/准确package，证据保存 `proofArtifactId,proofDigest`，发布时再校验原记录和实际字节。失败提供报告ID也必须通过校验。路径、恢复和真实应用验收见[质量报告手册](reference-quality.md)。

Web 候选必须有 browser 证据；移动端必须有相应真机证据；桌面端必须有对应系统证据。CI 或模拟器通过仍保留其结果，但不能满足上述发布门槛。这些标记必须由可信策略/独立人员据实际操作上报，平台不会自行生成物理验证事实。

## 持久消息协议 v1

`messages.send`：

```json
{
 "projectId":"project_...","goalId":"goal_...",
 "runId":"run_...","leaseToken":"current-run-lease",
 "recipient":"worker:worker_...","recipientTaskId":"task_...",
 "type":"clarification","key":"clarify-users-1",
 "payload":{"question":"目标用户是否包括访客？"}
}
```

类型：`request, progress, result, blocked, clarification, review, cancel`。sender 从认证主体推导；收件人必须是项目内有效成员、启用 Worker 或有效项目 Agent token 身份。有 goalId 时 requirementRevision 由控制面写入。可选 causationId 必须属于相同项目、目标和需求版本。payload 必须是序列化不超过 8 KiB 的对象，拒绝密钥。业务资源引用仍需检查权限与版本。

上例为 Worker：runId/leaseToken 必须在顶层，发送只允许当前已批准方案、当前基线的活动执行在自己的目标内进行。人类和外部 Agent 使用各自认证，不需要执行租约。相同 sender/project/key 和内容返回原消息，不同内容冲突；已失效的 Worker 租约不能用于重放。发送给 Worker 时建议指定 recipientTaskId；省略时只从该目标中唯一的活动执行推导。单节点最多一个活动任务；显式字段可预先排队给另一工作项，避免当前任务误收。

`messages.receive {runId,leaseToken}` 仅供 Worker，返回 `kind:message-inbox,id:runId,data:{items:[],deferred:false,receivedCount:0,blockedCount:0}`。items 为持久 message_receipt，包含 taskId、planId、requirementRevision、firstRunId、workerId、messageId 和无租约的消息快照。首次收件与原消息 delivered 在同一事务提交；同任务换租约或节点可以回放，其他工作项/基线/方案不能继承。只接收发给当前 Worker 且绑定本工作项的同目标、同基线消息。单任务最多 32 条且序列化 64 KiB，超出保留 pending 并返回 deferred，Agent 在模型调用前阻塞。空轮询和原样回放不新增审计。

`messages.claim {projectId}` 和 `messages.ack {id,leaseToken}` 继续供人类及外部 Agent 使用：按明确认证收件人领取 60 秒租约，空队列返回 `kind:message-poll,data.idle:true`，处理后 ack；最多三次领取。Worker 使用 receive，不能调用旧 claim/ack。收件表示进入持久任务上下文，不保证模型理解、执行或跨系统 exactly-once。消息不能代替审批或验收证据。恢复、完成前重查与迟到消息边界见 [协作收件手册](agent-inbox.md)。

`tasks.wait {id,leaseToken,requestMessageId,reason,summary,timeoutSeconds,steps,source:{repositoryId,commit},checkpointId?}` 仅供当前授权 Worker，通过相同 REST 命令或 MCP platform_command 调用。无固定命令的 analysis/code/design 工作项可以挂起，原子保存 message_wait、释放 run 租约并转为 waiting_message。analysis 不能携带 checkpointId，可信 Worker 拒绝其在执行命令后等待；code/design 必须提供本 run 保存的准确 checkpointId，保留原始源码绑定和集成决定，拒绝保存后源码提交或决定变更。reason/summary 最多 2000/8000 字节，非空；timeoutSeconds 为 60–86400、steps 为 1–23 的整数。每任务最多三次等待、挂起链最多 24 步；保留批准方案、需求版本、工作项配方和准确冻结源码。source 两字段必须为字符串，两者均空或实际项目提交；显式声明源码不能丢弃或换源。工作区头、下载、幂等及异常处理见[工作区等待](workspace-waits.md)。

只有原接收人的 result/clarification/review，指向准确原请求、请求者和工作项，才会原子写入 receipt 并使等待 ready、任务 pending。Worker 回复须来自被请求工作项。早到回复同样检查；重复回复不会重复唤醒。准确有效等待回复可发给已停用的原请求 Worker，由新节点读取原任务 receipt；普通消息仍检查启用身份。领取继续时重新验证权限、版本、累计预算和活动 timeout，创建新会话；不增加因挂起而产生的失败次数，不回放模型或工具。状态含 waiting/ready/resumed/expired/cancelled/stale/blocked。取消、方案/需求变化及超时关闭等待，迟到消息不自动复活。相同 Worker 的原输入准确重放返回原等待，不同内容冲突；节点停用或凭据失效仍拒绝重放。详情和信任边界见[协作收件手册](agent-inbox.md)。

## MCP 与 A2A

`POST /mcp` 提供带认证的无状态 JSON-RPC：initialize、ping、tools/list、tools/call。工具为 `get_resource`、`project_workspace`、`platform_command`；后者采用明确命令白名单，排除审批、策略降低、质量门槛修改和生产执行。工具仍经过相同服务端权限检查。

内部协作使用上述控制面契约。没有声称 AX 定义了产品级 Agent 通信协议，也没有把 MCP 当作业务消息总线。当前没有 A2A Agent Card、远程发现、推送或协议兼容性验证；将来可在控制面外增加适配器，保留相同业务权限和幂等规则。

## 受控源码集成

中断快照的人工决定使用 [源码接管协议](source-recovery.md)。读取检查可经 REST 或 MCP `inspect_source_operation`；决定使用人类会话，Agent/Worker 不具权限。confirmed 决定对应 recovered 原执行和 succeeded 原任务，不产生退出码或质量证据；下游验证决定链并使用原候选 SHA。sourceRecoveryId/sourceRecoveryIds 为控制面保留字段。

批准的 code 可声明 sourceMode=integrate，选择成功生产者并保留最多八个原始父输入。sources.prepare/resolve 仅限当前有效 worker 租约；prepare 返回 run，准备对象单独持久化。快照头增加 integrationId/resolutionDigest，最终父提交由绑定推导；当前 run 的已确认操作才可完成。integrationId、integrationParents、integrationWorkspaceCommit、integrationConflicts、integrationResolutions、integrationResolutionDigest、repairRecipeId 不能在外部方案中自填。见 [完整集成协议与冲突处理](source-integration.md)。

## Windows MSIX 受控签名

`plans.propose`支持`target=windows`、`runtime=windows`的`artifactStage=unsigned`开发构建和`signingMode=windows-msix`发布角色转换。签名必须声明`inputArtifactFrom`与依赖、证书DER SHA256 `expectedSigner`、准确`expectedWindowsPublisher`/`expectedWindowsPackage`/`expectedWindowsVersion`。版本为非零major、每段0–65535的规范四段值；不接受仓库command/outputPath或Apple身份字段。

控制面推导signingInput和上传制品中的批准身份/原unsigned摘要，完成回执还需certificateSHA1、publisher、packageName、version、architecture=x64与准确证书/输入/输出关联。SHA1用于Windows证书存储及既有publisher的选择器；不替代SHA256绑定。任务失败/结果不明保持blocked，通用重试不能重新签名。最终package才进入独立质量；CI仍不能满足Windows安装和发布环境门槛。版本化私有程序协议windows-1与企业节点要求见[操作手册](windows-signing.md)。
## AX 客体生命周期

`sandbox` 记录在 AX apply 前持久化，绑定原 Worker/run/lease 摘要、命令与后端摘要、准确 Task/Workspace 名及批准版本。REST 命令为 `sandboxes.prepare`、`sandboxes.finish`、`sandboxes.reconcile`，输入、回执、权限、不可重放与人工隔离要求见 [客体恢复契约](ax-sandbox-recovery.md)。状态 prepared/unresolved/stopped 与命令成功分别记录；API 人工核对只登记决定。普通 Agent 的 MCP 通用命令不开放人工核对。

后端支持原四字段及附加 `transport:{version:"mtls-v1",control,guest}` 的公开身份结构。两端只接收 endpoint、serverName、serverSPKI、caDigest、clientCertDigest，完整结构与 Worker TLS 行为见[固定身份接入](ax-trusted-transport.md)。身份随整体后端摘要绑定，同键重放不得更改；不接收证书正文、私钥或本地材料路径，历史记录不自动获得 mTLS 声明。
