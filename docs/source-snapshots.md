# Agent 完整源码提交

代码任务完成时提交工作区的完整源码，保留工具生成的源文件、二进制资源、删除和 Git 执行权限。分析/设计任务只提交知识产物。提交本身不会产生质量证据、批准评审或放行发布。

## 传输协议

`POST /api/v1/repositories/{repositoryId}/snapshot`，`Content-Type: application/x-tar`，请求体为完整、未压缩 tar。元数据放在唯一的 `X-IS-Source` 请求头，内容是下列 UTF-8 JSON 的标准 base64 编码，编码后最多 8 KiB；不接受查询参数。生产使用 HTTPS，代理日志应隐藏 Authorization、Cookie 和 X-IS-Source。

```json
{
  "key": "run_...",
  "branch": "agent/goal_...",
  "baseCommit": "完整基础提交 SHA",
  "expectedHead": "当前分支完整 SHA；首次创建分支为空",
  "message": "完成本次批准的代码工作",
  "runId": "run_...",
  "leaseToken": "当前租约"
}
```

幂等键为 1–96 字符的字母、数字、下划线、短横线或点，首字符须为字母或数字。worker 的 key 必须等于 runId。请求绑定主体、仓库、分支、基础提交、预期头、说明、执行 ID 及完整原始 tar 的 SHA256；换内容必须使用新的授权执行，不能用同一键覆盖。

人类开发者使用已登录会话与 CSRF；可信 worker 使用节点 Bearer 令牌并附当前 code 任务租约。普通 Agent 项目令牌没有此流式接口权限。worker 必须符合任务指定仓库、分支和显式基础提交；未声明分支的普通 code 固定为 `agent/{goalId}`，集成 code 使用绑定的专用目标。分析、测试或发布任务不能通过新接口或旧文本接口提交源码。权限在读取请求体前、准备记录时和移动分支前重新校验。

新分支必须给出明确 baseCommit；已有分支要求 `baseCommit == expectedHead == 当前分支头`。主分支只能走受保护的独立评审与合并流程。旧文本编辑接口保留原有限额，不适合二进制或完整多端工程。

## 源码边界

- 整个 tar（含头、PAX 和零填充）不超过 64 MiB，最多 20,000 个文件/目录条目。新 worker 自动选取源码，构建输出应写入被 Git ignore 排除的目录。大型依赖、SDK 和构建包使用节点缓存或制品能力。
- 只支持普通文件和目录，Git 模式为 100644/100755。拒绝符号链接、硬链接、设备、FIFO、子模块、路径越界、Git 元数据和平台验证制品。
- 路径须为可移植 UTF-8 相对路径；拒绝重复、大小写或 Unicode 规范化冲突、Windows 设备名/数据流、尾点/尾空格及保留字符。不会悄悄改名。空 HTTP 请求不代表清空源码；有效空 tar 可以表达删除所有原文件。
- 完整快照中缺少的原路径代表删除。Git 直接记录原始 blob，不执行仓库属性、编码转换、过滤器或钩子；源码导出也读取完整原始树，不应用 export-ignore/export-subst。
- 原先跟踪的文件即使新 ignore 规则排除，仍保留跟踪；新增文件遵循仓库 Git ignore 规则。Git 元数据与 `.intelligence-artifact` 始终不进入快照。忽略规则由应用仓库维护，控制器不替应用决定哪些构建目录属于源码。

## Worker 动作与权限

模型循环仍最多 24 步。`write` 接受 path/content，可选 `executable: true/false`；每次文本写入最多 1 MiB。`delete` 只删除一个普通文件，不能递归删除目录。工具可生成更大的二进制文件，但完整归档仍受 64 MiB 限额约束。

`finish` 会扫描实际工作区，不再要求模型把工具生成文件逐个重写。批准的 code 任务若自带 command，命令成功后也执行同一提交流程；普通代码任务没有源码变化不能报告代码成功。已批准集成可提交与输入相同的树，但必须保留所有批准父输入与明确冲突决定。后续构建在领取时自动绑定依赖的实际源码结果；经过非源码工作项或拆分父项也保留生产者追溯，未集成结果会阻塞。选择和重试规则见 [工作项源码输入](task-sources.md)。正式候选与证据仍绑定实际构建 SHA。

原始文件索引、选取用 Git 目录及待上传 tar 均在工作区之外，AX 替换返回树不会覆盖控制器状态。Linux/macOS 读取实际执行权限；Windows 保留原始归档的 Git 执行位，新建脚本或显式更改用 write 的 executable 字段。Windows 模式逻辑有本地单元验证，尚未在真实 Windows 节点运行。

工具进程不继承平台/模型令牌，上传由可信控制器完成。native 驱动仍只适合一次性专用节点；控制器目录隔离不构成原生进程的宿主机安全沙箱。

## 确认、重试和人工恢复

响应为 `kind:source_commit, status:committed` 对象，data 包含 repositoryId、branch、baseCommit、expectedHead、commit、digest、fileCount、authorId、runId 和 changeId。worker 完成结果记录 sourceOperationId。租约不保存在该对象、幂等绑定或审计中。

先保存 prepared 操作、生产者 change 记录和幂等绑定；候选提交通过控制面专属引用保持可达。随后再比较并更新分支，完成数据库确认。prepared 生产者记录用于独立性检查，不能报告开发完成；只有确认后的记录才可完成代码任务。Git HTTP 拒绝客户端写入 `refs/intelligence/`。

Git 与数据库没有跨系统原子提交。若 Git 已更新而数据库确认失败，prepared 记录继续保留来源；相同主体在授权仍有效时，携带完全相同请求可核对原 candidate 并确认，不生成另一提交。worker 对网络/5xx/无效确认响应最多重放 3 次，4xx 不自动重放。成功操作的原样重放返回原对象，不把后来推进的分支回退。

若分支指向意外提交、执行租约已失效、需求变更或节点停用，原 Worker 重放被拒绝。新的[人工核对与接管](source-recovery.md)允许负责人检查 prepared/committed 原候选及实际分支，在原批准绑定仍有效且没有活跃租约时确认采用；run 明确标为 recovered，保留原作者与退出状态。需求/方案失效或分支不含候选时不能采用；可停止采用并保留历史，再创建新批准的工作。不要伪造旧租约、强推或直接改 SQL。

这套恢复保证适用于新的快照端点；旧文本提交和普通 Git 推送仍沿用原有事务与恢复行为。

## 验证范围

Go 测试使用真实 Git、HTTP、SQLite、工具命令、构建制品与独立质量节点。模型输出是确定性样本。PostgreSQL 测试通过显式 IS_TEST_POSTGRES_URL 使用隔离数据库，覆盖双客户端并发和 Git 已更新后 SQL 确认故障的恢复。

```sh
rtk proxy go test ./internal/source ./internal/platform ./internal/httpapi ./internal/worker
rtk proxy env IS_TEST_POSTGRES_URL=postgres://... go test -count=1 -race ./...
```

这些检查不证明真实模型研发、AX 客体隔离、多端应用安装、真实 Windows 行为或渠道正式发布；完整产品仍按原 P0–P4 阶段验收。

## 受控集成扩展

集成快照元数据增加 integrationId/resolutionDigest，仍受原 8 KiB 限制。平台从绑定及准备记录推导父提交，核验每个冲突路径的字节/执行位/删除；不能用旧文本端点绕过。源码操作记录额外保存集成 ID、决定摘要和准确 parents。未带集成元数据的旧快照保持原幂等绑定。完整协议、模型动作和修复见 [受控源码集成](source-integration.md)。
