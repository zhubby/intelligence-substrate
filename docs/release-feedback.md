# 已发布版本的问题反馈

企业治理 → 交付效能选择目标与需求版本后，可上传上线问题报告，再登记上线缺陷。旧版本仍在使用时，即使需求已改版、移除该客户端或候选已失效，报告仍归属原发布版本。

## 上传与准确来源

`POST /api/v1/releases/{id}/reports?artifactId=...&name=...&key=...` 接收原始文件体，大小为 **1 B–16 MiB**。仅允许 artifactId、name、key 三个查询参数，每个恰好出现一次且非空；重复、未知、缺失或编码错误返回400。name最多200字符，key最多160字符。会话请求带 `X-CSRF-Token`，沿用登录Cookie。

仅当前有 owner/product/quality 权限的人类可写。单独的release、developer、viewer角色，以及Agent/Worker不能登记人的问题依据。上传前和注册事务内重新验证当前身份、成员权限及来源；流传输期间降权或原制品损坏会拒绝注册。

平台验证准确候选摘要、历史需求快照、候选清单中的package、项目、目标、需求版本、仓库、完整提交、SHA256及真实制品字节。该目标的发布记录必须关联同一制品，且当前或历史已有published/verified；旧delivered按原全端交付语义兼容。仅有externalId或未曾发布的halted不足以通过。其他客户端发布不能代替该目标。

请求不接受客户端指定需求版本、提交或仓库。平台从原候选派生元数据，登记artifact：

```json
{
  "type": "report",
  "purpose": "release-feedback",
  "requirementRevision": 1,
  "releaseId": "release_example",
  "releaseBinding": "candidate-binding",
  "releaseArtifactId": "artifact_package_example",
  "releaseArtifactDigest": "package-sha256",
  "commit": "full-source-commit",
  "repositoryId": "repository_example",
  "target": "web",
  "digest": "report-sha256",
  "size": 128
}
```

示例ID和摘要仅为说明占位值。下载通过 `GET /api/v1/artifacts/{id}/content`，沿用项目读取权限并核对摘要，始终作为附件返回。

## 重试与事务

key按项目与登记人去重，准确输入包括候选绑定、原制品摘要、文件名、报告摘要与大小。同key同输入成功重放返回同一报告；改变文件、文件名或来源复用key返回409。每次重放仍验证当前权限、原制品和报告字节。

工作台每次打开上传弹窗生成稳定key。回复中断时保留原文件重试；要更换文件或来源，关闭后重新打开。已保存文件不会被不同内容的重试覆盖。

报告资源、幂等记录和成功审计同一事务提交；审计失败则回滚。临时文件在成功或失败后清理；已进入私有内容寻址库、但注册失败的字节可能保留，不成为有效业务资源。备份保留报告、原候选、制品、真实源码、幂等与审计，不恢复访问凭据。

## 历史缺陷登记与更正

上传仅保存问题材料，不自动创建缺陷或声明修复。随后用原有命令登记：

```json
{
  "goalId": "goal_example",
  "type": "production_defect",
  "requirementRevision": 1,
  "releaseId": "release_example",
  "proofArtifactId": "artifact_feedback_report_example",
  "reference": "production-ticket-42",
  "key": "stable-defect-request-key",
  "note": "问题来源、观察范围与实际依据"
}
```

提交至 `POST /api/v1/commands/measurements.record`。可选requirementRevision必须等于平台从所引用候选派生的版本；不能把旧报告计入当前版本。报告对应目标必须已有发布观测，专用问题报告还会核对其准确候选/制品绑定。

历史页允许上传、登记、作废及更正上线缺陷。`measurements.void {id,key,reason}` 保留原记录；重新登记沿用reference并使用新key，保存replacesMeasurementId。预览确认、业务验收和本人投入仍要求当前需求版本。报告或原制品失去完整性时，观测记录保留，标为待核对并从有效统计排除。

## 用途和验收边界

purpose=release-feedback不能用于业务验收、独立质量/环境/发布证据、商店资料或运行恢复证明。上传和缺陷登记不改变需求批准、质量门槛、候选审批、渠道状态或原始制品。

发布来源表示平台保存的发布观测：可能来自受控连接器，也可能是有权人员提交的发布声明；本入口不独立证明外部商店或生产环境已发布。测试中的发布回执、问题材料和人工判断均为本机合成样本，验证系统行为，不证明真实生产故障、已修复、完整缺陷观察窗口或业务收益。

原18080预览保持原版。Apple原生构建、Windows节点、移动真机、企业正式签名、真实渠道/生产以及两个参考应用的完整P3交付仍需分别验收。
