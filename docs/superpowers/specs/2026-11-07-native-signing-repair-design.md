# Apple 与 Windows 签名包修复闭环

## 目标与依据

延续已授权的完整研发交付规划，补齐 iOS、macOS、Windows 在签后独立质量失败时的修复链。当前 `defects.go` 只对 Android 建立专用延续；另外三端在代码修复后因原签名任务没有仓库 command/outputPath 而阻塞。复用既有受保护质量、源码恢复、签名输入、租约、制品和备份机制。

## 设计

- 将 Android 的修复状态机扩展为受控签名修复状态机，保留原 `androidSigningRepairs` 的 version 1 记录和摘要兼容性。Apple/Windows 使用 `signingRepairs` version 2，记录签名模式、目标和完整批准身份。两个数组不得混用，单缺陷仍最多两轮。
- 质量失败只能来自已绑定的独立质量策略。冻结原方案任务摘要、原 unsigned/sign 配方、失败 run/test/artifact、修复任务及质量绑定。当前需求、方案、策略或任一配方变化均拒绝延续。
- 可信源码修复完成或经人工核对恢复后，创建新 unsigned → sign → 原策略 recheck。新任务、新输入、新 operationKey，原签名记录保持。第二轮必须从上一轮失败包对应的源码继续。
- 证书与目标身份继承原批准配方：macOS 两份证书及 team；iOS Distribution 证书、team 和 profile；Windows 证书、publisher、package、四段 version。自动修复不更新版本或证书，也不授权正式发布。
- macOS 的公证 deadline、polls、phase、operation、assignedWorkerId、活动时间、下次查询时间属于持久执行状态，不能纳入不可变配方摘要。它们仍由原签名状态机验证；派生新任务时清除全部旧状态，从新 sign 操作开始。未知签名结果仍阻塞，不走质量修复、不允许普通 retry。
- 质量中心在原有缺陷卡片显示修复轮次、阶段和准确任务/提交，支持桌面和手机。只呈现已存在的真实记录，不发明成功状态。

## 验收与边界

SQLite 三端端到端控制面测试覆盖两轮、身份/策略/来源漂移、撤权、未知签名和公证等待。PostgreSQL 验证多连接领取、重复完成、归档恢复及不重复签名。macOS 上真实 Worker/HTTP/Git/子进程用固定本地签名替身检验失败与修复字节；这是协议与执行链证据，不是合法签名或原生应用验收。Windows 仅在协议层测试，保留本机原生 guard。浏览器验证真实缺陷界面。

本轮不接受 Apple 协议、不修改钥匙串、不调用付费模型、不提交或推送、不操作商店或生产。保留 755 份原源码、339 个原输出、原索引/预览/Docker/AVD；仅改动本增量需要的源码。总体 P3 仍开放。
