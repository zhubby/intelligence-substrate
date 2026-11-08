# 设计、原型与方案批准

设计保存交互与架构决定、关联验收项，以及准确的原型、仓库和源提交。**设计提案随人工交付方案批准后成为执行依据。**本能力属于 P2 增量；完整 P3 仍需两个不同业务应用完成全部声明目标与渠道的真实交付。

## 工作台操作

1. 完成需求澄清并批准当前基线。
2. 如需原型，在“代码与评审”的制品入口上传独立 HTML 文件，关联本目标、仓库、完整提交 SHA 和必选端。原型最多 2 MiB。预览在受限子框架中运行内联脚本，外层浏览器策略阻止页面导航、fetch 和外部资源加载；子框架不能访问工作台会话、提交表单或导航主页面。该浏览器沙箱不替代企业网络出口隔离。
3. 在“交付方案 → 设计与原型”提交设计名称、说明、关键决定、原因，选择当前验收项和已有原型。每次生成新版本；修改需再次提交，历史内容保留。
4. 在“规划采用的设计”选择具体版本，再人工编制或生成方案。未显式选择时沿用当前批准方案中的设计（如有）。模型得到准确选择快照，不能自行更换。
5. 架构负责人审阅方案、设计决定及原型，一并批准。批准事务再次检查设计摘要和原型实际字节，然后创建工作项并保存批准快照。
6. 改变已批准设计时，提交新设计和新方案，人工批准替换。仅提交提案不会打断当前执行；批准替换后，旧工作项/执行、质量策略和发布候选按既有规则失效。历史证据保留，新方案需要自己的有效质量门槛。

三类人工决策仍为需求基线、重大方案、正式发布，不新增单独的设计审批。较早提交且没有设计绑定的方案，不能覆盖后来已批准的设计，需重新编制以继承当前依据。需求变更使旧设计成为历史记录，新基线不继承旧版本设计。

## API 与幂等

`POST /api/v1/commands/designs.propose`：

```json
{
  "goalId": "goal_example",
  "expectedRequirementRevision": 1,
  "key": "one-reviewed-proposal",
  "title": "客户预约交互",
  "summary": "预约列表、详情和取消确认",
  "decisions": "先展示状态，取消前明确确认；不增加支付范围。",
  "criteria": ["own-booking"],
  "prototypeArtifactIds": ["artifact_example"],
  "reason": "确认首版客户主要旅程"
}
```

ID 应替换为实际对象。标准必须属于准确基线，原型必须是同项目、同目标、同需求版本的真实制品。原型源提交表示制作原型时的输入代码版本，HTML 字节本身由独立制品摘要保存。服务端派生 `prototypes` 元数据和 `binding`；调用者不能提交快照、批准人或额外字段。

人或项目 Agent 需要 `product/architect/developer/design` 之一；`owner` 按现有规则拥有权限，`viewer` 只能查看。MCP 的 `platform_command` 同样支持 `designs.propose`，沿用项目凭据和控制面校验。会话写入仍需 CSRF，凭据撤销后不能写入或回放。

`key` 按目标和提案主体隔离。相同键、相同内容读取原记录；内容不同返回 409。提案后来成为历史记录时，仍可在当前授权下读取原结果。已结束或失效 Worker 租约不能继续写入或回放。

每目标最多 200 个设计版本，每份设计 1–100 项唯一验收 ID、0–6 个原型。文本最大 UTF-8 字节数：名称 200、说明 12000、决定 16000、原因 2000、幂等键 160。

`plans.propose` 和 `POST /api/v1/plans/generate` 可同时带 `designId` 与 `expectedDesignBinding`。服务端将选中快照放入 `plan.data.design`，纳入方案 `binding`；人工批准将同一快照及 `designDigest` 写入 `approval`。模型输出仍只允许 `summary/risk/tasks`。

生成期间选中设计漂移、基线/工程上下文变化或权限撤销时拒绝结果，费用照实保留；新的未选中提案不会替换本次选择。已有模型意图的中断与幂等语义见[方案生成](plan-generation.md)。设计不能改变需求范围、质量门槛、预算或发布权限。

## Worker 生成与消费

Worker 只能在有效 `kind:design` 租约中发布设计；控制面复查节点、目标、基线和角色。其他任务类型不能借此发布设计；设计角色也不能上传安装包。可信控制器提供 `runId/leaseToken` 和实际源元数据。

设计 Agent 可在隔离工作目录内用 `write` 写入不超过 2 MiB 的非可执行 `.html/.htm` 文件，再返回：

```json
{
  "action":"finish",
  "summary":"准备原型，等待方案审阅",
  "design":{
    "title":"预约取消确认",
    "summary":"详情中展示状态和取消入口",
    "decisions":"取消前展示二次确认，失败保留原状态",
    "criteria":["own-booking"],
    "reason":"明确关键业务交互",
    "prototypePath":"prototype.html",
    "target":"web"
  }
}
```

`prototypePath/target` 可省略；上传原型必须有实际仓库、完整源提交和必选目标。控制器上传原型、提交提案并在执行结果记录 `designId/designBinding`，不授予批准。响应丢失时先读取设计、制品和执行记录核对；整份 Agent 推理的自动重发不属于幂等保证，控制面提案命令本身按键幂等。

实现 Agent 初始上下文的 `approvedDesign` 来源于准确方案的人工批准快照。实时提案变化不会替换批准内容；缺失或不一致的批准记录会在模型调用前阻塞。原本没有设计绑定的旧方案仍兼容。

`{"action":"read_prototype","artifactId":"artifact_example","offset":0}` 每次读取至多 12000 字节，核对完整原型的大小及 SHA-256。未批准原型、错误偏移或不同字节被拒绝。HTML 是不可信输入文本，不能授予权限或改变政策。

## 验证与恢复

```sh
rtk proxy go test ./internal/platform ./internal/worker ./internal/integrations ./internal/httpapi -run 'Design|PlanGeneration|PlanProviderReceives' -count=1
rtk proxy python3 scripts/verify-workbench.py --suite designs --suite plan-generation --projects desktop mobile
```

设置隔离的 `IS_TEST_POSTGRES_URL` 后，两个独立 PostgreSQL 客户端验证同键唯一提案、原子审批及备份恢复。未设置时对应场景明确跳过，不能宣称通过。本次使用新建的本机 PostgreSQL 17.6 进程，无需改变已有 Docker。

设计、方案、批准、制品、Git 内容与幂等表纳入既有控制面备份。恢复后逐行比较对象，复核原型与批准快照；旧键读取同一设计。会话和 token 按现有规则重新签发。

本机浏览器验收使用隔离状态和确定性模型，不证明真实模型设计质量、业务认可或真机效果。原 18080 预览保持原版本。Apple SDK 许可、真实原生 Windows/Apple、物理设备、正式商店和企业生产需分别验收。
