# Agent 双向协作与持久收件手册

更新：2026-11-07。Worker 之间的请求、澄清与回复进入指定工作项的持久上下文，供模型每步使用及同任务恢复。无固定命令的分析、开发和设计工作项可以显式等待准确回复、释放节点，再由可用节点继续；开发和设计保留准确工作区。消息不能批准需求、降低质量门槛、取消执行、生成测试证据或批准发布。

## 使用路径

1. 在同一项目批准需求基线与交付方案，为需要协作的角色注册启用的 Worker 并签发项目凭据。每个 Worker 最多一个活动任务。
2. 调度领取后取得 runId 与 leaseToken。模型上下文的 approvedWork 提供当前方案的 task ID、logicalId、状态、依赖及范围索引，排除被替代方案的任务；待办工作保留指令/命令，已分配或完成的工作不再重复完整执行元数据，当前工作项的完整说明仍在 task 中。collaborators 提供 Worker 或成员 ID。
3. 发送消息时指定收件人和 recipientTaskId。回复保留原消息的 causationId；控制面验证它属于相同项目、目标和需求版本。发送者及需求版本由控制面写入，不信任调用者声明。
4. 模型每次调用前接收当前工作项消息；finish 前再查一次。工作台“Agent 与执行 → 执行记录”的工作项/执行详情可核对协作内容、发送者、需求版本、原消息及首次接收执行。
5. 无固定命令的分析/开发/设计 Agent，发出请求后可返回 wait 动作。分析任务不能已执行工具命令；开发和设计先保存经过校验的工作区。控制面持久记录等待并释放租约，准确回复到达后自动调度可用节点。继续执行使用新模型会话、有界摘要及开发/设计的准确文件，不回放模型步骤或工具。

## 发送契约

Worker 通过认证后的 `POST /api/v1/commands/messages.send` 发送：

```json
{
  "projectId": "project_...",
  "goalId": "goal_...",
  "runId": "run_...",
  "leaseToken": "current-run-lease",
  "recipient": "worker:worker_...",
  "recipientTaskId": "task_...",
  "type": "clarification",
  "key": "stable-request-id",
  "payload": {"question": "时间接口是否统一为 UTC？"}
}
```

回复可使用 type=result，并加 `causationId:message_...`。类型还有 request、progress、blocked、review 和 cancel；这些仅是内容分类。人类或外部 Agent 使用自身认证发送，不需要 Worker 执行租约；人类会话写请求仍需 Origin/CSRF。

收件人必须是项目内有角色的有效成员、启用 Worker，或未到期的项目 Agent token 身份。身份查询出错时拒绝投递；已停用身份不会因仍有成员记录而被接受。只有准确匹配有效等待的回复可以发给已经停用的原请求 Worker：它在同一事务中进入原工作项上下文，由替代节点继续，不恢复停用身份的权限。发送给 Worker 时建议显式指定目标工作项；只有该目标存在唯一活动执行时才能省略。可以预先给尚未执行的当前方案工作项排队，但普通消息仍只有在指定 Worker 实际领取后才会收件，不会指定调度节点。

payload 必须为对象，序列化最多 8 KiB，沿用密钥检查。请只传必要的业务上下文。相同项目/发送者/key 与完全相同请求返回原消息；不同内容冲突。人类准确重放不会因目标任务已完成而重复发消息；Worker 发送及重放均要求原有效租约，失效租约不能继续操作。

## 持久收件与恢复

Worker 调用 `POST /api/v1/commands/messages.receive`，正文为 `{"runId":"run_...","leaseToken":"current-run-lease"}`。只有当前批准方案、当前基线、活动工作项和未过期执行可以接收。响应为 message-inbox：

```json
{
  "kind": "message-inbox",
  "id": "run_...",
  "projectId": "project_...",
  "goalId": "goal_...",
  "data": {
    "items": [],
    "deferred": false,
    "receivedCount": 0,
    "blockedCount": 0
  }
}
```

items 中每条 message_receipt 包含 taskId、planId、requirementRevision、firstRunId、workerId、messageId 和消息快照。首次接收时记录 receipt，并把原消息标记 delivered；二者在同一数据库事务提交。事务失败时原消息仍未交付，重试可以继续。并发接收只生成一份任务/消息记录。

只接收发给当前节点、绑定当前工作项、同目标及同需求版本的待收消息。已经进入上下文的 receipt 属于工作项；同一有效任务在新租约、替代节点或备份恢复后可回放。准确等待回复在唤醒事务中创建 receipt，因此可以由替代节点接续；其他尚未接收的普通消息仍发给原指定 Worker。其他工作项、旧方案、旧基线和全项目消息不会自动注入。

delivered/recorded 表示“已进入工作项上下文”，不表示模型已理解或执行。模型收到之前提供方也可能失败；receipt 仍保留。单次 Agent 循环按 receipt ID 去重，新执行可重新读入历史。执行详情展示的是任务上下文及首次接收 run，不声称该执行处理了全部消息。

## 挂起等待与自动继续

`kind=analysis/code/design`、无固定 command 的工作项适用。模型先以 message 动作发送 request、clarification 或 review，再返回：

```json
{
  "action": "wait",
  "requestMessageId": "message_...",
  "reason": "等待架构协作者确认接口约定",
  "summary": "已核对批准需求，尚待确认 UTC 约定。",
  "timeoutSeconds": 600
}
```

请求必须由本次执行发出，并指向另一个工作项或项目协作者。Worker 拒绝分析任务在已执行命令后等待；code/design 先同步本机准确 tar 并保存控制面的 workspace_checkpoint，再带其 ID 落盘 journal、提交 `tasks.wait`。一次事务创建 message_wait、把 run 标为 suspended、删除租约并把 task 标为 waiting_message。节点可以领取其他工作，等待期间不占活动执行名额。恢复依赖可信 Worker；文件保存不能证明或重放外部副作用。完整选择、集成决定、原始分支绑定及异常边界见[工作区等待](workspace-waits.md)。

回复必须由原指定接收人发出，type 为 result、clarification 或 review，recipient 指回原请求 Worker、recipientTaskId 为准确等待工作项、causationId 为原请求。Worker 回复还须来自原请求指定的接收工作项与同批准方案。progress/cancel、其他人或其他工作项的消息不会唤醒。有效早到回复在登记等待的同一事务中检查；并发和重复投递仅创建一次收件及一次继续机会，后续普通消息仍保留。

等待状态为 waiting → ready → resumed；ready 表示已收到准确回复并等待可用节点，不代表已经运行或完成。expired、cancelled、stale、blocked 保留原因。工作台在工作项/执行详情展示请求、协作者、截止、摘要、回复、继续执行与历史；只有指定人类接收人能在该处回复，项目负责人不能冒充其他接收人。

新执行重新鉴权，使用当前仍有效的批准上下文、首次冻结的仓库/提交（或明确的空源码）、不可信摘要和持久收件。不会恢复原模型对话、reasoning 内容、工具输出或进程；不会自动回放命令。源码后来变化或新建仓库不会替换冻结输入。

每工作项最多等待三次，挂起链累计最多 24 个模型步骤，每次等待 60–86400 秒；reason 为 1–2000 字节，summary 为 1–8000 字节。继续执行扣除以往所有执行的模型费用和活动时间，保留原总预算及 timeout；等待时间不算活动时间，未核对预留费用会阻止继续。挂起本身不消耗失败重试次数，失败/租约过期仍受原重试规则约束。达到限制后阻塞，需要人工决定或新方案，不能自动扩额。

需求改版、方案替换和取消在事务内关闭旧等待；每 15 秒维护、节点轮询和新回复也检查失效/超时。迟到回复不会重新激活过期或取消任务。收件容量和投递次数已用尽时保留消息，明确阻塞。备份恢复保留等待及收件，凭据仍须重新签发。

Worker 的命令入口为 `POST /api/v1/commands/tasks.wait`，也可经 MCP platform_command 调用，输入为 `id,leaseToken,requestMessageId,reason,summary,timeoutSeconds,steps,source`，code/design 额外必填本 run 的 checkpointId；source 必须仅含字符串 repositoryId、commit，两者均为空或属于本项目的实际提交，声明源码必须保留。steps 是挂起链累计步数。原输入准确重放返回原等待，即使它已被唤醒；不同输入冲突，重放仍检查当前凭据与节点权限。租约令牌不出现在读取的等待对象中。

## 容量与异常处理

- 每工作项最多 32 条且序列化 64 KiB 的 receipt，包含来源元数据。超限未收消息保持 pending，deferred=true，Worker 在消耗下一次模型预算之前明确阻塞。不能通过不断重试清空上限；负责人需要按实际内容调整方案或工作项，并明确转发必要上下文。
- 空轮询及只回放历史不追加审计；新收件和超次消息转 blocked 会审计。旧协议未过期租约不抢占，超过三次投递的消息保留 blocked。
- 模型 finish 前会重查，处理模型响应期间到达的回复；最后一次检查之后仍可能有迟到消息。没有原子“排空并完成”保证。负责人应核对未收记录，必要时在有效任务重试或新批准工作项中显式转发；不要将 pending 当作已读。
- 租约、截止时间、需求或方案失效，节点停用或任务不再活动时拒绝消息操作。恢复不能恢复旧权限或旧租约。
- 人类和外部 Agent 保留 messages.claim/ack 的 60 秒租约及三次领取上限。Worker 禁用该旧协议，必须使用任务绑定的 receive。升级自定义 Worker 时需要同步修改调用方式。
- 固定 command 任务不运行模型循环，当前不会自动注入协作消息。构建、测试、安全和发布任务尚无消息挂起；没有消息压缩、模型/工具会话检查点或 A2A 联邦，MCP 仍为受控工具入口。开发/设计的文件检查点不保存进程或旧模型步骤。

## 验证范围

验证使用真实 SQL、认证、HTTP 和实际 Worker 循环，模型返回由本机确定性提供方生成；覆盖请求/挂起/协作者回复/新会话继续及五次调用的逐笔费用，恢复后使用同一工作项剩余预算。PostgreSQL 双连接竞争等待/回复/领取，以及仍在等待时的实际备份恢复另有证据。实际进程强制终止后恢复，并观察 60 秒超时及迟到回复拒绝唤醒。桌面和手机宽度浏览器核对真实 API 的来源、状态、回复权限及错误重试。这些结果不代表真实大模型的协作业务质量、物理手机或 AX 本轮执行验收。

本轮当时的只读预检显示本机安装 google/ax v0.3.1，kind-substrate-orbstack 中没有 ax-system 命名空间；当时使用的 Moonshot 凭据访问官方 models 入口返回 HTTP 401，没有安装 AX、修改集群或记录凭据值。后续实际 AX 的版本兼容、隔离执行与身份引导条件见 [AX 执行手册](ax-execution.md)；后续另一凭据已完成受治理的真实模型业务验收，见 [模型手册](live-model.md)。此收件箱的请求/回复行为仍由确定性提供方测试验证，不把另一项真实生成结果当作真实多 Agent 通信验收。生产 AX 接入、原生构建与设备、签名、正式渠道及企业运行仍需验证。
