# 真实 AX 执行与结果传输

日期：2026-11-06。继续批准规划的 P0/P1 执行基础设施验收，使用真实 AX、Substrate 与 gVisor，不以模拟 API 或 Task phase 代替实际命令完成。

现场已证实官方最新 release v0.3.1 与本机 Substrate 新 API 不兼容：旧 controller 调用 `ateapi.Control`，返回 `Unimplemented / unknown service`，Task 进入 Failed，未启动 guest。改用上游固定提交 `2187dd453d60952a4952eb1fc07c81b5721d5680`（2026-11-04），依赖 `go.ate.dev/substrate v1.0.0-rc.1`、PostgreSQL 和内置 reconciler。该提交尚未发布，不能称为稳定 release。保留旧版本失败证据。

## 验收环境

本机已有可用 Substrate/Kubernetes。编译固定提交官方 AX server/CLI/runner，在本轮专用容器与 PostgreSQL 数据目录运行；共享现有 kind 节点网络以访问实际 guest，控制面只监听节点本地地址。通过指定 context 与临时 API 凭据访问独立测试 atespace，不向现有命名空间安装 AX。发布本轮专用 runner 镜像到已有本地测试 registry，使用准确 digest。旧 v0.3.1 测试容器先保存日志再停止，不修改现有集群部署。

保留现有部署、counter WorkerPool、服务、SDK、凭据与 79 份交付文件；预览 18080 后端不重启。临时凭据不输出、不进入源码、日志或最终交付。只清理本轮确切创建的 Task、Workspace、Actor/模板及容器；不运行全局删除或镜像/卷清理。

## 目标与边界

1. 用官方 CLI 创建真实 Task/Workspace，等待实际 guest 可达；运行命令并核对真实退出码、输出及返回文件，覆盖非零退出。只把指定镜像 digest 的实际工作结果交回平台。
2. 验证 suspend/resume 后持久目录内容、控制面/PostgreSQL 重启后的任务状态、取消与删除资源；遇到不兼容保留准确失败证据，不降低现有权限或修改上游默认安全配置。
3. 真实平台 HTTP/Git/Worker 通过 AX 执行批准的代码、构建和独立质量任务，验证提交/制品/证据链；模型不参与该命令路径，批准身份明确为测试样本。新发现的适配缺口先复现后修复，包含传输、错误与清理语义。
4. 测试后确认本轮资源终止、原服务与文件保留，更新产品实际支持矩阵、运行手册和新源码导出。

运行于本机集群的可信测试控制面不能代表企业多租户隔离、生产高可用、真机/签名/正式发布或真实模型研发验收。暂停恢复的数据持久性与进程继续语义分开记录。
