# 双主题 Agent 交付控制台

## 范围

登录、应用外壳、目标与需求、代码与评审、质量与证据、构建与发布、
Agent 与执行、企业治理采用同一套深浅主题。页面围绕项目、交付目标、
Agent 执行、人工决策和证据组织，不是通用聊天产品。

本轮不改变后端 API、命令参数、项目权限、需求基线、方案和正式发布的
人工批准边界。签名、源码冲突、停用节点、未明操作核对和发布恢复继续
使用原有入口与安全条件。

## 组件与样式

- Mantine 8.3.18：Provider、AppShell、按钮、基础表单、原生选择、Tabs、
  弹层、通知和基础展示。React 保持 19.1.1，保留 Vite 和 Tailwind 4。
- Ant Design X 2.10.0 + 必要依赖 antd 6.6.4：仅在已有消息与执行记录中使用
  Bubble、ThoughtChain；没有另一套全站控件或模型调用运行时。
- React Flow 12.10.0：按需载入的只读任务依赖图。
- Tremor Raw BarList、DonutChart：选取源码适配 Tailwind 4、语义令牌和
  Recharts 3.3.0。来源、Git blob、修改清单与完整上游许可保留在
  `web/THIRD_PARTY_NOTICES.md`、`web/public/licenses/tremor-raw-LICENSE.txt`。

`web/src/styles/tokens.css` 定义两套语义色、字体、间距、圆角、阴影与焦点。
Mantine 主题和局部 Ant Design X Provider 使用相同令牌；图与图表使用
CSS 变量。`web/src/styles.css` 只保留原有业务布局和功能样式，颜色已改为
语义变量；`web/src/styles/workbench.css` 管理新外壳、页面层级与响应式。
Looseleaf 覆盖、旧基础组件及无消费者的 Radix/CVA 直接依赖已移除。

默认深色，偏好键为 `is-workbench-color-scheme`。同源的
`/theme-init.js` 在应用 bundle 之前同步设置主题；不使用内联初始化脚本，
不放宽服务端 CSP，不依赖外部字体 CDN。中文使用仓库已有的 Noto Sans SC。

业务页面、Ant Design X、React Flow 与图表分块按需载入；登录页面不请求
这些业务或专项 bundle。普通操作过渡约 150ms；减少动效时禁用持续动效，
图表不播放数据动画。

## 数据与行为

- 交付概览显示实际资源计数与已记费用；质量概览区分工作项完成声明、
  CI 证据、目标环境证据和未关闭缺陷，不能互相替代。
- 列表仍是默认。方案图来自该方案的任务逻辑 ID；执行图只接纳同时匹配
  项目、目标、方案和需求版本的真实工作项，包括同版本动态工作项。
- 未创建的方案任务不借用历史工作项。重复 ID、重复绑定、缺失依赖和
  循环依赖显示核对提示；不会补造节点、修改方案或触发执行。
- 图允许选择、缩放和平移，禁用节点拖动、连接、重连和删除。节点详情
  复用现有工作项弹层。切换项目或方案后重新回到列表。
- Bubble 显示已有消息及其原始负载与关联元数据；ThoughtChain 展示真实
  `agent_step` 操作、执行 ID、需求版本和已确认观察，不是模型内部思维。
  未确认操作不显示假进度，也不自动重放。
- 图表只读取现有资源、费用报告和效能报告。空数据、未知、无效数值不
  补零；真实零值保留。未登记费用不加入分布，未采样的比率仍显示待采集。
- 原生 select、字段名、文件上传和 FormData 布尔/数值转换保持兼容。
  Mantine 管理弹层焦点，App 管理关闭后的焦点恢复；移动导航限制焦点，
  背景内容 inert，Escape 关闭并恢复到打开按钮。

## 复验

```sh
cd web
npm ci
npm run check
npm run build
cd ..
python3 scripts/verify-workbench.py --themes dark light
python3 scripts/verify-workbench.py \
  --suite workbench-theme --suite workbench-surfaces \
  --projects desktop tablet mobile --themes dark light
```

每组使用独立合成状态、构建、截图和 trace，固定测试端口为 18082，不能
同时运行多个验证器。已有预览不会被复用或重启。报告记录主题、视口、
套件源码哈希、本机确定性模型调用和构建结果，不调用外部付费模型。

新增测试覆盖主题首载/持久化、登录按需载入、项目隔离、图版本绑定、
只读行为、缺失与零指标、消息/步骤呈现、键盘、焦点、移动隔离、长中文、
长摘要、两倍缩放等效布局、减少动效、CSP 和外部请求。原有业务断言保留。

界面验证和合成本地报告不代表真实模型效果、企业 SSO 接入、物理设备、
原生签名、正式渠道或生产部署完成。

## 本轮验收与预览

验收完成于 2026-11-09，所有测试使用隔离合成状态，未调用外部付费模型。

- 完整业务矩阵：36 个套件，桌面/移动端 × 深/浅主题，共 144 组、328 项
  通过（312 项浏览器用例、16 项纯展示适配用例），无 skipped、unexpected
  或 flaky。原有权限、基线失效、审批、签名与恢复断言全部保留。报告：
  `work/workbench-check-r8703akf/result.json`。
- 三档宽度：390、768、1440px × 深/浅主题，6 组专项检查通过、90 张截图。
  覆盖登录、六入口、代码与长摘要、治理面板、依赖图和弹窗；未发现页面
  横向溢出、CSP 错误、外部资源请求或图交互写入。报告：
  `work/workbench-ui-preview-8Z5GGK/visual-check-LmsQPK/result.json`。
- 实际 200% 页面缩放：使用独立 Chromium 配置，不使用 CSS zoom 或捏合
  缩放。1440/768px 窗口 × 深/浅主题共 4 组通过、60 张视口截图；布局宽度
  分别为 720/384 CSS px，DPR 为 2，visual viewport scale 为 1。再次覆盖
  六入口、长代码、图表、依赖图、弹窗和键盘焦点。截图通过 CDP 直接采集
  物理视口，避免浏览器缩放时自动截图的裁切。报告：
  `work/workbench-ui-preview-8Z5GGK/zoom-check-WZsZGL/result.json`；复验脚本：
  `work/workbench-ui-preview-8Z5GGK/zoom-smoke.mjs`。
- 减少动效：两主题下检查可见元素的实际计算样式，未发现超出 0.01ms 的
  动画/过渡或无限动画。报告：
  `work/workbench-ui-preview-8Z5GGK/motion-check.json`。
- 最终类型检查与生产构建通过，输出与测试构建隔离：
  `work/workbench-final-build-oUH9Cx/types.log`、
  `work/workbench-final-build-oUH9Cx/build.log`。依赖树、Prettier 和
  `git diff --check` 检查通过。

独立本地预览：`http://127.0.0.1:50824`。该实例使用专用合成数据和静态构建，
没有连接真实执行节点、外部模型或发布渠道。登录信息仅保存在本机权限为
0600 的 `work/workbench-ui-preview-8Z5GGK/access.json`，不在文档中公开密码。
实例元数据：`work/workbench-ui-preview-8Z5GGK/preview.json`。
没有提交、推送或部署，没有复用或重启已有预览服务。

## 依赖审计

正常依赖解析保留 npm 的 peer 检查，不使用 `--force` 或
`--legacy-peer-deps`。本轮 npm audit 报告 3 个 low，来自
`@ant-design/x → mermaid → katex` 的传递依赖链
（GHSA-238p-pmpm-9mq7）。本界面未启用 Markdown/Mermaid/KaTeX 渲染；
没有用强制降级或越界覆盖掩盖告警，仍需随上游修复继续跟进。
