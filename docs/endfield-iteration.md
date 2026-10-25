# 2026-10-12 · 连接策略、界面与托盘修复

## 真实任务与数据流

本软件的三项主任务是保存远端连接并挂载盘符、查看各磁盘的实际读取活动、管理启动与挂载环境。
保留 Vue/Wails 的业务桥接，未另造静态页面或游戏术语。

`连接表单 → SaveProfile → profiles.json → MountOptions → mounter.Start`。
挂载层将地址族和代理同时应用到 OpenList 登录/元数据、WebDAV、S3、文件 range 下载与上传使用的 HTTP transport。
配置码带出同一组 options，导入仍只新增配置、不会自动挂载。
CLI 的 `mount --ip-family auto|ipv4|ipv6` 覆盖 `OPENLISTFS_IP_FAMILY`；GUI 同步到 `.env` 时保留代理和地址族。
这些 CLI 路由设置用于 mount，其他诊断子命令不在本次功能范围内。

高级选项中的「连接地址族」支持自动、仅 IPv4、仅 IPv6。空字段保持旧配置的自动语义。
强制模式使用实际 `net.Dialer` 的 `tcp4` / `tcp6`，不向另一地址族回退；HTTP 重定向沿用同一 transport。
使用 HTTP(S)/SOCKS 代理时，本机可约束的是到代理的 socket，代理到远端的解析与连接由代理决定，界面会显示这个限制。
修改已挂载配置时，下次挂载才应用新选项。

## 设计对应与素材

已阅读本地 ReEndfield 的 AGENTS、design-language、design-philosophy、components、adaptation-recipes、acceptance、agent-playbook、typography-assets、spatial-interaction、visual-consistency，以及研究记录。
实际查看的参照包括 `evidence/cn/section-02.png`（官网稳定栏目）、`assets/reference/game-ui/G07-performance-graphics-settings.png`（官方支持公告中的游戏设置截图）、`evidence/skland/skland-endfield-wiki-desktop-1440.png`（官方 Wiki）。
这些证据用于观察结构，不作为本软件的界面截图。

| 保留的设计关系 | OpenListFS 中的实现 |
| --- | --- |
| 白纸工程档案 / 黑幕信息区 | 浅色工作面用于连接编辑与设置；盘符状态和活动曲线保持独立黑幕；深色主题使用同样的表面层级 |
| 荧光黄选择与动作 | 主导航当前页、选中的连接/协议、焦点内轨、挂载/保存动作；状态仍有明确文字 |
| 工业排版与索引 | 展示字族只标识产品和页面分类；连接索引对应真实连接，盘符标牌对应实际挂载目标 |
| 背景、图纸、工作面 | 连续平滑的原创地形远景、独立正交网格图纸、实体前景表单；装饰不拦截点击或进入阅读顺序 |
| 明确的对象动作 | SVG 方向图标、悬停/焦点边界与内部轨；触控保留可见入口；减少动态时保留静态状态 |

本次布局创新是将盘符标牌、连接资料和挂载动作合成同一个状态面，同时将协议/身份编辑、挂载参数和环境设置保持独立任务。
移动端提供可展开的连接索引，保留切换、导入和新建入口；从其他视图点击当前连接也会回到工作台。
没有为视觉验收增设业务页面，也没有固定某次活动/版本。

字体遵循 `--ef-font` / `--ef-latin` / `--ef-data` / `--ef-wide` / `--ef-mono` 的角色：
完整 Noto Sans SC 绘制功能中文，Space Grotesk 绘制普通 Latin/data，data 保留 tabular-nums；Michroma 仅用于展示分类和品牌，IBM Plex Mono 仅用于文件路径。
字体母版、OFL 和 metadata 保留在生产资源内。

用户随后明确要求使用官方素材，因此生产包额外纳入两张官网静态纹理，分别用于盘符状态面和选中连接右缘。
它们的来源、SHA-256、日期、使用目的、权属和本次用户选择分别记录在 `frontend/public/endfield/assets.json`；官方素材没有被改标为开放许可。
其余图形为本项目 SVG 和 ReEndfield CC0 原创图形。未纳入官网 JavaScript、官方角色/Logo 或研究字体模块。
全部界面资源离线加载。

## 托盘排查与修复

用户报告时的 PID 592032 中，`wailsWindow` 对 WM_NULL 能响应，`SystrayClass` 隐藏窗口不能响应。
证据：`build/tray-verification/running-instance.json`。
原实现以 `go systray.Run(...)` 启动托盘，未将隐藏 HWND 创建及 GetMessage 消息循环固定到同一个 Windows 线程；普通 Go 协程可能迁移线程。
另外双击未绑定动作，菜单的挂载/卸载/退出处理同步运行在托盘回调里，耗时操作可能阻塞消息处理。

修复包含：对托盘协程调用 LockOSThread；绑定双击显示并恢复最小化窗口；耗时菜单动作异步执行；菜单标题仅在变化时更新；使用已有的独立嵌入图标（原先误从 frontend embed FS 查找）。
对原运行实例仅调用 Windows 窗口恢复接口，将主窗口唤回；未重启进程、卸载磁盘或替换正在运行的文件。
证据：`build/tray-verification/running-window-restored.json`。
旧进程内的托盘代码不能热替换；完成旧实例的卸载和退出后，启动新版即可使用修复。

## 验收证据与范围

| 层级 | 实际结果 | 证据 |
| --- | --- | --- |
| Go 实现 | `go test ./...`、`go vet ./...` 通过；真实本地 IPv4/IPv6 HTTP socket、地址族不匹配拒绝、代理 socket 和重定向限制，以及配置码/GUI/CLI 路由转换通过 | `internal/netpolicy/policy_test.go`、配置码测试、`app_test.go` |
| 前端构建 | Vite 构建、TypeScript noEmit 通过 | 构建输出 |
| 既有业务回归 | 13 项通过，保存、导入导出、失败保留草稿、挂载状态、驱动恢复路径、明暗/系统主题 | `build/ui-verification/results.json` |
| 设计与可用性 | 9 组通过；18 个页面/主题/宽度组合无水平溢出；触控、标签键盘、下拉预览/取消/提交、焦点恢复、长名称、错误状态、reduced-motion、forced-colors | `build/endfield-verification/results.json` |
| 字体与对比 | 230 个实际 CDP 字体样本覆盖初始、弹窗、动态配置/会话及指标；中文为本地 Noto，普通数据为本地 Space Grotesk；111111 与 888888 等宽；18 个布局的文本对比达到常用 4.5:1 / 大字 3:1 门槛 | 同上，fonts 与 contrast |
| 原生托盘 | 隔离目录中的实际新 GUI：100 次消息响应、10 次关闭/双击恢复、最小化恢复、5 次右键菜单/取消、显示命令与退出命令通过 | `build/tray-verification/results.json` |
| 原生截图 | 实际 WebView2 的空配置实例，仅捕获该子进程窗口 | `build/tray-verification/native-window.png` |

浏览器截图来自本软件的真实 Chromium 渲染与操作，使用显式模拟的 Wails 数据桥；不把模拟挂载称为真实云盘挂载。
桌面/移动、明暗、监控、设置、高级下拉、触控、低动态与高对比截图在 `build/endfield-verification/`。
对截图按参考库的排版、灰阶/黄语义、轴线、素材、动态与泛用性逐项复核；这是内部观察，不是用户效率实验或官方风格认证。

未验证：真实远端 OpenList/WebDAV/S3 的 IPv4/IPv6 挂载与写入、SOCKS 服务器上游策略、真实安装/UAC/升级、其他浏览器/辅助技术、原生 WebView2 的逐节点字体 CDP。
原生托盘测试注入真实窗口消息，未模拟任务栏上的物理鼠标动作。
200% 采用 1440px 屏幕对应 720 CSS px 的等价重排检查，没有声称执行了浏览器工具栏缩放。

测试迭代中修正了“点击当前连接不返回工作台”和自定义下拉的高对比鼠标焦点；其余重跑涉及字体内部家族名称、状态文案与高级区域开关的测试定位修正。

## 运行与重验

```powershell
go test ./...
go vet ./...
.\frontend\node_modules\.bin\tsc.cmd --noEmit --project frontend/tsconfig.json
npm.cmd --prefix frontend run build
python -X utf8 scripts/check-ui.py
python -X utf8 scripts/check-endfield.py
python -X utf8 scripts/check-tray.py --exe build/bin/openlistfs-gui-endfield-compact.exe
.\scripts\build-installer.ps1 -GuiFilename openlistfs-gui-endfield-compact.exe
```

新版 GUI：`build/bin/openlistfs-gui-endfield-compact.exe`，从该目录启动可使用原目录中的 profiles.json。
新版安装包：`build/bin/OpenListFS-0.1.0-windows-amd64-setup.exe`，旁边提供 SHA-256。
安装包仍未签名，本次仅构建与核验内容，没有运行安装程序。

## 左栏溢出修正（2026-10-12）

用户截图中的连接列表发生横向滚动；除了左栏宽度，Grid 默认最小尺寸也让长域名撑大了卡片。
桌面左栏改为 280 CSS px，两个紧凑档为 240 / 220 CSS px；移动端仍占满宽度。
列表采用 `minmax(0,1fr)` 限制列宽、`max-content` 保留真实行高，名称和域名自动换行，挂载标记保留位置。
选中纹理定位到卡片自身，长内容仅使用列表的纵向滚动。

`build/sidebar-verification/results.json` 记录明暗两主题各九档宽度（390–1440）的实际 Chromium 布局检查，125% 像素比例。
覆盖截图中的名称/域名、长名称、连续长域名和已挂载标记，检查文本位于卡片内、行间不重叠及无横向溢出；18 个组合通过。
浏览器使用模拟 Wails 数据桥，没有连接截图中的服务器。
该目录提供整体截图和连接卡片截图；新版程序采用独立文件名，未替换或退出当前运行实例。

## 大字与紧凑布局（2026-10-12）

正文、操作、标签与输入从 14 提到 16 CSS px，说明及元数据从 12 提到 14；编辑页签为 18。
保留 Noto / Latin / data / Wide 的原有角色，英文索引保持独立层级。
通过页头、状态卡、表单分区和设置行的留白收紧密度，主状态卡最小高度从 168 降至 132 CSS px、页签从 56 降至 48；按钮和选项保留至少 44px 触控目标。
桌面索引宽度微调至 288px，卡片图标槽和间距收紧，名称与域名保持完整换行及按内容计算的行高。
黑幕状态区、纸面表单与共用轴线更鲜明；设置和编辑区用硬顶线建立分区，连接索引用细线分隔，黄色仍只强调选中和动作。

服务器与后端名称分别排版，后端整体换行，不拆成单个字母；窄屏操作区横排，运行时长有独立可读空间。
Vite、TypeScript、13 项业务回归及 9 组验收通过，覆盖 18 个页面/主题/宽度组合、230 个实际字形样本。
`build/compact-verification/results.json` 覆盖明暗各九档宽度的字号、长名称、长域名和挂载标记；`caption-results.json` 另验证 18 个操作行布局，防止运行时长被挤成竖排。
最终完整工作台截图为该目录中的 `final-workspace-*`，原生 WebView2 截图为 `build/tray-verification/native-window.png`。

原生托盘脚本前两次复测在恢复后立即打开菜单时超时；脚本现等到 Wails 的前台恢复完成再打开菜单，最终 6 项检查通过。业务托盘实现未变。
本次只修改排版及测试脚本同步，不重复运行未受影响的后端测试。没有真实远端挂载或运行安装程序，原有实例及挂载保持不动。
