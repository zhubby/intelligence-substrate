# 发布前 Web 开发预览

在「代码与评审 → 开发预览」中，从当前仓库、已批准需求版本的 Web 构建包创建一次限时体验。开发人员或有项目 `developer` 权限的 Agent 可以创建、停止；有项目访问权的人工查看者可以打开。预览固定完整 Git 提交、仓库、需求版本、制品 ID、SHA256 和大小，不随分支移动。

## 使用流程

1. 批准需求基线，提交 Web 源码，运行构建并登记 `type=package,target=web` 制品。
2. 构建包采用 tar 或 tar.gz，根目录包含 `index.html`，其他模块、样式和数据使用相对路径，例如 `./assets/main.js`。文件 URL 不支持查询参数，不提供 SPA 路由回退。
3. 在该仓库创建开发预览，填写名称、原因和有效期；默认 60 分钟，可选 5–240 分钟，每项目最多 10 个未到期活动预览。
4. 点击「打开预览」，在当前人工会话内领取短期链接，再在新窗口体验。链接最多 10 分钟，同时受原会话和预览期限限制；每身份最多 20 条未到期链接。
5. 完成后停止预览。需求变化使活动预览失效，已停止历史保留。重新构建或改版后创建新的预览，旧请求重放只返回当前状态。

列表区分「Worker 构建」和「登记制品」。前者必须来自已成功的实际构建执行，输出、生产者和需求版本准确对应；人工登记只证明有权上传者登记了准确字节，不证明平台实际执行过构建。创建预览不会产生独立质量证据、发布候选、部署或业务验收结论。

支持静态 HTML、普通脚本、ES modules、CSS、图片、字体和同包数据请求。首批 Flutter/Go 蓝图还可使用[完整应用专用包与独立执行节点](reference-previews.md)，在各自的临时数据空间执行精确业务 API。任意后端连接、外网、WebSocket、Web Worker、Service Worker、热更新和 IDE 尚不支持。需要独立质量系统执行正式验收，预览交互不自动通过质量门槛。

## 独立入口配置

控制面启动时同时配置 `IS_PREVIEW_LISTEN`、`IS_PREVIEW_URL` 和明确的 `IS_PUBLIC_URL`。留空前两个参数时功能不可打开，工作台显示未配置原因。

本地例子（从已有编译好的控制面启动，静态目录需另行构建）：

```sh
rtk proxy env IS_DEV_MODE=true IS_PUBLIC_URL=http://127.0.0.1:18082 \
IS_PREVIEW_LISTEN=127.0.0.1:18083 IS_PREVIEW_URL=http://localhost:18083 \
go run ./cmd/platform -listen 127.0.0.1:18082 -data work/preview-state -static work/preview-static
```

两个入口须使用不同 hostname，仅更换端口会被拒绝。开发 HTTP 只允许 loopback，生产必须使用 HTTPS。生产示例为 `substrate.company.example` 和 `preview.company.example`；控制面会话 Cookie 必须保持 host-only，不得由反向代理改成父域 Cookie。

Compose 用户可选加载 `deploy/compose.preview.yaml`，设置 `IS_PREVIEW_URL=https://preview.company.example`，再以独立 TLS 入口反代宿主机 loopback 8083。参考 `deploy/preview.Caddyfile`；代理须保留原 Host，不能把此入口转发到控制面 8080。默认 Compose 不开放预览端口。

```sh
rtk proxy docker compose -f compose.yaml -f deploy/compose.preview.yaml config --quiet
```

访问 URL 含短期 bearer 凭据，禁止写入访问日志、错误报告、知识、审计详情或截图。代理/CDN/APM 也须屏蔽完整路径，禁用此入口的缓存和跨域 Cookie 改写；不要在共享终端输出临时 URL。

`deploy/preview.Caddyfile` 是完整配置，包含全局规则。站点的 `log { output discard }` 只关闭访问日志，不能阻止默认 reverse-proxy 错误日志记录请求 URI 和 Referer。示例同时从 `log default` 排除整个 `http.log.access` 命名空间（包括未匹配站点的异常请求），并在 JSON 编码器删除 `request.uri`、全部 `request.headers` 和 `resp_headers`，保留错误、状态、耗时、方法、host 和连接信息；使用 INFO，不能以未验证的 debug/trace/APM 替代此配置。`strict_sni_host on` 对 TLS ServerName 与请求 Host 不一致的请求返回 421。

如果控制面与预览共用一个 Caddy 进程，将这两项全局规则合并到现有唯一的 `{ ... }`，再加入预览站点。不要仅复制站点段，也不要在已有全局块的配置后直接追加第二个全局块。自定义日志器、其他代理层、企业证书和实际发布拓扑仍须在企业环境单独验收。

## 复验 HTTPS 入口

准备已校验的 Caddy、Go、Node、OpenSSL、Chrome 和已安装的 `web/node_modules`。脚本不自动下载或全局安装依赖。示例中的 Caddy 路径须替换为实际已校验二进制，output 必须是尚不存在的 `work/` 子目录：

```sh
rtk proxy python3 scripts/verify-preview-ingress.py \
  --caddy /absolute/path/to/caddy \
  --output work/preview-ingress-local
```

非默认 Chrome 路径可用 `--chrome /absolute/path/to/chrome`。脚本启动独立的生产模式平台、两个同父域测试 hostname 和真实 Caddy TLS 入口；不修改系统 DNS/信任库，不读取仓库 `.env`，不操作现有 Docker/AVD 或 18080 服务。

curl 使用私有 CA 验证完整证书链和 hostname，并确认不相关 CA 被拒绝；Chrome 仅信任本次叶证书的 SPKI，未开启 `ignoreHTTPSErrors` 或全局忽略证书错误。该本机 pin 不证明企业证书已被设备信任。测试覆盖实际五文件应用、1440/390px 交互、Secure/HttpOnly/SameSite Strict/host-only 会话、同父域预览无 Cookie、CSP、绑定头、退出和停止撤销、新会话不复活旧链接、四类 502 请求、异常 Host 421、实际平台重启及原链接恢复。

`result.json` 是不含短期链接的结果；原始日志、CA/叶私钥、Cookie、CSRF 与浏览器诊断均留在 0700 的任务目录，禁止整体上传或分享。验证程序会正常关闭其服务；失败目录保留供核查，不计为通过。脚本不创建独立质量或正式交付证据。

## API 与 Agent

`POST /api/v1/commands/previews.create`：

```json
{
  "artifactId": "artifact_ID",
  "expectedRequirementRevision": 1,
  "title": "预约流程体验",
  "ttlMinutes": 60,
  "reason": "复核当前实现",
  "key": "stable-create-request"
}
```

`POST /api/v1/commands/previews.stop` 接受 `{id,expectedRevision,reason,key}`。创建和停止支持 MCP `platform_command`，仍按当前项目开发权限核对；普通 Worker 凭据不得脱离租约调用这两个命令。修改同一 key 的输入会冲突，旧创建 key 不重新激活已停止、过期或失效预览。

`POST /api/v1/previews/{id}/access` 请求体严格为 `{}`，需要人工登录会话和 CSRF，返回短期 `url`、`expiresAt` 和 `previewId`。Agent token 不能领取人工访问链接。预览记录可经现有 resource/workspace REST 或 MCP 读取，记录不包含访问凭据。

静态入口只有 `/p/<private-token>/`、同路径 `status` 及 `files/<relative-path>` 的 GET/HEAD。配置独立节点且准确包声明受支持运行清单时，才增加当前 token 下受限 `api/` JSON 业务路由；没有平台 API、Git、会话、任意 URL 代理或上传。每次请求返回当前需求版本、完整提交与制品摘要的绑定头，非法 Host、编码路径和查询参数被拒绝。

## 权限、完整性与恢复

预览凭据仅保存哈希，类型独立于平台 session/token，不能作为普通 Bearer/Basic 访问平台。每次请求在事务内检查原人工会话、身份启用状态、当前直接/团队权限、需求版本和制品绑定。退出登录、停用、停止、到期及基线变化后拒绝后续请求；项目角色/团队成员变化会原子删除受影响身份的项目预览凭据，再次授权不会复活旧链接，其他项目与签发人保留。

可信外层页承载 `sandbox=allow-scripts` 的不透明 origin iframe。父 CSP 约束导航范围，子 CSP 只允许同包资源，完整应用包额外限定当前 token 的 `api/`；禁止表单、弹窗、顶层导航、嵌套 frame 和 worker。HTML 只接受 iframe 加载，顶层文件导航被拒绝。静态资源提供无凭据 CORS，不携带平台 Cookie。外层每15秒检查状态，失效后移除应用。已下载字节和已执行操作不能召回；浏览器节流可能推迟界面关闭，服务端仍逐请求拒绝。

包与展开总量各不超过 64 MiB，单文件不超过 32 MiB，最多 10,000 条目。校验登记 size/SHA256 后在内存解析，不解压到文件系统。拒绝链接、设备、重复/隐藏路径、路径逃逸、损坏 gzip、非法尾随数据和缺失入口。网关只缓存最近一个已验证不可变包，缓存命中仍验证权限。若源归档被外部破坏，已缓存的原始正确字节可继续读取；重新启动或切换缓存后会重新验源并拒绝损坏归档。

正常进程重启保留资源、有效原会话与短期链接，仍受各自期限约束。平台备份保留预览资源和原制品，排除全部凭据；恢复后必须重新登录并领取链接。原预览本身已过期或失效时，应从当前有效构建包重新创建。

## 验证与限制

```sh
rtk proxy go test -race ./internal/webpreview ./internal/platform ./internal/httpapi -run 'Preview|WebBundle'
rtk proxy python3 scripts/verify-workbench.py --suite development-previews
```

完整 PostgreSQL 验证须提供独立的 `IS_TEST_POSTGRES_URL`，否则相关测试明确跳过。测试覆盖并发撤权、审计回滚、准确包、恢复、身份隔离、加载/错误和 1440/390px 交互。实际平台/Worker 进程另验证 Git→TypeScript 编译→制品→Chrome→重启→停止，不模拟 API。浏览器和本机 Caddy 路径的证据不代表全部企业浏览器、企业生产反代、容量或任意 Web 应用已经验收。

本增量不关闭总体 P3、Apple 原生许可/构建、Windows 实际执行、物理设备、企业签名、真实渠道上架或企业生产发布的验收缺口。

<!-- development-previews-end -->
