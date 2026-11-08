# 华为 AppGallery APK 发布连接器

本增量落实 P3 的首个国内 Android 渠道：在管理员固定的目的地内验证已批准 APK，上传准确字节，持久等待解析，复用原审批继续提审，并查询准确版本。不能用上传、提审或旧版本上架代替本次发布。

## 官方契约与边界

- 官方 `upload-url/for-obs` 支持 Service Account/API client/OAuth；`app-file-info`、`app-submit`、`package-list`、`appid-list` 当前文档只明确列 API client/OAuth。首版只实现共同明确支持的 API client，不推断 Service Account 兼容性。
- 固定 API origin 为 `https://connect-api.cloud.huawei.com`。OAuth client 和旧下载回调模式不在本增量内。OBS 上传与控制面使用不同客户端，不携带控制面 Bearer/client_id/Cookie。
- `fileDestUrl` 是 OBS objectId；`pkgVersion` 是 package ID。`appInfo.versionId` 与 `onShelfVersionId` 是业务版本 ID，不能当作 pkgVersion。
- 上传完成后至少等待两分钟，保存时间后立即释放任务；后续只读查询与单次 continuation 走现有平台权限、质量、预算、原绑定检查。
- `app-file-info.files` 采用文档表格声明的数组；其示例写成对象的差异记录在手册中。未知/缺失 ret.code 必须失败，不能按零值成功。

## 身份、版本与权限

管理员 profile 固定 project/goal/target、numeric appId、Android package name（复用 bundleId）、签名证书 SHA256、aapt/apksigner 绝对路径、API client 私有凭据、精确 OBS HTTPS host、是否分发中国大陆。任务不能覆盖这些值。

准确制品先按已批准 SHA256/大小复制到私有暂存，再调用 SDK 验证完整 APK 签名、唯一 signer、包名、versionName 与正整数 versionCode，拒绝 debuggable APK。真实 CLI 不提供绕过签名开关；Go 契约测试只在测试内部替换 SDK 调用。

先用 packageName 查询且要求唯一 appId 与 profile 一致，检查 publishCountry 和可编辑状态；既有 versionCode 不可覆盖。每个 AppID 以 SQLite 持久锁串行管理，处理和未知结果期间不允许另一发布操作覆盖。企业须在活跃操作期间让本服务独占该 AGC 应用发布写入；上游 app-submit 不支持版本条件写入，无法消除外部操作者在最后一次读和提交间的竞态。

## 持久状态

每次外部写入前持久化 intent：upload URL reservation → OBS bytes → file registration → processing → submit。只存 objectId、package ID、版本、时间和 stage，不存 token、签名 URL、OBS 授权或上游任意正文。网络错误、回执丢失和未知 stage 不自动重放。

processing 状态查询必须等待两分钟，再核对 package-list 中唯一 package ID、versionCode、versionName、大小、SHA256 和 APK 类型，并核对 app-info 的当前版本。缺失解析结果保持 processing；冲突拒绝继续。continue 以 SQLite 原 journal 条件更新争取唯一写入权，重新读取准确版本后提交，记录业务 versionId；不重新上传、不下载其他包、不调用模型。

提审 ret.code=0 只返回 awaiting_review。提交后的 published 只在本次 package 信息仍完全一致，且 releaseState=0、onShelfVersionId/onShelfVersionCode/onShelfVersionNumber 全部匹配持久的业务版本时返回；真实可安装/业务验证仍由独立验收完成。拒审、下架等也必须关联当前或上架的准确版本，否则 unchanged。确定终态后释放本地目的地锁；未知写入保留锁，首版不提供自动恢复/解锁。

## 验证与现场约束

使用真实 TLS 服务、SQLite 重开与并发验证协议和副作用；覆盖准确字节、上传隔离、版本漂移、无凭据泄漏、未知结果不重放、持久两分钟等待、只读状态和单次继续。实际 SDK/CLI 使用既有诊断 APK 验证拒绝边界，不将 debug 签名当正式发布签名。

所有宿主命令/验收脚本子进程使用 rtk。主线程顺序执行，不用子 Agent，不提交推送。不新增付费模型调用；不修改既有凭据、服务和业务数据库；保持 18080/PID 36063。保留 outputs 既有 109 个文件全部摘要，新交付使用 huawei 前缀。新 Go 改动完成后执行 vet/race、独立 PostgreSQL 回归与 21 个程序交叉编译。无 UI 修改时不重跑浏览器。真实渠道账户、合法 release APK、实际提审/审核/上架仍需企业现场验证，整体产品目标继续 active。

## 官方来源

- [Upload URL](https://developer.huawei.com/consumer/en/doc/AppGallery-connect-References/agcapi-upload-url-new-0000001111685200)
- [Upload file](https://developer.huawei.com/consumer/en/doc/AppGallery-connect-References/agcapi-upload-file-new-0000001111845090)
- [File registration](https://developer.huawei.com/consumer/en/doc/AppGallery-connect-References/agcapi-app-file-info-0000001111685202)
- [Submit](https://developer.huawei.com/consumer/en/doc/AppGallery-connect-References/agcapi-app-submit-0000001158245061)
- [Packages](https://developer.huawei.com/consumer/en/doc/AppGallery-connect-References/agcapi-obbfile-package-list-0000001111685210)
- [App info](https://developer.huawei.com/consumer/en/doc/AppGallery-connect-References/agcapi-app-info-query-0000001158365045)
- [App identity](https://developer.huawei.com/consumer/en/doc/AppGallery-connect-References/agcapi-appid-list-0000001111845086)

正文已从官方 documentPortal 公共接口缓存到本轮私有 work 证据目录；不依赖第三方插件作为协议来源。
