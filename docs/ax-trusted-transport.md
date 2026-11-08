# AX 固定服务身份接入

AX 驱动现在默认要求显式配置的双向 TLS 控制面与客体路由。它使用固定版本上游 protobuf 直接连接，不运行 AX CLI、kubectl 或认证插件，不读取 AX_SERVER、AX_HOME、KUBECONFIG，不使用系统 HTTP 代理，也不使用 Task 返回的 WorkerIP。连接错误不会回退到旧 CLI、直连或明文。

这是执行适配层的身份保护。AX 自身的服务仍没有产品级鉴权，atespace 也不是租户边界。企业必须在 AX API 前提供认证和授权网关、封闭原始服务端口，并为客体路由配置相应的身份与 Actor 访问策略。本增量没有部署或验证企业网关，也不把一个 TLS 地址视为完整的项目隔离方案。

## 管理员配置

参考 [Worker 示例](../deploy/worker.ax.example.json)。`ax.secure.control` 是 AX gRPC 网关，`ax.secure.guest` 是能承载上游 ProcessService 的客体路由。两者分别配置：

| 字段 | 要求 |
|---|---|
| endpoint | 明确的 `host:port` 或 `[IPv6]:port`，不接受 URL、gRPC resolver 或隐式端口 |
| serverName | 证书中明确的 DNS 服务名，不接受通配符配置 |
| serverSPKI | 服务端公钥 SPKI DER 的小写 SHA256；同时仍验证 CA、服务名和有效期 |
| caFile | 该端点独立信任根的 PEM 普通文件，绝对路径 |
| clientCertFile | 用于 clientAuth 的有效客户端 PEM 证书链，绝对路径 |
| clientKeyFile | 与证书匹配的私钥普通文件，绝对路径，权限不得向 group/other 开放 |

证书材料每文件最多 1 MiB，禁止最终文件为符号链接。平台不写系统钥匙串，不生成或续期企业证书。使用 Kubernetes 投射 Secret 等链接文件时，先由节点管理程序把材料安全复制到受控私有普通文件；不能为了通过检查开放私钥权限。修改文件的目录也须由管理员控制。

TLS 最低 1.3，必须协商 HTTP/2。控制面和路由均需验证 Worker 客户端证书；二者可以按企业策略采用不同 CA 和身份。网关须依据证书将允许的 atespace、操作和项目边界绑定起来，不能只检查“证书由某 CA 签发”。直接暴露的未认证 AX API 必须从网络侧封闭。

SPKI 摘要针对公钥，不是整张证书。可对已核实来源的公开服务证书计算：

```sh
rtk proxy openssl x509 -in control-server.pem -pubkey -noout | rtk proxy openssl pkey -pubin -outform DER | rtk proxy openssl dgst -sha256
```

不要从一次未经认证的网络响应自动采用新 pin。替换示例中的镜像摘要、服务地址、pin 和节点私有文件后，再启动专用 Worker。文件路径及任何私钥均不来自项目需求、生成源码或 Agent 动作。

## 执行与记录

每次工具执行先读取材料并建立内存快照，分别认证两个端点。任一认证失败，不登记 sandbox，不发出应用 RPC；工作项仍会保留实际失败。两个连接都准备好后，平台先持久化 sandbox，再发出 UpdateWorkspace、CreateTask 与 ResumeTask。连接准备只做 TLS/gRPC 握手，不执行这些业务写入。

`backend.transport` 的固定公开结构为：

```json
{
  "version": "mtls-v1",
  "control": {
    "endpoint": "ax-gateway.company.example:443",
    "serverName": "ax-gateway.company.example",
    "serverSPKI": "64 lowercase hex characters",
    "caDigest": "64 lowercase hex characters",
    "clientCertDigest": "64 lowercase hex characters"
  },
  "guest": {
    "endpoint": "atenet-router.company.example:443",
    "serverName": "atenet-router.company.example",
    "serverSPKI": "64 lowercase hex characters",
    "caDigest": "64 lowercase hex characters",
    "clientCertDigest": "64 lowercase hex characters"
  }
}
```

此结构说明字段格式，不是可执行配置。`caDigest` 对实际加载的 CA 文件字节计算，`clientCertDigest` 对客户端叶证书 DER 计算。后端整体摘要覆盖两条身份。记录不含凭据正文、私钥摘要或文件路径；业务鉴权仍信任节点正确运行受控适配器。原有四字段记录继续可读，不会被追溯标为已认证。

同一工具生命周期内复用已加载的证书、地址和连接。更改文件、环境或原配置不会把进行中的操作迁移到新后端。后续工具执行重新读取材料并生成新的公开身份；旧未停止客体仍须依据原记录处理。

读取 Task 时核对准确 name/atespace；执行要求 Running、debug=true，且 Actor 名与准确 Task 名相同，符合当前固定版本上游控制器的映射。只向固定 guest endpoint 发送 `ate-target-actor:<atespace>/<name>`，不尝试 WorkerIP。上游 ProcessService 必须明确返回同一 processId、EXITED 及 0–255 的退出码；没有退出消息、断流、错误进程、输出无法保存或取消均不能成功。等待/命令截止仍受原租约和任务控制，未确认命令不自动重放。

## 清理、轮换与撤销

命令结果和客体停止分开判断。始终分别尝试 Task/Workspace 删除，并完整读取该 atespace 的两个列表确认准确资源均不存在。任一删除或列表失败、作用域异常、空 metadata、重复项、残留对象均进入 unresolved。当前策略也不会把 DeleteTask 的 NotFound 当成删除成功，因此部分创建失败可能保守地要求人工核对，即使列表已为空。

处理仍使用[客体恢复流程](ax-sandbox-recovery.md)：先从基础设施侧 fencing 原节点和延迟请求，再停用原平台 Worker，等待租约失效，核实准确资源，记录人工决定。配置摘要和列表不存在不等于物理 Actor、模板或快照已停止/回收。

证书轮换发生在新工具生命周期。已经建立的连接不会因本地文件替换而立即撤销；本实现不提供 CRL/OCSP、企业身份签发或自动轮换。撤权须由网关/路由执行，并在必要时终止连接、隔离节点。丢失客户端身份而无法清理时保持原屏障，不能改用另一套证书把旧客体标为已停止。

## 旧 CLI 的显式诊断路径

历史隔离实验可在节点 JSON 中设置 `ax.allowLegacyCLI:true` 并配置原 binary/context/namespace/atespace/image。环境配置入口需显式 `IS_AX_ALLOW_LEGACY_CLI=true`。此路径保留其原有环境/路由/明文风险，工作台明确显示“旧版 CLI 诊断连接”。

`secure` 与 `allowLegacyCLI:true` 同时配置会被拒绝。没有 secure、也没有显式诊断许可时，不调用 CLI。升级旧节点前先迁移配置；旧示例不会继续悄悄继承宿主连接。生产节点应只使用 secure 模式，并由企业部署策略禁止诊断开关。控制面信任节点，不声称单靠 sandbox API 能远程证明管理员没有启用诊断模式。

## 本地验证与剩余验收

Go 测试启动真实双向 TLS 服务，使用固定上游 AX/Guest protobuf；本地提供方执行实际子命令并保存明确输出/退出码。覆盖 CA、服务名、公钥 pin、客户端身份、文件类型/权限、环境干扰、材料变更、Actor 绑定、断流、取消和清理不确定。提供方是本地契约替身，不是 AX/Substrate 集群；其中 Linux `/bin/true` 映射为本机对应的 `/usr/bin/true`，文件传输使用实际 tar/base64，不能计为 Linux 客体、原生应用或真机验收。

```sh
rtk proxy go test -race -count=1 ./internal/integrations -run 'TestAX'
rtk proxy go test -race -count=1 ./internal/platform ./internal/worker -run 'Test(SandboxTransport|WorkerAXSecure|PostgresSandbox)'
```

PostgreSQL 需要隔离的 IS_TEST_POSTGRES_URL。源码保留 Worker HTTP、双连接竞争、备份恢复和恢复屏障测试；浏览器覆盖两条身份展示与旧记录核对。当前增量不证明真实企业网关授权、租户/Actor 隔离、撤权时效、宿主丢失、存储回收或生产网络配置；这些仍须在企业环境单独验收。Apple 许可、真实多端构建/签名/设备、商店/生产发布和两应用完整正式交付保持独立边界。


## 后续046：可部署项目授权与完整清理观察

现在提供[AX项目授权网关](ax-access-gateway.md)，把客户端叶证书与项目、空间、镜像、control/guest角色绑定，并通过Unix SIGHUP执行同连接重授权与活动RPC取消。网关源码和本地验证不替代真实私有网络命名空间、原始端口封闭和企业证书接入验收。

固定上游ListTasks默认50，045的默认单页列表不能构成完整缺失证明。046已修正为显式limit100分页、重复/越界/后续页错误拒绝，并要求准确GetTask和GetWorkspace均返回NotFound。旧045历史输出保持原字节；当前源码、回归及046报告反映此纠正，清理不明仍为unresolved。
