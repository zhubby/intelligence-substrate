# Flutter/Go 完整应用开发预览

首批蓝图把真实 Flutter 前端和 Go 后端放进同一个不可变预览包，在工作台体验反馈提交、服务预约和取消。每次完整应用预览使用自己的临时数据；停止、到期或执行环境回收后清除，不连接生产后端或复用生产数据。

预览仍需当前已批准需求、准确仓库/提交和有权上传的 Web 制品。列表区分真正 Worker 构建与人工登记。创建、停止和访问使用[既有预览权限](development-previews.md)，预览交互不会自动形成独立质量证据或发布候选。

## 重复验证完整应用 HTTPS 入口

`scripts/verify-reference-preview-tls.py` 复用真实应用验证器，在新的私有 `work/` 目录内生成两个参考应用，由实际 Worker 构建，经独立节点、Docker 与已提供的 Caddy 模板运行。它为两个本地域名签发短期私有证书；curl 明确验证 CA 与 hostname，Chrome 只信任准确叶证书的 SPKI，并使用进程内 DNS 映射。脚本不修改系统信任、DNS 或既有平台服务。

```sh
rtk proxy python3 scripts/verify-reference-preview-tls.py \
  --caddy /absolute/path/to/caddy \
  --flutter /absolute/path/to/flutter \
  --go /absolute/path/to/go \
  --docker /absolute/path/to/docker \
  --docker-host unix:///absolute/path/to/docker.sock \
  --arch arm64 --output work/tls-run
```

可选 `--tool-wrapper /absolute/path/to/administrator-wrapper.py` 为本机已配置的 Flutter/Go 工具提供环境；`--chrome` 指定已安装 Chrome。保留 Docker 启动路径的文件名，OrbStack 等多调用入口可能依赖 argv[0]。`--output` 必须不存在，且完整路径足够短以满足节点 Unix socket 的 100 字节限制；超长路径在创建目录前被拒绝。

检查覆盖桌面/移动反馈与预约业务、Secure host-only 会话、预览无控制面凭据、准确包/提交响应、保留文件拒绝、实际上游故障、原链接/数据重启恢复及停止后失效。另验证预览资源不能替代独立运行证据登记效能指标。Worker 控制通信仍使用专用 loopback HTTP；这不代表跨主机 Worker TLS 验收。成功只证明当前本机 HTTPS 开发预览拓扑，不证明公开 CA、公网入口、企业部署、正式质量通过或发布。

`result.json` 和 `r/browser-*.json` 保存结构化结果；`r/` 内的配置、数据库、日志及恢复文件含私有凭据，必须按私有诊断数据保管。运行时日志另扫描会话、CSRF 和短期预览链接；对外导出只选择经过脱敏的结果及截图。

## 构建专用包

在已生成并提交的参考应用内，使用已安装的 Flutter、Go 和 Linux 节点架构：

```sh
rtk proxy python3 scripts/build-reference-client.py \
  --application . --target web-preview --preview-arch arm64 \
  --flutter /absolute/path/to/flutter --go /absolute/path/to/go \
  --output artifacts/preview.tar.gz
```

Linux amd64 节点使用 `--preview-arch amd64`。`web-preview` 不接受 `--api-url` 或 `--local-testing`；客户端从当前私有预览路径解析业务 API。脚本执行 pub get、analyze、widget tests，再编译 Flutter 和静态 Linux 后端，固定 `GOOS=linux,CGO_ENABLED=0`。构建本身必须在已授权的 Worker 隔离环境执行，构建脚本不会把任意源码变成可信源码。

专用归档保留静态字节，移除 Flutter `.last_build_id` 和根 base，不修改 build/web 或正常发布包。`substrate-preview/manifest.json` 声明 profile、mode、Linux 架构、后端 SHA256/大小；`substrate-preview/backend` 保存准确二进制，两项均不作为浏览器文件提供。包/展开总量64 MiB、单文件32 MiB、10,000项的既有上限不变。首版支持 `flutter-go-v1` 的 feedback/booking，预约初始化一个演示时段，与真实运营时段无关。

按现有 Web 制品流程上传 `type=package,target=web`，再创建预览。Worker task 的目标仍为 `web`，command 中使用 `--target web-preview`。保留旁路 JSON 清单以核对工具、源输入和字节摘要。普通发布继续使用 `--target web --api-url https://...`，不得借用预览结果满足正式独立质量门槛。

## 准备独立节点

`preview-node` 运行在可信专用执行环境，持有本机 Linux Docker 引擎权限。控制面只访问 Unix socket，不能获得 Docker socket。首版是单节点适配，Linux arm64 已在本机 OrbStack 实测；amd64、其他引擎、企业拓扑和容量需部署验收。

管理员从审查后的平台源码构建可信 relay、节点，并记录 relay SHA256：

```sh
rtk proxy env GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go build -trimpath -ldflags='-s -w' -o work/preview-relay-linux ./cmd/preview-relay
rtk proxy go build -trimpath -o work/preview-node ./cmd/preview-node
rtk proxy shasum -a 256 work/preview-relay-linux
```

替换实际管理员路径/摘要后启动：

```sh
rtk proxy work/preview-node \
  -state /absolute/private/preview-node \
  -relay /absolute/path/to/preview-relay-linux \
  -relay-sha256 APPROVED_RELAY_SHA256 \
  -arch arm64 -docker /absolute/path/to/docker \
  -docker-host unix:///var/run/docker.sock -capacity 10
```

`-state` 必须是私有0700目录，不自动修改现有目录权限。node.sock 权限0600，完整路径至多100字节；不提供 TCP 入口。同一 OS 服务账户及有权管理该目录者属于可信管理边界。任务容器不得访问节点目录、relay 配置或 Docker 引擎。Docker endpoint 只接受本机 Unix socket，Desktop/OrbStack 应填写管理员选定的实际 endpoint。

控制面配置原有独立预览入口，再设置 `IS_PREVIEW_NODE_SOCKET=/absolute/private/preview-node/node.sock`。未配置时静态预览保留，完整包创建返回未配置原因。「已配置」只反映配置存在，连通性和隔离仍需验证。Compose 默认不挂载节点或 Docker socket；企业须单独部署节点，并保证 socket 在节点重启后可访问。

## 业务接口边界

浏览器继续使用 `sandbox=allow-scripts` 的不透明 origin iframe，禁止同源、外网、Worker、表单导航、弹窗和嵌套 frame。子 CSP 只额外允许当前短期 token 下的 `api/`，不允许其他预览或控制面。

网关逐请求核对原人工会话、身份、权限、需求和准确制品。仅开放模式对应的 GET/POST，以及必要的无凭据 OPTIONS：

| 模式 | 路由 |
|---|---|
| 两种模式 | `GET /api/health` |
| feedback | `GET /api/feedback`, `POST /api/feedback` |
| booking | `GET /api/slots`, `POST /api/bookings`, `POST /api/bookings/cancel` |

拒绝查询、编码路径、Cookie、Authorization、升级或非 null 浏览器 Origin。POST 为最多16 KiB的 JSON 对象，有效 JSON 响应最多512 KiB。身份头和上游响应头不转发；API CORS 仅允许不透明 `null` Origin，不允许凭据。节点只接收 Method/Path/Body，可信 relay 固定请求容器自身127.0.0.1:8081。

冷启动后重新核对访问凭据，再执行业务操作。写入结果不明提示刷新核对，平台、节点、relay 均不重试。退出或撤权后拒绝新请求；已经进入执行阶段的操作不能撤回。同一 previewId 内用户共享临时数据，不同 previewId 不共享。

## 生命周期与恢复

每个预览独有 scratch 镜像和容器，只含准确后端和 pin 后的 relay。约束为 network=none、无宿主机端口/目录/设备/socket、只读 rootfs、10001用户、cap-drop ALL、no-new-privileges、64 PID、256 MiB内存、0.5 CPU、16 MiB数据 tmpfs 和4 MiB临时 tmpfs。容器/镜像绑定节点 owner、previewId、binding 和制品标签；回收前精确核对，碰撞资源保持原样。

执行前同步持久化 starting intent，确认后保存 running receipt。结果不明进入 uncertain，ensure 不重放启动。stopped 为终态，必须创建新预览。节点重启读取原 journal，在有效租约内继续使用原容器和临时数据；短暂控制面重启不重建后端。节点状态与 owner 必须一起保留，不得只恢复部分文件或替换 owner。

控制面每15秒核对权威状态并续租。停止或需求失效立即拒绝访问，通常在下一次维护回收资源。控制面失联60秒后租约到期，节点每5秒维护；失败保留 uncertain，同时继续处理其他实例。节点自身不可用时，容器内监督进程按绝对期限退出，最迟不超过预览240分钟上限。磁盘镜像/停止容器须等节点恢复后按标签清理，监督进程不访问 Docker 引擎。

停止/重建容器会丢失临时数据，不适合作为持久开发数据库。平台备份不备份 tmpfs。节点 journal 最多10,000条，达到上限明确拒绝新实例，长期运行须规划节点轮换。多主高可用、状态迁移及敌对租户独立内核隔离不在本次实测范围。

## 复验与验收范围

```sh
rtk proxy go test -race ./internal/webpreview ./internal/previewnode ./internal/platform ./internal/httpapi -run 'Preview|WebBundle'
rtk proxy python3 -m unittest discover -s scripts -p 'test_*.py'
rtk proxy python3 scripts/verify-workbench.py --suite development-previews
rtk proxy go run scripts/verify-reference-previews.go --help
rtk proxy go run scripts/verify-preview-node.go --help
```

真实闭环脚本需新的 work 子目录、Flutter/Go/Docker、Linux架构和明确的本机 engine；可用管理员 `--tool-wrapper` 提供 Python 环境启动器。它创建私有合成项目与审批，不调用模型、不发布。节点脚本使用准确包和 pin 后 relay，验证无维护的绝对期限、真实60秒租约、拒绝续租和回收。两者只操作各自的新 owner，原始 Cookie、链接、记录和日志留在私有 work，失败目录保留。

浏览器业务闭环不能证明独立质量通过、真机可用或正式发布。Apple许可/原生构建、Windows真实运行、企业签名与资质、真实渠道及两个应用全端正式交付继续按[总体缺口](validation.md)验收。

<!-- reference-previews-end -->
