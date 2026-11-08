# 受控源码集成与冲突处理

更新：2026-11-06。批准方案中的 `kind:"code", sourceMode:"integrate"` 工作项可以组合并行开发结果。平台固定输入、准备合并工作区、核对冲突决定，再提交实际源码；后续构建与独立质量仍使用准确提交。Git 合并成功不代表业务验收通过。

## 方案与输入

```json
{
  "id": "integrate-app",
  "title": "集成录入与列表功能",
  "role": "developer",
  "kind": "code",
  "sourceMode": "integrate",
  "runtime": "linux",
  "repositoryId": "repository_application",
  "dependsOn": ["implement-form", "implement-list"]
}
```

后续 build 依赖 integrate-app；它会使用集成工作项实际成功结果，不读取随后可能变化的分支。普通 code/build/security 保持严格规则：多个输入必须已经有包含全部输入历史的选定提交。不能靠重试、拆分或修改 sourceMode 解锁已绑定工作。

集成沿用最近 code/build 来源及 `sourceFrom` 祖先选择规则，必须有真实成功生产者，且仅选择同一目标、方案、需求版本及仓库的结果。多仓库需显式选择。不相关历史、超过八个唯一父提交或无生产者在领取前阻塞；超过一百个冲突路径在准备时失败。更大的工作图需在批准方案中分阶段集成，集成任务不能拆成重复集成。

`sourceBinding` 增加 sourceMode 与 integrationParents，并保留全部生产者 task/run。未指定 commit 时基础提交取一个选定输入；若现有目标头正好是某个选定输入，优先使用它。显式完整 commit 是额外批准的输入，若与生产者不同，也加入最终父提交并计入八个上限。所有输入须有共同历史。

默认目标是 `integration/{goalId}/{logicalId}`，也可在方案中声明有效的非 main 分支。已存在目标必须等于选定基础提交，否则阻塞，不能覆盖他人提交。

## 准备与运行记录

`sources.prepare {runId,leaseToken}` 只接受当前有效集成 code 租约，返回更新后的 **run**。run.data 包含：

| 字段 | 含义 |
|---|---|
| integrationId | 独立、不可变的 integration 准备对象 |
| integrationWorkspaceCommit | 准备工作区的准确提交，只供工作区下载 |
| integrationConflicts | 需要明确决定的冲突路径 |
| integrationResolutions | 本次执行记录的路径 → 内容/模式/删除决定 |
| integrationResolutionDigest | 当前决定集合的规范 JSON SHA256 |

准备对象绑定 task/plan/revision、仓库、sourceBinding 摘要、原始父提交和合并工作区。使用 `git merge-tree`，不检出工程或执行仓库代码；采用独立、无仓库配置的 Git 目录，共用可信对象库，禁用全局配置、钩子及外部属性配置。仓库本地自定义 merge driver 不会进入该过程。关闭自动重命名检测，路径/目录变化可能需要明确处理。

整个准备过程设 30 秒上限，Git 机器输出有界，准备后的完整树必须通过 64 MiB / 20,000 条目、普通文件、可移植路径与模式检查，才会交给执行工具。私有中间提交保存在控制面 refs 下，不移动目标或 main；最终源码的父提交始终来自批准输入，不包含中间冲突树。

相同不可变任务绑定在源码提交前复用同一准备记录，包括租约失败后重试；新 run 不继承旧 run 的冲突决定。工作台工作项与执行详情展示准备提交、来源和逐路径处理状态。

## 明确的冲突决定

```json
{
  "runId": "run_current",
  "leaseToken": "current-lease",
  "resolutions": [
    {"path": "shared.txt", "digest": "文件实际字节的64位小写SHA256", "executable": false},
    {"path": "obsolete.bin", "deleted": true}
  ]
}
```

调用 `sources.resolve`，每批 1–100 条，只能包含准备记录的冲突路径，不能重复或带未知字段。普通文件决定必须给出 SHA256 和布尔执行位；删除决定只有 path/deleted=true。可更新已有决定，但同一次执行开始源码提交后决定被冻结，以保证原样重试。

上传时服务器重新核对每个决定与完整快照中的文件字节、模式或缺失状态。决定后再改内容或模式，需要重新 resolve。删除原文件后以同名目录存放子文件是允许的；不能用“删除”掩盖仍存在的同名普通文件。

原为文本的冲突文件仍有 Git 冲突标记时拒绝提交，即使添加 NUL 字节伪装二进制也不能绕过。二进制可以明确选择某一输入的原始字节，包括与基础提交相同的树；没有内容变化的合法集成仍会产生带正确输入父提交的源码提交。

## Worker 与 Agent

Worker 在下载源码前 prepare，下载准备工作区 SHA；最终 snapshot 的 baseCommit/expectedHead 仍来自原绑定。没有冲突也没有批准 command 时，由控制器直接提交准备树，不调用模型。批准 command 返回零后，控制器记录所有冲突路径的实际决定，服务器最终仍会拒绝残留标记或不一致内容。

有冲突的 Agent 可继续 read/write/delete/run，并使用：

| 动作 | 参数与行为 |
|---|---|
| read_input | commit、path；只读一个选定父提交的固定文件，最多 64 KB；二进制以 base64 表达 |
| take_input | commit、path；选用对应输入的原字节、执行位或删除；若为冲突路径同时记录决定 |
| resolve | paths 数组；批量记录当前实际字节、模式或删除；修改后须重新记录 |

所选版本按需下载到工具目录之外的控制器私有目录，最多八份、每份不超过 64 MiB；保留原有 24 步上限。容器/AX 工具不获得平台令牌；native 仍只适用于专用一次性节点，私有目录本身不是宿主机沙箱。

## 源码确认、修复与边界

完整源码快照在既有 8 KiB 元数据头中增加 integrationId/resolutionDigest。父提交和决定路径由受保护记录取得，不接受调用者自填。旧快照未带集成元数据时保留原幂等绑定。integration 工作项不能通过旧文本 `repositories.commit` 绕过检查。

仍按“持久化 prepared 源码/生产者 → Git 分支 CAS → SQL 确认”提交；原样重试可确认原候选，不覆盖后来的分支。完成要求当前 run 的已确认集成快照以及所有父输入的历史。prepare/resolve 本身没有测试、评审或发布效力。

独立测试失败后，平台从失败制品的源码生产链选择唯一批准开发配置，不按任务排列顺序选择。修复清除旧集成模式/运行字段，在 `repair/{defectId}/{attempt}` 从失败制品 SHA 开始；原测试策略继续用于新构建。没有唯一匹配配置时转人工处理，不猜测另一个开发命令。

本地受控集成与独立修复链已接入[源码中断人工接管](source-recovery.md)：核对原集成候选、固定父历史及冲突决定后恢复下游，不重启旧租约或覆盖分支。真实模型与 AX、原生应用/真机、签名与正式渠道仍需各自验收。详见 [源码绑定](task-sources.md) 与 [完整源码提交及恢复](source-snapshots.md)。
