# Intelligence Substrate

企业智能研发与交付底座。围绕交付目标连接需求、Agent、实际 Git 代码、构建、独立验证、人工批准和发布结果。

当前提供可运行的控制面、六个工作台入口、可信执行节点、内置 Google Play 提交、Web/桌面分发、持久渠道查询，以及 Linux 后端运行、健康事件和人工批准回滚。两套 Flutter 参考应用已通过 Web release 构建与真实后端的 Chrome 业务验收，真实 AX/Substrate 已验证固定命令的代码、构建与独立质量链。另两项 Node Web/API 样本已从真实模型规划/开发，经人工重新规划或固定命令修复、准确源码构建、独立 API 与真实浏览器验证，形成待批准候选；这包含人工介入，尚未正式发布。它是产品规划的工程基础版，完整多端正式交付的 P3 验收尚未完成。更广业务的自主协作质量、生产 AX 身份接入、原生端独立验收、企业签名与渠道账户仍需验证，界面不会将“已配置”当作“已交付”。具体证据和缺口见 [验证记录](docs/validation.md)。

[产品验收与接续交付](docs/product-acceptance.md)逐项列出P0–P4、两个业务样本、五类客户端和八类发布目的地的现有证据、剩余条件及接续步骤。原18080预览保持既有版本，最新源码能力须按部署版本确认。

## 本地启动

需要 Go **1.27.1+**、Node.js **22.12+**、npm、Git；开发命令按本仓库约定使用 RTK。固定 AX 上游提交的类型验证要求 Go 1.27.1。无需 Docker 即可运行工作台。

```sh
rtk proxy npm --prefix web ci
rtk proxy npm --prefix web run build
rtk proxy env IS_DEV_MODE=true IS_BLUEPRINT_ROOT=. go run ./cmd/platform
```

访问 [http://127.0.0.1:8080](http://127.0.0.1:8080)，初始邮箱为 `admin@localhost.test`。随机密码写入 `.data/bootstrap-password`，文件权限为 0600：

```sh
rtk proxy cat .data/bootstrap-password
```

这条命令会显示本地开发密码。指定 `IS_BOOTSTRAP_EMAIL` 和 `IS_BOOTSTRAP_PASSWORD_FILE` 可自定义首次管理员；已有身份时不会重置。默认 SQLite 数据、Git 和制品保存在 `.data/`。生产默认要求 HTTPS，只有显式 `IS_DEV_MODE=true` 才允许开发环境的 HTTP 会话。

## 试用路径

1. 创建项目，定义业务结果、必选端与模型预算；验收标准可先留空，稍后整理。
2. 在需求基线提出澄清并确认答复；人工起草或按预算辅助整理[需求稿](docs/requirement-drafts.md)，逐项审阅、编辑并采纳为新版本。单独批准基线后，编制或生成交付方案，批准形成工作项。
3. 在“Agent 与执行”注册项目限定的节点、签发凭据、启动可信 worker。没有节点或模型时保留等待/阻塞状态。
4. 浏览真实 Git 仓库与工作分支，提交或接管变更。主分支合并要求独立评审和同一提交的质量证据。
5. 在“质量与证据”把受保护策略[绑定到当前批准方案](docs/quality-scheduling.md)。成功构建后由独立质量节点自动领取；失败创建缺陷，在批准范围内最多尝试两轮修复、重建与独立复测。
6. 必选目标都有同一提交的合格证据后，创建候选版本，由独立发布负责人批准。可信发布程序执行后，分别记录提审、审核、发布与验证结果。

首次试用可仅选择 Web。选择全部端会要求全部端的实际制品与相应证据，不会自动补齐成功记录。

## 已实现的核心约束

[需求澄清](docs/requirement-clarification.md)保存版本化问答、首次人工决定和批准快照，规划与开发 Agent 读取同一份确认记录。草稿模型入口受策略和预算约束，重复、失败及中断请求不自动重发。

[需求稿与人工采纳](docs/requirement-drafts.md)从业务描述形成说明与验收建议。原稿绑定准确上下文，人审内容保留来源并创建新版本；空标准不能批准，上下文变化时旧稿不能覆盖新决定。

[方案生成与恢复](docs/plan-generation.md)在调用模型前保存批准基线与工程上下文，同一请求成功读取原方案，失败/处理中/中断不自动重发。当前权限或规划输入变化阻止迟到结果，费用保留；人类架构批准后才启动工作项。

[设计与原型基线](docs/design-baselines.md)将版本化设计决定、验收项和真实 HTML 原型绑定到方案，随架构负责人批准冻结。模型规划保持选定版本，执行 Agent 读取批准快照及核对摘要的原型；新提案不会自行覆盖正在执行的设计。

[应用蓝图](docs/application-blueprints.md)将管理员安装的 Flutter/Go 参考源码冻结为版本化提案，随方案批准选择。Worker 仅向空源码的功能分支初始化准确归档；后续开发、构建和独立质量使用实际提交。目录默认对独立二进制关闭，停用不改变已有提案。

[验收追溯](docs/acceptance-traceability.md)按需求版本与必选端串联标准、准确制品、源码、实际构建和独立验证，列出证据缺口及候选/渠道状态。覆盖计数不替代发布审批，历史证据不用于新标准。

[自动独立质量验证](docs/quality-scheduling.md)由人类质量人员绑定策略，控制面按当前方案的实际成功构建创建一次验证工作项。策略变化、撤销和节点撤权阻止旧结果，修复后复用原策略复测；候选与批准固定准确策略证据。

运行中心区分应用故障与监测失联，控制面独立检测过期信号，支持人员接手和可追溯的恢复历史；接手不会改变发布审批或独立验收结果。

[交付成本](docs/delivery-costs.md)按目标汇总模型估算、待核对金额和负责人登记的构建/设备/存储/渠道/人工费用，保留来源去重、作废、更正及备份恢复。小计明确显示缺失范围，完整企业账单与自动用量计量仍需接入。

[交付效能](docs/delivery-metrics.md)按需求版本记录可运行预览确认、业务验收通过/失败、本人投入和上线缺陷，保留真实附件与更正历史；渠道状态单独采集等待。统计明确样本数与缺失值，不把技术测试当业务验收，也不预置完整成本或人工对照收益。

[上线问题反馈](docs/release-feedback.md)为准确已发布制品保留晚到报告。需求改版或移除客户端后仍可登记原版本缺陷、作废和更正；问题材料保留来源与摘要，不获得验收或发布权限。

[渠道拒审修复](docs/release-remediation.md)将准确拒审来源与人工修复要求纳入新方案和候选绑定。修复需要原提交的后继、新包、独立质量及新的发布批准；对应渠道的独立发布验收通过才解决问题。App Store 文字/HTTPS链接支持[原构建资料修订](docs/store-material-remediation.md)，需要独立资料批准与准确资料版本的独立发布验收；[截图修订](docs/store-screenshot-revision.md)支持另行批准、图片处理等待和原审核项重新提审；其他渠道补交仍待接入。

- 控制面拥有权威状态；需求与方案批准、测试策略、生产批准不能由 Agent 自行授权。
- 需求基线、计划、提交、制品摘要、证据与发布批准绑定；需求变化会取消运行并使下游记录失效。
- 独立质量节点不能兼任开发或发布；自动化证据取自可信控制器上报的实际命令退出结果，人工/真机验收需要独立主体和报告附件。
- 任务按项目、角色、系统、能力和依赖调度，支持租约、截止时间、有限重试、取消和结果重放。
- Agent 可在已批准的权限、命令与预算范围内拆分尚未绑定源码的待执行任务、调整其依赖、发送持久消息。扩大范围仍要审批新方案。
- [任务协作收件箱](docs/agent-inbox.md)把请求和回复写入指定工作项上下文。无固定命令的分析、开发和设计 Agent 可显式等待并释放节点；[开发/设计工作区](docs/workspace-waits.md)保存准确文件与集成决定，回复后由可用节点恢复文件并开始新会话。原源码、累计预算、执行时间和权限保持受控；保存不代表合入或质量通过。
- [普通 Agent 持久进度](docs/agent-progress.md)在工具执行前登记意图，完成后保存观察与准确工作区。换节点恢复最后确认进度并开始新模型会话；结果未明先阻塞，由负责人独立核查进程和外部副作用后显式重试，原累计配额和独立质量门槛继续生效。
- 代码任务通过[完整源码快照](docs/source-snapshots.md)保留工具生成文件、二进制、删除和执行权限；提交幂等、租约校验及 prepared 记录保护来源与恢复。
- 批准的 [受控源码集成](docs/source-integration.md)可合并并行结果、记录冲突字节/模式/删除决定，再交给实际构建与独立质量；干净合并无需模型。
- [中断源码人工接管](docs/source-recovery.md)核对原候选、当前分支和批准绑定，恢复后续构建与修复复测；保留原生产者和独立质量门槛。
- 代码 Agent 在预算等错误后受控停止时，保存已经改变的源码为私有快照；按原租约登记的候选仍须负责人核对。取消或上传未确认时保留本地副本并准确展示状态，不自动重放模型或工具。
- code/build/security 领取时生成[不可变源码输入](docs/task-sources.md)，追溯依赖的实际提交和执行；重试保留同一输入，未集成源码在启动前阻塞并展示原因。
- 发布没有自动盲重试。超时、失联或结果不明进入显式核对；所有必选渠道独立验证后才标记交付完成。
- [App Store 已接受操作恢复](docs/app-store.md#已接受操作的只读恢复)通过原制品、准确外部 ID 和 journal 快照核对结果，原子保存恢复记录；不重放上传或提审，平台批准与独立验收仍须通过。
- [完整截图展示与审核项恢复](docs/store-placement-recovery.md)核实准确新展示与原审核状态，恢复受保护等待；继续仅执行剩余审核动作，跳过已确认resolve，仍保留当前审批和独立质量门禁。
- [国内渠道未知操作核对](docs/store-recovery.md)为华为/OPPO提供准确API证明，为小米/vivo保留明确标注的人工后台声明与附件；核对原子更新日志和自己的锁，保留永久版本占用与独立交付门禁。
- 生成代码和工具进程不持有平台/模型凭据；签名与发布由管理员配置的可信程序执行。

## 工程结构

| 目录 | 职责 |
|---|---|
| `cmd/platform` | HTTP API、认证、静态工作台、模型与 SSO 接入 |
| `cmd/worker` | 可信任务控制器、执行隔离、制品与结果上报 |
| `cmd/admin` | 逻辑备份、恢复与完整性验证 |
| `cmd/publisher` | 固定项目/目标发布、持久收据、Google Play / App Store / 华为 / 小米 / OPPO / vivo 与 hosted 连接器 |
| `cmd/distribution` | 独立公开的 Web、安装包与签名更新索引；只读服务 |
| `cmd/runtime` | 可信 Linux 应用运行、只读网关、持久监测和批准回滚 |
| `cmd/updater` | 桌面签名索引消费、校验下载、按摘要确认安装和持久核对；Flutter 通过外置 CLI 接入 |
| `internal/platform` | 权限、状态机、调度、Git、证据、审计与持久存储 |
| `internal/httpapi` | `/api/v1`、MCP、Git HTTP、OIDC、SSE |
| `internal/integrations` | OpenAI 兼容网关与固定提交 AX 适配 |
| `web` | React/TypeScript 六入口工作台与真实浏览器测试 |
| `blueprints/flutter-go` | Flutter 多端源码、Go 参考后端、反馈/预约两种业务 |

## 配置与部署

[`.env.example`](.env.example) 列出部署配置。生产路径为 PostgreSQL + 私有 Git/制品卷 + TLS 反向代理：

```sh
rtk proxy python3 scripts/init-deploy.py --public-url https://substrate.company.example --admin-email admin@company.example
rtk proxy docker compose config --quiet
rtk proxy docker compose up --build -d
```

将示例域名替换为实际域名，并按 [运行手册](docs/operations.md) 配置 TLS。初始化脚本拒绝覆盖已有配置，生成的密钥不会进入 Git。工作节点必须单独部署，不能让不可信代码运行于控制面主机。

镜像和依赖镜像源可在私有 `.env` 显式选择；默认保留上游来源。完整镜像的生产模式、真实 HTTPS/Git、重启持久化和损坏/正常备份恢复可用[隔离容器演练](docs/container-deployment.md)重复验证，演练只操作随机测试项目。

## 验证

```sh
rtk proxy go vet ./...
rtk proxy go test -race -timeout=20m ./...
rtk proxy npm --prefix web run build
rtk proxy npm --prefix web audit --audit-level=high
rtk proxy npm --prefix web exec -- playwright install chromium
rtk proxy npm --prefix web run test:e2e
rtk proxy go -C blueprints/flutter-go/backend test -race ./...
rtk proxy python3 -m unittest discover -s scripts -p 'test_*.py' -v
```

`IS_TEST_POSTGRES_URL` 指向专用测试数据库时才运行 PostgreSQL 集成场景；不会连接默认系统数据库。测试数据保留在该专用库中。[GitHub Actions](https://github.com/zhubby/intelligence-substrate/actions) 执行 Go/PostgreSQL、Python、桌面与移动浏览器及生产容器恢复检查；全部通过后发布 AMD64/ARM64 镜像到 `ghcr.io/zhubby/intelligence-substrate`，含完整提交标签、SBOM 和 provenance。具体通过状态以对应提交的运行结果为准。Kubernetes 与本机 OrbStack kind 的 HTTPS 部署见[部署手册](docs/kubernetes.md)。

`test:e2e` 使用 `scripts/verify-workbench.py`，为每个测试文件/宽度创建独立状态与服务，构建到新的 `work/` 目录。需求澄清、需求稿、设计、方案生成和费用场景只使用本机确定性模型，其他场景保持模型未配置；测试不会使用现有预览或外部模型配置。可用 `-- --suite designs` 选择场景，`--projects desktop` 选择宽度，报告位置会打印到终端。直接 Playwright 默认配置只包含不需要模型测试服务的场景。

## 文档

[开发预览](docs/development-previews.md)从准确 Web 构建包打开多文件静态应用，支持限期、停止、需求变更失效及当前人工/团队权限检查。使用独立 hostname 和短期会话链接；开发 Agent 可经 MCP 创建和停止。预览不产生独立质量或正式交付结论。

[Flutter/Go 完整应用预览](docs/reference-previews.md)使用专用构建包和独立执行节点，在各个预览的临时数据中体验反馈、预约与取消。后端使用无网络的受限容器；控制面只接入节点 Unix socket，按原访问权限逐请求授权。

预览部署示例同时过滤 Caddy 访问/错误日志并约束 SNI/Host；可用 `scripts/verify-preview-ingress.py` 在独立本机 HTTPS 服务验证会话隔离、撤销、代理故障和恢复，方法与企业验收边界见开发预览手册。

- [架构、信任边界与状态模型](docs/architecture.md)
- [需求澄清、人工答复、批准快照与受控模型提问](docs/requirement-clarification.md)
- [业务目标、需求稿、人审编辑与版本采纳](docs/requirement-drafts.md)
- [方案生成、上下文保护、请求回放与中断处理](docs/plan-generation.md)
- [设计版本、原型追溯、方案批准与执行快照](docs/design-baselines.md)
- [验收标准覆盖、准确制品与独立证据追溯](docs/acceptance-traceability.md)
- [独立质量策略、自动调度、修复复测与发布证据](docs/quality-scheduling.md)
- [实际参考应用、同制品浏览器质量验收与报告](docs/reference-quality.md)
- [Android 同制品自动模拟器验收与真机门槛](docs/reference-android-quality.md)
- [Android 指定USB设备、准确release包与独立HTTPS业务验证](docs/android-device-quality.md)
- [Android 无签名 release 构建、可信签名与签后独立验证](docs/android-signing.md)
- [macOS 双证书签名、公证等待、安装包与独立验收](docs/macos-signing.md)
- [iOS 真机应用构建、批准 profile 与专用 App Store 签名](docs/ios-signing.md)
- [Android 签名包的自动修复、重新签名与受保护复验](docs/android-signing-repair.md)
- [iOS、macOS、Windows 签名包修复与独立复验](docs/native-signing-repair.md)
- [Google Play AAB 受控构建、上传签名、独立验证及派生 APK 边界](docs/android-aab.md)
- [签名库认证加密备份、同路径恢复与旧快照重放边界](docs/signer-recovery.md)
- [工作项源码选择、生产者与重试](docs/task-sources.md)
- [受控源码集成与冲突处理](docs/source-integration.md)
- [中断源码核对与人工接管](docs/source-recovery.md)
- [Agent 双向协作、收件恢复与容量边界](docs/agent-inbox.md)
- [API、Agent 消息与 MCP 契约](docs/api.md)
- [执行节点、AX 和可信发布接入](docs/workers.md)
- [AX 实际执行、版本兼容与路由接入条件](docs/ax-execution.md)
- [AX 双向 TLS 固定身份、旧 CLI 迁移与核对](docs/ax-trusted-transport.md)
- [AX 项目授权网关、策略撤权与受控部署](docs/ax-access-gateway.md)
- [真实模型、输出预算、调用间隔与费用核对](docs/live-model.md)
- [内置发布连接器、桌面签名校验与应用分发](docs/releases.md)
- [华为 AppGallery APK 上传、解析等待和原审批下提审](docs/huawei-appgallery.md)
- [小米应用新建/更新推送、审核资料绑定与人工渠道核对](docs/xiaomi-market.md)
- [OPPO 普通应用更新、异步任务与准确版本上架查询](docs/oppo-store.md)
- [vivo 普通应用同步更新、沙箱隔离与人工上架证明](docs/vivo-store.md)
- [国内商店未知操作、管理员证据核对与恢复边界](docs/store-recovery.md)
- [桌面应用内更新、可信 CLI、原生安装和中断核对](docs/desktop-updates.md)
- [Apple 参考应用诊断构建与许可后的接续验收](docs/apple-reference-builds.md)
- [后端运行、健康事件与批准回滚](docs/runtime.md)
- [部署、备份、故障恢复与运行边界](docs/operations.md)
- [控制面容器构建与隔离恢复演练](docs/container-deployment.md)
- [多端蓝图与两个参考验收规格](blueprints/flutter-go/README.md)
- [当前验证矩阵与阶段缺口](docs/validation.md)
- [批准的产品规格](docs/superpowers/specs/2026-11-05-intelligence-substrate-design.md)

App Store 的 IPA 上传、商店资料审批、App Asset Library 截图、处理等待、受控提审、状态查询与管理员只读恢复见 [App Store 发布手册](docs/app-store.md)。资料清单可在创建候选时选择，审批固定其原文摘要。当前完成本地 API 契约验证，真实签名/账号/上架仍需集成验收。

截图修订的最后提交或处理等待落盘结果不明时，可在停止准确进程后按原批准资料和完整图片ID执行[只读核对恢复](docs/app-store.md#截图提交已接受后的处理等待恢复)。恢复只进入processing并保留应用预约，后续替换与提审仍经过当前审批和独立质量；不完整图片集合或展示漂移继续阻塞。

企业治理支持项目成员角色编辑、按版本和原因移除、显式重新授权及最后负责人保护。角色变化／移除原子撤销该成员在此项目签发的 Agent／Worker token，缓存的认证身份也需在事务内重新验证；全局停用撤销全部会话与成员资格。操作与在途执行边界见 [项目成员与委托凭据](docs/memberships.md)。

[组织团队](docs/organization-teams.md)由企业管理员维护成员、项目负责人明确绑定角色；团队与直接角色合并生效，团队成员变更动态影响项目。版本、历史、审计、幂等与受影响委托凭据撤销在同一事务内完成。团队负责人不能替代项目所需的直接恢复负责人。

[企业 SSO 稳定身份](docs/sso-identity.md)将同一提供方主体绑定到唯一已开通账户，停用后保留绑定。登录状态在事务内验证真实有效期并一次性消费，控制面重启可继续仍有效的待回调登录；备份恢复后重新登录。

[SSO 传输边界](docs/oidc-transport.md)在生产校验发现端点和每次网络请求的 HTTPS 目标，拒绝降级跳转；配置时的证书信任延续到回调，令牌交换和验证共享有界等待。

Windows签名安装包路径见[MSIX构建与受控签名](docs/windows-signing.md)：准确构建、批准身份、专用私钥节点和最终独立质量。当前原生Windows签名/安装/升级仍待企业环境验收。
