# 平台内应用蓝图

蓝图把经管理员安装的 Flutter/Go 起始源码接入需求、方案审批、Worker 与真实 Git。当前提供客户反馈和服务预约两种参考业务；选用蓝图不代表业务完成、质量通过或正式发布。

## 安装与升级

独立二进制默认关闭目录，已有部署不需要安装模板即可启动。管理员通过 `-blueprints /opt/intelligence-substrate` 或 `IS_BLUEPRINT_ROOT` 指定完整发行资产根目录；`-blueprints -` 显式关闭。本仓库 `make run` 和 Docker 镜像分别使用仓库根与 `/app`。

资产根必须同时包含 `blueprints/flutter-go/` 和 `scripts/` 下的 `build-reference-client.py`、`android_bundle.py`、`reference_preview.py`、`windows_msix.py`、`scaffold-reference-platforms.py`。控制面不需要安装 Flutter，不执行生成命令，也不接受请求指定本地路径、模板 URL 或自定义脚本。发行资产应由管理员维护，应用任务不能写入。

启动时加载模板与辅助脚本的字节和执行位，冻结为内存快照并计算目录摘要。磁盘资产更新需要重启控制面才对新提案生效。请求携带原目录摘要，旧页面不能静默改用新模板。多实例部署须安装一致资产；若目录不同，旧摘要会被拒绝。

升级前按现有运维流程备份数据库、Git 与内容寻址存储。已经保存的提案使用持久源码归档，停用或更换目录不会改写原提案；恢复后可在目录关闭的情况下继续已批准初始化。完整性检查和逻辑备份包含蓝图归档。丢失或损坏的归档会阻止批准/执行，不能重新生成后冒充旧字节。

## 工作台流程

1. 创建交付目标，澄清需求并由人批准当前基线。
2. 在“交付方案 → 应用蓝图”选用目录，填写应用名、反向域名、参考业务和选用原因。项目无仓库时，批准后领取任务才创建空仓库；多个仓库时须明确选择。
3. 在人工编制或模型规划中选择准确提案。如需新建工程，明确勾选初始化；同一需求的后续方案默认沿用已批准蓝图并保留已有源码。
4. 审阅方案中的业务模式、命名空间与初始化选择。人工架构/负责人批准后才创建任务。
5. 启动具备开发角色及 code 能力的 Linux、macOS 或 Windows Worker。初始化采用固定控制器动作，不调用模型。随后开发、实际构建与独立质量沿用原有流程。

应用名须为 2–50 位小写字母、数字或下划线，以字母开头；命名空间采用小写反向域名，例如 `com.company`。参考模式为 `feedback` 或 `booking`。新需求版本不自动继承旧蓝图，须重新提案或明确规划。

## API 与 Agent

授权人或项目 Agent 使用与工作台相同的接口：

| 接口 | 输入或结果 |
|---|---|
| `GET /api/v1/projects/{id}/blueprints` | 管理员已安装目录，包含 ID、版本、目录摘要、目标、模式与限制；关闭时返回空数组 |
| `POST /api/v1/commands/blueprints.propose` | 当前已批准目标、需求版本、稳定 key、目录 ID/摘要、应用标识与原因 |
| `plans.propose` / `POST /api/v1/plans/generate` | `blueprintId,expectedBlueprintBinding,initializeBlueprint`；ID 指提案资源，不是目录 ID |
| `POST /api/v1/runs/{id}/blueprint` | 当前初始化 Worker 的 JSON `{leaseToken}`，返回冻结 tar；不接受查询参数 |

提案请求示例：

```json
{
  "goalId": "goal_ID",
  "expectedRequirementRevision": 1,
  "key": "application-start-v1",
  "blueprintId": "flutter-go-v1",
  "expectedCatalogDigest": "DIRECTORY_SHA256",
  "applicationName": "customer_portal",
  "organization": "com.company",
  "mode": "feedback",
  "reason": "采用已审阅的多端起点，按当前需求实现业务。"
}
```

可选 `repositoryId` 必须属于当前项目且主分支为空源码。同一主体、目标与 key 的成功请求重放返回原提案，换输入同 key 冲突；撤权后不能重放。提案保存归档摘要、大小、文件数、需求版本和内容绑定，不创建仓库、工作项、质量证据或发布制品。

`plans.propose` 和模型规划使用相同的选择契约。明确选择须带提案的 `data.binding`；`initializeBlueprint` 必须为布尔值，启用时必须明确选择提案。控制面派生蓝图快照和保留的 `blueprint-initialize` 工作项，模型不能自行注入保护字段或批准。MCP 提供 `blueprint_catalog` 与 `blueprints.propose`，权限和幂等规则与 REST 相同。

## 源码与失败处理

初始化只允许固定的空源码输入。控制面保存准确需求/方案批准、提案及归档摘要；Worker 凭有效租约下载归档，检查字节摘要和文件数，再通过既有完整源码快照 API 提交。控制面再次要求上传归档与批准归档逐字节一致。二进制字体、删除语义与执行位沿用现有源码格式。

仅写入目标功能分支，不更新主分支。领取前源码非空会阻塞且不消耗执行尝试；分支移动、租约失效、撤权或需求变化不能覆盖当前代码。提交复用持久意图、CAS 和准确人工恢复；网络结果不明时按源码恢复流程核对原操作，不能通过换提案覆盖已有代码。

初始化不能拆分，`blueprint-initialize` 为保留 logical ID；重排不能移除源码工作项对初始化的依赖。后续构建和检查读取初始化的准确提交。当前不提供把新蓝图强行覆盖到现有应用的迁移功能。

## 原生工程与交付边界

冻结归档包含受审阅的 Dart/Go 源码，原生工程准备是另一个明确开发步骤。安装受信 Flutter SDK 后，在生成应用根目录运行：

```sh
rtk proxy python3 scripts/scaffold-reference-platforms.py --application .
```

脚本执行 `flutter create --no-pub`，保留原客户端源码、锁文件和测试，移除自动生成的计数器测试，补充所需网络权限，并取消 Android release 的默认 debug 签名。已有原生工程时拒绝覆盖；部分失败后保留原受审源码，残留工程应人工核对，不能视为准备完成。

后续依赖安装、Flutter analyze/test、Go 测试、Web/原生构建、模拟器/真机、签名/公证、商店审核和生产部署分别验收。参考后端采用单实例 JSON 状态文件，生产业务、安全、容量和高可用仍需工程实现及独立验证。Apple SDK 协议须由操作人员自行阅读接受，平台不会代为处理。

<!-- END INTELLIGENCE SUBSTRATE APPLICATION BLUEPRINTS RUNBOOK -->
