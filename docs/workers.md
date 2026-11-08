# 可信执行节点与发布契约

## 节点接入

先在工作台注册节点：项目、角色、系统、能力、是否独立质量。开发节点使用 developer + code/build；质量节点使用 quality + test 且 trustedQuality=true；发布节点使用 release + publish。可信质量身份不得兼任开发/发布。节点令牌仅显示一次，保存在受控文件，示例：

Android APK 签名另用仅有 release/build 的专用节点，不能混用发布或仓库开发能力。`signers[target]` 配置固定绝对程序；无签名输入、签后来源、持久意图与恢复限制见 [可信签名手册](android-signing.md)。

```sh
rtk proxy go build -o bin/is-worker ./cmd/worker
rtk proxy env IS_PLATFORM_URL=https://substrate.company.example IS_WORKER_TOKEN_FILE=/run/secrets/worker-token ./bin/is-worker -config /etc/intelligence-substrate/worker.json
```

token 文件只供节点控制器读取。开发环境 HTTP 需要额外显式 `IS_WORKER_ALLOW_HTTP=true`。`-once` 处理最多一项，用于验收；无该参数时持续轮询。生产使用服务管理器重启控制器，stateDir 使用可靠私有卷，保留未确认结果。

## 驱动

| 驱动 | 适用 | 限制与边界 |
|---|---|---|
| container | Linux 编译、测试、工具 | 镜像必须 digest 固定、预拉取；无网络、只读根、受限 CPU/内存/PID；平台 token 不进入容器 |
| ax | AX/Substrate Linux | 双端 mTLS 固定身份、专用 atespace、企业网关授权、固定镜像与客体调试；旧 CLI 需显式诊断许可 |
| native | macOS/iOS、Windows、受控设备/工具 | 必须 allowNative=true 且 runtime 匹配；原生进程不是沙箱，使用一次性专用节点 |

配置样例在 `deploy/worker.*.example.json`。示例中的 digest 是占位符，必须换成真实 SHA256。源码归档不超过 64 MiB，拒绝路径逃逸、符号链接和设备文件。构建 outputPath 必须是工作区内普通文件，不是目录；多文件应用先打 tar/zip。制品最大 512 MiB，超限应扩展后端与相关限额后再交付，不能截断上传。

开发 Agent 的模型循环最多 24 步，支持读取、文本写入（可选 executable）、单文件删除、隔离命令、任务拆分、依赖调整、协作消息和完成/阻塞。代码任务完成时扫描并提交完整源码，保留工具生成的二进制、删除和执行权限；原先跟踪的文件继续跟踪，新增文件遵循仓库 Git ignore 规则。带 command 的代码任务也提交实际变更。控制器元数据与快照位于工作区外，恢复与权限边界见 [完整源码提交](source-snapshots.md)。模型与工具输出均视为不可信数据。生产工具镜像应预装离线依赖，不能为方便安装而给客体开放控制面密钥。

代码 Agent 尚未 finish 而受控返回错误时，已改变的源码会先保存到可信 `stateDir/.stopped-source-*/source.tar`，旁边的 context.json 记录无凭据来源。上下文仍有效且没有最终提交尝试时，原准确 tar 通过既有快照协议登记，原执行仍失败/阻塞，进入人类源码核对；上传未确认、取消或基线失效时仅保留本地副本，不延长租约、不自动重放或采用。工作台区分节点本地报告与平台实际候选。具体状态、人工处理及 kill -9/宿主故障边界见 [中断源码接管](source-recovery.md#agent-尚未-finish-的源码)。该机制不保存模型对话、续接推理或工具会话，不能称为自动续跑。

工作区是准确源码的解包快照，不含 `.git`；控制器负责提交。`run.command` 直接执行 argv，使用 shell 语法时必须明确提供 `/bin/sh -c`。模型输出预算由可信控制面配置，不能由任务提示突破预算；续接字段仅在当前有界模型会话中保留，截断或用量不明会阻塞执行。等待提供方入场仍受任务截止时间和租约约束，最长 HTTP 等待也受 Worker 客户端超时限制。模型会话不持久重播；开发/设计的显式协作等待可以恢复已确认的文件检查点，并以全新模型会话继续。没有已确认等待或普通进度时，失败重试从原绑定源码重新开始。详情见[工作区等待](workspace-waits.md)和[模型接入](live-model.md)。

[普通 Agent 持久进度](agent-progress.md)为无固定命令的 analysis/code/design 领取提供 `agentProgressVersion:1`。每个非终结工具先持久 prepare，再实际执行，随后确认观察和完整私有工作区；下一次推理不能绕过 prepared 步骤。`agentResume` 在当前绑定和权限下恢复最后确认字节、模式、删除及集成决定，并向新会话提供有界的不可信工具 trace。提供方推理、进程和外部连接不保存。原执行失效/节点停用且有未明操作时，同目标跨方案执行受阻；负责人独立停止进程、核对外部影响、停用节点并等待租约 inactive 后才能明确放弃未明步骤，再单独 retry。租约和停用本身不证明物理停止。显式 wait 成功后采用等待检查点；源码、费用、客体及独立质量门槛继续生效。

无固定命令的 analysis/code/design（排除蓝图初始化）现在跨所有执行累计扣减原预算、活动时间和 24 次模型请求。失败、换节点和租约过期不会重置；reserved/uncertain 费用须先核对。领取快照 `run.data.agentAllowance` 提供已用次数与剩余配额，推理前仍做事务检查。没有新增模型或工具会话重放。操作步骤见[累计配额](agent-allowance.md)。

模型循环每步通过 `messages.receive {runId,leaseToken}` 恢复当前任务的持久协作上下文；同一轮内按 receipt ID 去重，模型失败后同任务换节点重试仍可读取。发送 message 动作包含有效执行租约，建议指定 approvedWork 中的 recipientTaskId，回复带 causationId。单节点最多一个活动任务。消息没有批准或取消执行的权限。完成前重查可接收模型响应期间到达的回复；最后检查之后的迟到消息仍可能 pending，不承诺排空完成。

payload 最多 8 KiB；每任务上下文最多 32 条且 64 KiB。容量不足时停止模型调用并明确阻塞，未接收消息保留；单纯重试不会释放上下文。无固定命令的分析/开发/设计可显式等待准确回复并自动由可用节点继续；开发/设计先保存工作区。其他缺少澄清的任务明确 blocked，由负责人处理。旧 Worker 的 claim/ack 已拒绝；人类和外部 Agent 兼容旧协议。固定命令任务不运行模型循环，当前不会自动读取协作消息。操作与恢复边界见 [Agent 收件手册](agent-inbox.md)。

独立测试命令读取 `IS_ARTIFACT_PATH` 指向实际制品，也可检出对应源码。测试策略应确实检查包内容/运行结果，而不能只运行与制品无关的 `true`。控制面保证来源和绑定，测试质量由受保护策略及独立负责人保证。

受保护pipeline可声明 `reportPath`，Worker移除源码中同路径的旧普通文件，在实际命令后收集本次报告并返回 `reportArtifactId`。成功缺报告会阻塞；失败仍保留真实退出码及可用报告。控制面按租约推导准确被测制品，证据与发布门槛复核附件来源及字节。报告不能由任务覆盖，也不能借用同提交的另一个包或run。具体路径约束、错误行为及真实单应用Chrome适配器见[质量报告手册](reference-quality.md)。

Android 独立适配器复用同一报告契约：管理员明确指定专用模拟器、ADB端口和诊断包名，质量节点消费实际 `IS_ARTIFACT_PATH`，安装后再核对设备内APK摘要。策略使用 `method:simulator` 和 `verificationEnvironment:simulator`，不能替代 `android-device` 真机证据。设备归属、安装前拒绝、清理及原始报告见[Android验收手册](reference-android-quality.md)。

人类质量负责人可通过[自动质量绑定](quality-scheduling.md)批准首次验证。匹配的可信quality/test节点轮询时，控制面在领取事务中按当前方案的实际成功构建创建一次工作项；无需开发Agent调用测试入口。Worker执行前核对策略版本及内容摘要；完成时再次核验当前信任、能力、执行系统和策略，失败结果同样不能绕过。修复重建后复用原策略复测，租约恢复保持原调度；等待节点不占用新的执行资源。签名及正式发布仍需原批准。

code/build/security 在领取时由控制面确定准确源码与生产者，保存在 sourceBinding；Worker 按该 SHA 下载源码。code 在工具运行前核对目标分支头，完成时仍检查分支并发条件。build 和已有准确制品的 test 不查询分支。重试保留原输入；多个未集成依赖在创建执行前阻塞。修复、自动重建与独立复测绑定新提交，不能复用旧运行绑定。源码保存后失联、超时或失败，任务等待[负责人核对](source-recovery.md)；旧租约不会恢复，新租约不能冒领旧产出。声明方式见 [工作项源码输入](task-sources.md)。

## Flutter 参考应用构建

使用 `scripts/new-app.py --create-platforms` 生成并将整个应用源码导入项目仓库后，worker 从该仓库根运行自带的入口：

```json
{
  "kind": "build",
  "command": ["python3", "scripts/build-reference-client.py", "--application", ".", "--target", "web", "--api-url", "https://api.company.example", "--output", "artifacts/web.tar.gz"],
  "outputPath": "artifacts/web.tar.gz"
}
```

此为任务字段片段，项目、需求、仓库、提交和目标仍由平台正常绑定。构建入口执行 analyze、widget 测试和 release Web 编译，输出一个可上传的站点根目录 tar.gz。JSON 侧车保留本地 SDK、命令和字节摘要；worker 当前只上传 `outputPath` 指定文件，侧车不会自动进入平台证据库。需要保留侧车时，应另行登记为报告附件。

`blueprint.json.verifiedRecipes` 包含 Web 和 `android-debug` 的明确入口；后者仅为 arm64 诊断 APK，不能作为签名发布包。单一 blueprint 的原始 Flutter 命令仍需在 client 目录执行，并不自动生成可上传归档。

专用工具链镜像/原生节点须预装 Python、固定 Flutter/Dart、目标 SDK 和依赖缓存。native 驱动只传最小环境，工作 HOME 也与宿主不同；若依赖工具链缓存变量，应由管理员控制的包装程序设置，不能向不可信代码继承平台或发布密钥。

2026-11-06 另在隔离控制面完成两套应用的真实仓库提交、worker 领取、Flutter 构建、制品上传及独立质量节点归档检查，最新本地字体版证据在 `work/reference-worker-3363596321/result.json`。该检查确认实际包内有 Web 入口和应用代码，只覆盖 automated 方法；发布创建仍因缺少 browser 环境证据被拒绝。手工 Chrome 业务验收使用的是另外两份指向本地实际 API 的包，不能借用它们的证据放行 worker 生成的 HTTPS 示例包。该隔离验收的需求/计划批准身份是合成测试身份，未接入真实模型或生产企业审批。

后续 `work/reference-quality-e3fc4yp1/actual-source-exact/result.json` 用两套新应用补齐此断点：实际Worker构建带本机诊断API的包，质量节点自动领取后用同一下载包和准确源码Go后端完成真实Chrome业务检查，上传准确报告并形成 `awaiting_approval` 候选。每应用仅一次自动调度，无手动验证或模型调用；报告内139个源码文件与对应Git完整树及逐文件摘要一致。该结果不修改前轮HTTPS包的证据，也不代表正式Web部署或其他端验收。复验入口为 `scripts/verify-reference-quality.go`。

## AX / Substrate

适配器固定上游提交 `2187dd453d60952a4952eb1fc07c81b5721d5680` 的 schema，使用 Workspace + Task 的单数 `workspace`。该提交尚未发布；旧 v0.3.1 与本机新 Substrate API 已证实不兼容。实际版本、路由身份引导条件和重现入口见 [AX 执行手册](ax-execution.md)。业务 API 与 AX 内部对象解耦。

执行步骤：平台持久登记准确 sandbox 身份 → 确认新请求 → 创建独占资源 → resume → 用实际 guest 命令确认可达 → 分块传输受校验源码 → 执行 argv → 单独传输并校验结果 → 删除并确认本次资源不存在 → 平台记录清理回执。完成取真实命令退出码，不取 AX Task phase。未配置集群、WorkerPool 或可达 guest 时任务失败/阻塞。控制器失联或清理未明时同目标新执行受阻，处理见 [AX 客体恢复](ax-sandbox-recovery.md)。

企业需预先安装并验证 AX/Substrate，配置 namespace、atespace、WorkerPool、固定镜像及网络策略。**驱动不会自动安装/改动现有集群**；本轮操作员验收创建的临时池与受限 RBAC 已清理。专用 atespace 不配置直接模型/API/生产密钥，guest 不得访问控制面数据库、模型密钥或其他项目。必须在真实集群完成创建、命令失败、取消、失联和资源清理演练后才能作为生产执行后端。

适配器默认使用[显式双向 TLS 控制面与客体路由](ax-trusted-transport.md)，直接运行上游 gRPC，不继承环境/CLI/Kubernetes 路由，不使用 WorkerIP。配置、材料验证或连接失败不会回退。两条身份快照的公开摘要进入 sandbox，私钥/证书正文与本地路径不进入业务记录。固定版本要求 Task 与 Actor 同名；只有准确进程 EXITED 回执才可成功。旧 CLI 需 allowLegacyCLI:true，secure 与该开关混用拒绝。Failed / Terminating 提前停止等待，源码与结果仍按原协议校验；清理失败或不确定保留原屏障。企业网关/路由授权、凭据轮换与真实多租户隔离仍需验收，平台没有继承上游默认安全边界作为企业鉴权。

## 发布程序协议 v1

可信发布程序由管理员在节点 JSON 中使用绝对路径配置，不来自生成代码：

```json
{
 "driver":"native","allowNative":true,"stateDir":"/var/lib/substrate/publisher",
 "publishers":{"web":["/opt/company/bin/publish-web"],"ios-appstore":["/opt/company/bin/publish-appstore"]},
 "publishEnvironment":["COMPANY_CHANNEL_CREDENTIAL_FILE"]
}
```

stdin：

```json
{"protocolVersion":"1","action":"publish","operationKey":"stable-operation-hash","runId":"run_...","projectId":"project_...","goalId":"goal_...","releaseId":"release_...","version":"1.0.0","target":"web","artifactPath":"/private/temp/package","artifactId":"artifact_...","artifactDigest":"sha256-hex","artifactSize":123,"commit":"full-sha","binding":"approved-binding","externalId":""}
```

程序从 artifactPath 读取已批准内容，核对自身部署/渠道配置，用 operationKey 向外部服务执行幂等操作。可使用企业已有部署程序、Fastlane、商店 API 或签名服务。stdout **只能**输出一个结果对象：

```json
{"externalId":"actual-deployment-or-review-id","state":"awaiting_review"}
```

只有已确认公开发布/部署成功才能返回 published；上传处理返回 processing，提交审核返回 awaiting_review，审核后未公开返回 awaiting_release。诊断写 stderr，不能输出凭据。程序退出非 0、超时、断连、缺少真实标识或无效 JSON 都进入待核对，不能自动重新发布。发布程序进程树会在取消时停止，但外部请求已经提交的结果仍须核对。

允许的环境变量由管理员列出，禁止传递 IS_* 控制面变量。推荐仅向可信程序提供密钥库/签名服务地址或只读凭据文件路径，并使用最小渠道权限。旧 v1 程序需允许上述新增字段；使用严格 JSON schema 的现有程序应先更新解码器。

仓库现有 `is-publisher` 内置 Google Play AAB 提交、Web/桌面分发及系统签名校验；另含 App Store 的 IPA 上传/受控提审/查询，详见 [App Store 手册](app-store.md)，以及华为 APK 校验/OBS 上传/解析等待/原审批下提审，详见 [华为手册](huawei-appgallery.md)。小米提供资料绑定的新建/更新推送及人工渠道核对，详见 [小米手册](xiaomi-market.md)。OPPO 提供普通应用版本更新、资料上传和准确状态查询，详见 [OPPO 手册](oppo-store.md)。vivo 提供普通应用同步更新、受审资料上传和受限查询，上架须门户证明，见 [vivo 手册](vivo-store.md)。Android APK 可先经[专用可信签名节点](android-signing.md)生成最终包，macOS提供[双证书签名及公证链](macos-signing.md)，iOS提供[批准Distribution/profile的IPA转换](ios-signing.md)，Windows提供[准确MSIX与私有证书存储签名](windows-signing.md)，均仍需真实企业身份和原生执行验收。商店连接器均需真实企业账号与正式签名复验。目的地、密钥、公钥与能力边界详见 [内置发布连接器](releases.md)。

查询使用独立 `statusProviders`，不回退到 publishers。stdin 的 action=status，artifactPath 为空，externalId 为当前版本；其余绑定保持一致。stdout 返回相同 externalId，state 可为 processing/awaiting_review/awaiting_release/published/rejected/halted/unchanged，并可提供 upstreamState/reason。unchanged 表示 API 只能观察，不能证明状态变化。内置程序以 `-status-only` 拒绝任何发布请求。状态查询不能返回 verified。`upstreamState:MANUAL_VERIFICATION_REQUIRED` 保留当前状态并停止自动查询，供官方没有状态 API 的渠道使用；仍可人工请求一次只读说明。

继续提审仅使用显式 `continuationProviders`。action=continue，artifactPath 为空，沿用准确 externalId、operationKey 和全部批准绑定。`is-publisher -continue-only` 拒绝其他 action；没有对应程序不能回退到 publishers。已确认的处理查询可以生成一次继续任务，领取时复核当前门禁；未知外部写不能走此路径。

App Store 管理资料时，`metadataDigest` 从候选绑定贯穿 publish、publish_status、publish_continue，再传给固定发布程序。它必须与管理员私有清单的 SHA256 一致；任务不传本地资料路径、Apple 资源选择器或凭据。图片与 IPA 各自处理完成前不提审，等待期间释放租约。清单缺失的历史候选只适用于未配置资料管理的发布档案，不能自动借用新的清单。

App Store 文字/链接拒审可经独立 `store_revision` 批准后，由原节点执行 `publish_materials`。须显式 `materialProviders` / `is-publisher -materials-only`，无入口不回退；不下载 IPA。状态查询携新 publicationBinding/metadataDigest 及来源身份，原候选 binding 和包不变。平台 `sourcePublicationBinding` 映射到发布器 `sourceBinding`，详见[资料修订手册](store-material-remediation.md)。截图范围由人工 `store-media` 决定，贯通 `materialScope:"screenshots"`；上传后进入 processing 并释放租约，status 就绪后用原 operationKey 调显式 `continuationProviders`，资料/status/continue 均不下载 IPA。见[截图修订](store-screenshot-revision.md)。未知外部结果保持待核对，中间写入不自动重放。

## 发布后验证

发布程序的成功结果不是独立质量证据。独立质量角色上传同一提交/目标的真实报告，记录 `evidence.record`，指定 `purpose:publication, externalId`；证据必须在该次发布记录之后产生，且验证人与发布执行者不同。再调用 `releases.verify`。全部必选目标验证后才交付。

商店审核中释放执行资源，已有可信节点的状态查询按控制面时间持久调度，也可在工作台手工请求。外部结果不明仍需人工核对，查询失败不能触发重新提交。没有自动鉴权渠道 webhook、OS 签名证书续期或应用 SLO 告警；发布负责人仍可记录真实结果和附件。

## 后端运行与回滚程序

`is-runtime` 使用同一发布协议运行批准的 Linux HTTP 后端，配置见 [worker.runtime.example.json](../deploy/worker.runtime.example.json)。它属于可信发布节点，不能与不可信开发工具共用 Docker 管理权限。`allowNative:false` 禁用仓库原生工具执行，不会禁止独立配置的可信发布 CLI。

回滚仅调用 `rollbackProviders.web`，不回退到 publishers 或 statusProviders。stdin 的 action=rollback、artifactPath 为空，额外包含 expectedActive/rollbackTo；其余字段来自准确历史制品。程序必须返回本次批准生成的新 externalId 和 published，不能复用原历史激活。下载原制品并不是回滚的前提；节点保留经过验证的历史镜像与日志。

控制面只派发一次批准的运维恢复任务；失联/超时均进入人工核对，通用 retry/cancel 被拒绝。新需求/模型预算不阻止已批准的线上恢复，但也不能通过此路径运行开发命令或模型。状态、网络、持久化数据兼容与两层核对流程见 [应用运行手册](runtime.md)。

## 集成开发结果

已批准的 code 工作项可声明 sourceMode=integrate。控制器先 sources.prepare，下载私有准备提交；无冲突且无 command 时直接提交，不调用模型。Agent 可用 read_input/take_input/resolve 选择固定版本并明确冲突决定，批准 command 成功后由控制器记录实际决定。最终快照保留原绑定 base/expectedHead，附 integrationId/resolutionDigest。输入档案和决定位于工具目录之外；后续构建固定集成的成功 SHA。实际协议、八个父提交/一百冲突上限与修复边界见 [受控源码集成](source-integration.md)。

## macOS 专用签名节点

`signers.macos` 使用独立 `macos-1` 严格协议，仅在 darwin release/build 专用节点执行。控制面冻结双证书/团队和准确 unsigned-package；首次 sign 下载一次输入，processing 回执将任务挂为 awaiting_signature 并释放节点，后续 status 使用原 UUID/请求/载荷，不下载应用、不重签或重复公证。最终只有已装订并经系统验证的 PKG 才上传为 package；等待 run 不算成功源码或质量生产者。默认900秒活动时间由全部查询共享，另有24小时/288轮限制。配置、私有日志、故障核对和真实验收边界见[操作手册](macos-signing.md)。

`signers["ios-appstore"]`使用独立`ios-1`协议，专用darwin release/build节点将准确无签名iPhoneOS APP转换为IPA。Distribution DER、团队和原始CMS摘要固定在批准方案，管理员私有CLI验证CMS、有效期/授权与实际签后身份，签名前持久intent。框架不授予应用entitlements，App Store profile不能直接用于本地安装；最终QA、受控渠道真机和上架验收仍独立。见[iOS操作手册](ios-signing.md)。

项目/atespace授权可部署[AX访问网关](ax-access-gateway.md)：管理员登记Worker叶证书摘要、项目、专用空间、许可镜像和control/guest角色。Unix SIGHUP撤销现有连接准入及活动RPC，故障默认拒绝。原生AX h2c必须封闭在同一私有网络命名空间的数值loopback；Guest原始入口也须拒绝旁路。Worker清理现显式枚举100项分页，并要求准确Task/Workspace NotFound，修正固定上游默认50导致的单页缺失误判。
