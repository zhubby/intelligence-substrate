# App Store Read-only Recovery Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans sequentially. User instructions prohibit subagents. Preserve the unborn feature branch; no commit or push.

**Goal:** 在不重放外部写的情况下，恢复 Apple 已接受且可独立核实的上传或提审结果。

**Architecture:** 管理员 CLI 调用发布 Engine 的专用恢复方法，依据原批准绑定与 journal 快照执行固定 GET 核对，再通过原子比较并交换落盘。Worker 的常规发布/查询接口不获得恢复权限。

**Tech Stack:** Go、SQLite、App Store Connect JSON API、ES256、TLS。

**Spec:** [设计](../specs/2026-11-06-apple-recovery-design.md)

## Global Constraints

- 所有命令以 rtk 开头；主线程顺序实施，不提交或推送。
- 保留 83 份输出、18080 后端、已有集群/数据库/凭据和工具链；临时材料仅在忽略的 work/。
- 恢复仅发 GET，不接受替代 Apple ID，不绕过原发布绑定、制品摘要或独立验收。
- 不把本地 TLS 契约、签名替身或管理员测试身份描述为真实 Apple 成功。

### Task 1: Journal 快照与只读恢复

**Files:** `internal/publisher/publisher.go`、`apple_package.go`、新增 `journal.go`、`apple_recovery.go` 与 `apple_recovery_test.go`，扩展 `apple_test.go` 的外部边界。

**Interfaces:** `Engine.Inspect(ctx,key)` 输出 `journalDigest`；`Engine.ReconcileApple(ctx, AppleReconciliationRequest) (Operation,error)`。

- [x] 先加入行为测试并保存 red：实际 PATCH 成功后丢失响应，原操作保持 pending；恢复准确结果且 Apple 写次数不增加。
- [x] 添加 file MD5/父集合、完整 build 身份和版本核对；只恢复四个已声明阶段。
- [x] 原子更新与恢复历史；测试审批/目的地/快照变更、并发争抢、重开幂等和后续阶段旧输入拒绝。
- [x] 各种错误/部分状态保持 journal 原样；执行定向 race 测试。

### Task 2: 管理员命令与真实二进制

**Files:** `cmd/publisher/main.go`、`main_test.go`，新增显式运行 `scripts/verify-apple-recovery.go`。

**Interfaces:** `is-publisher -config ... -reconcile-apple` 从 stdin 读取严格 JSON；与其他动作模式互斥，普通发布协议继续拒绝恢复动作。

- [x] 用测试固定权限边界，先观察未实现时失败，再实现专用入口。
- [x] 真实 CLI 对缺少停进程确认、未知动作/字段、陈旧快照、替代外部 ID 和改变原绑定的拒绝验证，不访问真实 Apple。
- [x] 实际 TLS 契约经产品 Engine 核对恢复；真实二进制重开已恢复 journal 并幂等读取。二进制不提供替换 Apple origin 或跳过签名的生产选项；没有真实远程 CLI 恢复证据。

### Task 3: 回归与交付

**Files:** `docs/app-store.md`、`docs/validation.md`、`README.md`、本计划；输出源码/报告/手册/证据。

- [x] 执行 vet、全量 race（专用 PostgreSQL）与 21 个跨平台程序编译；无 UI 改动则不重复 UI/Flutter 检查。
- [x] 主线程正确性/安全/简化复查；核对 18080、旧输出摘要与临时进程关闭。
- [x] 导出新命名文件，源码 ZIP 逐字节核对和敏感值扫描；明确真实模型、原生设备/签名、商店材料和正式交付剩余边界。

## 验收记录

证据根目录：`work/apple-recovery-l4jlbjc_`。四个阶段均经真实 TLS/SQLite 的响应丢失或实际 SQL 失败验证；恢复零外部写，后续阶段不能被旧输入回退。199 个顶层 / 446 项含子场景 Go race 检查、vet 和 21 项跨平台编译通过；本轮没有界面改动。

第二次全量验收复用非空专用库导致旧临时仓库缺失，随后保留原库并新建空数据库通过最终检查。没有修改产品备份逻辑；运行时缺少 psql/createdb 的检查改用已有 pgx 驱动。失败与纠正记录保留，专用 PostgreSQL 已停止。

真实 CLI 验证的是本地检查、受控输入拒绝与已恢复契约 journal 的幂等读取。没有真实 Apple 账号/签名/远程恢复/上架，未满足 P3；管理员本地恢复不授予平台批准。完整产品目标保持未完成。


导出核验：新增源码 ZIP、续建报告、恢复手册、证据 JSON 共四份，均使用新名称，不覆盖旧文件。源码 248 文件，相对上一轮 AX 源码 15 项新增/变化；ZIP 逐文件比对、已知本机敏感值精确扫描与 git diff --check 通过。旧 83 份输出保持原 SHA256，输出总数 87。摘要与最终文件清单见证据目录 export-result.json。保留未提交、未推送状态。
