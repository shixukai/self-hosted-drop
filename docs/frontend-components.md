# 前端组件与界面实现

## 目录

- [1. 组件来源与采用范围](#1-组件来源与采用范围)
- [2. 界面设计规格](#2-界面设计规格)
- [3. 源码与构建](#3-源码与构建)
- [4. 原型验证与实施边界](#4-原型验证与实施边界)

## 1. 组件来源与采用范围

调研日期：2026-10-14。HeroUI 已实际接入本地 React 原型；生产前端选型与视觉方案待评审。以下均为组件作者或维护方的一手页面。

| 来源 | 可直接查看的示例 | 项目采用范围 |
|---|---|---|
| HeroUI v3 | [图文卡片](https://heroui.com/en/docs/react/components/card#with-images)、[按钮](https://heroui.com/en/docs/react/components/button)、[标签页](https://heroui.com/en/docs/react/components/tabs)、[弹窗](https://heroui.com/en/docs/react/components/modal) | 实际使用 Card、Button、Tabs、Chip、TextField、Input、Label、Modal；统一主题与交互基础 |
| Aceternity UI | [File Upload](https://ui.aceternity.com/components/file-upload) | 参考文件叠层与拖放反馈；未复制组件代码，FileStack 为项目内实现 |
| Untitled UI | [File Uploaders](https://www.untitledui.com/react/components/file-uploaders) | 参考文件行、进度、成功、失败与重试的状态组织；未复制代码或采用付费模板 |

HeroUI 的 [快速开始](https://heroui.com/en/docs/react/getting-started/quick-start) 与项目的 React 19、Tailwind CSS 4 技术路线一致。基础组件共用 HeroUI，业务组合独立实现，使按钮、间距、表单与弹窗的行为一致。Aceternity 和 Untitled UI 用作具体交互参考。

原型锁定 `@heroui/react` 与 `@heroui/styles` 为 3.2.6，React / React DOM 为 19.3.0，Tailwind CSS 为 4.3.3，Lucide React 为 1.56.0，Manrope 字体包为 5.3.0；完整依赖以 `design-preview/package-lock.json` 为准。

HeroUI 3.2.6 包的 `package.json` 标注 MIT，而随包 `LICENSE` 为 Apache-2.0；本项目保留随包许可原文，不以元数据替换它。Manrope 使用 SIL OFL 1.1，Lucide 保留 ISC 与所含 Feather 图标的 MIT 声明。构建产物随附 [第三方声明](third-party-notices.txt)。

## 2. 界面设计规格

| 区域 | 设计规格 | 对应源码 |
|---|---|---|
| 全局导航 | 顶部工作区、站点/部署/API/系统入口、主题开关；手机上分两行 | App 顶部导航、`.app-header` |
| 站点工作区 | 三列封面卡片，名称、域名、发布状态、版本与时间分层；可切换列表 | `renderProject`、HeroUI Card |
| 搜索筛选 | HeroUI Tabs 与关联内容面板；关键词搜索、空结果、清除筛选；⌘/Ctrl+K | `filters`、TextField、Tabs.Panel |
| 上传入口 | 文件叠层插画、拖放反馈、原生文件/目录选择 | `FileStack`、`.dropzone` |
| 发布设置 | 左侧文件与检查结果，右侧名称、固定域名、路由模式与主操作 | `.upload-layout`、TextField、Input |
| 部署详情 | 当前版本封面、固定网址、版本时间线、受控预览与回滚说明 | `.history-layout`、`.release-row` |
| 反馈 | HeroUI Modal 管理焦点；独立通知区域宣告复制结果 | `Modal.Backdrop`、`.app-toast` |

色彩使用近白背景、深蓝灰文字与蓝色主操作；深色模式替换语义变量。完整颜色值见开发设计第 11.2 节及 `src/style.css`。桌面主标题 34–36 px、卡片标题 17 px，移动主标题 28 px。中文使用系统字体回退，Manrope 在构建中随包分发。

内容布局在 1000 px 切换为两列，在 760 px 切换为单列。常规反馈使用短暂位移与透明度变化，关闭系统动效偏好时禁用动画与过渡。上传、发布等主要移动操作保持至少 44 px 触控高度。

封面属于内容类型示意：报告使用排版海报、接口分析使用曲线插画、产品演示使用几何构图。P0 使用可信内置模板，管理后台不执行上传的 HTML；自动截图需要另行设计隔离执行环境，不作为当前 P0 的前提。

## 3. 源码与构建

| 文件 | 用途 |
|---|---|
| `design-preview/src/main.tsx` | 页面、真实 HeroUI 组件、示例数据与本地交互 |
| `design-preview/src/style.css` | 主题、布局、封面插画、响应式与动效 |
| `design-preview/package.json` / `package-lock.json` | 锁定依赖与构建命令 |
| `design-preview/scripts/export-preview.mjs` | 将编译后的脚本、样式与字体内嵌到离线 HTML，并附第三方声明 |
| `docs/ui-preview.html` | 可直接打开的离线交互原型，由构建生成 |
| `docs/assets/ui-preview-*.png` | 桌面、深色、上传检查和移动端实际渲染截图 |

在项目根目录执行以下命令。验证环境使用 Node.js 22.21.1；首次安装需要访问 npm。

```sh
cd design-preview
npm ci
npm run dev
```

本地开发地址由 Vite 输出，绑定 `127.0.0.1`。生成可分享的离线文件时，在同一目录执行：

```sh
npm run build
```

构建先运行 TypeScript 检查，再打包并覆盖 `docs/ui-preview.html`。修改源码后重新构建，不直接修改生成的 HTML。`node_modules/` 与 `dist/` 不作为源码交付内容。

## 4. 原型验证与实施边界

已完成 TypeScript 与生产构建检查，并使用隔离的 Chrome 验证以下原型行为：

- 站点搜索、状态筛选、键盘切换标签页、卡片/列表切换与空结果恢复。
- 深浅主题切换及刷新后的偏好保留；⌘K 聚焦搜索。
- 新站点表单重置、现有站点标识锁定、原生文件选择及示例检查展示。
- 弹窗获得焦点、Escape 关闭并返回触发位置；发布和 Token 操作明确标注演示范围。
- 1440 px 桌面首页，以及 1024、768、390、320 px 下各主要页面无整页横向溢出。
- 原型加载及上述交互未出现 JavaScript/控制台错误，也未发出 HTTP/HTTPS 请求；未点击外部来源链接。

这不是完整的 WCAG、跨浏览器或后端验收。原型尚未包含真实上传、解压、扫描、任务进度、API 请求、权限、Token 生成、路由发布、回滚、自动截图或 DNS 操作。正式实现须接入 Go API，并覆盖开发设计第 11、16、17 章的状态与验收要求。
