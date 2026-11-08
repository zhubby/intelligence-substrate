# OPPO 普通应用版本更新

`oppo-store` 驱动连接 `android-oppo`：核验已批准 release APK 和审核资料，上传准确字节，提交普通应用的版本更新，跟踪异步处理、审核和准确版本上架。本地 TLS/SQLite 与 SDK 拒绝用例不能证明真实企业账户、正式签名成功、渠道兼容性、上架或真机业务验收。

## 首次交付和权限

企业管理员须先在 OPPO 开放平台创建普通应用，填写资料、资质并完成首次发布。本连接器只支持 **已全量上线、未冻结/转让/审核中** 的企业普通应用（type=7，is_business=0），新 versionCode 严格高于当前版本。首次发布、下架/拒审后的特殊处理由门户办理，并在平台按原有人工发布流程登记报告；不能用 API 发版冒充创建。

“新链路 API” `/gameinstaller/packet-apks` 的作用是包体预录入，不包含本产品要求的完整普通应用交付流程。普通 API 没有沙盒；本轮没有向生产商店发送测试包。

发布节点由管理员维护，只持有 release/publish 权限，不执行生成仓库代码或承担独立质量角色。安装固定 Android SDK build-tools/JDK，固定包名、AppID 和正式 signer，使用独立状态目录及可备份的本地磁盘。运行环境本轮面向 Linux/macOS；Windows 交叉编译不代表 Windows 发布节点验收。

## 固定配置与资料

参照 [publisher.oppo.example.json](../deploy/publisher.oppo.example.json)、[worker.oppo.example.json](../deploy/worker.oppo.example.json) 和 [oppo-metadata.example.json](../deploy/oppo-metadata.example.json)。替换全部占位值、分类、联系信息和实际资料；示例摘要和大小故意不能通过发布校验。

`appId` 是 OPPO 数字应用 ID，`bundleId` 为唯一 Android 包名。`expectedSigner` 为正式证书的 64 位小写 SHA256。`apksigner` 校验唯一 signer，拒绝已知 Debug 证书；`aapt` 校验 package/versionName/versionCode 和非 debuggable，versionCode 限制 1–2100000000。APK SHA256/大小、需求、计划和代码提交沿用平台的准确发布审批。

私有凭据结构如下，通过企业秘密配置渠道写文件，不把真实值放在命令行、仓库、任务或日志：

```json
{"clientId":"REPLACE_CLIENT_ID","clientSecret":"REPLACE_CLIENT_SECRET"}
```

当前文档 client_id 为 19 位，本实现兼容旧 18 位；client_secret 为 64 位。凭据和 metadata 必须是普通、权限 0600 的服务文件。stateDir 必须为 0700 普通目录，publisher.db 为 0600 普通文件，拒绝符号链接和公开权限；新数据库按 0600 创建。已有公共目录/数据库会明确拒绝，管理员需在检查原内容后自行配置正确权限，程序不改变已有权限。

metadataDigest 是审核通过的 **整个 JSON 文件原字节** 的 SHA256，并纳入持久 operation 绑定。清单包含应用名称、二/三级分类、无标点且不超过 13 字的 summary、至少 20 字的 detailDesc、至少 5 字的 updateDesc、HTTPS 隐私链接、最多 400 字的测试说明、商务联系人、年龄和适配设备；固定 `online_type=1` 审核后立即全量发布，单 APK `cpu_code=0`。

图标须 512×512 PNG；竖屏截图 3–5 张、1080×1920 PNG/JPEG；版权证明为图片，可通过 special 数组增加特殊资质图片。每张图小于 1 MiB，绑定绝对普通路径、SHA256 和字节数，完整解码后私有暂存。可选 `icp` 为备案号/网址，`adaptiveType` 1 为横竖屏自适应、2 为平行视窗。年龄/分类和资质正确性由负责人确认，平台不替企业判断。未建模的 PDF/压缩资质、游戏、横图、视频、多 APK、分阶段及定时发布应使用门户流程，不能通过附加任意字段绕过。

所有 APK/图片都经官方上传接口取得回执 URL；不接受自有 CDN 素材地址或从上游静默复制的商店资料。返回的资源 URL 只传给官方发版接口，不由本机访问。官方示例包含 HTTP 图片回执，连接器允许这种回执文本，但实际 API/文件上传请求始终 HTTPS。

## 网络和凭据边界

API origin 固定 `https://oop-openapi-cn.heytapmobi.com`。签名对公共与业务参数按 ASCII 排序后的原始 `key=value` 拼接做 HMAC-SHA256，再用 URL 编码传输。token 接口按官方协议在 HTTPS GET 查询中携带 client_id/client_secret，因此企业代理不得记录完整查询串；程序错误不输出请求 URL 或响应正文。

uploadHosts 由管理员核实，为 **准确 HTTPS authority**，最多 8 项，不支持通配、API origin 或非 443 端口。获取到的上传 URL 必须匹配该名单且不带原始 query/userinfo/fragment；公共 token/timestamp/api_sign 仅发给这一明确可信上传主机。每文件重新取得一次性 sign，multipart 文本显式 UTF-8；client_secret 不进入上传。禁止 Cookie、重定向和自动重放，上传使用磁盘暂存体与准确 Content-Length。

token 的 expire_in 是绝对 Unix 时间，按 client_id 与凭据摘要保存在私有 SQLite 表，提前 5 分钟刷新；事务串行签发防止多进程反复刷新。token/一次性 sign/资源 URL/上游 app_secret 等不进入 operation 收据或平台执行日志。备份 publisher.db 时必须按凭据保护，不能分享数据库作普通诊断附件。

## 持久生命周期

1. 制品和资料全部核验后才访问渠道。查询当前普通应用，要求准确身份、已全量上线和严格更低的旧 versionCode。
2. 同事务记录 operation、包名独占锁和永久版本占用。持锁后再检查官方状态；若权限或状态变化，不做外部写，保留明确的检查阶段供核对。
3. 各文件先保存获取上传配置的 intent，再保存字节上传 intent；每张图/APK 使用独立一次性 ticket，验证回执 MD5/大小/扩展名与本地暂存字节相同。MD5 只用于官方协议，批准依据仍为 SHA256。
4. 保存 submit_intent 后提交完整受审核资料。只有 errno=0 且 success=true 才算异步任务接受，返回 processing；同事务完成本地收据并释放包锁。永久 versionCode 占用仍保留，防止相同包换 key 再提交。下一次版本更新还须看到之前本地接受版本已出现在官方的可编辑准确版本中，避免异步旧数据窗口。
5. 等待时结束执行，平台用短 `statusProviders` 任务查询。task-state=1 为处理中，3 为处理失败；2 只代表资料处理成功，然后按同一 version_code 查询 app/info。没有自动 continuation 或重提。
6. 审核状态 1/4/00/11/-22 对应等待审核，2/6/7 对应等待上架，3/5/-11/444 拒审，22/222 冻结/下线。未知值返回 unchanged + MANUAL_VERIFICATION_REQUIRED，保留原状态并停止自动轮询；可由人工核对后再查询。
7. published 要求 appId/package/普通类型/versionCode/versionName/versionType=2/APK MD5/大小一致，未冻结/转让/资料审核且为全量发布，audit_status=111、state=1，并且唯一匹配 version_id 的上架记录有效；旧版本在架或审核通过均不足。
8. published 仍需独立质量人员上传同一制品/渠道版本的发布后 device 证据，再 `releases.verify`。全部声明必选端/渠道 verified 才能 delivered。

## 断线与未知结果

```sh
rtk proxy ./bin/is-publisher -config /etc/substrate/publisher.oppo.json -inspect OPERATION_KEY
```

收据仅保存阶段、版本号/名称和 APK MD5；externalId `oppo:<appId>:<versionCode>` 是本地精确跟踪标识，不是上游 task ID。

上传配置、字节上传或发版的超时/丢响应/异常 JSON/API 错误，以及本地完成事务失败，均保留 pending、包锁和版本占用。status 不采用 pending 写入，普通 retry/新 key/重开进程不能重放。管理员可使用[国内渠道核对](store-recovery.md)的 `-reconcile-store`，在停止原进程后只读验证final submit intent的准确APK与任务状态；处理中/失败任务也必须有准确制品证明。证明不足保持阻塞，没有直接解锁或从数据库删除记录的命令。

暂停这个发布目的地，由授权负责人结合 -inspect、平台原审批和 OPPO 门户的准确包/版本调查；备份完整私有状态，保留操作证据。不要删除状态、更换状态目录、换 key 或修改数据库尝试重提。平台人工核对不会自动解除 publisher 的未知操作锁。包锁只协调同一状态库，企业还须排除其他账号、脚本或另一状态库的并行发布。

## 官方文档

- [能力接入](https://open.oppomobile.com/documentation/page/info?id=10998)：现行手动创建、token、签名规则。
- [发布版本](https://open.oppomobile.com/documentation/page/info?id=10999)、[文件上传](https://open.oppomobile.com/documentation/page/info?id=11003)。
- [获取任务状态](https://open.oppomobile.com/documentation/page/info?id=11001)、[普通包详情](https://open.oppomobile.com/documentation/page/info?id=11004)、[审核状态表](https://open.oppomobile.com/documentation/page/info?id=11176)。
- [API FAQ](https://open.oppomobile.com/documentation/page/info?id=12442)：无沙箱、先创建/首次发布、必须官方上传资源。
- [新链路包体预录入](https://open.oppomobile.com/documentation/page/info?id=13438)。

本地官方缓存来自当前文档接口，旧 wiki 返回的内容可能较旧。生产 HTTP 参数表达、凭据/上传 host、完整资质、真实返回字段和审核结果仍需企业账户复验。
