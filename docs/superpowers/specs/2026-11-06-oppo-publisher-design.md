# OPPO 普通应用版本更新连接器

本设计实现已批准产品规划中的 OPPO 渠道增量。完整首次交付仍包含企业人员在 OPPO 门户完成创建、资质和首次发布；连接器只自动更新已存在且可编辑的企业普通应用。整体 P3 不因本增量完成而通过。

## 官方依据与边界

2026-11-06 从官方当前 `POST /oneoppoapi/doc/detail`（JSON `doc_id` 整数）读取并缓存文档 10998、10999、11003、11004、11001、11176、12442、13438。旧 `/wiki/doc/detail` 的 10998 内容与当前版不同，不作为现行前置条件依据。

- 10998 和 12442：先手动创建/首次发布；普通 API 无沙箱。现行 client_id 为 19 位，旧版为 18 位；兼容这两种长度。
- 13438 的 `/gameinstaller/packet-apks` 是包体预录入，不代表正式应用创建或发版，不用于本连接器。
- 使用固定正式域名 `https://oop-openapi-cn.heytapmobi.com`，GET token 的 expire_in 是绝对 Unix 时间。业务请求对排序后的原始参数计算 HMAC-SHA256。凭据不得出现在错误或收据中。
- 每个 APK/图片经官方获取一次性上传配置后上传，再使用返回 URL 发版；不接受用户自有 CDN 素材地址。
- `/resource/v1/app/upd` 成功只表示异步任务接受。`task-state` 的 1/2/3 表示处理中/处理成功/处理失败，不是审核或上架。
- `app/info` 按精确 version_code 观察；审核状态 111 才是上线。必须同时匹配 app_id、包名、版本名称、版本号、APK MD5/大小和该 version_id 的上架记录，且全量发布、未冻结/转让；应用级 state=1 单独不能证明候选版本上线。

## 管理员与产物边界

固定 profile：`oppo-store` / `android-oppo`，数字 appId、bundleId、release signer SHA256、绝对 SDK/凭据/metadata 路径、metadataDigest、精确 uploadHosts。禁止通配域名、HTTP、用户信息、重定向、Cookie 和仓库提供的目标。上传 URL 必须匹配管理员 HTTPS host allowlist；按公共鉴权规则将 access_token/timestamp/api_sign 只发给该可信上传主机，multipart 为一次性 sign/type/file。client_secret 仅用于官方 token 接口及本机签名，不传给上传主机。

metadata SHA256 纳入原有 requestHash。清单包含应用名、分类、简介、介绍、更新说明、隐私链接、测试说明、商务联系人、年龄和设备适配、图标、3–5 竖屏截图、版权证明，可选 ICP 和特殊资质图片。所有附件是本机绝对路径 + SHA256 + 字节数，并在网络访问前完整暂存、验证图片。图标 512×512 PNG；截图 1080×1920 PNG/JPEG；单图小于 1 MiB。首版固定审核后立即发布、单 APK cpu_code=0，不提交游戏、定时/分阶段/多 APK 和未建模资料。缺少资质须补充清单，不能从上游静默复制。

实际 SDK 验证包名、versionName/versionCode、唯一匹配 release signer，拒绝 Debug 证书和 debuggable；制品 SHA256/大小绑定现有 release approval。

## 持久化与协作

SQLite 在同一事务内保留包名独占锁、永久版本占用和 pending operation；持锁后复查现有应用可编辑、旧版本严格更低。上传配置、每个文件上传和发版均先持久化 intent。结果不明时保留锁和收据阶段，同 key 或新 key、并发和进程重开都不能重放。外部错误只记录安全错误码，原始 app/info 含 app_secret 等不落盘。

确认发版接受后原子记录 complete + `processing` 结果并释放本地包锁。版本占用禁止同 versionCode 换 key；下一次更新须看到之前本地已接受版本在官方已全量上线，排除异步旧数据、处理中/审核中的应用；其他写入者由企业运维排他管理。status 只读，不创建意图、不上传、不提审。pending/uncertain 不自动采用外部状态或解锁。

token 使用专用私有 SQLite 表按 client_id 与凭据摘要缓存；事务串行签发，提前 5 分钟过期，避免每次查询刷新 token 导致其他调用失效。状态目录/数据库分别强制 0700/0600 普通路径，token 不进入 operation 收据/结果或平台日志。

## 状态与验收

任务接受返回 processing。查询任务 state=1 继续 processing，3 返回 rejected（固定说明）；2 后查询精确版本。审核中/资质中返回 awaiting_review，审核通过/定时待上架返回 awaiting_release，拒审返回 rejected，冻结/下线返回 halted；未知或证据不完整返回 unchanged + MANUAL_VERIFICATION_REQUIRED，不推测通过。相同外部跟踪 ID 形如 `oppo:<appId>:<versionCode>`，明确为本地精确目标标识，不冒充官方 task ID。

published 仍需平台独立的、绑定相同制品/渠道版本的 device evidence 才能 verified/delivered。测试使用真实本地 TLS 与 SQLite，远端商店响应和 SDK 成功样本替代；另执行实际 SDK/CLI 的拒绝用例。真实企业账户、签名成功、正式商店兼容性和真机业务验收另列，不能由本地协议测试推导。

## 保留条件

主线程顺序工作、所有命令 RTK；不提交/推送、不改 staging、不使用付费模型或真实商店写操作。保留 121 份既有 outputs、原 18080/PID 36063、现有数据库/凭据/工具链。新增交付源包、手册、报告与证据并逐字节读回。Apple SDK 许可须由用户本人处理。
