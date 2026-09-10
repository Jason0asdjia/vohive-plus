# VoHive Plus 历史归档

> 本文件承接原 `tasks/todo.md` 中已经完成的阶段流水。当前待办见 `tasks/todo.md`，长期经验见 `tasks/lessons.md`。

## 2026-08：源码基线与运行路线

- 项目主线确定为 `windloom/vohive-open`，旧安装脚本仓库只作为参考，不再以 `openvohive/openvohive` 为主线。
- 确定两条运行路线：WSL2 作为优先交付路线，VirtualBox Headless + 最小 Debian 作为后续更可控 VM 路线。
- 安装/运行目标明确为离线友好：构建阶段可联网，用户安装和运行阶段不应依赖 GitHub 下载源码或二进制。
- 早期计划包含源码落地、WSL2 手工验证、VirtualBox 验证、Windows 桌面壳、便携包和离线运行。

## 2026-08：WSL2 USB 与 DJI 模组适配

- 验证 Windows 侧 WSL2、`usbipd-win`、目标发行版、systemd 与 USB 直通能力。
- DJI 4G 模组在 Windows 侧枚举为 `2ca3:4006 Baiwang`，WSL 中需要临时执行 `option`/`qmi_wwan` 动态 ID 绑定。
- 适配重点从“让 WSL 看见设备”扩展到“让 VoHive 同时拿到 AT 口、QMI 控制口和 `wwan0`”。
- 修复 WSL USB prepare 对 DJI ECM/QMI 场景的自动绑定，避免全部接口被 `option` 抢占导致缺少 `/dev/cdc-wdm0`。
- 增补 Quectel 原厂族 ID 识别范围，覆盖非 DJI 包装但同类 QMI 模组。

## 2026-08：漫游与卡策略

- 卡策略语义扩展为驻网、蜂窝数据、数据漫游等独立意图，不再把漫游和设备全局配置混在一起。
- Web 前端补充卡策略面板的状态归属校验，防止旧请求覆盖当前 ICCID。
- live API 调整为先保存用户意图，再应用硬件状态；失败路径明确暴露错误或恢复旧策略。
- 后续 eSIM 切卡修复继续沿用“目标 ICCID 的 `card_policies` 是权威状态”的原则。

## 2026-08：桌面端基础能力

- Windows 桌面壳采用 Tauri 路线，管理 WSL 后端启动、停止、日志、Web UI 打开和环境诊断。
- 桌面命令返回结构化 `ActionResult`，前端展示错误消息和建议管理员命令。
- 桌面端增加 WSL 进程识别、后端版本展示、Web 品牌版本兜底、USB prepare 等能力。
- 内置后端二进制从 Git 跟踪中移出，改由构建/同步脚本管理资源目录。

## 2026-08：Release 与 GitHub 工作流

- 建立 GitHub 远程、版本号、CI、Release 产物和 release notes 规则。
- Docker 发布 workflow 被删除或弱化，桌面/二进制发布成为主线。
- Release 触发规则改为 main 推送触发，并对普通用户补充前置依赖说明。
- 版本从 1.0.1 逐步推进到 1.0.7，每轮发布前核对桌面配置、README、workflow 默认版本和 release notes。

## 2026-08 至 2026-09：VoWiFi 协议调试

- 修复 ePDG MNC 解析问题，特别是 `MNC=00` 不能丢前导零。
- 修复部分 ISIM 回退与 AT SMSC 刷新路径，增强运营商兼容性。
- 针对 Vodafone UK/VOXI 的 IKE 行为做多轮对齐：记录 payload/notify，分析 `NO_PROPOSAL_CHOSEN`，调整 ESP proposal。
- 发现并修复 `EAP Success` 后还需发送 final AUTH 的状态机缺口。
- 在代理模式下修复 TUN 路由保护误绑 `wwan0` 的问题：有 SOCKS5 UDP 代理时外层出口是代理 relay，不应给 ePDG 强绑可能 down 的蜂窝网卡。

## 2026-09：前置代理与国家规则

- 初始实现按 SIM home MCC 解析国家，并将国家规则映射到 SOCKS5 UDP 前置代理。
- 发现国家规则命中后仍可能直连或误绑路由，后续修复为代理模式下正确走 SOCKS5 UDP relay。
- 对 Lebara/VOXI 场景进一步明确：用户当前选择的代理意图应优先于 MCC 国家表。
- 新增 `vowifi_default` 默认代理语义，VoWiFi 启动顺序改为“默认代理 -> 国家规则 -> 直连”。
- 修复 SOCKS5 UDP Associate 返回 loopback/unspecified relay 时 WSL 发错目标的问题。

## 2026-09：第三方运行体对比与打包

- 对比 VoCat 与本项目：VoCat 可作为独立运行体，不与 VoHive Plus 同时运行。
- 明确 `iniwex5/vohive` 是原项目来源，Orson 只是备份镜像；桌面端显示和文档统一改为 `iniwex5/vohive`。
- 确定三运行体关系：VoHive Plus、VoCat、`iniwex5/vohive` 三选一。
- 路径语义固定：VoHive Plus 与 `iniwex5/vohive` 共用 `/opt/vohive` 唯一活动槽位；VoCat 使用 `/opt/vocat`。
- 桌面 Release Action 拉取 `MengMengCode/VoCat` latest release 并校验 `SHA256SUMS`；本地构建优先复用本地资源，GitHub 不可达时软跳过并提示。
- 固定打包 `iniwex5/vohive` 备份运行体资源，避免每次构建都临时下载备份包。

## 2026-09：桌面端 1.0.7 发布准备

- 版本升级到 `1.0.7`，同步桌面 package、Tauri/Rust crate、Tauri 配置、Cargo.lock、Release workflow 默认值、README 和 release notes。
- 审查发现并修复 VoCat 首次部署缺少管理员 bootstrap 的问题。
- Release workflow 增补 VoCat LICENSE 下载，`resources/vohive/THIRD_PARTY_BINARY_NOTICES.md` 记录第三方二进制来源。
- 修复本地 `sync:vocat` 在资源缺失时静默跳过导致桌面 build 缺 VoCat 的问题。
- 修复桌面窗口关闭后进程残留，避免 release exe 被后台进程锁住导致编译时间和产物不更新。
- 本地构建曾生成 `desktop/src-tauri/target/release/vohive-plus-desktop.exe`，并确认 `target/release/resources/vocat/` 包含 VoCat 资源。

## 2026-09：eSIM 切卡后收敛

- 日志显示从 VOXI 切到 Lebara 后，eUICC profile 状态与 AT 实时身份出现分裂：profile 列表显示目标卡启用，但 `AT+QCCID`/`AT+CIMI` 仍读到旧卡。
- 根因之一是切卡前 VoWiFi 已启用，模组处于 `CFUN=4`，后处理按旧快照恢复而没有先让 SIM 身份重新加载。
- 修复后，切卡后若需要 radio cycle 或切卡前处于 VoWiFi 临时飞行模式，会先临时拉 `ModeOnline`，再轮询 live ICCID/IMSI。
- 目标身份确认后执行 `resolveAndApplyPolicy("esim_switched")`，按目标卡策略恢复运行态。
- SIMAuth gate 暂时失败时保留目标 VoWiFi 期望态和 desired recover 退避，等待低频恢复。

## 2026-09：当前未提交 VoWiFi 默认代理修复

- Lebara IMSI `204...` 被 MCC 表解析为 `NL`，只靠国家规则会导致用户“开了英国代理但没有命中”的认知冲突。
- 新增前置代理默认标记 `vowifi_default`，同一时间最多一个默认代理；禁用代理会清掉默认标记。
- VoWiFi 启动 resolver 改为先查询默认代理，再查国家规则，最后直连。
- 前端代理列表和编辑抽屉增加“VoWiFi 默认”显示和设置，文案改短。
- SOCKS5 UDP 数据面修复：当 UDP Associate relay 返回 `127.0.0.1`/`0.0.0.0`/`::` 且 TCP 代理入口非 loopback 时，改用 TCP 入口主机。
- 失败分类修复：本地 relay UDP timeout 归类为 `proxy`，便于 UI 和日志判断。
- 验证通过：相关 Go 包、Web 测试、Vite build、Vue 类型检查、`git diff --check`。
- 2026-09-09 已重新编译 Linux 后端、同步桌面资源、部署并重启 WSL `/opt/vohive`，运行中后端 SHA256 为 `c396bedde669a2c71a7e51d3b916dec06638152a2b15ae9c25944d22f47e7e75`；本地桌面 release 也已重新构建。
