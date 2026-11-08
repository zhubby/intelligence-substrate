# Kubernetes 与 OrbStack kind 部署

控制面以独立 `intelligence-substrate` 命名空间部署，包含 PostgreSQL 17、平台 Deployment 和两个 PVC。平台保持单实例、Recreate 升级，数据库故障只影响 readiness，不应触发平台反复重启。应用非 root、根文件系统只读、不挂载 Docker socket、不挂载 Kubernetes service account token。初始化容器仅负责将数据卷根目录设为 uid/gid 10001、0700。

这套清单部署本仓库的 Intelligence Substrate Web 控制面；不会重新安装或重置 `ate-system` 的 Agent Substrate 运行时，也不会自动注册未授权的 Agent、模型、签名或发布节点。

## 镜像发布

`.github/workflows/ci.yml` 为 PR 和 push 执行 Go vet/race/PostgreSQL、桌面与移动浏览器、Python 工具、原生容器及备份恢复演练。只有全部检查成功后的非 PR 事件才能向 `ghcr.io/zhubby/intelligence-substrate` 发布 `linux/amd64` 和 `linux/arm64` 镜像。PR 不获得包写权限。

标签包括完整 `sha-<commit>`、分支、Git 标签；默认分支额外更新 `latest`。发布包含 OCI 源码/提交标签、SBOM 与 provenance。部署应记录并使用 `@sha256:...`，不能将可变标签视为不可变版本。私有 GHCR 包需要单独的只读 imagePullSecret；不要将个人 token 写入清单。

镜像内提供全部 `cmd/*` 的 `is-<name>` 程序，入口仍为 `is-platform`。Worker 的 Flutter、Xcode、Android/Windows 工具和私有凭据需要专用受控节点，镜像包含二进制不代表这些外部条件已经具备。

## 首次准备凭据

先确认 Docker context 为 `orbstack`，Kubernetes context 为 `kind-substrate-orbstack`，已有集群和 PVC 完整。以下初始化仅运行一次；已有私有目录会拒绝覆盖，不会轮换现有账户密码。

```sh
python3 - <<'PY'
from pathlib import Path
import json, os, secrets
os.umask(0o077)
root = Path('.secrets/kind-orbstack')
root.mkdir(parents=True, mode=0o700, exist_ok=False)
bootstrap = secrets.token_urlsafe(32)
database = secrets.token_hex(24)
(root / 'bootstrap-password').write_text(bootstrap + '\n')
(root / 'postgres-password').write_text(database + '\n')
secret = {
    'apiVersion': 'v1', 'kind': 'Secret', 'type': 'Opaque',
    'metadata': {'name': 'substrate-secrets', 'namespace': 'intelligence-substrate'},
    'stringData': {
        'bootstrap-password': bootstrap, 'postgres-password': database,
        'database-url': f'postgres://substrate:{database}@substrate-postgres:5432/substrate?sslmode=disable',
    },
}
(root / 'secret.json').write_text(json.dumps(secret))
PY
kubectl --context kind-substrate-orbstack apply -f deploy/kubernetes/namespace.yaml
kubectl --context kind-substrate-orbstack apply -f .secrets/kind-orbstack/secret.json
```

默认管理员为 `admin@substrate.local`。修改管理员或 HTTPS origin 时，在应用前调整 Kustomize 的 ConfigMap；已有账户不会被 bootstrap 再次覆盖。数据库密码与既有数据库必须一致，不能仅修改 Secret 就假定 PostgreSQL 用户口令同步变化。

## kind 与本机 HTTPS

```sh
kubectl --context kind-substrate-orbstack apply --dry-run=server -k deploy/kind
kubectl --context kind-substrate-orbstack apply -k deploy/kind
kubectl --context kind-substrate-orbstack -n intelligence-substrate rollout status statefulset/substrate-postgres --timeout=180s
kubectl --context kind-substrate-orbstack -n intelligence-substrate rollout status deployment/substrate --timeout=180s
docker --context orbstack compose -f deploy/kind/gateway.compose.yaml up -d
curl --fail https://substrate.orb.local/healthz
```

`deploy/kind` 通过 NodePort 30080 向 kind Docker 网络提供 HTTP。网关容器加入现有 `kind` 网络，固定上游为 `substrate-orbstack-control-plane:30080`，不发布宿主机端口；OrbStack 的本机 HTTPS 代理为 `substrate.orb.local` 终止 TLS。网关拒绝其他 Host、不记录访问 URL，并支持 SSE。网关由 Compose 的 restart 策略管理，不依赖临时 `kubectl port-forward` 或当前终端。

访问地址为 **https://substrate.orb.local/**。该地址面向安装 OrbStack 的本机，外部网络访问需企业域名、入口与证书配置。若 OrbStack 尚未信任其本机 CA，使用 OrbStack 自身的证书安装提示；不要关闭浏览器或客户端的 TLS 验证。

本地 ARM kind 要显式构建 `docker build --platform linux/arm64 ...`，避免宿主机 `DOCKER_DEFAULT_PLATFORM` 造成模拟执行。可通过现有 `kind-registry` 发布构建镜像，再在生成的部署清单中把平台、初始化容器及数据库镜像同时固定到实际 digest。私有 registry 的地址映射须事先由 kind 管理员配置，不能擅自重置集群。

## 部署验收与恢复

发布前保存原 Pod/PVC 状态；确认镜像 revision、架构、私有凭据和所有测试结果。部署后检查：

- 所有新 Pod Ready、PVC Bound，原有 `ate-system` 与 Counter 不受影响。
- HTTPS `/healthz`、首页、JS/CSS/字体，以及真实浏览器登录均成功。
- Secure/HttpOnly/SameSite Cookie、匿名访问拒绝、跨源与缺失 CSRF 拒绝。
- 创建测试项目后重启平台与 PostgreSQL，身份、项目、Git/制品及审计仍可核验。
- `is-admin verify` 通过；记录镜像 digest 和实际验证范围。

```sh
kubectl --context kind-substrate-orbstack -n intelligence-substrate get pods,pvc,svc
kubectl --context kind-substrate-orbstack -n intelligence-substrate exec deploy/substrate -- is-admin -data /data verify
kubectl --context kind-substrate-orbstack -n intelligence-substrate logs deploy/substrate --tail=50
```

升级前按[运行手册](operations.md)暂停写入并进行逻辑备份，在隔离目录/数据库恢复验证。回退应用可使用记录的旧 digest 或 `rollout undo`，保留原 PVC；涉及数据变更时须按匹配版本恢复备份，不能把回退镜像当作数据库已经回退。首次部署没有前一业务版本，不删除 PVC 或命名空间来排除故障。

kind 当前是本机单节点评估环境；PVC 不等于异地备份。这次控制面部署不替代真实模型、多端真机、商店签名/审核、HA、压力和完整 P3 正式交付验收。
