# vivo 发布连接器设计

## 范围与授权

继续执行已批准的平台规划，补齐必选 vivo 渠道。复用现有 trusted publisher、Worker 和发布门禁，主线程顺序实施；不提交、不推送、不改变暂存、不调用付费模型、不写真实商店。现有 125 份交付文件与 18080/PID 36063 保留。

本增量支持已在企业 vivo 账户下创建的普通应用版本更新。首次创建、游戏、分包、分阶段、定时发布和特殊资质准备通过门户处理，不把接口目录的旧“创建”文字视作当前可用创建 API。现行 343 文档只列 `app.sync.update.app`，不存在应用返回 12002。

## 官方依据

读取官方 `https://dev.vivo.com.cn/webapi/doc/tree`，以及 `/webapi/doc/info?id=ID`，验证 `code=0` 和准确 `data.docId` 后缓存完整正文与 SHA256。文档 326/327 说明 API 开通、独立沙箱/正式凭据与入口；330 返回码；344 状态字典；343 同步更新；329/331/332 图片和 APK；346 查询详情；648 官方 Go 签名示例。公开链接为 `https://dev.vivo.com.cn/documentCenter/doc/ID`。

签名按公共说明采用 `HMAC-SHA256`、毫秒 timestamp、原始 UTF-8 参数按 ASCII 键排序后 `k=v` 用 `&` 连接，上传排除 file。局部接口表仍写 hmac、Go 示例误用秒；不自动更换签名或重试写入，真实沙箱验证仍是待验条件。

## 配置与资料

新 driver `vivo-store`、target `android-vivo`。管理员显式指定 `environment=production|sandbox`，包名 AppID=BundleID、release signer SHA256、绝对 SDK/凭据/资料路径与 metadata SHA256。环境只能映射到官方固定 HTTPS `/router/rest`，禁止仓库传入 endpoint 或任意上传主机。沙箱收据明确带 sandbox，不能作为正式上线证据。

私有凭据为 accessKey/accessSecret。无 token 缓存。审核清单包含 updateDesc、compatibleDevice、privateSelfCheck HTTPS URL，及可选 detailDesc/simpleDesc/remark/customerService/rateAge/testAccount/icon/screenshots。首次和特殊版权/备案材料在门户准备；未声明的字段保留现有门户资料，不复制上游任意字段或 URL。资料原字节摘要受配置审核；图片按绝对普通路径、SHA256、大小完整解码后暂存，icon PNG 方图 256–512、少于 500 KiB，截图 3–5 张 1080×1920 PNG/JPEG、每张少于 2 MiB。APK 复用真实 SDK 验证版本、包名、release signer 与非 debug 条件，使用准确暂存字节计算协议 MD5，审批仍用 SHA256。

## 写入与幂等

本地预检后只读查询现有应用，要求精确包名、appType=1、完整已知状态、正 versionCode，候选更高；审核中、已审核待上架、草稿/未知状态拒绝更新。已审核在架、被拒或撤审的现有应用可更新。上次本地接受版本尚未被上游观察到时拒绝继续，防响应后的旧数据窗口。

一次 SQL 事务保留 pending operation、环境+包名独占锁、永久环境+包名+versionCode 占用。获得锁后重新查询，逐文件上传和最终同步更新之前持久化 intent。上传采用磁盘 multipart、准确 Content-Length，每项回执必须精确包名和受限流水号；APK还要求准确 versionCode/name/MD5。code、subCode 必须同时明确为成功；未知、重复字段、截断、错误、重定向及丢响应均不能确认。

隔离标准 HTTPS transport，禁用 Cookie、重定向、keepalive 自动重放，无 GetBody；无自动 retry、下载文件接口或 continuation。任何未知写入保留包锁和版本占用，同 key/new key/并发/重开都不能重放。明确同步更新成功后原子完成 receipt/result 并释放包锁，版本占用永久保留。一个受控状态库协调写入者；其他门户操作需企业自行排他管理。没有自动解锁/恢复。

## 状态与质量

确认同步更新成功只返回 awaiting_review。externalId 是 `vivo:<environment>:<package>:<versionCode>` 本地精确标识，不是官方任务 ID。只读 status 必须关联已完成的批准绑定和准确 externalId。

查询结果先核对包名、appType、版本号/名、APK大小，再映射 status=2 为 awaiting_review，4 为 rejected，3+saleStatus=0 为 awaiting_release，3+saleStatus=2 为 halted。版本不匹配、撤审/草稿/未知、或看似上架状态，均返回 unchanged + MANUAL_VERIFICATION_REQUIRED，停止自动轮询。详情没有 APK 摘要或明确在架版本字段，不能仅凭最新版本和应用级 saleStatus=1 宣称新版本已发布；沙箱永远不能确认生产上架。

授权人员上传门户版本证明后，通过既有 releases.transition 记录真实发布。独立 QA 另行提供同制品/外部版本的真机 publication evidence 才能 delivered；旧 QA、发布执行者自验、无证明或 Worker 冒充人工均被拒绝。

## 验证与边界

TDD 覆盖完整本地 TLS+真实 SQLite、独立签名与文件字节核对、sandbox/production 隔离、严格回执、资料变化、SDK拒绝、错误/丢响应/本地写失败、并发重开、永久版本防重、状态和控制面独立验收。协议样本只替代 SDK 成功环节，不证明正式签名。另用真实 Android SDK 与新编译 CLI 验证 debug APK 拒绝，执行完整 Go vet/race、专用 PostgreSQL 和 21 项编译。

不声称真实账户、沙箱、生产提审、原生设备或 P3 完成。Apple 许可仍由用户本人处理，Windows原生节点、真实签名和双应用正式交付继续开放。
