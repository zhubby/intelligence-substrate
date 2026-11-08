# AX 项目授权网关

网关将已验证的 Worker 证书绑定到项目、专用 atespace、固定镜像和操作角色。它与[Worker 双向 TLS 接入](ax-trusted-transport.md)共同形成受控入口；平台仍负责需求、租约、预算、批准、独立质量和持久 sandbox 归属。

## 部署结构

```text
可信 Worker
  ├─ TLS1.3/h2 + client certificate → control listener :8443
  │                                  └─ 同一私有网络命名空间的 loopback h2c → 原生 AX
  └─ TLS1.3/h2 + client certificate → guest listener :8444
                                     ├─ 精确读取 AX Task 验证 Actor
                                     └─ 固定 CA/DNS/SPKI + client certificate → Guest 路由
```

原生 AX 固定版本使用 h2c。管理员必须令 AX 仅监听同一受信 Pod/私有网络命名空间内的数值 loopback 地址，并封闭所有原始控制端口和可绕过网关的 Guest 入口。不要使用 hostNetwork，也不要让其他项目或不可信工作负载进入该命名空间。`privateNetworkNamespace:true` 是管理员对这些条件的明确声明，程序只能检查数值 loopback，不能证明部署拓扑或阻止节点管理员访问。

Guest 上游必须已具备可达的认证路由。网关不申请 Kubernetes 凭据、不读取 AX 缓存，也不从 Task WorkerIP、环境变量或代理推导地址；没有远程明文或失败回退。两条上游连接准备完成后才提供业务 RPC。TLS 材料在启动时固定，改变服务 CA、证书或 pin 需要受控重启与重新验收。

## 安装与配置

```sh
rtk proxy go build -trimpath -o bin/ax-gateway ./cmd/ax-gateway
rtk proxy bin/ax-gateway -config /etc/intelligence-substrate/ax-gateway/config.json
```

管理员根据[网关配置样例](../deploy/ax-gateway.example.json)和[默认拒绝策略](../deploy/ax-gateway-policy.example.json)准备真实配置。样例的全零 Guest SPKI 是占位值，不能用于实际连接。两个数值监听地址必须不同；端口范围1–65535，配置不接受动态端口、DNS监听或默认环境路由。推荐控制/客体分别使用独立CA、服务证书与Worker证书。

配置与策略文件必须是绝对路径、普通文件、最多1 MiB、禁止group/other权限，最后一级不能是符号链接；JSON拒绝未知字段、重复字段、超深结构和尾随文档。私钥同样要求私有权限，证书与CA为受控的普通文件。父目录必须由管理员保护，避免材料被非授权进程替换。网关不负责证书签发或节点隔离。

审计父目录预先提供，建议0700。审计文件以0600新建或打开现有私有普通文件，拒绝符号链接和与已加载配置/策略/凭据文件的硬链接别名。每次准入和策略变更先追加JSON并sync；写入失败后拒绝新请求并取消已有请求。审计包含时间、策略revision、准入/拒绝、方法、主体/项目、合法资源作用域和公开证书摘要，不保存命令、数据、私钥或原请求头。它是节点上的追加日志，没有防管理员篡改、远程持久化或平台审计链保证；企业需接入日志保留与告警。admitted仅表示通过准入，不证明上游完成。

## 证书到项目的策略

`certificateSHA256` 是客户端**叶证书 DER**的SHA256小写十六进制；`serverSPKI` 是服务端**公钥 SPKI DER**的SHA256，两者用途不同。策略由管理员根据平台真实project ID配置，例如：

```json
{
  "version": 1,
  "revision": 2,
  "principals": [{
    "id": "project-one-control-worker",
    "projectId": "project_012345678901234567890123",
    "atespace": "project-one",
    "certificateSHA256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "control": true,
    "guest": false,
    "images": ["registry.example/runner@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"]
  }]
}
```

示例摘要和ID均需替换。另为 Guest Worker证书登记guest:true主体；同一项目可有多个不同证书，分别授予control、guest或两者。一个证书只能绑定一个项目/atespace，一个项目只使用一个专用空间，空间不能共享给其他项目。没有通配符。空principals表示全部拒绝；策略最多4096主体，每个主体1–32个不同固定digest镜像。项目ID、空间名和资源名格式受校验。

control角色允许Get/List/Create/Delete/Suspend/Resume Task和Get/List/Update/Delete Workspace。其他Model/管理方法不暴露。仅管理`is-sandbox-`加24位小写hex名称；不能无作用域列举。创建Task限固定许可镜像、debug、`/bin/true`就绪、同名`/workspace`、CPU2/memory4Gi，无env/config/requests，Workspace spec为空。创建返回的spec还必须与请求完全一致。按业务改变这些限制需要修改并验证执行契约。

guest角色仅允许StartProcess和StreamProcessOutput。每次请求恰好一个`ate-target-actor: atespace/name`，网关从控制面独立读取准确Running/debug Task，验证同名Actor、空间、镜像与工作区后才转发。原客户端凭据/元数据不成为上游权限，只重建这个Actor头。cwd/env/stdin、未知protobuf字段、非法进程状态及错误完成绑定被拒绝。控制RPC限30秒，Guest RPC最多30分钟，工具自身仍受平台预算和超时约束。完成必须有同一processId的EXITED及0–255退出码；EOF不算成功。

## 撤权与恢复

Unix/Linux使用SIGHUP读取**固定policyFile**。先以私有临时普通文件准备更高revision的完整策略，再原子替换到固定路径，向网关PID发送SIGHUP。不要同时重载多份相互矛盾的策略。

```sh
rtk proxy kill -HUP <gateway-pid>
```

成功报告`policy.accepted`；所有旧generation调用会取消，已有TLS连接的下一次RPC按新策略判断。移除证书可立即阻止该连接继续准入。读取失败、无效/重复字段、空文件、过大策略或revision相对上次成功安装值不严格增加时报告`policy.denied`，全部拒绝并取消旧调用；修正为更高revision后可恢复。主进程有界关闭，终止信号只取消请求和关闭连接，不删除远端资源。

Windows可以编译命令，当前没有SIGHUP重载入口；生产推荐Unix/Linux。若在Windows试验，撤权需停止进程再受控重启，没有在线撤权验收。revision只在进程内单调，重启后的配置回滚防护依赖企业配置管理。本轮未实现CRL/OCSP、跨副本撤权共识或证书自动轮换。

取消或撤权不能撤回已执行的写入，也不证明外部进程已停止。网关从不重试/补偿业务变更；响应丢失仍是不确定结果，按[持久客体核对](ax-sandbox-recovery.md)保留unresolved并阻止替代执行。策略故障修复前，清理和核对同样可能被拒绝，必须由授权负责人处理。

## Worker 迁移和清理证明

将Worker的`secure.control`和`secure.guest`分别指向两个网关入口，配上各自CA、DNS、SPKI和Worker证书；不使用原始AX地址。关闭allowLegacyCLI，重新构建Worker，先在隔离项目验证允许/跨项目拒绝、真实命令、撤权、响应丢失与清理，再承载生产任务。只更新JSON不能替代原始端口封闭。

固定AX的ListTasks默认只返回50。当前Worker显式按limit100/offset0、100…枚举，最多10000项，拒绝重复、越界、错误作用域、后续页错误和不完整观测；再独立要求准确GetTask和GetWorkspace返回NotFound。网关将limit0规范到100，仅接受limit100及0–9900的百位offset。ListWorkspaces无上游分页字段，逐项验证。两个删除、列表和精确查询的结果彼此独立，任一不明均不能证明清理完成。不能把单页缺失计为资源停止。

## 验收边界

本地测试使用实际TLS1.3/h2、固定上游protobuf、独立网关/平台/Worker进程和真实子命令。控制上游为显式本地h2c协议桥，后端是本地测试提供方；这不等于原生AX/Substrate、容器隔离或企业网络命名空间验收。接口契约测试、交叉编译、模拟数据分别记录。

正式部署仍需验证企业证书签发/轮换、原始端口封闭、项目隔离、拒绝旁路、真实节点故障与物理fencing、存储GC、容量/灾备。原生设备、签名、商店、生产和两应用所有必选端/渠道的P3要求继续开放。本轮没有修改真实集群、调用付费模型或执行实际商店/生产发布。
