# AX / Substrate 实际执行

平台持有需求、任务、源码与质量的权威状态。AX 负责 Linux 客体的创建与生命周期，可信 Worker 在客体中执行批准命令，并把实际文件与退出码交回平台。AX 的 Running 状态不能证明业务命令成功。

当前默认接入使用[固定服务身份与双向 TLS](ax-trusted-transport.md)：控制面与客体路由分别配置并直接使用上游 gRPC，不继承 CLI 路由。下述已完成的真实集群验收使用较早的显式 CLI 诊断路径；它不证明新增 mTLS 接入已通过企业集群验收。

## 兼容版本

| 组合 | 本轮结果 |
|---|---|
| AX release v0.3.1 + 本机新版 Substrate | 不兼容，旧 `ateapi.Control` 返回 Unimplemented；guest 未启动 |
| AX `2187dd453d60952a4952eb1fc07c81b5721d5680` + 本机 Substrate | 实际创建、执行、暂停/恢复与持久状态重启已验证，附下述路由引导条件 |

Go schema 固定为 `github.com/google/ax v0.3.2-0.20261104182607-2187dd453d60`，对应上述未发布提交。Task 使用单数 `spec.workspace`，没有保留旧 `workspaces[]` 适配。CLI、server、runner 必须按同一提交构建；此结果不覆盖其他版本或升级兼容性。

该上游提交使用 PostgreSQL 和 server 内置 reconciler，不再需要独立 Redis/controller。Substrate API 使用验证 TLS 和短期 token；默认留空模板快照位置，以使用集群配置，不能为通过验证擅自启用 preview 或关闭证书检查。

## 接入要求

管理员预先提供 AX 控制面、Linux WorkerPool、镜像仓库、快照存储和受控的 guest 管理通道。平台驱动不会自动安装它们。Worker JSON 见 [样例](../deploy/worker.ax.example.json)，`image` 必须是实际可获取的 `@sha256:` digest，工具镜像包含同版本 runner、`/bin/sh`、GNU tar、base64 及任务需要的离线工具链。

当前任务声明 2 CPU / 4 GiB。池的可用容量必须容纳该声明及运行开销；已有较小池不等于该任务有执行容量。注册在平台中的 runtime 为 linux，可信质量节点另用 quality/test 身份，不能与开发身份合并。

secure 模式以显式 endpoint、CA、DNS 服务名、公钥 pin 和客户端证书分别固定控制面与客体路由；context 在该模式只是记录标签，namespace 保留部署描述，atespace 指定资源空间。可使用[项目授权网关](ax-access-gateway.md)实施证书到项目/空间/操作的授权；原始端口封闭和真实企业拓扑仍须独立验收，资源空间本身不是权限边界。旧 CLI 仅在 `allowLegacyCLI:true` 下可使用 AX_SERVER、包装程序及 Kubernetes 路由。控制面数据库、模型密钥、签名/发布凭据仅供可信服务使用，不放入 Workspace、Task env/config 或源码。AX 管理入口只向可信 Worker 开放。

此前本机新版 Substrate 的 Worker 仅开放受认证入口，不能把 Worker pod IP 的 80 端口视为可直接访问的 guest。CLI 诊断路径需要 kubectl、受限 kubeconfig、ClusterTrustBundle 读取、路由 port-forward 及证书申请能力。该次安装允许的 ingress 身份为 `ate-system/atenet-client`，而该 AX 提交默认申请 `ax-server` 身份；未处理时 SSH 被 TLS 拒绝。当前 secure 模式不申请这些权限或使用 CLI 缓存，要求管理员另行提供可访问的受认证路由。

本轮可信测试工具由操作员预置短期许可身份的 AX TLS 缓存，未更改路由白名单、未关闭 TLS、未将证书/密钥复制进 guest。它只证明该条件下的实际互通；**上游 CLI 自动引导与企业身份接入尚未验收**。正式接入应由管理员建立可更新的许可身份与最小授权，并再次验证到期/续期、撤权及访问隔离，不能直接复制本轮私密测试缓存。

## 执行、错误与清理

1. Worker 先在平台持久登记准确 sandbox 身份、原执行/租约摘要与后端，提交成功且确认新请求后，创建同名独占 Workspace / Task，申请 resume。准备重放不得执行。
2. 读取持久 Task 状态。Failed / Terminating 立即失败并保留 condition 原因；随后以 guest `/bin/true` 检查管理通道。secure 另核对 name/atespace/debug/准确同名 Actor，只接受同一进程的明确退出回执；RPC 错误只记录公开状态码。显式旧 CLI 保留最多 4 KiB 诊断。
3. 将工作目录作为 tar/base64 分块传输。导入使用 `--no-same-owner`，不把宿主 uid/gid 当作 guest 身份；base64 解码成功后才解包。
4. 在最小环境执行 argv。平台和模型 token 不随环境传入。独立 QA 的 `IS_ARTIFACT_PATH` 指向准确下载制品。
5. 独立传回结果归档并校验路径、文件类型和大小。支持普通文件、二进制、删除与可执行位；拒绝路径逃逸、符号/硬链接及特殊文件。无效结果在替换宿主输入前被拒绝。
6. 返回真实命令退出码，非零不能产生成功制品/通过证据。最后分别删除本次 Task 和 Workspace，并以成功、格式完整且限定空间的列表确认两者不存在；删除或查询不确定时执行失败并持久记录 unresolved。控制器死亡留下 prepared，同目标替代工作受阻，不能靠换方案绕过。负责人隔离原节点并核实准确 Actor/模板/存储后记录决定，详情见 [客体恢复](ax-sandbox-recovery.md)。

源码和结果传输各受 64 MiB 限制，即使平台其他驱动允许更大的制品，AX 也不能绕过该限制。结果文件保留所有者执行位，不承诺保持完整 Unix 权限、属主、时间戳、ACL 或扩展属性。该驱动每条命令创建新客体，Agent 的下一条命令通过回传源码继续，不能假设前一 guest 的内存进程仍然存在。

上游当前采用持久卷快照。suspend/resume 的文件保留与原进程继续是不同验收项，本轮只证明持久文件保留。等待业务审批仍由平台保存状态；不要占住客体等待外部审核。

## 实际验收与重现

本轮隔离证据目录为 `work/ax-live-k51g2ce8`。`lifecycle-result.json` 记录真实非零退出 17、暂停后的数据保留、AX 和 PostgreSQL 重启、恢复及精确删除。第一次数据库重启脚本漏传专用端口造成连接拒绝，随后仅修正本轮数据库配置并恢复；初次失败日志保留，不能把它归因于产品或数据丢失。

`platform-proof/result.json` 的 code/build/test 三次真实 AX 执行绑定同一提交和制品，独立 automated QA 通过；缺少 browser 证据时创建正式版本被拒绝。`cancel-result.json` 记录已启动长命令的实际取消。最终通过 AX 与 Substrate API 分别确认 Task/Workspace、Actor/模板为空，删除测试 atespace、临时 WorkerPool/命名空间/RBAC并停止本轮控制面、Redis和数据库。原部署、counter 池和 18080 预览保持；快照对象存储 GC 未另行验证。

管理员准备可用的一次性执行环境后，可以运行：

```sh
rtk proxy go run scripts/verify-ax.go \
  -config /absolute/path/worker.ax.json \
  -state /absolute/path/new-private-evidence-directory
```

脚本拒绝覆盖已有目录，创建独立本地平台状态和合成批准身份；真实 Worker 通过认证 HTTP、Git、AX 执行固定代码/构建/独立 QA，并检查准确提交与制品绑定。它同时检查二进制往返、删除、执行位、退出 17、拒绝链接后宿主输入保留。脚本不调用模型、不发布应用、不把 automated 结果当作浏览器验收。

本轮是本机单节点 gVisor 验证，尚不证明企业多租户网络隔离、生产高可用、任意依赖工具链、节点丢失迁移、预算/撤权及时终止、原进程续跑或真实模型研发。多端原生构建、真机、签名与正式渠道维持各自验收边界。
