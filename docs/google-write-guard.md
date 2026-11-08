# Google Play 发布占用与跨 key 防重

可信 publisher 在同一权威 SQLite 状态库内，按 Google 应用保存发布写入占用，并永久登记已批准的业务版本、制品 SHA256 和已知 versionCode。结果不明时，新 operationKey 或另一 Track 不能绕过原操作再次上传。不同应用可以独立发布。

新版程序同时按应用持久保存状态/恢复查询窗口，查询与发布在同一事务中互斥，准确清理确认后才释放。其他状态库、旧程序、Play Console、外部脚本不受本机数据库约束，仍须由发布负责人安排独占编辑窗口。查询状态、摘要及管理员清理见[查询窗口手册](google-query-guard.md)。

## 持久化规则

| 时刻 | 必须持久化的状态 | 外部动作 |
|---|---|---|
| 新批准发布 | 同一事务创建 operations.pending、永久预约和应用占用 | 事务成功后才可鉴权、创建 edit |
| 创建 edit 成功 | 原日志收据与预约共同校验自己的应用占用 | 检查点成功后才可上传 |
| 上传成功 | versionCode 唯一预约与原日志收据同一事务保存 | 成功后才更新 Track、commit |
| 发布成功 | 原操作 complete 与释放自己的应用占用同一事务 | 返回 awaiting_review，永久预约保留 |
| 发布或日志失败 | 保留 pending、已有收据、永久预约及应用占用 | 不自动重传，不换 key 继续 |
| 准确管理员恢复成功 | 恢复历史、原操作 complete、释放自己的应用占用同一事务 | 只查询，不上传、更新 Track 或 commit |

永久预约由 `google_publications` 保存：operationKey、原 request hash、应用、固定 Track、业务版本、制品摘要、已知 versionCode、登记来源和时间。应用与业务版本、应用与制品摘要、应用与非空 versionCode 分别唯一；versionCode须为规范的正 int32 十进制字符串。

`google_destinations` 每个应用最多一个发布写入占用，owner为原 operationKey。不同 Track 共用应用的编辑空间，不能各自获得一个并发发布占用。失败的竞争请求整体回滚，不保留新的 pending 行。已完成的同 key 发布返回原结果，不重新激活旧版本；普通 publish 重放仍校验本地文件 SHA256/大小。

成功只释放当前应用占用，永久预约不会删除。新版本且新字节可在同应用继续发布；同版本或同字节不能借新 key、不同 Track 重发。将同一 AAB 晋级其他 Track 需要单独设计受审批的晋级操作，当前单 AAB 发布入口不提供该能力。不同应用可以使用相同业务版本和字节。

如果上游在新上传后返回已被其他操作占用的 versionCode，系统在更新 Track 和 commit 前停止。此时新上传可能已经发生，原 pending 及应用占用仍保留；不能把本地唯一约束错误当作上传未发生。没有充分收据或渠道证明时，不提供强制解锁。

## 查看与恢复

使用原管理员配置查看：

```sh
rtk proxy ./bin/is-publisher -config /etc/substrate/publisher.json -inspect ORIGINAL_OPERATION_KEY
```

Google 操作会附加可选 `googleReservation`：

```json
{
  "appId": "com.example.application",
  "track": "production",
  "version": "1.0.0",
  "artifactDigest": "原批准制品的64位SHA256",
  "versionCode": "42",
  "source": "publish",
  "enrolledAt": "登记时间",
  "applicationHeld": true
}
```

示例字段是说明，不能当作真实渠道证据。`applicationHeld:true` 表示该操作仍拥有应用发布写入占用；false不代表审核通过或公开上架。检查应在原写入进程停止后进行，避免把多个读取时刻的状态当作一个原子快照。

`journalDigest` 仍覆盖原 operations 行的 request hash、state、原始 receipt/result和更新时间，不包含新增预约表，保持旧恢复输入兼容。预约另按原准确 Request/profile 和持久 owner 校验；不能用旧 journalDigest 证明当前占用归属。Request、Profile及其哈希定义未变化，Worker不能指定应用、Track或恢复权限。

未知操作的处理见[Google Play管理员恢复手册](google-play-recovery.md)。管理员必须停止原发布、排除其他写入并保存完整状态目录。恢复只接受原批准 Request、准确 pending 快照、本地原字节、保存的 edit/versionCode和充分的渠道证明。完成与释放同时提交；SQL失败、日志或 owner变化均保留阻塞。状态查询不会清除占用，已有旧版本也不能通过查询进入已知有新发布占用的应用。

## 升级已有状态库

新程序启动时创建发布预约/占用、查询历史/窗口附加表及唯一索引，不改写原 operations 或原批准哈希。旧哈希不可反向还原应用和制品，程序不会猜测归属。升级前停止全部旧查询及发布进程；旧程序不遵守新增查询窗口，不能混用。

1. 停止所有写入，备份完整私有状态目录及仍存在的 WAL/SHM，保留原管理员配置和批准文件。
2. 已完成旧 Google 操作使用**原批准 publish Request**重放，程序校验原 hash、保存的 edit/versionCode及准确结果身份后，无网络登记永久预约。此入口需要原字节可读。准确 status Request也可登记，但还需通过应用占用和其他旧日志检查后才会实际查询；多条旧日志优先用 publish逐项登记。
3. 已停止的旧 pending操作只能通过准确管理员恢复登记应用占用和永久预约。证据不足时保留占用；不会因升级自动重发。
4. `googleReservation` 缺失时，即使重复旧的已完成恢复输入能返回原恢复结果，也不代表已完成新增预约登记；仍需按第2步使用原 publish Request登记。
5. 未登记的旧 Google 收据（有editId或versionCode），或厂商无法辨认的 pending日志，会阻止新的 Google 发布和查询。空 `{}` pending可能来自上传前中断，也可能属于旧 hosted/其他厂商；程序不能仅凭空收据认定没有外部动作。可辨认的Apple/华为/小米/OPPO/vivo收据按其原流程处理；已完成的空 hosted日志不会触发此屏障。
6. 两条旧记录若竞争同一应用的版本、字节或versionCode，不能自动覆盖或合并。第一条准确登记后，冲突旧记录仍未归属，后续新 Google写入继续阻塞，须管理员核对历史和渠道事实。不要删除日志、篡改占用或换状态库绕过。

早期空收据、缺失versionCode、历史冲突及渠道证据不足的情况，当前没有自动解除屏障入口。保留记录并进入人工处理，不把未确认结果计为发布失败后可安全重试。

## 本机验证

```sh
rtk proxy go test -race -count=1 ./internal/publisher ./cmd/publisher -run 'Google|Publisher'
```

可选保留三个原始 SQLite 快照供独立读取，父目录须存在、目标证明目录须不存在：

```sh
rtk proxy env IS_GOOGLE_WRITE_GUARD_PROOF_DIR=/private/NEW_GOOGLE_GUARD_PROOF go test -race -count=1 ./internal/publisher -run '^TestGoogleWriteGuardDurableEvidence$'
```

该测试用真实SQLite、两阶段本机TLS服务、合成Bearer和明确标记为非AAB的字节，保留未知、恢复、后续成功的数据库及原请求/配置。普通测试同样执行全部断言，未设置导出目录时使用临时目录。031恢复验证器另覆盖实际RS256 OAuth与真实CLI；这些本机证明均不代表真实Google、合法AAB、正式身份、商店审核或真机验收。

备份、报告和原始数据库包含业务身份及本机路径，按企业权限保存。公开交付仅提供脱敏摘要。整体版本仍须完成真实渠道发布及独立设备验证，平台才可按全部必选目标判定交付。
