# Google Play 持久写入占用与跨key防重

1. 保存641源码、307既有输出、261索引/零提交、原18080及Docker/AVD基线；从当前实现复现未知发布换key会重复上传。
2. 先写TLS/SQLite失败测试，覆盖跨key、跨Track、同版本/制品、两个连接并发、不同应用并行、持久重启及SQL故障，再实现Google专用执行路径。
3. 新增`google_publications`，保存原operationKey、request hash、应用、固定Track、版本、制品摘要和后续保存的versionCode；应用+版本、应用+制品及非空versionCode唯一。新增`google_destinations`保存当前未知/执行中发布的应用占用。两个表不更改Request/Profile序列化。
4. 新发布的operation、永久预约和应用占用同事务创建；Google edit/versionCode检查点同步到预约及原日志。成功结果与释放自己的应用占用同事务；任何错误保持unknown并阻止新key写入。原key成功只返回原结果；不同应用不互相占用。
5. ReconcileGoogle在原批准、准确pending和本地字节验证后确保自己的预约；恢复CAS与释放同事务。状态查询拒绝已知占用，不承担解锁。
6. 旧complete Google日志在原准确Request重放/查询时无网络登记；pending仅由已停止的准确恢复请求登记。新Google写入前检查未登记旧Google及无法辨别的pending日志，保持阻塞并要求核对；不能猜其应用或认定未发生上传。旧重复/冲突记录不能自动覆盖。
7. 运行相关及必要完整Go/PostgreSQL、CLI/旧恢复验证器和构建检查；独立Python读取原始SQLite/制品/请求/日志及预约，重算相关摘要并检查约束/占用。主线程复核、保全原环境，交付中文报告、手册、源码与脱敏证据并独立读回。

不扩大到分阶段发布或查询编辑锁。此处的应用占用保护可信publisher同一权威SQLite状态库中的发布写入；查询与新发布的原子互斥、其他状态库/控制台/外部脚本仍需独占编辑窗口。整体目标仍是原规划的两个应用全部声明端/渠道正式交付，本项不证明真实Google、AAB、原生/真机或整体P3。
