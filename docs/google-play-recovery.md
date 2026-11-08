# Google Play 未知发布结果核对

`is-publisher -reconcile-google` 让可信节点管理员核对一个已停止、结果不明的 Google Play 发布。它重新验证原批准文件的 SHA256/大小，读取原固定应用的 Bundle 和 Track，原子补全本地收据。它不会上传、修改 Track 或 commit，也不会直接改变控制面的版本状态。

`operations.state:complete` 表示本地发布操作获得可解释结果。`completed` Track 只恢复 `awaiting_review`，`halted` 恢复 `halted`。这两者都不能代替审核通过、公开上架、安装验证或完整交付。

## 操作前提

1. 停止原 operationKey 对应的 publisher/Worker，确认没有未结束的远程写入；暂停同一应用在其他 Track、门户、脚本及其他状态库中的写入。Google 通过临时 edit 提供查询快照，新 edit 可能使该账户已有 edit 失效；核对需要独占应用编辑窗口。程序不能从 `confirmStoppedOperationKey` 自动保证停机或跨状态库互斥。
2. 在无写入时备份完整私有 publisher 状态目录，包括仍存在的 WAL/SHM。保留管理员固定配置、原批准请求和准确 AAB。不要换 key、改数据库或复制状态库继续提审。
3. 用原配置检查日志并保存当前 `journalDigest`：

```sh
rtk proxy ./bin/is-publisher -config /etc/substrate/publisher.json -inspect ORIGINAL_OPERATION_KEY
```

仅支持原 `google-play` 单 AAB 发布，Request 的 `action` 必须为 `publish` 或空。日志须为准确 `pending` 快照，已保存原 `editId` 和规范正整数 `versionCode`。只有 edit、上传结果尚未保存或无法在准确 Track 证明版本时保持阻塞；本入口没有强制解锁或重新提交选项。

同一权威 SQLite 状态库按应用持久保存发布占用、永久版本/字节/versionCode预约及查询窗口，结果不明时换key或换Track不能再次上传。恢复在原批准与本地字节验证后校验自己的发布占用；恢复查询与状态查询、新发布在同一事务争用窗口。其他状态库、旧程序和门户互斥仍需管理员保证。旧日志升级见[发布防重手册](google-write-guard.md)，查询中断处理见[查询窗口手册](google-query-guard.md)。

## 管理员输入与执行

准备权限 0600 的 UTF-8 JSON。`request` 填写完整原批准 Request，保留项目、目标、releaseId、版本、artifactId、SHA256/大小、commit、binding。`artifactPath` 可以改为恢复后的同字节 AAB 绝对路径；符号链接、非普通文件及字节不符会被拒绝。不能自行提供替代 editId、versionCode 或渠道证明。

```json
{
  "request": {"说明": "替换成完整原批准 Request；此占位对象不能执行"},
  "journalDigest": "替换为当前 inspect 的64位小写SHA256",
  "confirmStoppedOperationKey": "替换为原operationKey",
  "reason": "已停止原进程、排除同应用其他写入，并记录核对原因"
}
```

若填写 `request.externalId`，必须等于原固定包名加 `:` 加保存的 versionCode。原因须非空且不超过4096字节。凭据可轮换，批准制品、版本、绑定和目的地不能借轮换改变。

```sh
rtk proxy ./bin/is-publisher -config /etc/substrate/publisher.json -reconcile-google < /private/google-recovery.json
```

各 CLI 模式互斥。输入最多64 KiB，拒绝未知/重复字段、无效 UTF-8、null、数组、尾随对象及过深结构。不能将此管理员命令配置为 Worker 的 publish、status 或 continue provider；普通 Request 协议没有恢复动作。

## 核对与持久化规则

| 核对对象 | 必须满足 |
|---|---|
| 当前批准与固定配置 | 与原持久 request hash 一致，包括准确应用、Track、版本及制品绑定 |
| 本地文件 | SHA256及大小重新核对；不把文件名或管理员文字当证明 |
| 查询快照 | 先持久取得应用查询窗口，POST前保存创建意图；创建与原edit不同的临时edit，不复用、不提交原edit |
| `edits.bundles.list` | 保存的 versionCode 只匹配一个 Bundle，SHA256与原批准文件一致 |
| `edits.tracks.get` | 返回准确固定 Track；版本名或versionCode指向的 release 唯一，name等于原Request.version，只含保存的一个versionCode，状态为completed或halted |
| 查询资源清理 | 无论证明是否充分都尝试删除已知临时edit；确认清理与释放自己的查询窗口同事务，未确认时不完成原发布恢复 |
| 本地事务 | 同时比较原state、request hash、原始receipt/result和更新时间，并校验自己的应用占用/预约；全部不变才在同一事务追加历史、写入complete并释放自己的占用 |

draft、inProgress、未知状态、缺少或重复Bundle/release、错误字节和名称、HTTP/解析失败及并发日志变化均不释放发布占用。查询创建/删除结果不明时，持久查询窗口也保留；管理员可按准确queryId、queryDigest和停止确认清理已知ID的查询，再重新核对原发布。没有ID时不强制解锁，不能把本地失败解释为上游没有动作。

输出 `receipt.googleReconciliations` 保存原日志摘要、语义输入摘要、停止确认、原因、观察时间、结果状态，以及应用、Track、版本、制品、queryId、查询edit、release状态和两份完整API JSON值的SHA256；旧记录可无queryId。摘要不含响应周围空白。生产日志保留观察摘要和身份字段，不保留完整API正文；若企业要求独立复算原响应，应另行以受控方式保留响应报告并登记为私有制品。

相同语义输入、原日志摘要和原因重放时，读取已完成记录，不访问网络，也不要求原文件或凭据再次可用。原因、批准绑定或目标发生变化会拒绝。普通 publish 重放返回恢复结果，不再次提交，但现有 publish 入口仍要求本地批准文件可读且字节正确。历史快照不能用于覆盖后来变化的日志。

成功恢复只释放应用占用，永久版本、字节和已知versionCode预约保留。`inspect.googleReservation` 显示登记身份及是否仍持有应用占用；原 `journalDigest` 仍只覆盖原 operations快照，不含新增预约表。旧的已完成恢复记录若未登记预约，重复恢复只返回原结果，须用原批准 publish Request重放完成登记。

核对证明的是该查询快照中准确制品与release的状态，不能证明历史上是哪一次进程完成了commit，也不实现跨状态库目的地锁、分阶段发布或停止分阶段发布。

## 同步控制面与完成交付

发布负责人将真实核对报告登记为当前目标、提交及制品绑定的 report，使用既有 `releases.reconcile`，提交 `proofArtifactId`、准确 `externalId`、实际状态及原因。该流程仍执行当前权限、人工批准和独立证据约束；不能因为本地操作complete而把渠道写成published或版本写成delivered。

Google Track 状态不能独立判断商店审核、Managed Publishing/发布设置、目标地区可用性或用户安装结果。真实发布须继续留存渠道记录，并由独立人员在目标地区、真实设备核对同一版本及关键业务，登记publication evidence后执行 `releases.verify`。所有必选端和渠道均达标后才能完成整体交付。

## 本机验证与边界

```sh
rtk proxy go test -race -count=1 ./internal/publisher ./cmd/publisher -run 'GoogleRecovery|Publisher'
rtk proxy go build -trimpath -o work/is-publisher ./cmd/publisher
rtk proxy go run scripts/verify-google-recovery.go -publisher work/is-publisher -state work/NEW_GOOGLE_RECOVERY_PROOF
```

证明目录必须不存在。验证器使用本机TLS服务、合成RSA service account和明确标记为非AAB的字节，经真实OAuth/JWT、Engine、SQLite和实际CLI演练。官方host映射仅存在于验证器自定义HTTP transport中，生产CLI没有API origin覆盖或包校验绕过开关。真实Google账户、合法AAB、企业上传身份、商店审核和真机仍需企业环境验收。

2026-11-07核查了Google维护的[Go客户端Discovery文件](https://github.com/googleapis/google-api-go-client/blob/main/androidpublisher/v3/androidpublisher-api.json)，revision `20261026`：Bundle的versionCode为int32、sha256为十六进制字符串；Bundle列表无分页字段；TrackRelease.versionCodes为字符串数组，状态含draft/inProgress/halted/completed。两个官方Discovery域名本次连接超时，没有据此声称实时Google服务已验证。接口参考：[Bundle列表](https://developers.google.com/android-publisher/api-ref/rest/v3/edits.bundles/list)、[Track查询](https://developers.google.com/android-publisher/api-ref/rest/v3/edits.tracks/get)、[Edit工作流](https://developers.google.com/android-publisher/edits)。
