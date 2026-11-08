# Google Play 查询窗口与准确清理

可信 publisher 在同一权威 SQLite 状态库中按应用互斥发布、状态查询和管理员恢复查询。查询会创建 Google edit，因此即使只读取 Track，也必须先持久取得窗口，清理得到确认后才释放。不同 Track 共享同一应用窗口，不同应用可以继续工作。

这项约束覆盖使用新版程序和同一状态目录的可信进程。Play Console、其他状态库、旧程序和外部脚本仍需发布负责人保证独占。`confirmStoppedQueryId` 是管理员的停止确认，程序不能据此自动终止其他进程或证明远程请求已经结束。

## 状态与持久规则

`google_queries` 保存每次查询的历史，`google_query_locks` 保存当前应用窗口。新发布的应用占用和查询窗口在同一个 SQLite immediate 事务内互相检查。查询同时绑定原 operationKey、原 request hash、固定应用和 Track。

| 查询状态 | 已确认的事实 | 可否释放窗口 |
|---|---|---|
| reserved | 已取得持久窗口，尚未进入创建 edit 的意图 | 停止对应查询后，可记录 not-started 并原子释放 |
| creating | 创建意图已在 POST 前持久；结果可能已经发生 | 没有准确 edit ID 时保持阻塞 |
| created | 已保存与原发布不同的准确查询 edit ID | 仅准确 DELETE 2xx 或该 ID 返回 404 后释放 |
| cleaned | 确认清理结果和释放自己的窗口已同事务提交 | 保留历史，不再次操作远程 edit |

`cleanupSource` 为 `not-started`、`delete-confirmed` 或 `already-absent`。404只记录“该准确 ID 已不存在”，不说明谁删除过它。没有 TTL、强制解锁或创建新 edit 以推测旧 edit 是否失效的入口。

鉴权开始前已持久保存 reserved；鉴权失败且尚未进入创建意图可以安全关闭。GET 失败或被取消后，程序以独立的10秒清理上下文尝试删除已知查询 edit。创建结果不明、删除未确认、SQL提交失败均保留窗口；状态查询不返回成功观察，发布恢复不写 complete。获得 ID 后的检查点失败会尝试删除该已知 edit，并在确认后保存清理结果。

更新比较查询的全部12个原始字段及当前窗口 owner。完成清理和释放自己的 owner 同事务提交，日志或 owner 改变时拒绝覆盖。查询清理不改变 operations、永久发布预约、发布写入占用或控制面的渠道状态。

## 检查记录

使用原管理员配置：

```sh
rtk proxy ./bin/is-publisher -config /etc/substrate/publisher.json -inspect ORIGINAL_OPERATION_KEY
rtk proxy ./bin/is-publisher -config /etc/substrate/publisher.json -inspect-google-query EXACT_QUERY_ID
```

operation 的可选 `googleQuery` 只显示该操作最新查询。清理旧会话必须用准确 `queryId` 检查，不能根据“最新”猜测。`windowHeld` 表示该会话当前持有窗口，不表示渠道审核或交付结果。检查应在目标查询停止后进行；多次读取不构成一个跨表原子快照。

`queryDigest` 是以下12项字符串组成的 Go JSON 数组的 SHA256：

```text
[query_id, operation_key, request_hash, app_id, track, kind, state,
 edit_id, cleanup_source, started_at, updated_at, raw_reconciliation_json]
```

原始 reconciliation JSON 字节参与摘要。动态 owner 另行核对，不包含在摘要内；不能用旧摘要证明窗口仍归该会话。原 operations 的 `journalDigest` 与 Request/Profile 哈希定义保持，查询历史不会改变原发布批准快照。

## 管理员清理已停止查询

1. 停止准确 queryId 对应的进程，确认其网络请求已经结束；排除门户、旧程序、其他状态库等应用编辑者。备份完整私有状态目录及仍存在的 WAL/SHM，保留原配置与批准请求。
2. 检查该准确 queryId 的当前状态、摘要和 owner。creating 且没有 ID 时保持阻塞，本命令不会强制清除；不要手改数据库或换状态目录。
3. 准备权限0600的 UTF-8 JSON，request 使用原批准发布请求或其准确 status 形式。文件路径及凭据可迁移，批准绑定、应用、Track、版本及制品身份不能改变。query清理不读取原制品字节；发布恢复仍会重新验证字节。

```json
{
  "request": {"说明": "填写完整原批准Request；此占位对象不能执行"},
  "queryId": "准确查询的64位小写ID",
  "queryDigest": "该准确会话当前的64位小写SHA256",
  "confirmStoppedQueryId": "重复准确queryId，确认已停止该查询",
  "reason": "已停止对应进程并排除其他编辑者；记录清理原因"
}
```

4. 在可信管理员节点执行：

```sh
rtk proxy ./bin/is-publisher -config /etc/substrate/publisher.json -reconcile-google-query < /private/google-query-cleanup.json
```

输入最多64 KiB，拒绝未知/重复字段、无效 UTF-8、null、数组和尾随对象。原因非空且不超过4096字节。created 状态需要有效的 Google 凭据，仅 DELETE 固定应用的已存查询 ID；禁止删除原发布 edit。reserved 状态不访问渠道，可在确知尚未开始创建后关闭。两种清理都不上传、修改 Track、commit 或完成原发布操作。

管理员记录保存输入的 queryDigest、语义 inputDigest、停止确认、原因和观察时间。inputDigest覆盖 `[原requestHash, queryId, queryDigest, confirmStoppedQueryId, reason]` 的 Go JSON SHA256。清理成功后的完全相同语义输入可无凭据重放，直接返回原记录；改变原因或批准身份会拒绝。即使另一查询已经活跃，重放旧清理也不触碰它的窗口。

各 CLI 模式互斥。不能把管理员清理命令配置为 Worker 的 publish/status/continue provider；普通 Request没有清理动作。普通Agent不能自行声明查询已经停止或借查询清理释放发布占用。

清理恢复查询的临时 edit 后，原发布仍可能 pending且持有应用写入占用。须另按[发布恢复手册](google-play-recovery.md)重新取得充分证明；查询清理不能把结果不明的提交认定为成功。发布占用及永久防重见[发布防重手册](google-write-guard.md)。

## 验证与当前边界

```sh
rtk proxy go test -race -count=1 ./internal/publisher ./cmd/publisher -run 'GoogleQuery|GoogleRecovery|GoogleWriteGuard|Publisher'
rtk proxy env IS_GOOGLE_QUERY_PROOF_DIR=/private/NEW_GOOGLE_QUERY_PROOF go test -race -count=1 ./internal/publisher -run '^TestGoogleQueryGuardProcessDeathAndReplayEvidence$'
```

导出父目录须存在，目标目录须不存在。演练在本机TLS服务中启动真实测试子进程，分别在 reserved和created状态终止，重开Engine验证持久窗口、准确清理、旧清理重放与无ID阻塞；使用一致的 `VACUUM INTO` 快照供独立SQLite读取。loopback origin及合成Bearer仅存在于测试程序，生产CLI没有origin覆盖或凭据绕过。

这些证据不代表真实Google、合法AAB、企业身份、商店审核或设备验收。跨状态库/门户互斥、未知无ID的进一步恢复、同AAB跨Track晋级及分阶段发布仍需后续建设。整体版本仍须真实公开发布及独立验证，不能由本地清理成功判定交付完成。
