# 控制面容器部署与恢复演练

平台镜像仍使用多阶段构建：Node 编译工作台，Go 编译全部 `cmd/*` 下的 Linux 服务程序，Debian 运行 Git 与控制面。生产 Compose 使用 PostgreSQL 17、uid/gid 10001、只读根文件系统、私有持久卷、临时 `/tmp`、全部 capability 移除及 no-new-privileges。数据库不发布宿主机端口，平台 HTTP 仅绑定 127.0.0.1；企业 HTTPS 入口另行配置。

## 选择镜像和构建网络

```sh
rtk proxy python3 scripts/init-deploy.py --public-url https://substrate.company.example --admin-email admin@company.example
rtk proxy docker compose config --quiet
rtk proxy docker compose build platform
```

初始化拒绝覆盖 `.env` 或 `.secrets`。先在私有 `.env` 中确认域名、管理员、端口、数据库密码和企业允许的镜像来源。

| 变量 | 默认值 | 用途 |
|---|---|---|
| `IS_PLATFORM_IMAGE` | `intelligence-substrate:local` | 构建产物标签或已准备好的平台镜像 |
| `IS_POSTGRES_IMAGE` | `postgres:17-alpine` | PostgreSQL 17 镜像 |
| `IS_NODE_IMAGE` | `node:22-alpine` | 前端构建基础镜像 |
| `IS_GO_IMAGE` | `golang:1.27.3-alpine` | Go 构建基础镜像 |
| `IS_RUNTIME_IMAGE` | `debian:trixie-slim` | 运行基础镜像，需支持现有 apt 安装步骤 |
| `IS_BUILD_GOPROXY` | `https://proxy.golang.org,direct` | 非秘密 Go 依赖代理地址 |
| `IS_BUILD_NPM_REGISTRY` | `https://registry.npmjs.org` | 非秘密 npm registry 地址 |

企业可以显式填写自己的镜像仓库或准确的 `@sha256:...` 引用。基础镜像和依赖源均无静默第三方回退；`go.sum`、npm lockfile integrity 和 TLS 校验继续生效。不要将密码或私有 registry token 放入 build args；需要认证的企业构建另接受控 secret 投射。

如果已有镜像，先确认来源、摘要、Linux 架构与部署主机匹配，再使用 `docker compose up -d --no-build --pull never`。本手册的演练不会执行此企业部署动作。`DOCKER_DEFAULT_PLATFORM` 会影响构建/拉取的平台；在 ARM 主机运行 amd64 镜像属于模拟执行，不能用来证明原生性能或另一个系统的兼容性。

## 运行隔离演练

先通过上述完整 Dockerfile 准备平台镜像，并显式拉取 PostgreSQL 17。演练不会自行构建或拉取。需要本机 Docker/Compose、Python 3、OpenSSL 和 Git；RTK 存在时自动代理子命令，导出的脚本也支持没有 RTK 的 CI 主机。

```sh
rtk proxy docker pull postgres:17-alpine
rtk proxy python3 scripts/verify-deployment.py --work-dir work/my-new-container-drill --platform-image intelligence-substrate:local --postgres-image postgres:17-alpine
```

`--work-dir` 的父目录必须已存在，目标必须不存在；已有目录及符号链接均拒绝。所有操作在新的私有目录、随机 Compose 项目、临时 loopback 端口和独立数据卷内执行。脚本复制初始化器和配置模板，在私有目录生成自己的配置；不会读入仓库 `.env`、启动原有预览、修改 Docker daemon/mirror/auth、访问模型或执行真实发布。

演练使用仓库的真实 `compose.yaml` 和少量临时覆盖，以生产模式运行完整平台镜像。一个仅监听 loopback、校验准确 Host、固定上游的 TLS fixture 提供临时 HTTPS origin。客户端显式信任本次生成的证书，未关闭 TLS 校验。这个 fixture 只用于演练，不能替代企业 Caddy/Ingress、证书管理、流式请求及生产网络验收。

演练依次检查：

1. 私有配置、非 root、只读根目录、可写数据/临时目录、Git/CA、私有数据库和 PostgreSQL 17。
2. HTTPS 登录、Secure/HttpOnly/SameSite Cookie、未认证访问、跨源和缺失 CSRF 拒绝。
3. 实际容器内的工作台首页及打包 JS/CSS，合成项目、需求基线批准与历史记录。
4. 仓库 API 提交、真实 HTTPS Git clone/fetch/push、受保护 main 拒绝，以及制品上传和准确字节读回。
5. 平台与数据库停止/重启后的身份、会话、委托 token、仓库、制品和审计保留。
6. 停止平台写入后，以镜像内 `is-admin` 校验和逻辑备份；备份保留业务/身份，排除会话和 token。
7. 损坏备份中实际制品的正文，验证恢复完整性拒绝、导入事务回滚和新目录清除。
8. 将完整备份恢复到新数据库/新卷，验证 Git、制品、需求与审计，拒绝旧会话/旧 token，重新登录并签发新 token。
9. 清理本次随机项目的容器、网络和卷，并检查剩余资源。任何步骤或清理失败，整体返回非零；结果只包含实际通过的检查点。

需求批准和 API token 都是隔离测试 fixture，不代表批准真实业务方案或生产发布。脚本验证 API、Git 与静态文件，不进行浏览器交互、企业 SSO、真实模型、物理设备、多端签名、商店上架、负载或 HA 验收。

## 结果与私有诊断

`result.json` 记录通过/失败、实际检查点、镜像身份、运行策略、静态/制品/备份摘要和清理结果。`commands.json` 记录成功、失败和超时命令，密码、会话、CSRF 和 token 脱敏；原始备份输出只记录字节数和 SHA256。CI 只上传这两个文件。

**同目录的其他文件是私有诊断资料**，可能包含假账户密码、数据库连接、TLS 私钥、Git askpass token 或含身份口令摘要的备份，不要作为公开 CI artifact 上传。数据卷清理后，这些文件仍保留在 0700 的目录内，便于复核。备份文件为 0600。

失败时先看 `result.json` 的错误、已通过检查点与 `cleanup`，再按 `commands.json` 中的命令序号定位边界。若清理不完整，在所记录的准确 Compose 项目上处理；不要使用全局 prune，不要改用真实项目配置重试。旧目录不可复用，修复后选择另一个全新演练目录。

完整产品仍要求企业环境部署、持续监测、容量/迁移与灾备、可信执行节点、两项不同业务的所有声明端与渠道正式交付。一次本地演练不等于 P3 完成。
