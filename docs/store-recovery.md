# 国内渠道未知操作核对

`is-publisher -reconcile-store` 为可信节点管理员恢复华为、小米、OPPO 和 vivo 的已接受操作。它核对原制品和原日志，不重发上传、更新或提审。当前证据来自本地 HTTPS/SQLite 契约、实际 CLI 和并发/故障测试，没有真实商店或设备结果。

## 先停止写入，再核对

1. 停止该 operationKey 对应的 publisher/Worker 进程，确认没有尚在执行的上游写入；暂停该包的其他门户、脚本和其他状态库写入者。`confirmStoppedOperationKey` 是本地管理员的声明，程序不能据此召回已派发的远程请求。
2. 暂停 publisher 状态库写入，备份完整私有状态目录及 WAL/SHM；单独保护凭据、固定配置、批准制品和资料。不要复制另一份库继续发版。数据库备份不是外部商店回滚。
3. 用原配置检查准确日志：

```sh
rtk proxy ./bin/is-publisher -config /etc/substrate/publisher.json -inspect ORIGINAL_OPERATION_KEY
```

保留 `journalDigest`、stage、版本、包/业务 ID 和恢复历史，核对原控制面批准绑定。不能通过修改数据库、换 key、换应用、删除锁或降低验收门槛来解除未知结果。

## 管理员输入

准备一个权限 0600 的 JSON 文件，`request` 使用原批准发布请求，保留其项目、目标、releaseId、artifactId、SHA256/大小、提交、binding、版本及资料摘要。`artifactPath` 可以指向重新恢复的同字节 APK；程序重新暂存、校验 SHA256，并调用固定 SDK 检查签名和版本。不得提供另一制品。

```json
{
  "request": {"说明": "替换为完整的原批准 Request；此占位对象不能执行"},
  "journalDigest": "替换为当前 inspect 的 64 位小写 SHA256",
  "confirmStoppedOperationKey": "替换为原 operationKey",
  "operator": "企业本地发布管理员标识",
  "reason": "已停止准确进程，排除其他写入者，记录核对原因",
  "proof": "api"
}
```

`operator` 是本地声明，不是平台认证或商店签名。恢复入口应只允许管理员执行，不能注册成 Worker 的 publish/status/continue provider。各 CLI 模式互斥，输入上限 64 KiB，拒绝未知/重复字段、无效 UTF-8、多对象和过深结构。

```sh
rtk proxy ./bin/is-publisher -config /etc/substrate/publisher.json -reconcile-store < /private/recovery.json
```

命令输出完整 Operation 和 `storeReconciliations`。相同输入仅在恢复后的准确日志快照仍未改变时幂等返回，无需凭据、SDK或原证据文件再次可用；如果后续继续提审改变了日志，必须重新 inspect，旧恢复请求不能借用新结果。

## 各渠道可以恢复什么

| 渠道 | 允许的持久阶段 | 核对要求与结果 |
|---|---|---|
| 华为 | `file_registration_intent` / `processing` | 唯一准确包的文件名、SHA256、大小、versionName/code，以及当前可编辑业务版本一致，恢复 `processing`。登记时间丢失时重新等待至少两分钟，后续继续提审仍走原平台审批与独立 continuation |
| 华为 | `submission_check_intent` | 原 packageId 和业务 versionId 仍准确且可编辑，恢复 `processing`，不执行 submit |
| 华为 | `submit_intent` / `submitted` | 必须有持久业务 versionId；准确包与当前/在架版本匹配，才能恢复渠道实际审核/待上架/拒审/下架/上架状态。一般状态保留 AppID 锁，准确终态才原子释放自己的锁 |
| OPPO | `submit_intent` / `submitted` | 查询准确版本任务及包信息；即使任务处理中或失败，也要求准确普通 AppID、包名、版本名/code/type/ID、APK MD5和大小。只接受可确认状态，原子完成后释放自己的包锁，永久版本占用保留 |
| 小米 | `push_intent` / `submitted` | `proof:portal`，管理员准确后台报告与附件，只恢复 `processing`、`PORTAL_ACCEPTED_UNVERIFIED` |
| vivo | `submit_intent` / `submitted` | `proof:portal`，原生产/沙箱环境及准确后台报告与附件，只恢复 `processing`、`PORTAL_ACCEPTED_UNVERIFIED`；沙箱不能证明生产交付 |

华为/OPPO 使用 `proof:api`，不接受用人工附件替代准确接口证明。查询过程可以包含鉴权请求和官方查询 POST；“只读”指没有商店发布副作用，并非一律只使用 HTTP GET。华为的分配上传对象 GET 不属于只读，恢复不会调用它。

缺少 SHA256/MD5、身份冲突、未解析、模糊或未知状态、无准确上架版本等均保持阻塞。较早上传阶段没有强制解锁或放弃入口。程序没有从“查不到”推断“没有执行”。

## 小米与 vivo 的后台报告

这两个渠道的现有接口不能独立证明准确制品已接受并上架。发布负责人须实际登录正确环境的企业后台，核对准确 APK、批准资料、版本和已接受提交记录，将实际观察写入私有报告，附后台截图/导出文件。不得把本地测试样本用于正式核对。

报告原字节 SHA256 写入管理员输入的 `evidenceDigest`，绝对路径写入 `evidenceFile`，并设置 `proof:portal`。报告只允许声明 `state:accepted`；不能用此入口声明审核通过或上架。

```json
{
  "protocolVersion": "1",
  "driver": "xiaomi-market",
  "environment": "production",
  "appId": "com.company.application",
  "packageName": "com.company.application",
  "operationKey": "替换为原 operationKey",
  "journalDigest": "替换为本次日志摘要",
  "artifactDigest": "替换为原 APK SHA256",
  "artifactSize": 123456,
  "metadataDigest": "替换为固定配置的批准资料 SHA256",
  "binding": "替换为原批准 binding",
  "version": "1.0.0",
  "versionCode": 42,
  "state": "accepted",
  "operator": "与管理员输入相同",
  "reference": "真实后台提交/工单标识",
  "observedAt": "2026-11-06T04:00:00Z",
  "summary": "核对准确制品、资料及后台接受记录的实际结论",
  "attachments": [{
    "name": "portal-receipt.png",
    "path": "/private/portal-receipt.png",
    "sha256": "替换为附件原字节 SHA256",
    "bytes": 12345
  }]
}
```

以上时间、标识、大小和摘要均为结构占位，必须替换为当前实际观察。vivo 使用 `driver:vivo-store` 及原 `environment:production` 或 `sandbox`。营销版本填写去掉开头 `v` 的版本；`metadataDigest` 必须等于固定 profile，原 Request 声明了资料摘要时也必须一致。

观察须在过去24小时内，最多容许5分钟时钟偏差。报告最多64 KiB，附件1–8个，每个最多8 MiB、合计最多16 MiB；名称唯一且不能使用保留名称 `report.json`。全部路径须绝对、普通文件、服务所有者私有权限，拒绝符号链接。publisher状态目录须0700，数据库须0600；新数据库按0600创建，既有不安全权限会拒绝保留证据，程序不会自动修改已有文件权限。

报告和附件都按原字节校验并随核对事务写入私有 publisher.db。`-inspect` 仅返回观察来源、操作员、原因、前后阶段、原/结果日志摘要、报告/附件摘要和后台引用，不输出附件正文。完整数据库及备份包含私有证据，OPPO还包含token，不能作为普通工单附件分享；由服务管理员在隔离副本中按 `store_reconciliation_evidence.input_digest` 读取原始 `body` 并复算 `digest/bytes`。不要直接编辑这些表。

人工报告及其摘要只证明“保存了哪份声明”，不能证明后台截图真实或独立验证。恢复结果保留 `PORTAL_ACCEPTED_UNVERIFIED`，不会返回 published，也不会自动完成交付。

## 同步控制面和继续交付

发布负责人将实际 Operation/报告登记为同目标、提交及当前需求版本的 report 制品，使用既有 `releases.reconcile`，关联 `proofArtifactId`、准确 `externalId`、实际 state 和核对原因。该操作保留人工权限、发布批准及独立证据门槛，不会因为 publisher 本地恢复而自动批准发布。

API观察到拒审/下架等终态时，应显式登记其实际结果；不要为迁就普通 publish provider 的成功状态范围而改成“待审”。需要修复时创建新候选并按平台流程审批。所有恢复后的版本均永久保留本地versionCode占用；华为/小米还阻止在新key下复用或倒退到已恢复版本，不能以渠道历史清理为由重提同一版本。

后续真实published仍需独立质量人员在目标地区和物理设备验证同一制品、externalId、安装版本及关键业务，登记晚于发布的publication evidence，再执行 `releases.verify`。只有所有声明必选端/渠道都通过，版本才可delivered。商店接受、审核、上架、设备验证和整体交付分别留证。

## 本地复验

```sh
rtk proxy go test -count=1 -race ./internal/publisher ./cmd/publisher -run 'StoreRecovery|Publisher'
rtk proxy go vet ./...
```

测试用真实TLS请求和SQLite事务，SDK成功在包内fixture替换；CLI没有跳过SDK校验、修改API origin或自动解锁开关。完整回归另用新建专用PostgreSQL；交叉编译不是Windows/Linux实机或原生客户端验收。实际企业凭据、合法release APK、渠道字段/资质、生产/沙箱兼容性及真机仍需企业环境验收。
