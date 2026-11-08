# Google Play 已接受操作恢复

落实原规划的结果不明核对与故障恢复；主线程执行，沿用现有管理员恢复流程。原平台状态、批准、独立质量与真机门槛保留。

1. 保存634源文件、303输出、261索引、原预览和Docker/AVD基线；核查Google官方v3 discovery契约。
2. 先写实际已接受commit丢失结果的恢复/不重放测试；再实现管理员 `ReconcileGoogle`。输入包含原发布Request、准确journalDigest、confirmStoppedOperationKey和原因；只接受原单AAB发布。必须验证本地SHA256/大小、保存的edit/versionCode及准确固定目的地。
3. 使用查询用临时edit读取bundles和固定track，随后删除临时edit。准确versionCode须只有一个bundle且SHA256匹配；track须有唯一匹配的name、单versionCode和completed/halted状态。其他状态、重复/歧义、缺失及错误保持pending。不得上传、改track或commit；completed只恢复awaiting_review，halted保持halted。
4. 追加来源、快照、原因、观察时间与证明摘要；CAS原子写入complete收据。同语义输入重放读取原记录且不访问网络；旧快照/不同理由/批准或目的地冲突拒绝。
5. 新增 `-reconcile-google` 专用CLI，与状态、继续、其他恢复、检查、密钥及续期互斥；通用Worker协议不得接受恢复动作。严格单对象/未知字段/尾随输入检查。
6. 验证真实本机TLS、实际SQLite持久/重启、同键不重放、两个连接并发和故障回滚；执行必要完整Go/专用PostgreSQL回归、CLI与编译检查。无UI变更时不机械重跑工作台全套；相关发布浏览器回归可按最终影响范围运行。
7. 主线程复核；独立Python读取包、SQLite、记录摘要和API调用证据；输出中文报告、操作手册、源码与脱敏证据并独立读回。只关闭031，整体P3继续开放。

本轮不增加分阶段发布或停止分阶段分发、不声明真实Google账户或原生/真机成功。管理员仅确认原执行已停止；API正向证据由可信连接器读取，不能由输入自由文本替代。中间阶段只有edit但无versionCode，或只能证明AAB已上传而不能证明进入准确track时，仍保持阻塞。

## 执行记录

- 已实现原批准Request和固定profile核对、准确pending CAS、重新暂存文件、Bundle SHA256/唯一Track release证明及可选收据历史。completed仅awaiting_review，halted保持halted。只创建/删除查询edit，不上传、更新Track或commit；成功清理未确认时不完成恢复。
- 已实现管理员独立CLI和64KiB/UTF-8/重复字段/未知字段/单对象输入边界，256种模式组合验证。普通Worker动作保持原协议。手册明确独占应用编辑窗口由管理员保证，没有跨状态库锁或新分阶段发布能力。
- 针对性9顶层/65项、完整Go race602顶层/2117项含子场景（含27项专用PostgreSQL）、vet/build/tidy、格式、Linux/Windows publisher交叉编译通过。相关Web分发CLI/TLS/真实Chrome验收通过；完整工作台浏览器和Python套件本轮未重跑。
- 最终contract-a3经真实本机TLS及RS256 OAuth验证原提交已接受但响应丢失，draft拒绝、准确completed恢复、无凭据重启/重放和23项CLI正负调用通过；独立Python直接读前后SQLite、文件、原API JSON，重算所有相关摘要。所有身份和制品均为合成样本，没有真实Google或AAB验收。
- 验证中保留缺失功能/CLI red、leading-zero夹具漏写的首轮失败及a2 HTTP客户端重复包装的演练失败；均定位原因后修正并复验。a1曾先通过，a3补充完整pending数据库独立读回后通过。复验没有增加生产绕过开关。
- 原18080、634份既有源码的无关内容、303份既有输出、261索引/零提交及Docker/AVD基线保全。只在交付物独立读回后更新031完成记录；整体产品目标/P3继续开放。
- 4份脱敏输出首次独立读回通过：641源码/642ZIP项、摘要/权限、Go原始事件602/2117、文档与二进制摘要、3条报告链接均一致。随后更新031完成记录并重新导出最终包；整体goal仍active。
