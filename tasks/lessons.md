# Lessons

## 工作流与 Git

- 没有用户明确确认，不推送远程；本地提交和远程推送要分开确认。
- 大改动先写计划并记录到 `tasks/todo.md`，完成后把过程沉淀到 `tasks/history.md`，只把长期有效经验留在本文件。
- 当前仓库常有未提交改动；修改前先看 `git status`，不要回滚用户或前序任务留下的变更。
- 发布前必须核对版本号、Release notes、README、桌面配置、workflow 默认版本是否一致。
- GitHub Action 正式发布必须强校验第三方二进制；本地开发构建可以在 GitHub 不可达时软跳过，并在总结中明确提示资源缺失。

## 构建与工具链

- Windows 侧 Go/Node/npm 不一定可用；本项目有效构建工具链优先使用 `.toolchains/go/bin/go`、`.toolchains/go/bin/gofmt`、`.toolchains/node/bin/node`。
- Web 测试和构建应在 WSL 中显式使用项目内 Linux Node，避免误用 Windows npm 或跨平台 `node_modules`。
- 不要并行操作同一个 `node_modules` 目录，pnpm 可能移动依赖导致测试 runner 丢失。
- WSL `/mnt/f` 上的 Web 构建可能耗时较长；无输出超时不等于失败，先看进程和 `dist` 更新时间。
- 后端重新编译后不能只更新 `dist/`；还要同步桌面资源、已存在的 Tauri target resources，并核对 WSL `/opt/vohive/bin/vohive` 的 SHA256。
- 从 Windows/PowerShell 调 WSL 编译后端时，BuildTime 不要依赖嵌套 `bash -lc` 里的 `$BUILD_TIME` 拼接；优先用 Makefile 默认 UTC ISO BuildTime，或在最终 `go build -ldflags` 参数里直接传入已生成的时间字符串，并用 `strings`/`/info` 核对。

## WSL2 与 USB

- `usbipd-win` 是 WSL2 USB 直通硬前置；安装需要管理员权限，安装后当前 PowerShell PATH 可能不会刷新，应直接检测服务或绝对路径。
- `usbipd attach --wsl` 需要目标 WSL2 发行版保持 Running，可用隐藏 `wsl.exe -d <distro> --exec sleep 3600` 保活。
- DJI 4G 模组 `2ca3:4006` 需要分接口绑定：前几个接口给 `option` 生成 `/dev/ttyUSB*`，最后接口给 `qmi_wwan` 生成 `/dev/cdc-wdm0` 和 `wwan0`。
- 如果把 `2ca3:4006` 全部交给 `option`，会有 `/dev/ttyUSB0-4`，但没有 QMI 控制口，VoHive QMI 发现会失败。
- WSL 后端启动必须以 `/opt/vohive` 为工作目录；只传 `-c /opt/vohive/config/config.yaml` 不够，数据库和缓存相对路径可能落错位置。
- 停止 WSL 后端不要用会匹配当前 `bash -lc` 命令行的宽泛 `pkill -f`；应锚定真实进程或先枚举 PID 再逐个停止。

## 桌面端与运行体

- VoHive Plus、VoCat、`iniwex5/vohive` 是三选一运行体；VoHive Plus 与 `iniwex5/vohive` 共用 `/opt/vohive`，VoCat 使用 `/opt/vocat`。
- 桌面端保存的运行体必须是持久配置；读取到未知旧值时回退默认后端，避免重启后悄悄切到另一套运行体。
- Tauri v2 不能只假设关闭最后窗口就退出进程；窗口关闭/销毁和无窗口事件循环都要显式清理后端并退出。
- 桌面命令返回 `ActionResult { ok:false }` 后，前端必须展示 `message` 和 `suggested_admin_command`，不能只等 Promise resolve。
- 本地构建与 GitHub Action 构建要保持资源语义一致：正式产物不能缺 VoCat 或第三方 NOTICE，本地软跳过也要有清晰提示。

## 卡策略与 eSIM

- `card_policies` 是按 ICCID 生效的权威意图；新增字段要检查默认策略、旧库迁移、API PATCH/PUT 语义和设备上线后的策略投影。
- live API 要先保存用户意图，再碰硬件；保存失败不能先发 AT，硬件失败时要回滚或暴露分裂状态。
- eSIM 切卡成功后的权威运行态是目标卡策略，不是切卡前快照；旧快照只适合切卡失败、身份未确认或目标策略不可用时兜底。
- VoWiFi 启动前会临时进入 `CFUN=4`/飞行模式；如果此时切 eSIM，后处理要先拉 `ModeOnline` 触发 SIM 身份重新加载。
- UIM readiness 只是加速路径；backend 不支持 readiness 时应回退 live ICCID/IMSI 轮询，不应直接判 degraded。
- 目标卡策略要求 VoWiFi 但 SIMAuth gate 暂时不就绪时，应保留目标 VoWiFi 期望态和 desired recover 退避。
- eSIM 切卡后的补刷新不是普通身份刷新；一旦确认目标 ICCID，必须重新投影目标卡策略，并用 identity generation 拒绝迟到刷新写回旧流程。
- `switch_token` 不能在 finalize 返回时无条件清理：若有延迟身份重试，必须以独立 session/lease 保留有限期授权；新 token、取消或 deadline 必须使旧 session 失效。验证异步 retry 应等待可观察的 I/O/投影事件，不能以内部 map 被删除作为“已完成”证据。

## VoWiFi 与代理

- IMSI/MCC/MNC 解析要注意 `MNC=00`，不能把前导零丢掉，否则 ePDG 域名会错。
- IKE_AUTH 返回 Notify 14 (`NO_PROPOSAL_CHOSEN`) 时，优先检查 Child SA/ESP proposal、TSi/TSr，不要继续归因到 EAP 身份或 SIM。
- AES-GCM 属于 AEAD；在只完整支持 CBC+HMAC 时，默认 ESP proposal 只能声明实际可承载的 CBC-SHA256/CBC-SHA1。
- `EAP success without CHILD_SA` 不一定是失败；部分 ePDG 会先返回 EAP Success，再要求客户端发送 final `SK { AUTH }`。
- 启用 SOCKS5 UDP 前置代理时，IKE/ESP 外层出口是代理 relay，不是 ePDG 直连；此时不要把 ePDG 保护路由绑到可能 down 的 `wwan0`。
- SOCKS5 UDP Associate 返回 `127.0.0.1`、`0.0.0.0` 或 `::` 时不能总按字面使用；WSL 连接 Windows 本地代理时应归一到 TCP 代理入口主机。
- VoWiFi 代理选择应优先尊重用户显式设置的默认前置代理，再使用 MCC 国家规则；国家表是高级分流兜底，不应覆盖当前代理意图。
- Lebara/VOXI 这类差异若卡在 IKE_SA_INIT timeout，优先记录目标 ePDG、代理、relay、本地 UDP、Non-ESP marker 和 timeout；此阶段尚未进入 EAP-AKA，不能直接归因到 SIM 鉴权。

## 前端与用户体验

- 设备/卡详情请求要防旧响应覆盖：切换设备或 ICCID 时先清旧状态，再用请求序号和 ICCID 归一比较确认响应归属。
- 卡策略面板只应在策略属于当前 ICCID 时允许操作，避免迟到响应污染当前卡。
- 版本展示要有应用版本兜底，避免后端缺字段时显示 Unknown。
- 备注文案要短，尤其是运行体选择和代理设置；复杂背景放文档，不压到桌面端 UI。
