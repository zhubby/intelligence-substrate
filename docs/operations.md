# 部署与运行手册

GitHub Actions 多架构镜像发布，以及现有 OrbStack kind 集群的隔离部署与 HTTPS 入口，见 [Kubernetes 部署手册](kubernetes.md)。

## 部署前提

控制面与生成代码执行环境分开。控制面需要 PostgreSQL、私有持久卷、Git、HTTPS 和备份存储；执行需要隔离 Linux/macOS/Windows/设备节点；正式发布需要企业主体、账号、证书和可信发布程序。缺失条件保留为阻塞。

默认 Dockerfile 构建前端和 Go 六个程序，以 uid/gid 10001 运行，仅 `/data` 和 `/tmp` 可写。Compose 不开放 PostgreSQL 端口，平台 HTTP 只绑定本机 127.0.0.1。发布/分发/运行程序需在独立节点使用独立数据和 origin，不能直接复用控制面卷。完整平台镜像已通过显式 HTTPS 基础镜像/依赖镜像参数构建，并完成本地生产模式容器与 PostgreSQL 恢复演练；默认 Docker Hub 路径仍遇现有代理 token HTTP400，不代表代理已修复。镜像选择、私有演练和准确验收边界见[控制面容器手册](container-deployment.md)。

```sh
rtk proxy python3 scripts/init-deploy.py --public-url https://substrate.company.example --admin-email admin@company.example
rtk proxy docker compose config --quiet
rtk proxy docker compose up --build -d
rtk proxy docker compose logs --tail=50 platform
```

初始化脚本拒绝覆盖已有 `.env` 和 `.secrets`。`.env` 为 0600，密钥父目录为 0700。Compose 的本地 secret 实际使用 bind mount，不能依赖 YAML uid/mode 改变源文件权限；bootstrap 文件是私有父目录内的 0444 文件，使非 root 容器可读取。若企业 secret 管理器投射文件，按其机制提供 uid10001 可读文件即可。

平台默认 8080，`deploy/Caddyfile` 是 TLS 反向代理样例。域名和上游需按代理实际运行位置调整；宿主机代理上游为 `127.0.0.1:8080`，同 Compose 网络内可用 `platform:8080`。`IS_PUBLIC_URL` 必须是用户浏览器实际访问的 HTTPS origin。不要通过生产配置的 HTTP 地址登录，否则 Secure Cookie 不会回传。

`IS_PLATFORM_IMAGE` 可选择已构建的平台镜像，`IS_POSTGRES_IMAGE` 可选择企业 PostgreSQL 17 镜像；`IS_NODE_IMAGE`、`IS_GO_IMAGE`、`IS_RUNTIME_IMAGE` 和非秘密依赖地址 `IS_BUILD_GOPROXY`、`IS_BUILD_NPM_REGISTRY` 显式传入构建。默认来源不变，企业可按策略固定摘要；build args 不承载认证秘密。本地演练使用新配置/随机项目/独立卷，不读仓库 `.env`，结束后只清理其自己的容器、网络和卷。

### 不使用 Docker

```sh
rtk proxy go build -trimpath -o bin/is-platform ./cmd/platform
rtk proxy go build -trimpath -o bin/is-worker ./cmd/worker
rtk proxy go build -trimpath -o bin/is-admin ./cmd/admin
rtk proxy go build -trimpath -o bin/is-publisher ./cmd/publisher
rtk proxy go build -trimpath -o bin/is-distribution ./cmd/distribution
```

使用服务管理器设置 `IS_DATA_DIR`、`IS_DATABASE_URL`、`IS_PUBLIC_URL`、`IS_STATIC_DIR`，初次启动设置管理员邮箱和密码文件。生产数据卷与数据库均要限制权限，密码不要写入命令行参数或仓库。

正式渠道与独立分发节点按 [发布运行手册](releases.md) 部署。签名密钥不进入公开卷；控制面备份不包括独立发布/分发节点，必须另备份其状态、不可变制品和私钥。

开发/设计[工作区等待](workspace-waits.md)将确认的归档存入控制面私有 CAS 并纳入备份/完整性检查。Worker 尚未确认的 `stateDir/workspace_<runId>.tar` 和操作 journal 属于节点私有状态，需要单独持久化；不向工具容器挂载 stateDir。上传或等待结果不明时保留原 tar 核对，不通过中断源码路径自动采用，也不能从旧租约自动执行。恢复控制面后重新签发节点凭据，再由当前批准方案调度继续。

## 模型

配置 OpenAI 兼容地址 `IS_MODEL_BASE_URL`、`IS_MODEL_NAME`、`IS_MODEL_API_KEY_FILE`、输入/输出每百万 token 美元价格。外部网关要求 HTTPS；仅明确的内网/本地场景使用 `IS_MODEL_ALLOW_HTTP=true`。内网模型用 `IS_MODEL_EXTERNAL=false`，外部模型还需项目负责人授权项目策略。

每次调用预留费用，限制上下文、输出、执行预算和总预算。供应商超时、缺失/异常 usage 不退款，标为 uncertain，负责人依据账单核对；金额要求显式整数美分。交付成本页支持登记构建、设备、存储、渠道和人工等费用，保留来源、作废与更正历史；聚合模型估算、人工核对和外部登记，预留及不确定金额另列。缺失类别/非法旧记录明确展示，小计不作为完整实际成本。企业账单、基础设施用量与工时尚未自动接入，见[交付成本手册](delivery-costs.md)。原模型预算准入不包含这些非模型费用。

可配置 `IS_MODEL_DEFAULT_MAX_TOKENS`（默认 4096，整数 1–8192，含推理 token）和 `IS_MODEL_MIN_INTERVAL_SECONDS`（默认 0，有限 0–300）。可选 `IS_MODEL_OPTIONS_JSON` 仅接收管理员配置的受限推理选项，具体值必须由提供方支持，不能覆盖模型、消息或预算。调用间隔保存在数据库，排队不预留费用，入场重新检查策略、需求、租约和预算；不重放失败的外部请求。截断或拒绝即使已结算用量也不会执行。费用保留当时的提供方身份、用量及估价费率。配置、错误核对和真实验收方式见 [真实模型手册](live-model.md)。

## SSO 与身份

设置 `IS_OIDC_ISSUER`、`IS_OIDC_CLIENT_ID`、`IS_OIDC_CLIENT_SECRET_FILE`。回调为 `{IS_PUBLIC_URL}/auth/oidc/callback`，管理员先创建对应企业邮箱身份并授予项目角色。实现 PKCE、nonce、一次性 state、签名与 verified email 校验，再绑定 issuer/subject。

[SSO 稳定身份](sso-identity.md)将同一 issuer/subject 限定为一个已开通账户，原账户停用后保留绑定；历史重复绑定拒绝登录。state 在请求事务内校验真实过期时间和 nonce/PKCE 后一次性消费，失败交换需重新开始登录。控制面重启保留仍有效的待回调 state，备份恢复排除登录凭据，恢复后重新登录。企业 IdP 的端点、证书、MFA 与离职联动另行验收。

[SSO 传输](oidc-transport.md)在生产拒绝 HTTP、相对、缺失、带用户信息或片段的必要端点，发现/交换/JWKS 请求的每次重定向在实际发送前重新检查。系统/私有 CA 信任由运行环境提供，配置客户端的代理、拒绝重定向策略和较短超时保留；交换与验证共用15秒时限。平台只控制初始浏览器授权 URL，IdP 后续浏览器导航和企业出网策略需单独治理。

项目负责人可修改完整角色集合、按版本和原因移除成员，并显式重新授权。角色变化或移除会在同一事务内撤销该成员在此项目签发的 Agent／Worker token；再次升权不会复活旧凭据。全局停用会撤销所有项目资格、会话及委托，任何项目的最后一位有效负责人都必须先移交。事务内重新验证当前凭据，防止缓存身份和并发签发绕过变更。完整接口、交接与在途执行边界见 [项目成员与委托凭据](memberships.md)。当前没有企业组织/IdP group 自动同步、账号找回、MFA 或 SCIM。

## 备份与恢复

备份包含逻辑数据库（资源、身份、审计、幂等记录）、Git 和制品，不包含会话、API token、OIDC state、外部模型/商店/签名私钥。恢复后需重新签发节点令牌，外部 secrets 由企业独立恢复。

先暂停新交付/写入，确认没有平台外部进程直接修改裸仓库或制品，再执行：

```sh
rtk proxy ./bin/is-admin -data .data -archive /private/backups/substrate-20261105.tar.gz backup
rtk proxy ./bin/is-admin -data .data verify
rtk proxy ./bin/is-admin -data /private/restored-substrate -archive /private/backups/substrate-20261105.tar.gz restore
rtk proxy ./bin/is-admin -data /private/restored-substrate verify
```

PostgreSQL 部署通过 `IS_DATABASE_URL` 或 `-database` 指定数据库。恢复仅允许全新目录和空数据库，不覆盖已有数据。完整性验证与数据库导入在同一事务内进行；报告损坏、Git 损坏或审计链不一致时导入回滚。失败恢复只清理由本次新建的目标目录，保留原始备份。备份为逻辑一致性快照，不替代 PostgreSQL PITR 和跨地域灾备。

恢复演练需要用隔离环境验证目标、需求历史、Git fsck、制品 SHA256、审批失效和权限，之后才切换服务入口。审计链不是抵抗管理员重写的签名记录；有此要求时外送到独立审计系统。

## 故障处理

需求澄清的生成请求和费用在网络前持久化；进程中断可能留下running和reserved。同key返回409，不自动重发；先核对原生成/费用记录和提供方情况，再决定是否显式新建请求。批准记录保留准确问答快照，规划/执行遇摘要或结构错误会阻塞。问答、生成、批准、幂等及审计沿用原备份，操作见[需求澄清](requirement-clarification.md)。

| 情况 | 当前处理 |
|---|---|
| 没有匹配节点 | task 保留 pending，检查 runtime/role/capability/disabled |
| 模型未配置或超预算 | 保留错误/阻塞；人工编制方案或调整已批准基线 |
| 普通节点失联 | 租约到期，由后续领取触发恢复，受尝试上限限制 |
| 平台短时不可达 | 节点有限续租失败后取消进程树；完成记录本地保留并重放 |
| 发布超时/外部不明 | reconciliation_required；用 operationKey 查询渠道后显式核对 |
| `.unresolved` 完成记录 | 不删除或重新执行发布；核对运行/外部结果，记录结论 |
| 商店拒审/证书失效 | 记录 rejected/reason，修正材料/制品，必要时新候选、新审批 |
| 需求变更时仍有外部审核 | 旧批准失效，人工在渠道核对并停止旧外部流程；新需求创建新候选 |
| 备份损坏 | 恢复回滚；从已验证的前一备份恢复，不覆盖现有实例 |

没有自动登录不明渠道、关闭验收门槛或猜测“已经发布”的恢复路径。

国内商店未知操作由可信发布节点管理员按[专用核对手册](store-recovery.md)处理。华为/OPPO读取准确制品及版本，Xiaomi/vivo明确保留人工后台报告和附件；报告、日志、永久版本归属及自己的锁在同一事务核对。停止原写入者是管理员前置，早期或证据不足的操作仍阻塞。完整publisher备份包含私有证明，须单独保护；本地核对不替代控制面的人工报告、原批准和独立设备验收。

## 运行监测与回滚

可选开发预览入口须使用独立 HTTPS hostname，配置 `IS_PREVIEW_LISTEN/IS_PREVIEW_URL` 与明确的 `IS_PUBLIC_URL`；Compose 可叠加 `deploy/compose.preview.yaml`，TLS 示例为 `deploy/preview.Caddyfile`。保持控制面 Cookie 为 host-only，保留 Host，预览路径含短期凭据，代理/CDN/APM 不得记录或缓存。普通重启保留仍有效链接，备份恢复不恢复凭据；旧链接不能代替重新登录。配置、静态包范围和撤权/在途内容边界见[开发预览手册](development-previews.md)。

预览 Caddy 示例同时过滤默认错误日志中的 URI/请求头/响应头，并强制 TLS SNI 与 Host 一致；仅关闭站点访问日志仍会在 502 时泄露短期 URL。共享 Caddy 进程必须合并示例的全局 logger/servers 规则，不能只复制站点或追加第二个全局块。`scripts/verify-preview-ingress.py` 使用私有 CA、同父域不同 hostname 和真实浏览器验证该路径；企业证书、CDN/APM 与自定义日志器仍需部署时复验。

首批 Flutter/Go 完整应用预览另需独立 `preview-node`、管理员 pin 的 Linux relay 和 `IS_PREVIEW_NODE_SOCKET`，控制面不持有 Docker socket。节点按准确包运行 network=none、只读根、限额和独立 tmpfs 容器，执行前持久化 intent。停止/失效逐请求立即拒绝，节点维护回收；控制面断联60秒租约失效，节点自身不可用时容器按绝对期限退出。临时数据不进入平台备份，恢复后不能假定原容器仍存在。部署、终态、结果不明和真实复验见[完整应用预览手册](reference-previews.md)。

企业交接先核对[组织团队与直接授权](organization-teams.md)的完整权限来源。撤销某一来源不会移除其他来源；团队成员变更动态影响所有绑定项目并原子撤销相关委托。全局停用保留团队历史名单、停止继承访问，启用团队前需清理失效成员。恢复备份后重新登录及签发凭据，核查团队、绑定、历史、幂等键和项目直接恢复负责人。

[交付效能](delivery-metrics.md)按需求版本另行聚合明确的人工观测与渠道等待，展示样本数、缺失值和附件核验。首次业务验收、已登记投入与上线缺陷不从测试计数或告警推断；完整成本、生产缺陷观察窗口与人工收益对照尚未采集。

[上线问题报告](release-feedback.md)允许当前有权的人为仍在使用的旧发布制品补充材料。保留原候选、制品、源码、发布观测和幂等记录，历史缺陷按原版本登记/作废/更正；报告用途不提升为独立质量、发布或运行恢复依据。原制品或报告损坏时保留记录并排除统计；恢复后先核对准确绑定与真实字节。

`/healthz` 检查数据库连接；认证的项目 metrics 提供状态计数和模型费用；审计 SSE 提供事实流。is-runtime 提供持久应用探针；控制面启动时及每15秒检查活动部署，将90秒过期信号记录为持久失联事件，支持人类接手和可信报告恢复，应用故障与信号失联分别处理。控制面停机时无法告警，恢复后按原时间重查。发布节点领取时调度持久渠道查询。企业仍需配置独立进程/卷/数据库告警、证书过期告警和外部应用探针；当前没有完整监控后端、自动外部通知、SLO 告警或完整交付指标聚合。详见 [应用运行](runtime.md)。

服务端应用由 is-runtime 在专用节点执行批准镜像的停旧启新和人工批准回滚，使用同数据契约、单写入者切换；没有灰度、零停机或迁移引擎。未知结果先核对节点日志和真实激活，再附报告核对控制面；已完成收据不能重放成再次切换。网络、数据备份与恢复详见 [应用运行手册](runtime.md)。Windows/macOS 已有签名包校验、不可变下载与签名更新索引，仍需签名安装、客户端自动更新及目标系统验证。iOS/Android 已发布版本通常无法原地“回滚”，应暂停分阶段分发、维护后端兼容、发布修复版本；切勿将源代码回退称为用户设备已回退。

平台自身升级前备份并隔离验证。当前 schema 以创建表为主，尚未建立长期版本迁移框架；生产规模运行需要补齐迁移、保留策略、对象存储、容量与灾备演练。
