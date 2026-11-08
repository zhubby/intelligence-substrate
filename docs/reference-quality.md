# 同一制品的参考应用质量验收与报告

可信质量Worker可以收集受保护测试命令生成的报告，控制面把报告与准确测试执行、源码提交和应用包绑定。两套参考应用已用此机制从实际Git源码构建Flutter Web，再自动调度真实Chrome业务验收，形成带独立证据的待审批候选。没有执行正式发布。

同一契约也供[Android同制品模拟器验收](reference-android-quality.md)使用；原生模拟器的环境与报告独立记录，正式候选仍需真机证据。

## 受保护报告契约

人类质量负责人或项目owner通过 `pipelines.create` 声明可选 `reportPath`：

```json
{
  "projectId": "project_example",
  "title": "真实浏览器业务验收",
  "command": ["node", "/opt/company/qa/verify-reference-web.mjs", "--application", ".", "--api-url", "http://127.0.0.1:18136", "--output", "quality/browser.tar.gz"],
  "runtime": "darwin",
  "method": "automated",
  "verificationEnvironment": "browser",
  "reportPath": "quality/browser.tar.gz"
}
```

示例HTTP仅用于已明确选择的本机诊断。真实产品配置继续使用受控HTTPS。参考验证器及依赖由管理员提供，不能把开发源码中的任意脚本当作可信独立检查。

`reportPath` 必须是普通相对文件路径，最多512字符，每段最多128字符，仅支持ASCII字母、数字、下划线、点和连字符。拒绝父级跳转、重复分隔符、反斜线、绝对路径、Git元数据、`.intelligence-*`、Windows设备名和尾点。省略或空字符串表示只保留原有日志行为。外部工作项不能声明或覆盖该字段；自动绑定摘要包含策略的完整数据。

Worker先移除源码中同路径的普通旧文件；遇到链接或目录则阻塞。实际命令结束后，控制器在原执行租约下上传新产生的普通文件，最多512MiB。任务和工具不接收控制面令牌。成功但缺报告不能产生通过证据；真实命令失败仍保留非零退出码、日志及已产生的报告。失败且无报告时保留失败，不伪造附件；报告上传或安全校验失败时显示阻塞。

上传时，服务器从当前测试租约推导 `testedArtifactId`，不相信调用者上传的关联字段。结果里的 `reportArtifactId` 必须指向本次独立质量Worker上传的 `report`，同项目、目标、需求版本、仓库、提交、客户端与准确被测package，并通过实际字节完整性检查。失败结果若提供报告ID也必须通过来源校验。其他run、Worker或同提交的另一package不能借用。

通过证据保留 `proofArtifactId` 与 `proofDigest`。候选创建和后续发布门槛重新核对原成功执行、准确附件、摘要及存储字节；删除附件字段不能降级为日志策略。原手动 `pipelines.run` 同样支持报告，自动绑定要求继续独立生效。报告、制品、执行、证据及去重历史均进入现有备份恢复。

质量页可填写策略报告路径，在独立证据和自动验证记录下载准确附件。失败记录仍可查看已产生的报告。报告是实际工具输出，报告内的文字自述不能替代受保护命令的实际检查、独立身份或物理验收。

## 单应用真实浏览器验证器

`scripts/verify-reference-web.mjs` 消费Worker下载的 `IS_ARTIFACT_PATH`：

```sh
rtk proxy env IS_ARTIFACT_PATH=/private/qa/web.tar.gz node scripts/verify-reference-web.mjs \
  --application /private/qa/exact-source \
  --api-url http://127.0.0.1:18136 \
  --output /private/qa/quality/browser.tar.gz
```

它只支持当前 `flutter-go-v1` 的feedback和booking已知业务，不能验收任意生成应用。它不调用模型。

1. 在任何提取/应用工具执行前，`reference-web-archive.py` 完整校验tar。只接受普通文件/目录，拒绝路径跳转、链接、设备、稀疏文件、重复/大小写冲突和文件充当父目录。最多20000条目、400MiB文件总量、单文件100MiB；另在tar解析前限制gzip展开量，避免PAX元数据绕过限额。结构检查要求Flutter入口及应用代码，但结构本身不证明真实编译或功能。
2. 用该次Worker准确检出的源码构建Go后端，使用独立临时状态文件。诊断API仅绑定显式127.0.0.1端口；端口已占用则拒绝，绝不接管已有服务。站点使用随机loopback端口，只服务已校验目录。
3. 启动真实Chrome，关闭service worker，拦截并拒绝声明的本地站点/API之外的浏览器请求。实际点击、输入、API返回和重启数据检查共同确定退出码。
4. feedback检查空标题、真实提交、重复点击单次写入、脚本文本不执行、离线恢复和后端重启持久性。booking检查过期客户端冲突、不透明48位十六进制凭据、服务端不保存明文、错误取消拒绝、正确取消、重启持久性和离线恢复。
5. 两种业务各检查1440px桌面及390px手机宽度、不溢出、Web不出现原生更新入口，并收集截图。页面异常或未声明外部请求使验证失败。
6. 关闭自己的Chrome、后端进程组和站点，归档 `result.json`、截图及API日志。报告记录实际包SHA256、源码文件摘要、后端二进制摘要、检查、时间及清理结果；失败时保留可用诊断。SIGTERM/SIGINT会清理自己的后端组后退出；SIGKILL/宿主丢失仍依赖专用节点的资源回收，不能称为无条件恢复。

这是一台机器上的编译包与源码后端一致性验收，没有公开部署、生产容量、真机或商店证据。Chrome手机宽度与移动设备验收分开记录。

## 隔离平台验收

`scripts/verify-reference-quality.go` 新建两套应用并逐一执行实际HTTP/Git/Worker流程：批准样本需求/构建方案→先绑定报告策略→真实Flutter构建/上传→质量Worker轮询自动调度→同一下载包的Chrome业务检查→上传报告/通过证据→创建待人工审批候选。它不调用 `pipelines.run`，重复轮询不会重复验证。

```sh
rtk proxy go run ./scripts/verify-reference-quality.go
```

运行前准备Go、Git、Python3、Node、RTK、`web/node_modules/@playwright/test`和本机Chrome。当前本机诊断入口复用忽略目录 `work/run-native.py` 配置固定Flutter和缓存；移到其他机器时由管理员替换为自己的工具链包装程序。API端口18136/18137必须空闲。结果写新建 `work/reference-quality-live-*`；也可传 `--work-dir` 指定不存在的证据目录。不会改已有预览、main仓库暂存状态或Docker。

本轮最终证据为 `work/reference-quality-e3fc4yp1/actual-source-exact/result.json`。两套新应用分别形成一条自动调度、两次实际执行（构建/质量）、一个独立报告、一条通过证据和一个 `awaiting_approval` 候选。报告包已独立展开，制品SHA和测试执行再次核对；每应用139个源码文件的完整集合及逐文件SHA256均与对应Git提交完全一致。所有批准身份是明确的本机测试身份；固定命令执行没有真实模型推理或企业人员验收。

独立回读曾发现源码清单混入RTK生成的运行元数据，并因目录名过滤遗漏Android命名空间内的 `quality` 源码。现仅排除明确工具运行目录及根目录报告/制品目录，保留嵌套业务目录；最终重新生成两套包和报告，以完整Git树集合及逐文件字节校验阻止遗漏或混入。此前报告保留在私有证据中，不作为最终源码一致性证据。

## 复验与产品边界

```sh
rtk proxy go test -race ./internal/platform ./internal/worker -run 'QualityReport|WorkerCollectsProtectedQualityReports' -count=1
rtk proxy python3 -m unittest discover -s scripts -p test_reference_web_archive.py -v
rtk proxy npm --prefix web run test:e2e -- --suite quality-scheduling
```

真实PostgreSQL归档恢复需要显式专用 `IS_TEST_POSTGRES_URL`，未配置时跳过，不能当作数据库完整验收。本轮全量Go与工作台结果另见[验证记录](validation.md)。

两套Web候选补齐此前“外部浏览器包与平台构建包不同”的证据断点。完整P3仍需两种业务在所有声明必选端/渠道正式交付；iOS/macOS许可、Windows节点、原生/真机、真实签名、商店资质及企业生产验收继续开放。等待用户自行接受Apple协议，不代接受，不把仅生成原生工程文件当作原生构建成功。
