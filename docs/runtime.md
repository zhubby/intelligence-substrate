# 应用运行、持续监测与批准回滚

`is-runtime` 在专用可信节点运行已批准的单个 Linux HTTP 应用。控制面记录实际部署、健康信号、事件、回滚批准和独立运行验收；独立只读网关提供应用流量。它补齐 Web 后端的受控运行路径，当前采用单节点、停旧启新的部署方式。

## 配置与部署

构建 `cmd/runtime`，以管理员身份安装到生成代码不可写的路径。使用 [runtime.example.json](../deploy/runtime.example.json)、[worker.runtime.example.json](../deploy/worker.runtime.example.json)，替换实际项目、目标、域名、路径和持久化数据契约。每个配置和 stateDir 只属于一个应用/交付目标；使用专用 release + publish 节点，不兼任开发或独立质量。

`instanceId` 初次部署时生成并固定，备份恢复时保持不变。它与项目/目标/appId 一起决定 Docker 资源归属；不得给两个同时运行的实例复制同一身份。控制面阻止现有应用普通发布更换 worker；迁移需要独立的停写、备份、恢复、核对和入口切换流程。

```sh
rtk proxy go build -trimpath -o bin/is-runtime ./cmd/runtime
rtk proxy ./bin/is-runtime -config /etc/substrate/runtime.json -mode init
rtk proxy env IS_PLATFORM_URL=https://substrate.company.example IS_WORKER_TOKEN_FILE=/run/secrets/feedback-release-worker-token ./bin/is-worker -config /etc/substrate/worker.runtime.json
```

单独启动网关和监测进程，使用服务管理器持久运行：

```sh
rtk proxy ./bin/is-runtime -config /etc/substrate/runtime-gateway.json -mode gateway -listen 127.0.0.1:8091
rtk proxy ./bin/is-runtime -config /etc/substrate/runtime.json -mode monitor
```

gateway 的配置复制固定应用字段，移除 environmentFile/platformUrl/workerTokenFile；其 OS 身份只允许读取运行数据库及 SQLite 必要的配套文件，不得读取 Docker socket、环境密钥和 worker 凭据。代码以只读数据库模式运行，网关不执行 Docker。只读代码模式不能替代 OS 文件权限隔离。

应用网关仅监听 loopback，经 [runtime.Caddyfile](../deploy/runtime.Caddyfile) 的独立 HTTPS origin 暴露。不能与平台会话共用 origin 或扩大 Cookie Domain。生产节点使用本地 Unix Docker socket；Docker 管理权限只属于可信控制器，不能安装在不可信开发/构建节点上。平台容器镜像包含此二进制，但不包含 Docker CLI，也不会自动挂载宿主 socket；推荐在单独运行节点部署这些服务。

**网络前提：网关和监测进程必须能路由到 Docker 内部网络的私有容器 IP。**应用没有发布宿主端口。Linux 本机 Docker 可按此结构部署；本次在 OrbStack 实际验证。未验证其他 Docker Desktop 的宿主路由能力，不可假设所有 macOS/Windows Docker 环境适用。需要先在目标部署环境验证网络；不能为绕过网络问题去掉 internal 隔离。

## 镜像、数据与权限

发布制品是 `docker image save` 生成的 tar 或 gzip，恰好包含一个 Linux 镜像，最大归档/展开大小均为 512 MiB，最多 20,000 条目。运行节点先保存不可变快照并验证批准的 SHA256/size，再解析、去除外部 tag/index 数据、导入未标记镜像并验证实际 image ID，避免覆盖其他应用标签。拒绝路径逃逸、符号链接、特殊或重复条目及未受控匿名卷。

镜像必须声明：

```dockerfile
LABEL org.intelligence-substrate.data-contract=feedback-v1
# /data 内容由实际应用提供，并预先归属运行 UID。
COPY --chown=10001:10001 data /data
USER 10001:10001
```

标签值必须等于管理员配置的 dataContract。持久化启用时，只允许 `/data` 卷；关闭时不能声明镜像卷。运行时强制 UID/GID 10001、只读根、cap-drop ALL、no-new-privileges、128 PID、显式 CPU/内存和日志限制，提供受限 tmpfs。容器使用专属 Docker internal 网络、无宿主发布端口、无宿主目录或 Docker socket。环境文件只在创建容器时注入应用所需的配置，不传平台 token。管理员必须确保其中没有平台或 Docker 管理凭据。

持久卷名为 `isrt-{owner}-data`，owner 由稳定实例身份计算。第一次创建时依靠镜像中的目录权限初始化；平台不执行自动 chown 或数据迁移。切换前先确认旧写入者停止，再启动候选并检查固定健康路径。失败时停止候选、恢复旧版本、验证并更新真实私有地址；恢复不明确则锁住切换。

**只能部署经过验证、前后版本数据兼容的应用。**相同 dataContract 标签是受控声明，并不证明代码兼容。候选健康失败前可能已写入数据，回退镜像不会撤销这些写入；发布前必须由独立质量验证升级/降级兼容性，保留数据备份。本实现没有数据库迁移、灰度流量、零停机、数据库写入回滚或自动高可用。

## 协议与模式

publish/status 使用 [Worker 协议 v1](workers.md) 的完整绑定。回滚额外包含 expectedActive（批准时的当前激活）和 rollbackTo（准确历史激活），操作 key 对应全新 activationId。三个模式仅接受相同 action，不会将查询或回滚回退为发布。

成功结果示例：

```json
{"externalId":"runtime:customer-feedback:<operation-key>","state":"published","url":"https://feedback.company.example","runtime":{"driver":"docker","application":"customer-feedback","activationId":"runtime:customer-feedback:<operation-key>","imageId":"sha256:<image-config-digest>","dataContract":"feedback-v1"}}
```

控制面据可信发布完成结果登记 deployment，并将原运行版本标记 superseded。普通发布不能静默改变已有应用、worker、数据契约或 URL。回滚始终产生新激活和待完成的独立运行验收，不复用历史通过记录。

| 模式 | 行为 |
|---|---|
| init | 初始化归属固定的私有 SQLite 状态 |
| publish | 校验制品、持久保留操作、切换并返回准确收据 |
| status | 查询已登记激活；被替代时 halted，不重新部署 |
| rollback | 核对准确当前版本与历史目标，重新激活历史镜像 |
| gateway | 只读当前路由，不读取凭据或调用 Docker |
| monitor | 持久采样并向控制面报告；`-once` 执行一轮 |
| inspect | `-operation-key KEY`，只读检查操作日志 |
| reconcile | `-operation-key KEY -keep previous\|candidate`，管理员明确核对并恢复选定版本 |

网关覆盖响应中的 `X-Substrate-Release-ID`、`X-Substrate-Artifact-SHA256`、`X-Substrate-Activation-ID`，独立验证应同时检查这些字段与真实业务结果。`/__substrate/health` 仅表明有活动路由和网关可读，不代表后端或业务健康。

## 监测与独立验收

monitor 每 30 秒执行一次固定 HTTP 健康探针，单次超时 3 秒，不跟随重定向，不采用环境代理。SQLite 保存单调序号，重启后继续递增；最多保留最近 10,000 条样本，平台暂时不可达时保存最新待发送样本。恢复后只发送当前激活的最新检查，丢弃历史积压，不能把监测空窗补记为连续可用。它不是完整时序数据库或 SLO 数据源。

报告使用原可信发布节点的凭据，绑定准确 externalId/releaseId/binding/sequence。控制面拒绝停用节点、其他项目/激活、旧序号、冲突重复、抵达时已过90秒有效期或超出容忍范围的未来时间；较大序号也不能带入不前进的检查时间。3 次连续失败形成 `application_unhealthy` 应用事件，2 次连续成功关闭事件；信号 90 秒过期后显示未知。旧的2分钟接收窗口已收紧，队列中已过期样本不能关闭当前事件。

控制面在启动时及每15秒独立扫描活动部署，不依赖 worker 领取任务或发送报告。最后信号到期，或首次激活后90秒仍未收到报告时，持久创建 `monitor_signal_lost` 事件；应用故障与监测失联事件可以同时存在。同项目、目标、应用、节点和类型的未关闭事件不重复创建，重启保留记录。新部署本身不能消除已有事件。

新接收的可信检查关闭监测失联事件，即使它报告应用不健康；应用故障仍需两次连续健康检查。监测空窗重置连续计数。若空窗在新报告抵达时才被发现，会保留一条已恢复的失联记录，不补造历史可用性。事件打开/恢复均有系统审计，保留原激活、制品/提交、触发序号、信号截止和恢复部署/序号。

工作台“构建与发布 → 应用运行”显示活动事件与可展开的恢复历史。人类 release/quality/owner 可以“确认接手”，填写处理说明；同一表单重试使用相同幂等键，首次接手后不能覆盖。接手不会关闭事件、改变健康、通过独立验收或批准回滚。恢复保留处理人、时间与说明，不能新接手已恢复事件。Agent/worker、viewer、跨项目和失权身份均不能接手。

已认证会话可按现有 CSRF/Origin 规则向 `POST /api/v1/commands/runtime.incident.acknowledge` 提交：

```json
{"id":"incident_…","key":"one-investigation-id","note":"已接手检查监测进程与连接，恢复后继续观察业务健康。"}
```

90秒是信号有效期，定时发现通常另有至多一个15秒扫描周期；控制面停机时无法告警，恢复后立即重查，扫描错误记录服务日志。生产仍需独立外部探针、证书/卷/进程告警及通知渠道；本实现不自动发送邮件或消息，也不能据本地采样宣称 SLO。

需求基线改变后，已部署软件的监测、批准的运维回滚及其证据仍可继续。运行验收与新需求的业务验收分开，不能使新需求自动通过。独立质量人员上传本次激活后的同提交 Web 报告并调用 runtime.verify；制品生产者、发布/回滚批准者和执行者不能验收自己的部署。若新需求已移除 Web，上传报告必须额外填写准确 deploymentId 和历史源仓库；此例外仅适用于运行报告，不能上传新发布包绕过基线。

## 回滚与未知结果处理

1. 开发/架构/发布角色提出当前版本至准确历史版本的回滚建议，说明原因；平台检查同项目、目标、应用、节点和数据契约。
2. 人类发布角色批准，且不能是目标制品生产者。批准绑定准确源、目标和制品，只排队一次、没有自动重试。
3. 独立配置的 rollbackProviders 执行；原发布程序不会成为兜底。切换成功后监测新激活，独立质量重新验收。
4. 租约丢失、超时或不明失败进入 reconciliation_required，同时阻止后续发布/回滚；通用 tasks.retry/cancel 不能绕过。

需要核对时有两层状态。先暂停该节点的发布/回滚 worker，确认没有仍在执行的切换进程，再检查节点并更新控制面；不能并发执行多个 reconcile：

```sh
rtk proxy ./bin/is-runtime -config /etc/substrate/runtime.json -mode inspect -operation-key OPERATION_KEY
rtk proxy ./bin/is-runtime -config /etc/substrate/runtime.json -mode reconcile -operation-key OPERATION_KEY -keep candidate
```

`-keep previous` 明确恢复原版本；`-keep candidate` 明确保留候选。操作没有 switch intent 时只能确认没有切换并保留原状态；首发没有 previous 时，停止未明候选后需管理员检查，不能猜测成功。候选/原版本再次启动后都需要健康检查，路由刷新不会改写同一激活的历史。

然后上传真实核对报告，在工作台或 runtime.rollback.reconcile 确认 succeeded/failed。成功必须核对新 activationId 和目标制品；失败必须确认原版本仍运行。控制面核对仅记录事实和释放旧任务租约，不替代节点检查。普通发布不明结果使用 releases.reconcile，恢复后的独立验收仍需另做。已完成操作重放只返回原收据，不重新激活旧版本；未明操作一律不自动重放。

## 备份与验收

控制面备份不包含独立运行节点。暂停发布/回滚和监测写入，停止应用写入或使用应用一致性快照，协调备份 runtime state、完整镜像/归档、持久卷和独立保管的环境/节点凭据；不要只复制正在写入的 runtime.db。恢复保留 instanceId，先停旧实例，避免双写同一数据；确认镜像摘要、卷权限、操作日志、当前激活与控制面一致后再切入口。禁止把旧数据库恢复直接等同于外部状态已核对。应用备份恢复/主机灾难演练尚需部署方实测。

```sh
rtk proxy go test -race ./internal/runtimehost ./internal/platform ./internal/worker
rtk proxy env IS_BROWSER_EXECUTABLE='/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' node scripts/verify-runtime.mjs
```

实际验收脚本需要当前 bin/is-platform、is-worker、is-runtime、Docker、Go、Node、OpenSSL 和 Playwright/Chrome。它离线构建 scratch 镜像，实际执行 v1/v2 写入、失败探针、批准回滚、两条反馈保留、版本标识、网关重启及 Chrome 检查；确认非 root/隔离/资源限制。脚本用测试身份和合成的发布前门禁，不代表生产人工批准、真实模型开发、正式公网发布、移动/桌面真机或商店交付。结束后停止本次进程/容器，保留本次镜像、卷和 work 报告，不清理其他 Docker 资源。
