# 小米应用商店发布

`xiaomi-market` 驱动为 `android-xiaomi` 提交新应用或更新单个 release APK。它验证准确制品、管理员审核的资料/截图及开发者账户权限，再推送获批字节。推送被接受只返回 `awaiting_review`。官方没有审核/上架状态查询，也没有沙盒；本轮验证使用本地 TLS，未操作真实商店账户。

## 节点和权限

独立可信发布节点安装 publisher、固定 Android SDK build-tools 和 JDK，只授予 release/publish 权限，不运行仓库工具或充当独立质量节点。publisher、配置、SDK、凭据和本地 SQLite 状态均由服务管理员管理。不要把私有目录放进公开分发卷、共享可写文件系统或生成代码可写的目录。

同一包的所有发布进程必须使用同一 publisher 状态库。SQLite 包名锁不能协调另一状态库、其他平台或人工后台操作；企业在操作期间应将该包的发布写入交由本服务独占。上游查询与推送之间没有条件写入保证，最后一次查询后外部操作员仍可能更改商店信息。

参照 `deploy/publisher.xiaomi.example.json`、`deploy/worker.xiaomi.example.json` 和 `deploy/xiaomi-metadata.example.json`，替换所有占位值。示例不能直接用于发布。`appId` 与 `bundleId` 必须是相同 Android 包名；`expectedSigner` 为正式证书的 64 位小写 SHA256，SDK 路径必须绝对固定。`metadataDigest` 是管理员确认的整个 JSON 文件的 SHA256；重新排版也会改变它。

签名、包名、versionName、versionCode 和 debuggable 检查复用 Android SDK 验证：只接受单一获批 release signer，拒绝已知 Android Debug 证书；包的营销版本须与批准版本相符，versionCode 为 1–2100000000，APK 上限 512MiB。生成蓝图的诊断 APK 不满足正式签名要求。

## 私有凭据与商店资料

凭据文件和资料清单必须为服务所有者的常规私有文件，权限 0600，父目录保持私有。凭据通过秘密配置渠道写入，格式为：

```json
{"userName":"REPLACE_DEVELOPER_EMAIL","accessPassword":"REPLACE_PRIVATE_API_ACCESS_PASSWORD","publicCertificatePEM":"-----BEGIN CERTIFICATE-----\nREPLACE_XIAOMI_RSA_CERTIFICATE\n-----END CERTIFICATE-----\n"}
```

`accessPassword` 使用开发者后台提供的接口访问密码/私钥文本，不回退到登录密码，不重置企业账户。`publicCertificatePEM` 是该企业管理员预置的小米 RSA 加密证书，不是 TLS CA；当前兼容官方示例中的 1024 位及以上 RSA key，最大 8192 位。更新 API 密码不改变原操作身份。不要把真实凭据粘贴进任务、命令行、源码或日志。

资料清单最多 64KiB，只接受明确结构：`synchroType` 为 0（新增）或 1（更新包）；`appInfo` 包括 appName、category、keyWords、desc、updateDesc、brief、privacyUrl，可选 publisherName 和 `testAccount:{"zh_CN":"审核用测试账号说明"}`。程序将 testAccount 转为上游要求的嵌套 JSON 字符串。包名和版本来自已验证 APK，资料不能覆盖。拒绝自定义 API host、onlineTime、双 APK 和只更新内容的类型。

新增要求分类、关键词、说明、简介和三至五张截图；版本更新要求 updateDesc，截图可省略。两者都要求图标、应用名和 HTTPS 隐私链接。每个附件使用绝对路径、准确 SHA256 和字节数；只接受可完整解码的 PNG/JPEG，单个最多 8MiB、每边不超过 8192 像素、总计不超过 16777216 像素。此为本连接器的防护上限，不代表商店尺寸要求；实际图标、截图、分类、资质、隐私说明和账号可用性仍由发布负责人核对。

管理员在批准资料后计算各附件及清单摘要，更新固定配置，并将该资料版本纳入企业发布审核。控制面的制品审批仍绑定准确提交、APK、需求与方案；该清单由可信服务管理员额外审核，当前没有工作台商店资料编辑/审批界面。改动清单摘要或发布目的地不能复用旧 operationKey 的结果。

## 协议与生命周期

1. 在私有临时目录暂存并验证 APK；完整核对、解码并暂存资料附件。任何素材或凭据错误都在渠道请求及 journal 预约之前失败。
2. 固定向 `https://api.developer.xiaomi.com/devupload/dev/query` 发 POST，核对相同账户/包名的 create/updateVersion 权限。更新必须有匹配包名和更小的既有 versionCode，不能以相同版本覆盖或认领另一账户的包。
3. 原子写入包名 reservation 和 `permission_check` 阶段，然后持锁重新核对上游权限。第二次检查不通过时不会推送，保留该停止阶段与锁供管理员核对。
4. 在任何推送前持久记录 `push_intent`。向固定 `/dev/push` POST：RequestData 使用 JSON 对象形式的 appInfo，发送已暂存 APK、图标和截图。对 RequestData 及每个文件计算官方要求的 MD5；与接口密码组成 SIG 信封，按 RSA PKCS#1 v1.5 分块加密，再编码为小写十六进制。批准制品与素材始终用 SHA256 校验，MD5 仅用于上游协议。
5. multipart 使用私有磁盘文件、准确 Content-Length 和不可重放的独立 HTTP/1 连接；不继承 Cookie jar、不跟随重定向。query 超时 45 秒，push 超时 5 分钟。响应上限 1MiB，必须为完整单个 UTF-8 JSON 对象；拒绝重复字段、超深结构、缺失/无效结果码及尾随内容，不记录上游自由文本错误。
6. result=0 后在同一 SQLite 事务中写入 submitted 收据与 awaiting_review 结果并释放包名锁。`xiaomi:包名:versionCode` 为本地追踪 ID，不是上游审核单号。相同操作返回持久结果，不再次发请求；新的版本仍需新的批准操作、更大 versionCode 和新获得的上游更新权限。

## 人工渠道核对与独立验收

`-status-only` 不访问商店，核对持久的准确操作后返回 `unchanged`、`MANUAL_VERIFICATION_REQUIRED` 和人工核对说明。平台保留待审状态，停止后续自动轮询；人工仍可请求一次只读说明。包查询只描述最新推送包及编辑权限，不能证明审核通过、实际在架或已安装。没有自动 continuation 或自动推断上架。

发布负责人在开发者后台核对包名、versionCode、审核和公开结果，上传绑定同一目标/仓库/提交的实际报告附件，再执行 `releases.transition` 或显式 `releases.reconcile`。发布记录中的本地追踪 ID 需要保留，并在附件中对应真实后台结果。Agent/Worker 不能冒充人工核对，缺报告不能记录 published。

published 之后，独立质量人员还须在真实 Android 设备核对同一版本与业务流程，上传实际报告，以 `evidence.record` 绑定 `purpose:publication`、准确 artifactId/externalId 和实际目标环境，再执行 `releases.verify`。验证证据须晚于发布记录，验证者不能是发布执行者。只有所有必选端/渠道都 verified，整体交付才完成。

## 中断与运维

丢响应、非成功 HTTP/API 结果、含糊 JSON、进程中断或本地完成事务失败时保留 pending 和包名锁，不自动重放。使用可信 `is-publisher -config /etc/substrate/publisher.xiaomi.json -inspect OPERATION_KEY` 查看阶段与摘要，输出不含 SIG、接口密码或账号。管理员可按[国内渠道核对](store-recovery.md)，用准确日志、停止声明和带摘要的实际后台报告/附件，恢复已接受的 final push intent。结果仅为processing / PORTAL_ACCEPTED_UNVERIFIED，不构成API审核或上架证明。平台改ready不删除publisher锁；没有自动解锁或继续写入入口，不删除状态库、换库或换key绕过核对。

备份时先停止发布写入，完整备份状态库及私有资料，并单独保管凭据；恢复后核对真实外部结果，数据库恢复不能证明商店当前状态。无自动撤审、定时上线、分阶段发布、账号初始化、资质代填或渠道 webhook。真实企业 API、正式签名成功、审核、公开上架和真机业务验收仍需集成验证。

## 本地复验

```sh
rtk proxy go vet ./...
rtk proxy go test -count=1 -race ./...
rtk proxy go test ./internal/publisher ./internal/platform -run Xiaomi -count=1
```

完整 PostgreSQL 场景须显式将 `IS_TEST_POSTGRES_URL` 指向新建的专用测试库；不能指向现有业务库。本轮真实 SDK/CLI 使用已存在的诊断 APK 验证拒绝路径，没有正式签名成功样本。协议成功路径只用本地 TLS 与合成账户，人工门户/真机证明在状态测试中也是合成样本。

## 官方依据

[应用自动发布接口操作指南](https://dev.mi.com/distribute/doc/details?pId=1134)，官方内容接口 `https://dev.mi.com/uiueapi/doc/getArticle?aId=1134`，本轮缓存返回 updateTime=1705556832880。字段表将 appInfo 称为 JSON 字符串，但完整官方 curl 示例采用对象；当前实现遵循该完整示例，真实账户兼容性仍需验收。FAQ 明确说明暂无沙盒以及审核/在架状态查询接口，本轮不会为测试调用生产推送。
