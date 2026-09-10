# VoHive Plus 当前任务面板

> 详细历史已归档到 `tasks/history.md`；长期经验规则在 `tasks/lessons.md`。

## 当前决策

- 源码基线：采用 `windloom/vohive-open`，不再以 `openvohive/openvohive` 作为主线。
- 运行体选择：VoCat、本项目 VoHive Plus、`iniwex5/vohive` 备份运行体是三选一关系，不做同时运行。
- 部署槽位：VoHive Plus 与 `iniwex5/vohive` 共用 `/opt/vohive`；VoCat 使用 `/opt/vocat`。
- 打包策略：桌面 Release Action 拉取并校验 `MengMengCode/VoCat` latest release；`iniwex5/vohive` 备份运行体资源固定随包提供。
- 运行路线：WSL2 是当前优先交付路线；VirtualBox Headless + 最小 Debian 保留为后续更可控的 VM 路线。
- 安装目标：构建阶段允许联网；用户安装和运行阶段不应再从 GitHub 拉源码或二进制。
- 桌面目标：Windows 桌面程序统一管理后端启动、停止、日志、Web UI 打开、运行体选择和环境诊断。
- Git 规则：没有用户明确确认，不推送远程。

## 近期状态

- [x] 桌面端已支持 VoHive Plus、VoCat、`iniwex5/vohive` 三选一运行体。
- [x] 版本已升级到 `1.0.7`，Release/README/桌面配置已同步。
- [x] 本地 VoCat 资源同步策略已改为：优先复用本地资源，其次拉取 latest；GitHub 不可达时本地构建软跳过并在总结中提示。
- [x] 桌面窗口关闭后后台进程残留问题已修复，关闭/无窗口时会释放 WSL 后端与保活子进程。
- [x] eSIM 切卡后处理已改为按目标卡策略收敛，避免沿用旧卡快照。
- [x] VoWiFi 前置代理选择已改为“VoWiFi 默认代理 -> 国家规则 -> 直连”。
- [x] SOCKS5 UDP Associate 返回 loopback/unspecified relay 时会归一到 TCP 代理入口主机，避免 WSL 把 UDP 发到自己的 `127.0.0.1`。

## 待处理

### 2026-09-09 本轮联合修复计划

目标：切卡后模组真实身份与目标卡一致，随后按目标卡策略运行；用实机握手证据定位 Lebara VoWiFi。

#### 本轮根因证据（2026-09-09 复核）

- eSIM：默认 `use_refresh_true=false`、`radio_cycle=false` 时，profile enable 成功只代表 eUICC APDU 已接受；AT `QCCID/CIMI` 仍可能保留旧卡。此前仅在显式 RadioCycle 或 VoWiFi 临时飞行模式下拉起 Online 并轮询，因此默认 AT 切卡会在旧身份上超时并降级。当前工作区已有未提交的 `post_switch_at_reload.go`，其目标是以受控 `CFUN=0→1` 完成真实 SIM 重载；须先用测试证实其涵盖默认配置、旧 token、取消和重载失败，不能将其当作已修复。
- Lebara：实机失败在 `IKE_SA_INIT` 超时，此阶段尚未进入 AKA，不能把问题归因于 Lebara SIM 鉴权。Lebara `204/04` 会导向 NL 归属 MCC；当前代理策略必须先证实实际使用的默认代理/relay 与 ePDG 可达。未收集到 Lebara 与 VOXI 在同一 SOCKS5 UDP 路径、相同 proposal 下的可比 INIT 探针结果前，不修改 IKE proposal。

- [x] 并行根因调查：eSIM 子代理追踪恢复所有权，VoWiFi 子代理核对 IKE/运营商差异；主代理采集 WSL 实机证据。
- [x] 确认当前运行的是上轮二进制；AT 默认切卡路径未触发 SIM reload。实测 CFUN=4→1 仍旧卡，CFUN=0→1 读到目标 Lebara。
- [ ] eSIM 子任务（首要）：先运行新增 AT 重载回归测试并确认 RED/GREEN；验证默认 `radio_cycle=false` 情况确实执行一次受控 `CFUN=0→1`，只在目标 ICCID 与 IMSI 都收敛后投影目标卡策略。覆盖正常在线、自然生效、重载失败、旧 token、取消、旧 identity generation 和目标策略。
- [ ] VoWiFi 子任务（先诊断后改动）：用本地 INIT 探针固定同一 SOCKS5 UDP 代理/relay 与当前 proposal，对比 VOXI ePDG 和 Lebara ePDG；记录目标、UDP relay、请求长度、响应/超时。仅当该证据显示协商差异时，才修改 `third_party/vowifi-go/engine/swu/ikev2/sa.go` 及测试；若两者均无响应，优先修代理出口或路由。
- [ ] 修复 `internal/device/vowifi_start_profile.go` 的新 IMSI 回退旧 PLMN 漏洞，并补回归测试。
- [ ] 主代理集成评审：审查既有脏工作区改动与本轮最小补丁是否重叠，运行相关 Go 回归；编译并核对本地/桌面资源/WSL 实际运行哈希。
- [ ] 实机验收：VOXI→Lebara→VOXI 双向切卡，以及两卡 VoWiFi 启动；明确记录未通过的阶段，不以单元测试代替实机结论。
- [ ] 在本节记录评审结果，并更新 `tasks/lessons.md`。

本轮评审：进行中。旧记录中的“已修复”不能代替本轮实机验收。

### 2026-09-09 eSIM 第三轮审查结论（等待架构确认）

- [x] 已以回归测试锁定 AT 默认重载、ICCID/IMSI 错误门控、旧 token/generation、重复 finalize、CFUN=1 短暂失败恢复等风险；定向 AT 套件 19 项在 WSL 通过。
- [x] 已确认并修复 `switch_token` 同时承担 finalize 互斥与延迟 retry 授权造成的生命周期冲突。
- [x] 已实现会话化 token：每个非零 token 的 `postSwitchSession` 持有 identity generation、finalize claim 和有限期 retry lease；finalize 释放自身 claim，retry 完成/超时/新 token 替换后回收会话。短临界区只做授权，不在锁内执行 AT、网络、数据库或策略 I/O。
- [x] 已添加事件化回归：正常 finalize 后第二次实时 ICCID 读取与 retry 策略投影均发生；取消与新 token 会废止旧 retry。该测试以事件而非 session map 消失判断完成，并以 `go test -race` 在 WSL 通过。
- [x] 实机验收（切卡）：已完成 Lebara→VOXI 及“VOXI VoWiFi 已建隧道”→Lebara；实时身份与目标策略均收敛。

- [x] 将当前 VoWiFi 代理修复重新编译并部署到 WSL `/opt/vohive`，确认正在运行的后端使用新版逻辑。
- [ ] 用户确认后再做本地 git commit；除非用户再次明确确认，否则不推送远程。
- [ ] 联合修复 Lebara VoWiFi 与 eSIM 切卡问题。
  - [x] Lebara：VOXI 可拉起但 Lebara `204/04` 在 IKE_SA_INIT 阶段无响应；已补 SOCKS5 UDP IKE timeout 上下文，错误会带目标 ePDG、代理、relay、本地 UDP、Non-ESP marker、timeout 和请求长度。
  - [x] Lebara：已将 PrepareStart 算出的 AKA app 偏好真正绑定到启动用 SIM adapter，避免进入 IKE_AUTH 后仍默认 USIM。
  - [x] eSIM：切卡后新卡不生效；已修补刷新成功后未重新投影目标卡策略的问题，并拒绝旧 generation 迟到身份写入。
  - [x] 失败态：启动失败后前端详情保留最后一次 `last_error_class`/`last_error`，不再被 Pool 失败处理立刻清空成 `--`。
- [ ] 实机复测 Lebara/VOXI WiFi Calling：确认日志出现预期代理路线，并确认失败原因能明确区分代理/ePDG IKE 无响应、身份、AKA 或隧道协商。
- [ ] 视实机结果决定是否继续优化“飞行模式预热等待超时”的日志等级或等待策略。
- [ ] 后续评估 VirtualBox Headless 路线是否仍需要投入，避免和 WSL2 主路线重复建设。

## 当前验证记录

- [x] `go test -race ./internal/device -run '^TestHandleESIMSwitchAfterNormalFinalizeKeepsRetryLeaseForIdentityRefreshAndPolicyProjection$' -count=1 -v`：通过。
- [x] `go test ./internal/device -run '^TestATSwitchReload' -count=1 -v`：19 项通过。
- [x] `go test ./internal/device -run 'TestResolveVoWiFiUpstreamProxyPrefersDefaultForLebara|TestBuildVoWiFiStartProfileDerivesLebaraEPDGFromLiveHomePLMN|TestBuildVoWiFiStartProfileOnlyUsesHomeCacheForSameIMSI' -count=1 -v`：通过。
- [x] `go test ./third_party/vowifi-go/runtimehost/identity -count=1 -v`：通过。
- [x] WSL 只读环境检查：`/opt/vohive/config/config.yaml` 存在，但 `/dev/cdc-wdm0` 缺失；本轮不能进行真实切卡或 Lebara IKE 探针。
- [x] 2026-09-10 实机 eSIM：部署本轮后端后，Lebara→VOXI 与“VOXI VoWiFi 已建隧道”→Lebara 两次切卡均经过 AT `CFUN=0→1` 后以实时 ICCID+IMSI 收敛；目标卡策略已投影，未发生旧卡身份或策略回写。
- [x] 2026-09-10 实机 VoWiFi：VOXI `234/15` 经当前 SOCKS5 UDP 前置代理成功建立 IKE/IPsec/IMS 隧道。Lebara `204/04` 使用正确 ePDG、NL 代理规则和 USIM AKA 启动画像，但在 IKE_SA_INIT UDP 读超时。
- [x] Lebara ePDG 同出口探针：默认 proposal（两次独立 relay/源端口）、AES256/SHA256/PRF512/MODP2048、无 Non-ESP marker、UDP/500、以及直写已解析 IPv4 `109.39.144.148:4500` 均零响应；VOXI 默认 INIT 同出口收到 responder SPI。结论：不修改 IKE SA/marker/ePDG DNS，需更换为可达 Lebara ePDG UDP/4500 的代理出口或处理远端出口 IP 过滤。
- [x] `go test ./internal/db ./internal/device ./internal/vowifihost ./internal/upstreamproxy ./third_party/vowifi-go/engine/swu -count=1` 通过。
- [x] `tsx --test web/tests/*.test.ts` 31 项通过。
- [x] `vite build` 通过；存在 Browserslist 旧数据和 chunk size warning，但不是失败。
- [x] `vue-tsc --noEmit` 通过。
- [x] `git diff --check` 通过。
- [x] Linux 后端已重新编译为 `dist/vohive-open_linux_amd64`，版本 `1.0.7`，BuildTime `2026-09-09T04:08:49Z`。
- [x] 桌面资源、Tauri debug/release target resources 和 WSL `/opt/vohive/bin/{vohive,vohive-plus}` 已同步到同一 SHA256：`45b1c9aff3d91ea417b31cf0266938a86e2dbab40e264b8dd94ac91696d5add5`。
- [x] WSL 新后端已启动，PID `2135`，`/ping` 返回 `{"message":"pong"}`，运行中 `/proc/2135/exe` 哈希与磁盘二进制一致。
- [x] `pnpm --dir desktop tauri build` 通过，生成 `desktop/src-tauri/target/release/vohive-plus-desktop.exe`。

## 历史索引

- `tasks/history.md`：阶段计划、修复过程、版本发布、部署和验证记录的归档摘要。
- `tasks/lessons.md`：从历史中沉淀出的工程规则和排障经验。
