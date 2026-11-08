# 企业组织团队与项目权限

在「企业治理 → 组织团队」中，企业管理员维护团队成员，人工项目负责人授予团队项目角色。当前一个平台实例代表一个企业，团队为平级结构；嵌套组、SCIM、IdP 分组同步和多企业隔离需另行验收。

## 权限如何生效

项目有效角色是直接授权与所有启用团队授权的并集。移除直接成员记录不会移除团队继承角色；撤销一个团队授权也不会撤销其他团队或直接授权。工作台「项目成员」显示服务端计算的当前角色及来源，「组织团队」区分当前项目授权和企业团队目录。

团队不能成为平台管理员，不能替代人工批准、独立质量身份或生产发布权限。项目必须始终保留至少一位已注册、启用、直接授权的负责人，团队负责人不计作恢复所需的直接负责人。全局停用账户同样执行该保护。

企业管理员可新建、修订、停用、启用团队。成员必须是已注册、启用的企业身份，每组最多 1000 人，空组有效，重复 ID、未知账户、非字符串或缺失完整名单被拒绝。名称为 1–160 字节，说明最多 4000 字节，变更原因 1–2048 字节。修改采用完整成员列表替换；停用不清除名单、项目绑定和历史。已停用账户保留团队历史，但不获得继承权限；保存或启用团队前需移除失效成员。

负责人明确授权当前团队版本和项目角色，之后团队成员变更动态影响该项目。企业应把成员管理权作为项目授权信任的一部分。目录只显示团队名称、说明、状态、版本及人数，普通项目负责人不能读取全局成员名单或其他项目绑定。团队原始对象和历史仅企业管理员可读。

## 版本化 API

所有写操作经 `POST /api/v1/commands/<command>`，人类会话需要 CSRF。Agent、Worker 和 MCP 不能执行团队治理。`key` 为 1–128 字节稳定请求标识；首次请求前生成，超时后使用完全相同输入重试。相同作用域同 key 不同内容返回 409。准确重放仍检查当前权限，返回资源当前状态，不恢复旧名单或已撤销的授权，也不再次撤销后来签发的凭据；审计仍记录请求。

| 命令 | 输入与条件 |
|---|---|
| `teams.create` | `title,description?,memberIds,key`；人工企业管理员 |
| `teams.update` | `id,expectedRevision,title,description?,memberIds,reason,key`；人工企业管理员 |
| `teams.disable` / `teams.enable` | `id,expectedRevision,reason,key`；人工企业管理员 |
| `teams.grant` | `projectId,teamId,expectedTeamRevision,roles,reason,key`；人工项目负责人；已有绑定还需 `expectedRevision` |
| `teams.revoke` | `id,expectedRevision,reason,key`；人工项目负责人 |

新授权要求当前启用团队和准确版本。角色为 `owner,product,architect,developer,quality,security,release,viewer`，非空且无重复。已有授权的角色集合相同则不增加版本或撤销凭据；每次实际状态或角色变化保存不可覆盖的 `team_grant_revision`。团队实际变化保存 `team_revision`。重新授权沿用原绑定 ID、递增绑定版本，需明确提交当前团队版本。

`GET /api/v1/teams` 需要企业管理员；`GET /api/v1/teams?projectId=<ID>` 需要此项目的人工负责人，可选择目录中的团队。未知、重复、空和编码错误参数返回 400。读取原始团队使用 `GET /api/v1/resources/<teamId>`，仅企业管理员可用。项目工作区返回 `team_grant` 的当前名称、团队状态、版本和启用人数，以及仅对当前请求者计算的 `effectiveForCurrentActor`；项目对象附有 `effectiveRoles`。这些字段只在读取时生成，不保存为共享权限状态。

## 凭据撤销、审计与恢复

团队状态或成员变更、绑定状态或角色变更，在同一 SQL 事务内保存当前对象、不可变历史、幂等键和审计，并撤销受影响签发人在受影响项目的全部 Agent/Worker token。团队成员增删以实际权限变化的成员为范围；绑定变更影响该团队的成员。即使直接角色或其他团队仍使其拥有负责人权限，相关旧委托凭据也会失效。其他项目、签发人及人类会话保留。名称/说明修改及准确重放不撤销凭据。全局停用账户删除全部会话与委托，历史团队名单保留，并在其团队绑定项目记录权限变更。

HTTP、Git、模型调用、任务及命令共用事务内当前身份与角色检查。签发和撤销串行执行，因此并发签发要么被拒绝，要么在该次权限变更中撤销。SQLite immediate 事务和 PostgreSQL advisory lock 目前为全局串行化，尚未验证大企业吞吐。项目审计记录团队权限变化，客户端随后刷新当前角色；旧缓存不能继续授权。

备份包含团队、绑定、不可变历史、身份、请求键和审计；会话及 token 均不包含。恢复后重新登录、重新签发委托，准确旧请求只返回当前记录。审计链校验可发现意外变化，但不能抵抗有数据库管理权的人重写整个记录链。

撤销凭据不能追溯召回已派发的进程或外部发布。继续核对租约、节点和渠道实际结果，未知结果不得自动重放。原生/真机/生产渠道的验收仍需对应环境。

## 本地验证

```sh
rtk proxy go test -race ./internal/platform ./internal/httpapi -run 'OrganizationTeam|Membership' -count=1
rtk proxy python3 scripts/verify-workbench.py --suite organization-teams --suite memberships
```

PostgreSQL 场景需 `IS_TEST_POSTGRES_URL` 指向专用测试数据库；缺少时会跳过，不能计作完整验证。`IS_ORGANIZATION_EVIDENCE_DIR` 可指定新建证据目录以保留双连接并发、备份和恢复数据库供独立读回。浏览器运行器使用临时数据库、独立静态目录及端口，覆盖桌面与 390px，不使用运行中的旧预览、付费模型或真实渠道。
