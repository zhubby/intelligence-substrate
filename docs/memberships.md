# 项目成员与委托凭据

项目负责人在「企业治理 → 项目成员」维护企业账户的完整角色集合。项目权限变更与该成员签发的 Agent／Worker 凭据撤销在一个数据库事务内完成。企业单点登录仍使用预先开通的身份；本能力不包含 SCIM、IdP 分组同步、MFA 或账户找回。

## 授权与角色调整

先由平台管理员开通企业账户，再由有权限的人授予项目角色。成员必须对应已注册、启用的身份，项目必须存在。角色可多选，只允许 `owner, product, architect, developer, quality, security, release, viewer`，空列表、重复项和混合类型均拒绝。

```json
{
  "projectId": "project_ID",
  "identityId": "user_ID",
  "roles": ["developer", "release"],
  "expectedRevision": 2
}
```

通过 `POST /api/v1/commands/members.grant` 提交。已有调用可省略 `expectedRevision`；工作台编辑与重新授权始终提交显示的成员版本，冲突返回 409，用户须关闭弹窗、刷新并重新审阅。角色参数是完整替换列表。相同集合的重复授权保留成员版本和现有凭据，角色顺序不影响比较。

角色集合发生变化时，即使仍保留负责人角色，该成员在此项目签发的全部 Agent／Worker token 也立即删除；其他项目及其他签发人的 token 保留。成员的人类登录会话继续有效，随后按新角色判断项目访问。凭据失效后须重新签发，不会因再次升权而恢复。

## 移除与重新授权

[组织团队](organization-teams.md)提供另一种项目角色来源：直接与团队角色合并生效。这里的移除和角色编辑只改变直接成员记录；团队来源仍可能保留访问，需在「组织团队」另行处理。项目所需的最后有效负责人必须是直接授权，继承角色不能替代。

```json
{
  "id": "member_ID",
  "expectedRevision": 2,
  "reason": "职责调整，已完成项目交接"
}
```

通过 `POST /api/v1/commands/members.revoke` 提交。需要人工负责人权限、准确版本和非空原因（不超过 2048 字节），Agent／Worker 不能执行，MCP 不提供此治理动作。

移除后保留同一成员 ID、角色、递增版本、`revokedBy`、`revokedAt` 与 `revocationReason`，状态为 `revoked`。重新授权再次提交 `members.grant`；账户仍需启用，沿用成员 ID 并递增版本，历史移除字段保留作为最近一次事件，完整历次操作见审计。

项目必须保留至少一位已注册且启用的负责人。移除、降权及全局停用均执行此规则；平台管理员也不能绕过。请先给替代负责人授权，再处理离任者。不存在或已停用身份的历史成员不算有效替代负责人。

平台管理员在项目中具有管理员访问权限。移除其项目成员记录不会消除平台管理员权限；全局停用属于独立的企业管理动作，不能停用自己。

## 企业账户停用

`POST /api/v1/identities/disable` 接受 `{"id":"user_ID"}`，需要人工平台管理员。事务内重新读取当前身份，保留 OIDC subject 等现有字段，保护各项目最后负责人，移除所有有效项目成员资格并删除全部会话与委托凭据。每个项目各自产生成员移除审计，另有平台身份停用审计。任一检查或审计失败，整个停用回滚。

本轮没有新增全局停用的工作台入口或复原停用身份的接口。应在企业交接程序中调用既有受保护 API；不能用「重新授权项目成员」复原已停用账户。

## 执行时权限与故障处理

项目角色或团队成员变更还会删除受影响身份在此项目的短期开发预览凭据，其他项目与签发人保留。即使其他来源仍保留访问，也须重新打开预览；重新授权不复活旧链接。每个预览请求再次检查原会话、账户与当前权限，退出登录后也失效。已下载内容无法召回，外层页面定期移除失效应用的边界见[开发预览手册](development-previews.md)。

认证层给 Actor 附加内部凭据引用，该字段不出现在 API、持久 Actor JSON 或协作消息中。业务命令与项目权限检查在事务内重新核对准确凭据、到期时间、当前启用身份、签发人的当前项目所有权或管理员资格，以及 Worker 的当前状态和角色。缓存的认证 Actor 无法在凭据撤销后继续发起受控业务变更。

凭据签发的权限检查、Worker 检查、插入和审计在同一事务中；角色变化或移除与并发签发串行化。SQLite 使用 immediate 事务；PostgreSQL 使用现有事务级 advisory lock。审计失败不会留下无审计凭据，后续升权不会使删除的凭据复活。

在治理页本人失去项目访问权后，工作台重新获取可访问项目、清空旧工作区并切换项目；无项目时显示「等待项目授权」。会话失效时返回登录。其他客户端最迟在下一次请求时收到拒绝，应停止领取新任务并向管理员报告。

已在授权期间派发的原生进程、Git 操作或远程发布不能由凭据撤销追溯召回。不要把 token 删除当成远程执行已经停止：仍需跟踪租约、进程、渠道实际状态和未知结果核对，不能自动重放发布。移除成员不会自动创建新 Worker 或替换正式签名权限。

## 验证边界

本地验证覆盖真实身份／会话／token API、Basic／Bearer、缓存身份拒绝、项目隔离、审计回滚、最后负责人、SQLite 并发、独立 PostgreSQL 连接并发，以及桌面和手机宽度的真实 Chrome 工作台操作。Go 跨平台编译仅证明程序可编译，不等于企业生产、原生客户端、真机或真实渠道验收。

当前仍使用全局事务串行化和基础身份生命周期，企业规模下的容量、延迟、组织目录、审批分权、恢复及合规策略需要独立试点与验收。

## 重现工作台验证

各次命令启动并停止自己的测试服务。为避免多文件集中登录触发正常的每 IP 五分钟十次限制，按文件和浏览器项目分次运行；不要降低生产限流。本轮保留了组合执行失败与隔离重测的真实日志。

```sh
rtk proxy env IS_BROWSER_EXECUTABLE='/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' npm --prefix web run test:e2e -- memberships.spec.ts --project desktop
rtk proxy env IS_BROWSER_EXECUTABLE='/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' npm --prefix web run test:e2e -- memberships.spec.ts --project mobile
```

命令使用已有 Playwright 配置；执行前先构建当前 Web 静态资源。在保留运行中预览时，使用单独 outDir 和配置的 `-static` 目录，不覆盖其静态文件。Go 测试为 `rtk proxy go test -race ./...`；PostgreSQL 场景需要 `IS_TEST_POSTGRES_URL` 指向专用测试数据库，缺失时会明确跳过，不能算完整验收。
