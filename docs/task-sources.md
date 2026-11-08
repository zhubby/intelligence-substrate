# 工作项源码输入与结果追溯

更新：2026-11-06。控制面在领取 code、build、security 工作项时确定实际源码，写入工作项及执行快照。构建、验证和重试使用同一完整提交，后续分支推进不会替换已经选定的输入。

## 批准方案如何选择源码

`dependsOn` 同时表达执行顺序和默认源码来源。平台沿依赖图寻找最近的成功 code/build 结果，穿过 analysis/design 等非源码工作项；遇到拆分父项则展开其实际子项。输入仅限相同项目、交付目标、活动方案和需求版本，记录生产工作项、成功执行、仓库和完整提交。

每个源码生产工作项必须有唯一成功执行（或有完整负责人决定链的 recovered 执行）、相符的执行快照和可核对的实际提交/制品。开发结果需要原可信开发身份的已确认 change；新绑定工作项还要求提交包含基础历史且 change 来自该次执行。prepared 快照不能直接作为成功源码结果。历史在途执行保留兼容规则，不把旧记录追认为新的完整绑定。

方案可用非空 `sourceFrom` 数组明确选择依赖祖先的 logicalId。不能选择自身、无依赖关系的工作项或重复 ID。选择非源码工作项时仍沿其依赖寻找最近源码。显式 `repositoryId` 过滤对应仓库的结果；未指定且来源涉及多个仓库时阻塞。选择的仓库没有任何匹配源码结果时，也不会回退到随意分支。

```json
{
  "id": "build-web",
  "title": "构建反馈应用",
  "role": "developer",
  "kind": "build",
  "runtime": "linux",
  "repositoryId": "repository_application",
  "dependsOn": ["implement", "design-review"],
  "sourceFrom": ["implement"],
  "command": ["/bin/sh", "scripts/build-web.sh"],
  "outputPath": "artifacts/web.tar.gz",
  "target": "web"
}
```

普通工作项中，同仓库的多个输入，只有某个输入提交包含其他所有输入历史时，才能自动选取它。方案中显式给出的完整 `commit` 也必须包含全部选中输入。未合并的结果进入 `blocked`，保留 `sourceConflict: {code,message,inputs}`。这一步不会生成运行租约、增加尝试次数或预留执行预算，并记录 `tasks.source.blocked` 审计。

没有源码生产依赖时，使用显式完整提交；否则在领取时读取批准分支当时的实际 Git ref。未指定分支的 build/security 使用仓库默认分支。code 默认目标为 `agent/{goalId}`：已有分支从其当前头开始，新分支从仓库默认分支开始；若已选定的上游/显式提交与现有目标分支不同，则阻塞。批准的是分支策略时，具体 SHA 在领取时才固定；需要在批准时固定版本应显式给出 commit。

新应用的根 code 工作项，在项目没有仓库时由控制面创建 application 仓库并审计；已有多个仓库时必须选择。Worker 不再为新的绑定任务猜测仓库。

## 不可变绑定 v1

`task.data.sourceBinding` 和 `run.data.task.sourceBinding` 包含：

| 字段 | 含义 |
|---|---|
| version | 当前为 1 |
| taskId / logicalId / projectId / goalId | 当前工作项及业务归属 |
| planId / requirementRevision / kind | 活动方案、需求基线和任务类型 |
| repositoryId / commit | 实际输入仓库与完整提交 |
| declaredCommit / declaredBranch | 批准工作项原先声明的提交/分支，可为空 |
| targetBranch / expectedHead | code 输出分支和领取时的分支头；空 expectedHead 表示新分支 |
| inputs | 最近生产工作项及其 runId、repositoryId、commit |
| selectionDigest / digest | 依赖选择和规范 JSON 内容的摘要 |

摘要用于检查意外改写，不是数字签名或对数据库管理员的防篡改保证。外部方案不能提供 sourceBinding/sourceConflict。领取后 task.data.repositoryId/commit 为解析后的准确值。

失败或过期后重试保持原绑定，不能通过重排、拆分或重试切换源码。重试会再次核对绑定归属、摘要、提交存在性和 code 目标头；分支已变化则保留原绑定并阻塞。需要不同输入时创建新的方案并取得批准。由独立测试失败产生的修复/重建任务会清除复制的运行绑定与旧 sourceFrom，再绑定准确失败制品或修复提交。

## Worker 与完成校验

code 工具运行前通过 `GET /api/v1/repositories/{id}/refs` 核对目标头。该授权只读接口返回 `{repositoryId,heads:{branch:fullSha}}`，空缺分支与请求失败明确区分。源码始终从绑定 SHA 的归档读取；build 和绑定实际制品的 test 不查询可变分支，也不因分支查询不可用回退到 main。

工具运行期间分支仍可能变化，因此最终源码提交继续使用 compare-and-swap；预检不能代替最后的并发检查。code 提交必须使用绑定的 baseCommit/branch/expectedHead，结果必须来自当前 run 并包含基础历史。build 注册和成功完成的制品/结果必须与绑定仓库、提交一致。security 成功结果也必须报告同一仓库与提交。

工作台“执行记录”中可打开工作项，查看固定输入、来源执行和冲突提交；执行历史保留当时的输入快照。等待处理的任务不显示为正在执行。

## 集成与恢复边界

批准方案可声明 kind=code、sourceMode=integrate 的[受控集成工作项](source-integration.md)，准备准确输入的合并工作区、处理冲突并将实际结果交给构建与独立测试。普通工作项继续阻塞未集成结果。集成绑定增加 sourceMode/integrationParents；默认使用独立集成分支，最多八个父提交和一百个冲突路径。Git 历史包含关系只证明来源关系，不能证明功能完整；仍需独立测试与验收。

开发上传成功但完成确认丢失时，Worker 在原有效租约内保留快照重放/结果确认能力。原租约失效后不会让新租约冒领旧源码；未完成候选使任务进入源码待核对，通用 retry 被拒绝。负责人可经[人工接管](source-recovery.md)确认原候选并恢复下游，或停止采用后重新规划；恢复执行保留原作者且明确标为 recovered。禁止伪造旧租约、强推或直接修改 SQL。

本轮使用真实 Git、SQL、HTTP、原生命令、构建与独立质量验证，模型输出为确定性样本；这不构成真实模型/AX 执行、应用原生交付或正式渠道验收。
