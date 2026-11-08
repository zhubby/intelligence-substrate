# Android 构建与可信签名

原规划要求签名密钥由可信服务持有，开发Agent只操作受控请求。现有发布器要求预签名包，参考构建仅生成debug APK；缺少无私钥release构建、可信签名转换及签后独立验证的实际链路。

采用现有构建工作项的受控制品转换，保留独立质量和正式发布审批。先构建unsigned-package，再由release角色的专用build节点执行管理员固定签名程序，最终产物为新的package。签后包重新独立验证，不能借用签前证据。

## 输入与密钥

- `scripts/new-app.py --android-unsigned-release`显式将新生成Android release配置置为无签名；debug配置保持原状。未识别的Gradle模板拒绝转换。
- `build-reference-client.py --target android-unsigned-apk`仅接受显式无签名配置、HTTPS API、管理员指定aapt/apksigner。执行release构建后检查非debuggable、准确包名及未签名状态；报告不宣称企业签名、安装或发布。
- `scripts/sign-android-apk.py --config /private/signer.json`是可信stdin/stdout程序；固定profile绑定projectId、goalId、target、bundleId及预期证书SHA256，支持android-huawei/xiaomi/oppo/vivo。Google Play AAB、iOS、桌面签名继续独立推进。
- keystore、alias、密码文件、SDK、Java目录与私有状态目录仅在管理员配置；请求不能选择它们。密码通过SDK的file输入读取，内容不进请求、环境、命令行、日志或响应。程序不提供HTTP服务或商店提交。
- 本轮实际验证使用新建临时测试证书，不访问既有企业凭据，不把临时证书当作企业身份。native执行和本地keystore不代表HSM或多租户隔离。

## 受控协议

请求字段：protocolVersion=1、action=sign、operationKey、projectId、goalId、runId、repositoryId、commit、target、artifactId、artifactDigest、artifactSize、artifactPath、expectedSigner。未知字段、大小写别名、重复键、额外JSON对象、非法身份和路径拒绝。当前只接受arm64、minSdk>=24的原生Flutter release APK。

校验真实输入摘要/大小、ZIP路径/重复/类型/CRC/展开上限、Flutter结构、无现有v1/v2/v3签名、真实aapt准确包名/版本/非debuggable和SDK失败的未签名状态。在持久化意图前不读取密码内容或运行签名命令。

持久意图绑定规范请求及profile公开策略摘要，不含输入临时路径或密码内容。成功重放核对原签名包与receipt，不再次签名；同operationKey换输入/策略拒绝。中断未完成保留待核对状态，不自动重签。inspect只读，不触发签名。

签名在私有临时目录执行zipalign和apksigner v2/v3，禁止v1/v4副文件。签后要求准确证书、非Android Debug、v2/v3验证及16KiB原生页对齐；ZIP每个payload文件与签前完全一致。结果保存到私有内容寻址目录，已存在的对象必须逐字节核对，输出不能覆盖其他文件。响应只含准确包路径/摘要/大小、证书、版本、payload摘要和绑定。

## 控制面与Worker

计划普通构建可声明`artifactStage:unsigned`，输出必须登记为unsigned-package，不能用于质量/发布候选。签名转换声明kind=build、role=release、signingMode=android-apk、inputArtifactFrom=依赖祖先逻辑ID、expectedSigner=SHA256；不得有仓库command/outputPath或自行传入signingInput。

控制面领取时从当前需求/批准计划的实际成功上游构建推导唯一输入，核对producer/run/repository/commit/target与实际字节，保存signingInput快照。上传和完成时复核绑定，签后artifact由租约推导输入关联，不能借用其他run或输入。需求/方案/权限变化继续失效。

Worker只调用Config.Signers[target]的管理员固定绝对程序，不检出或运行仓库代码，不向程序传平台令牌。它下载并核对准确unsigned-package，验证固定程序输出字节及证书绑定，上传新的package；未配置程序阻塞，不回退到任意仓库构建命令。签名节点仅有release/build角色能力，独立质量身份保持分离。

仅release/build的节点只领取签名转换；配置非空Signers的Worker拒绝任何其他执行/发布任务，阻止普通同角色构建借用签名节点。签名响应拒绝重复JSON键和未知字段。最终质量与发布入口须重新核对已确认成功的原始签名run和回执。

## 验收

真实新应用release unsigned APK经临时测试密钥完成实际zipalign/apksigner签名，payload不变、正确证书、非debuggable、重复请求无重签、输入/策略冲突拒绝。平台实际Worker完成unsigned→trusted signing→自动独立同包验证，原unsigned-package不能生成候选；真机门槛仍阻止正式候选。

保持原18080/PID36063、3静态文件、261暂存记录/零提交、201既有输出及37容器/23镜像引用/15卷。无真实模型、商店、生产、通知、协议接受、主仓库暂存/提交/推送。完整P3保持开放，不能将组件或模拟器验收替代正式交付。
