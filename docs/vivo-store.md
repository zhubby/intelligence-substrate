# vivo 普通应用更新

`is-publisher` 的 `vivo-store` 驱动为 `android-vivo` 提供现有普通应用的 APK/可选图片直传、同步更新和受限只读状态查询。当前实现通过本地 TLS/SQLite 契约验证；尚无真实企业账户、沙箱提交、正式 release 签名成功、商店上架或真机验收证据。

## 官方依据与前置条件

- [服务开通](https://dev.vivo.com.cn/documentCenter/doc/326)、[公共接入与沙箱](https://dev.vivo.com.cn/documentCenter/doc/327)、[官方 Go 示例](https://dev.vivo.com.cn/documentCenter/doc/648)。企业管理员在 API 管理中申请 access_key/access_secret；正式和沙箱凭据分别申请。
- [同步更新](https://dev.vivo.com.cn/documentCenter/doc/343)、[APK 上传](https://dev.vivo.com.cn/documentCenter/doc/332)、[icon](https://dev.vivo.com.cn/documentCenter/doc/329)、[截图](https://dev.vivo.com.cn/documentCenter/doc/331)。
- [应用详情](https://dev.vivo.com.cn/documentCenter/doc/346)、[状态字典](https://dev.vivo.com.cn/documentCenter/doc/344)、[公共返回码](https://dev.vivo.com.cn/documentCenter/doc/330)。

先在门户完成普通应用创建、版权/特殊资质/备案、合同和相关基础资料。现行目录只有 `app.sync.update.app` 更新接口，不假定已删除的旧创建文档仍有效。支持现有已审核在架、被拒或撤审的普通应用，用更高 versionCode 更新；草稿、审核中、已审核待上架、未知或不完整状态均阻塞。拒审时先核对门户原因与资质，再修复、构建更高版本并重新审批。

暂不实现首次创建、游戏、分包、分阶段、定时上架、特殊资质/PDF上传或 URL 下载包。未在本次审核清单出现的资料保留门户已有值，不从查询结果复制任意文本或附件。需变更其他资料时，由授权人员走门户并重新核对对应版本。

## 管理员配置

复制 `deploy/publisher.vivo.example.json`，替换项目/目标、包名、release signer SHA256、绝对 SDK/私有路径和准确 metadata 文件原字节 SHA256。包名 AppID 与 BundleID 必须一致。Java 由管理员配置 JAVA_HOME，不从仓库启动器继承任意进程环境。

`environment` 必须显式为 `production` 或 `sandbox`。生产入口固定 `https://developer-api.vivo.com.cn/router/rest`，沙箱固定 `https://sandbox-developer-api.vivo.com.cn/router/rest`。不能配置任意 API/上传 URL。沙箱仅供独立 CLI 调试：使用独立凭据、配置和状态目录，不将沙箱 provider 接到生产发布 Worker，不把沙箱截图/回执作为正式发布证明。正式接口能力和频限以当前账户与官方文档为准。

凭据文件由服务所有者保存，普通文件、0600，内容：

```json
{"accessKey":"REPLACE_ENVIRONMENT_ACCESS_KEY","accessSecret":"REPLACE_ENVIRONMENT_ACCESS_SECRET"}
```

密钥不进入仓库、Agent 上下文、命令参数或日志。publisher 不缓存 token，也不把凭据和商店测试密码存入操作收据。状态目录由受信发布服务独占，建议0700；限制数据库和备份访问。

`deploy/worker.vivo.example.json` 注册固定 publishers 和独立 `-status-only` 入口，无 continuation provider。Worker 的发布命令只在批准绑定下调用；应用仓库不能改目标、凭据或资料。

## 审核清单与字节绑定

`deploy/vivo-metadata.example.json` 是占位样本，不可直接用于生产。每次资料变化先审核，再更新 metadataDigest 并重新发起符合审批的候选操作。

- 必填 updateDesc：5–200 Unicode字符；compatibleDevice：1手机/2手机和平板/3平板；privateSelfCheck：审核过的 HTTPS 隐私自检链接。
- 可选 detailDesc：50–1000字符；remark：10–200字符；simpleDesc：1–50字符；customerService：3–254字符。后两项仅做本地界限限制，真实渠道规则仍须沙箱/门户复验。
- 可选 rateAge：3/8/12/16/18。testAccount 是最多5项的私有数组，每项 `testAccount`、`testPassword` 均非空。为商店审核准备受控测试账号；不可使用企业生产凭据。
- 可选 icon：PNG 方图，边长256–512，少于500 KiB；不能自动判断美术上的直角图标要求，需人工审核。
- 可选 screenshots：声明时必须3–5张，1080×1920 PNG/JPEG，每张少于2 MiB。
- 每张图声明绝对普通文件路径、SHA256和准确 bytes。先完整解码和暂存，再开始网络访问；不接受软链接或仅可信图片头。APK同样先暂存并以真实SDK核对包名、版本、单release signer及非debug状态，再上传准确字节。平台输入上限512 MiB比官方APK上限更严格。

元数据/凭据解析拒绝重复字段、无效UTF-8、未知清单字段和多余JSON。更改原图片/包路径不会替换本次已审核的暂存内容。清单不接受流水号或外部附件URL注入。

## 协议与持久状态

公共参数+业务参数按 ASCII 键排序，对原始UTF-8 `key=value`、`&`连接串做HMAC-SHA256并小写hex；file不参与签名。timestamp使用毫秒，sign_method固定HMAC-SHA256。公共说明与官方Go示例支持该算法名称，但局部接口表仍写hmac、Go示例时间戳使用秒。实现采用公共说明的毫秒值，不会自动切换签名或重放写入；须真实沙箱复验。

实际POST固定HTTPS入口。上传使用磁盘multipart、UTF-8文本字段和准确Content-Length，禁止Cookie、重定向和自动传输重放。所有接口要求HTTP200、code=0且subCode="0"，不把缺失/可疑成功码当确认；这是比部分文档“可选subCode”更严格的确认策略，需要实际账户验证。

预检后查现有包，事务保留 pending operation、环境+包独占和永久环境+包+versionCode占用；获得锁后再次查权限/状态。每项上传前和最终同步更新前保存intent。APK回执必须吻合包名/版本号/名/MD5，图片必须吻合包名且流水号唯一有效。MD5只用于官方线协议，业务审批使用SHA256。

明确同步更新成功才原子保存 submitted receipt/result并释放包锁，versionCode占用不释放。响应丢失、错误、截断、未知回执或本地SQL失败都保留pending/锁；同key、换key、并发和重开均不能重放。已接受结果原key重读不访问网络；上游暂时还显示旧版本时，不能启动下一更新。

## 状态、门户证明与独立验收

成功同步更新仅 `awaiting_review`。外部标识 `vivo:production:<package>:<versionCode>` 或 `vivo:sandbox:...` 为本地精确跟踪标识，官方没有返回该格式任务ID。

只读status必须匹配已完成操作、批准绑定、环境与准确标识。对精确包名/普通应用/版本号/名/大小的生产观察：待审→awaiting_review、审核不通过→rejected、审核通过待上架→awaiting_release、已下架→halted。撤审、草稿、不完整、旧/不同版本或未知状态进入人工核对。

详情的saleStatus没有明确在架APK版本或摘要；即使最新版审核通过、应用级状态在架，也可能仍是旧包在架。因此驱动不自动返回published，而返回unchanged + MANUAL_VERIFICATION_REQUIRED，停止自动轮询、释放执行资源。沙箱始终只输出人工核对提示，不能确认生产上架。

授权人员在正式门户核对包名/versionCode/versionName、对应审核与上架记录，上传同目标/提交的report，用既有`releases.transition`登记published并关联proofArtifactId。随后独立QA另行记录同制品、同externalId的真机publication evidence；旧QA、发布执行者自验或缺少证明都不能delivered。门户证明和真机证明是两份不同用途的证据。

## 未知操作处理

先停止该包的其他发布写入者，备份完整状态库及WAL/SHM，并保留配置/批准绑定/制品/清单摘要。通过受控CLI `-inspect <operationKey>`只读检查阶段和附件流水号；不要把含私有路径的日志公开。

在门户核对附件、准确版本和审核任务，记录观察时间与企业决定。管理员可按[国内渠道核对](store-recovery.md)使用 `-reconcile-store`，绑定原日志、停止声明、准确环境和带摘要的实际后台报告/附件，恢复已接受的 final submit intent；结果仅为processing / PORTAL_ACCEPTED_UNVERIFIED，不证明正式上架。仍没有自动解锁或强制重提命令；不删除记录、不复制新库、不换key、不降低门禁。一个共享受控状态库协调自动写入者，其他门户/脚本并发操作需企业管控。

本地契约、程序编译和诊断APK拒绝不能代替真实沙箱、正式签名、企业账号、商店上架、物理设备或两个正式应用的P3验收。
