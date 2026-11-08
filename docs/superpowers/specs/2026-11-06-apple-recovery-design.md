# App Store 已接受操作的只读恢复

本增量落实已批准规划中的故障恢复与渠道真实性要求，继续使用现有发布服务和审批边界。

## 问题与范围

上传提交或审核提交在 Apple 侧成功，但 HTTP 回执或本地完成写入失败时，现有 journal 保持 pending。它正确阻止重放，却没有受控入口恢复已确定的结果。

新增本地管理员专用 `is-publisher -reconcile-apple`，只对 Apple 发 GET，核对已保存的 ID；不创建上传、草稿或审核，不接收替代外部 ID。只支持 `file_commit_intent` / `processing` 与 `review_submit_intent` / `submitted` 四个阶段。其他中间阶段继续阻塞。

## 输入与权限

- 输入包含原批准 Request、`journalDigest`、`confirmStoppedOperationKey` 和非空 `reason`。原请求与管理员目的地必须匹配 journal 的 request_hash。
- `-inspect` 输出当前 journal 摘要。恢复必须精确绑定这个快照；变更、陈旧摘要和并发写不能覆盖新状态。
- 操作员须先停止对应发布进程、确认没有仍在飞行的写请求。CLI 的确认字段记录该声明，不能自动证明外部写已停止。
- 恢复入口不加入 Worker/MCP，也不允许通过普通 Request.action、status-only 或 continue-only 模式访问。CLI 运行身份是发布服务的可信本地管理员，不等同于企业 SSO 审批。

## 正向核对

1. 重新验证本地制品 SHA256 和大小，计算 Apple 协议的 MD5；不执行包内代码或重新签名。
2. 固定 Apple API origin / TLS / ES256，核对管理员 app/bundle。
3. 读取准确 uploadId 的文件集合，要求无后续分页且恰好包含已保存的唯一 fileId，检查文件名、大小、ASSET/IPA、完整上传状态和 MD5。Apple 文件对象没有反向 buildUpload 关系，关联通过父资源集合核实。
4. 读取准确 uploadId 与关联 build，要求处理 COMPLETE/VALID，绑定 app、营销版本、构建号、IOS 平台和原 upload。
5. 上传阶段要求准确版本仍为 PREPARE_FOR_SUBMISSION、AFTER_APPROVAL、未绑定构建，恢复为 processing。后续独立继续任务仍须通过控制面的当前审批与质量门禁。
6. 提审阶段另核对准确 submissionId、其 items 集合中的唯一 itemId、app/平台、版本、build、submittedDate 及已提交状态；按版本的真实状态恢复 awaiting_review / awaiting_release / published / rejected / halted。unknown/unchanged、草稿、已移除项目和取消中状态不能作为正向证明。

## 持久化

一次 SQL 比较并交换同时更新 receipt、result 和 complete；比较原 request_hash、receipt、result、updated_at 与 pending 状态。receipt 附加恢复原因、原快照、前后阶段、观测时间和输入摘要。原意图和恢复历史保留，不删除旧记录。

相同输入在恢复后、后续发布尚未改变 journal 时可幂等读取。后续阶段改变后旧恢复输入被拒绝。网络错误、身份漂移、错误校验和、未完成处理、无确切审核证明均不改变 journal。

本地恢复不会授予发布审批或更新控制面事实。操作员仍通过现有 `releases.reconcile` 提交准确报告，然后恢复平台查询；平台独立验收要求不变。

## 验证边界

使用真实 SQLite、ES256、本地 TLS、实际字节上传和真实 publisher 二进制，模拟外部已接受但响应丢失。验证重开恢复、零外部写、陈旧快照、审批/目的地变更、并发唯一恢复、幂等性与失败不解锁。签名成功依赖只在测试内替换，不提供生产跳过开关。没有 Apple 企业账号或合法签名 IPA，不能声明真实 Apple 提审、上架或 P3 完成。
