# Agent 双向协作上下文 Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans sequentially. User instructions prohibit subagents; preserve the existing unborn feature branch and outputs. No commit/push.

**Goal:** 把协作消息接入实际 Agent 循环，持久保存到工作项并在重试时恢复，保持版本、租约和审批边界。

**Architecture:** messages.receive 在 Store 事务内原子登记 message_receipt 与原消息交付；Agent 每轮读取、去重并注入不可信上下文。工作台读取同一权威资源。

**Tech Stack:** Go、SQLite/PostgreSQL、HTTP、React/TypeScript、Playwright。

**Spec:** [设计](../specs/2026-11-06-agent-inbox-design.md)

## Global Constraints

- rtk 命令与子进程；不派生 Agent、不提交/推送、不切换 18080 后端。
- 保留此前 73 个交付文件、已有集群/服务/SDK/凭据。
- 消息不产生权限、验收证据、审批或成功状态。
- 当前任务最多 32 条且 64 KiB 的持久协作上下文，单一 payload 最多 8 KiB；不能截断或跨目标借用。

### Task 1: 消息身份、版本和持久收件

**Files:** Create internal/platform/message_inbox.go and message_inbox_test.go; modify messages.go/service.go; extend PostgreSQL verification.

**Interfaces:** messages.receive {runId,leaseToken} returns Resource{Kind:"message-inbox",Data:{items:[],deferred:false}}. New immutable message_receipt contains taskId/planId/requirementRevision/firstRunId/workerId/messageId/message snapshot.

- [x] 用现有 fixture、goal、must 建立批准 analysis 任务和真实 Worker；先调用 messages.receive 并断言返回实际 payload、原消息 delivered、唯一 receipt，观察 unknown_command 失败。
- [x] 编写并观察过期 Worker 仍能发消息、旧目标消息被接收、其他项目收件人、错误因果引用、非对象/超大 payload 等缺口对应失败。
- [x] 在现有 validWorkerRun 上验证发送与接收；实现版本/收件人/因果绑定、原子收件及历史回放，不引入另一套身份系统。
- [x] 真实 SQLite 并发、回滚、同任务换节点恢复与上下文容量验证；PostgreSQL 双客户端及 backup/restore 重验同一行为。

### Task 2: 实际 Worker 双向协作

**Files:** Create internal/worker/inbox.go/inbox_test.go; modify agent.go; HTTP/MCP command exposure follows existing generic command handling.

**Interfaces:** Runner.receiveMessages(ctx,run,seen) returns newly seen receipt snapshots or error. It calls messages.receive before each model request; does not manufacture a finished task or ack understanding.

- [x] 实际控制面/HTTP/Worker 测试：第一个模型步骤收到真实请求、回复给另一节点；在工具动作之后发来新消息，下一步模型必须见到；最终知识记录保留正确答复。先观察未读消息或缺少租约失败。
- [x] 接入每步接收及本地去重，保留 message action 的 leaseToken；模型/网络失败不删除 receipt，deferred 明确阻塞。
- [x] 验证故障后重新领取、其他 Worker 接手相同任务仍能读取先前消息，其他任务不继承。

### Task 3: 工作台、回归和交付

**Files:** Add focused message-context component and browser scenario; update docs/api.md, architecture.md, workers.md, validation.md and README.

- [x] 先写桌面/手机实际 API 场景，打开工作项核对消息来源、内容、接收执行与需求版本；观察缺少入口失败。
- [x] 在既有任务详情展示持久协作上下文，复用布局和模态框；核对长标识、键盘/Escape 及手机溢出。
- [x] 运行 go vet、完整 race 与新隔离 PostgreSQL、21 项命令编译、TypeScript/Vite 与完整浏览器回归；保留首次失败及修正证据。
- [x] 主线程正确性/安全/简化自查，更新验证边界，停止本轮隔离服务，保持原预览。
- [x] 在指定 outputs 新增源码、手册、报告、证据与截图，逐字节验证源码 ZIP、原 73 份文件摘要及输出链接。

## 验证记录

完整验证：`work/agent-inbox-zu3334oo/result.json`。186 个顶层 Go 测试、394 项含子场景、21 个程序编译、14 个浏览器场景；新 PostgreSQL 集群已停止，18080 后端未重启。主线程复查，无独立审查者。导出逐字节核验记录：同目录 `export-verification.json`。

修正既有边界：单 Worker 仅一个活动工作项，显式 recipientTaskId 用于后续任务排队。先失败测试另保留在 work/agent-inbox-*.stdout/stderr；身份 SQL 故障先复现，再拒绝投递。最后检查后的迟到消息、pending 原节点归属、无自动唤醒及容量不会因重试解除均在手册说明。完整产品目标仍未完成。
