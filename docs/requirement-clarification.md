# 需求澄清、人工答复与批准快照

在“目标与需求 → 需求基线”提出问题、确认业务答复和查看历史。模型可以辅助提问，业务决定由人确认。

## 工作流程

1. 创建目标，填写业务结果、必选端和预算。初始验收标准可以稍后通过[需求稿](requirement-drafts.md)人工整理或受控模型起草，再审阅采纳；空标准不能批准。
2. 围绕用户、流程、范围、数据、权限和验收提出问题，可附业务影响；每项问题绑定需求版本。
3. 人类产品角色或项目负责人逐项答复，保留原文、确认人和时间。首次答复不能覆盖，更正需变更需求版本。
4. 当前未决问题同时阻止界面和服务端批准。负责人核对业务结果、验收标准与答复的一致性后批准；无问题也可人工批准，不能据此声称已完成智能澄清。
5. 批准保存准确问答快照与 SHA256 摘要。方案生成和开发 Worker 使用同一份快照，排除其他项目/目标、旧版本及未批准的账本变化；结构或摘要损坏时阻塞。
6. 需求变更保留历史，新版本重新确认必要问题。旧问题不自动阻塞新范围，旧答复不自动成为新版本决定；需求稿人审采纳有明确的快照复制与来源记录，见需求稿手册。原计划、执行、质量与发布审批失效规则继续适用。

答复是业务数据，不替代验收标准、测试证据、工具权限或发布批准。可以通过[需求稿](requirement-drafts.md)把答复整理为验收建议，由人编辑采纳并单独批准；生成本身不改变基线。

## 权限与 API

所有路径以 `/api/v1` 开头，沿用认证、当前项目权限、会话 CSRF/Origin 和业务事务内权限复核。工作区返回资源数组。

| 操作 | 主体 | 约束 |
|---|---|---|
| 提问 | product/architect/developer/analysis/design/owner | 当前草稿；项目 Agent 可经 MCP 提问 |
| 答复、辅助生成、基线批准 | 人类 product/owner | 当前权限与需求版本，未决问题必须答复 |
| Worker | 只读取批准问答 | 无人工提问或答复权限，缺失决定应报告阻塞 |

`POST /commands/clarifications.propose`：

```json
{
  "goalId": "goal_...",
  "expectedRequirementRevision": 1,
  "key": "stable-question-request",
  "questions": [{ "question": "谁可以取消已确认预约？", "context": "此决定影响权限与验收。" }]
}
```

每批 1–10 项，当前版本最多 100 项；question 非空，context 可省略，各最多 2000 UTF-8 字节。服务端分配 ID，拒绝 answer 等未知字段。大小写、连续空白归一后，当前版本内不能重复提问。

`POST /commands/clarifications.answer`：

```json
{
  "id": "clarification_...",
  "questionId": "question_...",
  "expectedRequirementRevision": 1,
  "key": "stable-human-answer-request",
  "answer": "客户只能取消本人的预约；员工可取消任何预约并说明原因。"
}
```

answer 非空、最多 12000 字节；key 最多 160 字节。同 key/完整内容读取原资源当前状态，改变内容返回 409；新 key 也不能覆盖已答复问题。权限撤销后不能利用旧 key 回放。资源、幂等和审计同事务提交，审计失败不会留下部分决定。

`goals.approve {id,expectedRequirementRevision?}` 可携带版本，工作台始终携带；不匹配返回 409。approval.data.clarifications 为准确批次快照，clarificationsDigest 按持久化 JSON map 计算，避免结构体键顺序导致恢复后摘要变化。

MCP 的 platform_command 只增加 clarifications.propose。answer 及生成生命周期不在工具白名单，生成开始/完成/失败是内部受信接口。

## 受控模型辅助与费用

`POST /clarifications/generate {goalId,expectedRequirementRevision,key}` 返回问题批次。使用原模型配置、项目外部访问策略、调用间隔、上下文/输出额度及目标预算；未配置返回 424。

网络请求前持久化 clarification_generation，记录发起人、需求版本、当时业务/知识/问答输入及摘要。每个目标最多 200 个生成请求，模型费用另有原调用上限。只有该受信请求允许草稿推理；普通 model/chat 与方案生成仍要求批准，公开 allowDraft 或 clarificationRequestId 字段不能绕过。

模型仅可返回 `{"questions":[{"question":"...","context":"..."}]}`。无效结构、附带答案或命令、不完整输出、版本变化、批准或权限撤销均不写问题，实际费用保留。成功时问题、请求完成、费用关联与审计同事务提交。

| 请求状态 | 含义 | 同 key 再次提交 |
|---|---|---|
| running | 待返回或进程中断后待核对 | 409，不重发 |
| succeeded | 已形成持久问题批次 | 读取原批次，不再次计费 |
| failed | 未形成可用问题，可能已有费用 | 409，不自动重发 |

charge 绑定 clarificationRequestId 与需求版本；配置估算、reserved/uncertain 和人工核对口径见[交付成本](delivery-costs.md)。失败不代表未发送或没有费用，不能自动归零。

同一个确认框保持相同 key。超时或丢回执先查生成与费用记录，核对后如需新请求，关闭旧框并显式重新发起；新 key 可能产生新费用。模型等待不持有数据库事务。running 不会自动转为成功，未提供恢复外部模型结果的接口。

## 恢复与验收边界

问答、批准快照、生成、费用、幂等及审计沿用[一致备份及空目标恢复](operations.md)。PostgreSQL 双客户端测试覆盖首次答复竞争及新问题/批准竞争；真实归档恢复校验准确快照与非空幂等历史。

隔离进程演练确认请求和费用预留持久化后，实际 SIGKILL 自有平台并用原数据重启。旧 key 返回 409，模型只有一次调用，running 请求和 reserved 费用保留，没有自动补写或重发。这证明控制面意图恢复，不能等同模型会话、工具步骤或提供方结果恢复。

浏览器使用真实 API/SQL/权限，但问题、答复和模型为合成样本。没有付费模型，也未验证真实模型提问质量、业务覆盖率或企业人员认可。原预览和旧容器镜像未更新，需另行构建部署；原生端、真机、签名、正式渠道及整体 P3 仍待验收。

## 重现浏览器验收

```sh
rtk proxy npm --prefix web run test:e2e -- --suite clarifications
```

去掉 suite 参数运行全部场景；可重复指定 suite，或用 `--projects desktop mobile` 选择宽度。先安装 web 依赖和 Playwright Chromium，或用 IS_BROWSER_EXECUTABLE 指向本机浏览器。标准运行器 scripts/verify-workbench.py 会构建平台及隔离前端，为每个测试文件/宽度创建新数据库，并为澄清、需求稿和费用场景配置本机模型服务。其他测试不配置模型。报告、截图、构建、配置和合成凭据保留在新的 work 目录；目录不可复用，18082 被占用时拒绝连接旧服务。

CI 通过同一 npm 命令调用该运行器；本地使用 RTK，CI 使用原生命令。测试运行器不读取真实模型密钥，不访问外部提供方，也不会重建正在使用的 web/dist。直接 Playwright 默认配置排除依赖本机模型的三组场景，完整验收应使用上述命令。
