# Android 签名服务备份与恢复

备份同时保存私有密钥、密码、管理员配置、SQLite 签名意图和已签 APK/AAB。恢复保留准确路径、原请求绑定和成功回执，不把旧备份当作最新的控制面状态。此能力由节点管理员离线操作，没有 Web、MCP 或 Agent 入口。

## 部署与目录

当前支持 Linux/macOS 的私有 POSIX 目录与锁；本轮真实恢复运行于 macOS。Windows 不支持这套私有文件操作，Go 程序交叉编译不代表 Windows 原生恢复成功。Linux 的独占目录发布需要 libc 提供 `renameat2`，macOS 使用 `renamex_np(RENAME_EXCL)`；缺少原语时拒绝恢复，不退回可能覆盖已有目录的普通 rename。

从审查过的源码构建独立加密工具：

```sh
rtk proxy go build -trimpath -o work/is-signer-archive ./cmd/signer-archive
```

管理员安装 `is-signer-archive`、`signer-backup.py`、`sign-android-apk.py`、`android_signer_state.py` 和 `android_bundle.py` 到仓库任务不可写的固定工具目录。AAB 另需既有的固定 Java 验证器及按 SHA256 固定的 bundletool。恢复节点必须使用含维护锁和恢复围栏的本版本签名程序；旧程序不能接入恢复库。

签名库及其父目录必须由当前服务用户拥有、权限 0700。一个可备份的目录布局为：

```text
/var/lib/intelligence-substrate/private-signers/       # 0700
  application/                                       # 0700，vault root
    signer.json                                      # 0600
    credentials/                                     # 0700
      upload.p12                                     # 0600
      store-password                                 # 0600
      key-password                                   # 0600
    state/                                           # 0700
      operations.sqlite                              # 0600
      <sha256>.aab 或 <sha256>.apk                    # 0600
```

配置、所有 profile 的 keystore/密码和状态都必须位于显式库根以内；密钥与密码仍须在 state 目录以外。SDK、Java、bundletool 和验证程序放在库外，单独部署。私有路径不允许链接、硬链接或不安全权限，归档名称仅接受有界的普通路径字符。以往 `/etc` 配置、`/var` 状态分开的部署不能直接整体备份；工具不会迁移旧绑定。

备份要求所有历史意图仍能匹配当前准确 profile。修改管理员路径、别名、证书或工具配置会改变签名绑定；应先保存原库并按企业程序另建签名实例，不能删掉历史意图来消除冲突。当前没有证书轮换或自动迁移功能。

## 生成恢复密钥并备份

使用独立的 32 字节随机恢复密钥，不接受交互密码或把密码写入参数：

```sh
rtk proxy /opt/intelligence-substrate/trusted/is-signer-archive keygen \
  --output /secure/recovery-keys/application.key

rtk proxy python3 /opt/intelligence-substrate/trusted/signer-backup.py backup \
  --vault-root /var/lib/intelligence-substrate/private-signers/application \
  --config /var/lib/intelligence-substrate/private-signers/application/signer.json \
  --key-file /secure/recovery-keys/application.key \
  --crypto-tool /opt/intelligence-substrate/trusted/is-signer-archive \
  --output /secure/encrypted-backups/application-2026-11-06.enc
```

示例中的恢复密钥和输出目录须预先具备当前用户的 0700 权限；文件独占创建为 0600，拒绝覆盖。密钥和归档必须在库外，检查实际解析位置，不能用父目录链接绕回库内。生产环境还需单独安排密钥与密文的异地、分权保管；本工具不会自动完成托管或保留策略。

签名程序持有共享维护锁，备份持有独占锁。先停止向此节点分派新任务；已有签名尚在执行时，备份立即返回 busy。备份期间到达的签名请求会在新意图前被拒绝，需要操作员按原任务状态核对，不能盲目改 key 重试。只读查询保持既有行为。

SQLite backup API 取得一致快照，包含 signing/reconciliation_required 等不明意图。每条成功记录核对请求、profile 绑定、证书、输入、原回执和内容寻址文件的准确摘要/大小。未知状态条目、缺包、损坏包、链接或中断遗留临时目录均使备份失败。WAL/SHM 和锁文件不作为恢复数据，未关联的合法内容寻址包会保留。

明文 tar 仅短暂存在于库父目录下的私有临时目录；正常退出会清除。加密使用标准 AES-256-GCM、随机 nonce，版本头也参与认证。原始 tar 最大 512 MiB，文件合计最大 400 MiB、单文件最大 256 MiB、文件数量最大 10,000。达到限制会拒绝，不截断历史记录。进程被强制终止可能留下私有 staging，管理员应先清点再处理；不要将这些目录加入公开制品。

## 同路径恢复

在隔离恢复节点先部署相同的管理员程序和外部工具。目标路径必须与加密清单内的原绝对库根完全一致，且目标不存在；工具不会合并、覆盖、自动隔离或删除原目录：

```sh
rtk proxy python3 /opt/intelligence-substrate/trusted/signer-backup.py restore \
  --destination /var/lib/intelligence-substrate/private-signers/application \
  --archive /secure/encrypted-backups/application-2026-11-06.enc \
  --key-file /secure/recovery-keys/application.key \
  --crypto-tool /opt/intelligence-substrate/trusted/is-signer-archive
```

先认证整个密文，再在私有 staging 内逐项写入普通文件；不使用 extractall。完整清单、配置、文件集合、摘要/大小/模式、SQLite 结构与内容、成功回执和包关系全部核对后，写入恢复围栏，fsync 并原子独占发布整个目录。错误密钥、篡改、缺包、危险归档或被并发占用的目标均不能发布恢复库。配置和回执保留原字节/路径；SQLite 文件布局可能因一致快照而变化，逻辑行必须完全一致。

## 恢复后的授权边界

恢复库标记为 `successful-replay-only`：

- 可查询原意图；同一准确请求的已签成功操作仅返回原包和原回执。
- 新 key、备份中没有的 key、signing/失败意图均阻塞，不调用签名程序。即使某次签名是在备份后完成，也不能通过旧快照再次签名。
- 配置、私有文件或逻辑日志变化会使恢复围栏检查失败。
- 没有清除围栏、改为 active、清空日志、补记成功或重置意图的接口。恢复库也不能重新备份成一个看似活跃的新库，应保留原加密归档。

恢复成功后仍需核对控制面任务、Worker 上报、制品和备份之后的全部操作。建立新的活跃签名实例及新的批准任务是管理员与企业交付治理步骤；本增量未提供自动激活许可。不要删除围栏或换旧程序绕过核对。

## 本轮证据与限制

实际演练用新的临时 RSA 上传身份，对此前准确 unsigned AAB 签名；加密备份后隔离本轮原目录，在同路径恢复，核对原成功回执、AAB 字节和 SQLite 两条记录。恢复证书摘要不变，keytool 生成 CSR 后由 OpenSSL 验证其签名，并核对 CSR/证书公钥相同，证明恢复的私钥可用。独立 Java 检查 AAB 签名覆盖全部 77 个 payload 文件，jarsigner 的实际调用次数保持 1。恢复密钥错误、密文篡改、已有目标、未知和不明操作均被真实命令拒绝。恢复的企业托管、异地灾备、HSM、生产隔离和正式发布仍需另验。

本轮最终检查通过 106 项 Python、新工具 4 个顶层/19 项含子场景 Go race、全包 vet/build、gofmt 和模块 tidy-diff。全平台 Go 和浏览器结果保留此前轮次，没有作为本轮重新执行；本次未改控制面 API 或工作台代码。Linux/Windows 程序及 Windows 拒绝路径测试只做交叉编译，不代表目标系统运行成功。

本轮不重新构建 Flutter 应用、不运行模拟器或真机、不提交商店，也不部署原工作台。Apple SDK 最近仍返回许可错误 69，需要用户本人处理。完整多端正式交付 P3 保持开放。既有配置与协议见 [Android 可信签名](android-signing.md) 和 [Google Play AAB](android-aab.md)。
