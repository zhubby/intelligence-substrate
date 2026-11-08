# Windows 准确 MSIX 构建与私有签名

Windows 主路径为：批准需求/方案 → 原生 Flutter release/x64 → 无签名 MSIX → 专用 release/build 签名节点 → 准确签名 MSIX → 独立质量 → 人工发布批准 → 既有分发与更新验证。**本地合成签名提供方不证明 Windows 证书、时间戳、安装、升级或正式交付成功。**

## 企业节点准备

构建/质量节点和签名节点使用隔离的 Windows 执行环境与不同服务身份。构建节点安装受信 Flutter、Visual Studio C++桌面工具和Windows SDK；签名节点仅安装受信Python3.11+、Windows PowerShell5.1及固定版本SignTool。不向签名节点授权仓库执行、质量断言或发布权限。

企业管理员自行配置受信的RSA代码签名证书及受控私钥/HSM访问，并将公钥DER导出到固定私有目录；不要向任务、仓库或Worker配置提供PFX/口令。证书必须具有代码签名EKU、当前有效且完整链/撤销检查通过，准确Subject用作MSIX Publisher。CurrentUser/My按专用服务身份解析，LocalMachine/My增加`/sm`，两者都用DER的SHA1 thumbprint准确选择单个证书；批准和来源绑定使用DER SHA256。

复制[签名服务配置](../deploy/signer.windows.example.json)和[Worker配置](../deploy/worker.signer.windows.example.json)，将项目/目标、包标识、四段版本、证书/SignTool/辅助脚本摘要及时间戳URL替换为实际批准值。固定HTTPS RFC3161服务必须能从签名节点访问；代码不会选择备用URL、忽略时间戳或降级信任。

把`sign-windows-msix.py`、`windows_signer.py`、`windows_msix.py`及`windows-signing-tools.ps1`安装到仓库外的管理员目录。PowerShell策略需允许管理员安装的固定辅助脚本；程序不使用ExecutionPolicy Bypass、不安装证书、不修改信任或提升权限。工具、脚本和配置的修改属于管理员部署操作。

管理员预建私有目录、状态根和Worker状态目录。DACL只向专用服务账户SID、SYSTEM和Administrators授权；私有根禁止继承，所有子文件须保持相同许可边界。不要以Unix模式位代替Windows DACL。状态路径及配置/证书父目录必须由管理员控制；禁止符号链接/reparse point和其他用户的允许规则。服务实际读取DACL，不会自动修复企业权限。

## 构建无签名 MSIX

先用标准蓝图生成应用及原生平台。准备本地标识目录，包含真实有效的PNG：`StoreLogo.png`为50×50、`Square150x150Logo.png`为150×150、`Square44x44Logo.png`为44×44。图像不会通过网络补取。

在Windows构建节点运行以下命令，路径和身份需替换为实际值：

```powershell
python scripts/build-reference-client.py --application work/customer-feedback --target windows-unsigned-msix --flutter 'C:\Flutter\bin\flutter.bat' --makeappx 'C:\Program Files (x86)\Windows Kits\10\bin\10.0.26100.0\x64\makeappx.exe' --windows-publisher 'CN=Company' --windows-package com.company.feedback --windows-version 1.0.0.0 --windows-assets 'C:\BuildAssets\feedback' --api-url https://api.company.example --output work/customer-feedback-unsigned.msix
```

管理员先用Flutter自带工具准备SDK缓存；助手遇到标准flutter.bat/cmd时直接运行缓存中的dart.exe与flutter_tools.snapshot，避免批处理解释器改变URL/路径参数，不自动更新SDK。助手先执行Flutter分析/测试，再构建Windows release/x64；仅使用准确Release目录、固定单入口和本地标识生成Windows.Desktop/full-trust manifest。MakeAppx以SHA256打包，不使用`/nv`。输出与`.json`清单一起发布，拒绝覆盖已有文件。清单记录源文件摘要、SDK、原生命令、包内容、身份、准确字节和未完成的安装/发布验证。

首批支持单个x64 GUI EXE、普通x64 DLL和标准Flutter资源，Windows路径须为规范ASCII。最低Windows版本10.0.17763.0，MaxVersionTested为10.0.26100.0。manifest仅声明单个`Windows.FullTrustApplication`、Internet和runFullTrust；服务、扩展、附加入口、框架依赖、ARM64/混合架构、MSI/EXE安装器制作不在本配方中。非Windows和HTTP API在运行Flutter前被拒绝。

## 批准方案与节点

方案中的构建任务声明`developer/build`、`runtime=windows`、`target=windows`和`artifactStage=unsigned`，上传`unsigned-package`。签名任务无需仓库命令和输出路径：

```json
{
  "id": "sign",
  "title": "Windows MSIX 签名",
  "kind": "build",
  "role": "release",
  "runtime": "windows",
  "target": "windows",
  "signingMode": "windows-msix",
  "inputArtifactFrom": "unsigned",
  "dependsOn": ["unsigned"],
  "expectedSigner": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "expectedWindowsPublisher": "CN=Company",
  "expectedWindowsPackage": "com.company.feedback",
  "expectedWindowsVersion": "1.0.0.0"
}
```

注册只具有`release`角色和`build`能力、runtime=windows、trustedQuality=false的专用Worker，采用配置的`signers.windows`固定程序。控制面推导准确unsigned祖先、原成功run、仓库/提交、需求/计划、源binding与三项包身份，调用者不能自报这些制品来源。

## 签名与恢复

程序核对完整ZIP、内容类型、manifest、SHA256 BlockMap、PE及固定身份，在私有目录保存准确输入。SQLite日志先提交`signing_intent`，随后使用固定SignTool、证书存储thumbprint、`/fd SHA256 /tr <approved URL> /td SHA256`签名。完成必须同时通过SignTool `/pa /all /v /tw`和系统Authenticode、准确叶证书与受信任时间戳检查。

签后重新读取MSIX；所有原载荷和BlockMap逐字节一致，仅允许新增`AppxSignature.p7x`及其准确的Content Types映射。签名内容不计入原payloadDigest；证书SHA256、SHA1存储选择器、发布者/包/版本、准确最终摘要与来源进入回执。只有最终包fsync后才保存`signed`并交Worker上传。Windows目录fsync不可用，依赖文件FlushFileBuffers/SQLite FULL与预建NTFS状态目录；企业断电恢复需要实际演练。

重复相同成功操作仅校验并返回原MSIX，不重复签名；临时下载路径和当前runId可变，其余来源或配置变化拒绝。签名结果不明或`signing_intent`中断时保持待核对，不以通用重试、换key、旧快照或删除日志自动重新签名。

管理员在原签名节点读取状态：

```powershell
python C:\IntelligenceSubstratePrivate\sign-windows-msix.py --config C:\IntelligenceSubstratePrivate\signer.windows.json --inspect sign-task_REPLACE
```

检查固定私有日志、原unsigned和signed文件，使用受信Windows工具核对操作。当前没有强制解锁或未知结果的自动重签入口。更换需求/方案必须经过批准；旧操作日志和对应配置保留到完成回执与审计已确认，不覆盖历史完成对象。备份私有状态需停止签名服务、保持一致快照和原ACL；控制面备份不包含机器私钥。

## 最终质量、分发与验收

最终MSIX由独立质量Worker消费。跨系统CI可检查内容、哈希和协议；正式发布仍需准确包在目标Windows上的安装、启动、业务、卸载及原版本升级验证，证据绑定同一MSIX和提交，不能以合成签名、编译成功或CI替代。未满足`desktop-native`等既有门槛时发布候选会被拒绝。

既有Windows publisher/updater继续使用受信系统签名与准确证书SHA1 thumbprint、Ed25519更新索引和包SHA256。将其固定WindowsPublisher/包标识/版本规则与本签名配方匹配，独立审批后才公开分发；不自动运行真实发布或安装操作。

本轮主机为macOS，真实Windows SDK/PowerShell/证书存储/时间戳/安装及更新均待企业节点验收。Apple许可仍69；整个平台P3保持开放。

工具参数与包结构参考Microsoft官方文档：[MakeAppx](https://learn.microsoft.com/en-us/windows/msix/package/create-app-package-with-makeappx-tool)、[MSIX签名](https://learn.microsoft.com/en-us/windows/msix/package/sign-app-package-using-signtool)、[SignTool选项](https://learn.microsoft.com/en-us/windows/win32/seccrypto/signtool)及[BlockMap数据块](https://learn.microsoft.com/en-us/uwp/schemas/blockmapschema/element-block)。文档核对和跨平台语法解析均不能替代真实Windows节点验收。

签后已绑定独立质量失败支持[受控修复与复验](native-signing-repair.md)：保持原证书、Publisher、包名和四段版本，产生新无签名构建与签名操作。签名未知结果仍需核对，不允许普通重试。

<!-- intelligence-substrate-windows-signing-runbook-end -->
