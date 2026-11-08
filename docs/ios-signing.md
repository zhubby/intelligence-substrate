# iOS 真机应用构建与 App Store 受控签名

此链路把准确无签名 iPhoneOS APP 转为批准证书、团队和 provisioning profile 对应的 IPA，再提交独立质量验证。签名不代表真机业务通过、商店接收或正式发布。

## 构建和批准方案

使用完整 Xcode、已由使用者接受的 SDK 协议、固定 Flutter/依赖缓存和准确源码，在独立 darwin 开发节点执行应用自带构建器：

```sh
python3 scripts/build-reference-client.py --application . --target ios-unsigned --api-url https://api.company.example --output artifacts/unsigned.app.tar.gz
```

先检查 `iphoneos` SDK，许可失败会在 Flutter 调用前停止，不产生成功包。构建使用 release/no-codesign，要求 iPhoneOS/iphoneos、仅 arm64、顶层无签名与无 profile。侧车记录源码/命令/SDK/原包摘要；侧车本身不会自动成为平台证据。没有解除本机许可69或实际原生构建证明时，不得将脚本契约测试计为构建成功。

批准方案中的对应工作项：

```json
[
  {"id":"unsigned","title":"构建准确真机应用","kind":"build","role":"developer","runtime":"darwin","target":"ios-appstore","artifactStage":"unsigned","repositoryId":"repository_example","commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","command":["python3","scripts/build-reference-client.py","--application",".","--target","ios-unsigned","--api-url","https://api.company.example","--output","artifacts/unsigned.app.tar.gz"],"outputPath":"artifacts/unsigned.app.tar.gz"},
  {"id":"sign","title":"App Store 签名","kind":"build","role":"release","runtime":"darwin","target":"ios-appstore","signingMode":"ios-ipa","inputArtifactFrom":"unsigned","dependsOn":["unsigned"],"expectedSigner":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","expectedTeamId":"ABCD123456","expectedProvisioningProfile":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}
]
```

替换项目真实仓库/提交和三项身份。`expectedSigner` 是 Apple Distribution 公开证书 DER 的 SHA256；`expectedProvisioningProfile` 是从 Apple 获取并经管理员批准的 CMS 原文件 SHA256，不是解码 plist 的摘要。`expectedTeamId` 为10位大写团队。首版要求 App ID 前缀等于团队，明确注册的 bundle ID；旧式不同前缀或 wildcard App ID 不支持。

控制面从成功依赖推导准确 unsigned-package/执行/仓库/提交，冻结三项批准身份和 source binding；Worker不能填写来源字段、选择 profile/密钥路径或运行仓库签名命令。只有最终IPA签名执行成功后才自动调度独立QA，原始unsigned包不会派发为最终验收输入。

## 管理员部署

注册仅 `release` 角色、仅 `build` 能力、`runtime:darwin`、`trustedQuality:false` 的专用节点。不能兼任开发、质量或发布。采用 [Worker配置](../deploy/worker.signer.ios.example.json) 与 [签名配置](../deploy/signer.ios.example.json)，替换全部示例身份、路径和摘要。

将 `sign-ios-ipa.py`、`ios_signer.py`、`ios_payload.py`、`macos_signer.py`、`android_signer_state.py` 安装在管理员拥有、Agent不能修改的可信目录。需要经过验证的 Python3.11+、完整 Apple 工具和固定 OpenSSL3。签名节点不需要仓库检出、Flutter或模型凭据；构建节点不接触私钥。

证书、profile、entitlements、keychain、CMS信任CA和配置为运行身份拥有的单链接普通私有文件，0600；状态目录0700；凭据必须位于状态根之外。管理员预先建立独立keychain、导入准确Distribution身份并配置非交互ACL/解锁策略；程序不导入/导出私钥、不解锁keychain、不改变搜索列表、默认keychain或系统信任。证书申请、续期和profile更新由管理员处理，摘要变化须新批准方案。

`profileTrustCAFile` 是管理员审核的 Apple provisioning CMS 信任CA PEM bundle，原文件另以SHA256固定；不得使用示例或本地测试CA作为Apple成功证据。OpenSSL3使用显式 `-CAfile -no-CApath -no-CAstore -purpose any` 验证CMS签名和证书链，再读取认证过的plist。profile须未过期、UUID合法、iOS平台、准确团队/App ID、包含批准DER证书；拒绝设备清单、enterprise all-devices、调试profile或wildcard App ID。

[entitlements示例](../deploy/signer.ios.entitlements.example.plist)只包含应用标识、团队、禁用调试和明确keychain组。所有能力由管理员审核固定，且必须落在profile授权范围内；profile wildcard仅用于验证明确的批准值，不能出现在实际签名entitlements中。最多64项，深度8；推送/关联域等业务能力需要相应有效profile和实际设备复验。

## 转换、完成和故障处理

首版支持单一平面 iOS APP、arm64 executable/dylib和非版本化framework。拒绝模拟器/macOS/其他CPU、损坏Mach-O、脚本、路径逃逸/歧义、符号/硬链接和特殊文件。app extension、Watch/nested app、多profile应用和版本化framework尚未支持。输入最多256MiB，展开768MiB，50,000项；最终IPA最多256MiB，不支持的输入明确阻塞。

固定codesign从framework/dylib到主应用签名。framework不获得应用entitlements；主应用嵌入准确CMS，使用管理员授权和DER entitlements。每个对象提取叶DER重新核对SHA256/团队，并以Apple distribution requirement验证；最终深度严格验证及实际entitlements/profile/普通资源/路径/模式读回。输出只有一个 `Payload/APP.app`，归档后再核对每项字节。原输入与源码不改变。

所有工具使用隔离HOME/TMPDIR、固定PATH/C语言环境，每调用120秒、每路输出2MiB，仅保存在私有诊断中。平台token、IS_*和宿主凭据不进入工具。工具继承Worker专用进程组，取消或程序退出立即清理后代；不能从进程被停止推断外部服务未接收任何动作。本签名器没有渠道上传或公证请求。

私有 `ios-operations.sqlite` 使用DELETE/FULL、维护锁和签名前持久 `signing_intent`；成功IPA与目录fsync后才保存 `signed`。同operationKey完成重放只核对原IPA字节，不再调用工具。中断、失联、未知签名结果或配置/profile/授权变化均阻塞，通用任务retry不会重签。管理员先停止派发/全部进程、检查私有日志与准确对象；需要重新签名时先批准新方案，不清空旧日志掩盖历史。

```sh
/path/to/verified/python3 /Library/IntelligenceSubstrate/trusted/sign-ios-ipa.py --config /Library/IntelligenceSubstrate/private/signer.ios.json --inspect sign-task_example
```

控制面备份不含签名节点。管理员停机后完整备份状态根、原配置和私有凭据，恢复先隔离核对全部历史操作；旧快照可能遗漏后续执行，不能直接激活。当前不提供iOS keychain自动加密备份/恢复激活器。签后已绑定质量失败可进入[受控签名包修复](native-signing-repair.md)，保持原Distribution身份与profile，以新任务重新构建和签名。已完成对象只能在原批准请求和私有状态完整时重放读回。

## 独立验收与正式发布

App Store distribution IPA不能直接安装到真机。Apple在渠道处理后重新签名；真机业务验收应通过企业授权的TestFlight等受控渠道，保留原IPA/提交/版本、Apple build ID与实际安装构建的对应证据。平台当前不自动执行这项TestFlight验收或代签发设备证明。通用CI包检查不会放行 `ios-device` 门槛。

发布仍走原候选与人工审批、独立[App Store发布器](app-store.md)的上传/渠道核对。构建成功、签名成功、渠道接收、审核和上架分别记录；提审成功不能等同交付。真实Distribution私钥、合法profile、完整Xcode原生构建、真机业务和实际上架必须各自取得证据，合成工具/本地测试CA不满足这些条件。

本地契约复验：

```sh
rtk proxy python3 scripts/test_ios_payload.py -v
rtk proxy python3 scripts/test_ios_signer.py -v
rtk proxy go test -race ./internal/platform ./internal/worker -run 'TestIOSSigning|TestWorkerIOSSigning|TestPostgresIOSSigning' -count=1
rtk proxy python3 scripts/verify-workbench.py --suite ios-signing
```

PostgreSQL需专用 `IS_TEST_POSTGRES_URL`。Python测试真实运行本地测试CA的OpenSSL3 CMS签名/验证，Apple keychain/codesign成功明确为合成；不会修改系统信任。官方契约见 [TN3125 Provisioning Profiles](https://developer.apple.com/documentation/technotes/tn3125-inside-code-signing-provisioning-profiles) 与 [Entitlements Troubleshooting](https://developer.apple.com/library/archive/technotes/tn2415/_index.html)。

<!-- END INTELLIGENCE SUBSTRATE IOS SIGNING RUNBOOK -->
