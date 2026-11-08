# 业务目标、需求稿与人工采纳

在“目标与需求 → 需求基线”从业务描述开始。可以先不写验收标准，完成澄清后，人工起草或用模型整理需求稿，审阅、修改并采纳，最后单独批准基线。

## 操作顺序

1. 新建目标，填写业务结果、必选渠道和预算。验收标准可以稍后整理；空标准无法批准或进入开发。
2. 提出并确认当前版本的业务问题。有未决问题时整理和采纳均阻塞，操作见[需求澄清](requirement-clarification.md)。
3. 选择“人工起草需求”或“辅助整理需求”。模型调用计入目标预算，使用项目授权策略；未配置返回424，仍可人工起草。
4. 每份稿展示业务结果、说明、标准、起草来源及引用的人工决定。对话框对照当前标准，允许编辑结果、说明、标准及验证方式，并填写采纳原因。
5. 采纳创建新版本，保留原稿及人审内容，重新等待单独批准。渠道、预算、权限及质量门槛仍由原控制面管理。
6. 上下文变化后旧稿不可采纳，须重新整理。批准后规划和实际Worker读取新说明、标准和批准问答。

采纳会明确复制本稿引用的已确认决定，保留原确认人、时间、原批次和版本；旧记录不变。这是人审采纳的特定操作，普通需求变更不自动继承答复。手工改写范围会解除旧稿绑定，工作台允许同步编辑说明。

## API、权限与快照

路径以 `/api/v1` 开头，沿用认证、事务内当前项目权限、CSRF与Origin。`goals.create` 可省略criteria，存为空数组；明确传入时仍执行原验证。goals.approve强制1–100项有效标准及当前问题全部已答。goals.revise可传expectedRequirementRevision防止旧编辑覆盖，工作台总是传版本。

向 `POST /commands/requirements.propose` 提交：

```json
{
  "goalId": "goal_...",
  "expectedRequirementRevision": 1,
  "key": "stable-proposal-key",
  "content": {
    "outcome": "客户预约并管理本人预约。",
    "brief": "客户选择服务和时段，员工管理可约时段。首版排除在线支付。",
    "criteria": [{"text": "客户不能取消其他客户的预约。", "method": "automated"}]
  }
}
```

product/architect/analysis/design/developer/owner的人或Agent可起草，Worker拒绝，MCP开放propose。只允许当前未批准版本，最多200稿/目标。content严格限定三个字段，outcome最多12000 UTF-8字节，brief最多16000，每项标准最多2000；1–100项，连续空白和大小写规范化后不重复。method须明确为automated/manual/device/simulator。ID由服务端分配，提交ID、budget、targets、答案、证据或批准等未知字段拒绝。key最多160字节。

`POST /commands/requirements.apply` 仅人类product/owner：

```json
{
  "id": "requirement_proposal_...",
  "expectedRequirementRevision": 1,
  "key": "stable-human-adoption-key",
  "reason": "已核对权限边界与首版范围。",
  "content": {
    "outcome": "客户预约并管理本人预约。",
    "brief": "人工审阅：客户预约与本人取消；排除在线支付。",
    "criteria": [{"text": "登录客户不能取消其他客户的预约。", "method": "automated"}]
  }
}
```

reason非空且最多2000字节。content是完整人审内容，须去掉服务端ID，采用时分配新标准ID，返回新goal。同key/完整请求返回该goal当前状态，不重复建版本；不同内容409。撤权后旧key也不能回放。

原稿保留content/input及SHA256，proposed变为applied，保存appliedContent/appliedDigest、采纳人/时间/原因/resultRevision；新requirement版本保存说明及proposal来源。快照包含原目标、标准、说明、渠道、预算、来源、当前已答问答和有效项目知识，使用规范JSON；模型费用变动不影响内容摘要。新问题、答复或知识变化、版本变化、批准和摘要不符均阻止采纳。两份稿并发采纳只能一份成功。资源、历史、幂等和审计同事务提交。

## 模型生成、费用与恢复

`POST /requirements/generate {goalId,expectedRequirementRevision,key}` 仅人类product/owner，返回待审阅稿。在网络前保存requirement_generation意图及准确输入，最多200意图/目标。沿用128KB上下文、输出额度、调用间隔、项目策略、目标预算和调用次数限制。

charge关联requirementRequestId和需求版本。完整、费用可核实的JSON结果才能形成稿；稿、费用链接、请求完成和审计原子提交。格式错误、截断、网络异常、撤权、需求或知识变化不补写旧稿，实际费用保留，见[交付成本](delivery-costs.md)。

| 状态 | 含义 | 同key重放 |
|---|---|---|
| running | 待返回或进程中断后待核对 | 409，不重发 |
| succeeded | 持久稿已形成 | 返回原稿当前状态 |
| failed | 未形成有效稿，可能已有费用 | 409，不重发 |

每个确认框保持固定key。失败先核对请求和费用，再显式发起新请求；新key可能产生新费用。普通model/chat、计划生成和公开allowDraft/requirementRequestId不能选择草稿推理。生成生命周期为内部受信接口，命令和MCP不开放。

本机提供方实际收到请求后SIGKILL自有测试平台，原数据重启，同key409；仅一次提供方请求，running意图与reserved费用保留，没有稿。这不恢复提供方结果、模型会话或工具步骤。备份恢复见[操作手册](operations.md)；专用PostgreSQL覆盖跨连接采纳竞争、原稿/人审内容/答复与非空幂等历史恢复。

## 重现与边界

```sh
rtk proxy npm --prefix web run test:e2e -- --suite requirement-drafts
```

运行器为每个文件/宽度建立独立数据和服务，仅本机确定性模型，不修改原18080和web/dist。覆盖纯业务描述、空标准门禁、问答、模型稿、人审编辑、采纳、批准、失败转人工和同版本上下文变化拒绝。手机宽度不是原生端或真机证据。

当前验收证明技术流程，尚未验证真实模型需求完整性、业务正确性和企业人员认可。人必须核对说明、答复和标准的一致性，实现仍需独立质量验收。原生端、真机、签名、正式渠道、企业生产及两个应用完整交付继续开放。
