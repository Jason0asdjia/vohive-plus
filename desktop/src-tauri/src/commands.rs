use std::io::{BufRead, BufReader};
use std::path::Path;
use std::process::Stdio;
use std::thread;
use std::time::Duration;

use tauri::{AppHandle, Manager, State};

use crate::backend_variants::{
    by_id as backend_variant_by_id,
    variants_with_vocat_version as backend_variants_with_vocat_version, RESOURCE_VOCAT,
    RESOURCE_VOHIVE_PLUS, VARIANT_VOCAT,
};
use crate::desktop_config;
use crate::health::{check_health, WEB_URL};
use crate::logs::RingLog;
use crate::models::{
    ActionResult, BackendStatus, BackendVariant, RuntimeStatus, SetBackendVariantRequest, UsbDevice,
};
use crate::process::{clean_output, hidden_command, run_output};
use crate::{usbipd, wsl, AppState};

#[tauri::command]
pub fn detect(state: State<'_, AppState>) -> RuntimeStatus {
    build_status(&state)
}

#[tauri::command]
pub fn status(state: State<'_, AppState>) -> RuntimeStatus {
    build_status(&state)
}

#[tauri::command]
pub fn start_wsl(state: State<'_, AppState>) -> ActionResult {
    match ensure_wsl_running(&state) {
        Ok(pid) => action(
            true,
            format!("WSL 已启动并保活 pid={pid}"),
            Some(build_status(&state)),
            None,
        ),
        Err(err) => action(
            false,
            format!("启动 WSL 失败: {err}"),
            Some(build_status(&state)),
            Some(suggested_wsl_keepalive_command()),
        ),
    }
}

#[tauri::command]
pub fn stop_wsl(state: State<'_, AppState>) -> ActionResult {
    {
        let mut guard = state.wsl_keepalive.lock().expect("wsl mutex poisoned");
        if let Some(mut child) = guard.take() {
            let _ = child.kill();
            let _ = child.try_wait();
            state.logs.push("已释放 WSL 保活进程");
        }
    }

    match wsl::current_distro_running() {
        Ok(false) => action(true, "WSL 已是停止状态", Some(build_status(&state)), None),
        Ok(true) => match wsl::terminate_distro(Duration::from_secs(8)) {
            Ok(out) if out.status.success() => action(
                true,
                "WSL 已停止，WSL 内后端也会随之退出",
                Some(build_status(&state)),
                None,
            ),
            Ok(out) => action(
                false,
                format!("停止 WSL 失败: {}", clean_output(&out.stderr)),
                Some(build_status(&state)),
                Some(format!(
                    "\"{}\" --terminate {}",
                    wsl::executable(),
                    wsl::DISTRO
                )),
            ),
            Err(err) => action(
                false,
                format!("停止 WSL 失败: {err}"),
                Some(build_status(&state)),
                Some(format!(
                    "\"{}\" --terminate {}",
                    wsl::executable(),
                    wsl::DISTRO
                )),
            ),
        },
        Err(err) => action(
            false,
            format!("检查 WSL 运行状态失败: {err}"),
            Some(build_status(&state)),
            Some(format!(
                "\"{}\" --terminate {}",
                wsl::executable(),
                wsl::DISTRO
            )),
        ),
    }
}

#[tauri::command]
pub fn attach_usb(state: State<'_, AppState>) -> ActionResult {
    let usbipd_status = usbipd::detect_usbipd();
    let Some(path) = usbipd_status.path.clone() else {
        return action(false, "未找到 usbipd-win", None, None);
    };
    let devices = usbipd::list_devices(&path);
    let Some(target) = devices.iter().find(|d| d.is_target) else {
        return action(
            false,
            "未发现 2ca3:4006 Baiwang 或 2c7c:* Quectel 模组",
            None,
            None,
        );
    };

    let step = usb_attach_step(target);
    if let Err(message) = usb_attach_preflight(step, wsl::current_distro_running().unwrap_or(false))
    {
        return action(
            false,
            message,
            Some(build_status(&state)),
            Some(suggested_wsl_keepalive_command()),
        );
    }

    match step {
        UsbAttachStep::AlreadyAttached => {
            return action(true, "USB 已连接到 WSL", Some(build_status(&state)), None);
        }
        UsbAttachStep::BindThenAttach => {
            match run_output(&path, &["bind", "--busid", &target.busid]) {
                Ok(out) if out.status.success() => {}
                Ok(out) => {
                    let msg = clean_output(&out.stderr);
                    return action(
                        false,
                        format!("usbipd bind 失败: {msg}"),
                        None,
                        Some(format!("\"{path}\" bind --busid {}", target.busid)),
                    );
                }
                Err(err) => {
                    return action(
                        false,
                        format!("usbipd bind 执行失败: {err}"),
                        None,
                        Some(format!("\"{path}\" bind --busid {}", target.busid)),
                    );
                }
            }
        }
        UsbAttachStep::AttachOnly => {}
    }

    let attach = run_output(&path, &["attach", "--wsl", "--busid", &target.busid]);
    match attach {
        Ok(out) if out.status.success() => {
            action(true, "USB 已连接到 WSL", Some(build_status(&state)), None)
        }
        Ok(out) => action(
            false,
            format!("usbipd attach 失败: {}", clean_output(&out.stderr)),
            None,
            Some(format!("\"{path}\" attach --wsl --busid {}", target.busid)),
        ),
        Err(err) => action(false, err.to_string(), None, None),
    }
}

#[tauri::command]
pub fn prepare_usb(state: State<'_, AppState>) -> ActionResult {
    let wsl_running = match wsl::current_distro_running() {
        Ok(running) => running,
        Err(err) => {
            return action(
                false,
                format!("检查 WSL 运行状态失败: {err}"),
                Some(build_status(&state)),
                None,
            )
        }
    };
    if let Err(message) = wsl_required_action_preflight("准备 WSL USB", wsl_running) {
        return action(false, message, Some(build_status(&state)), None);
    }

    match wsl::run_root(&["--exec", "/opt/vohive/bin/vohive-usb-prepare.sh"]) {
        Ok(out) if out.status.success() => {
            state.logs.push(clean_output(&out.stdout));
            action(true, "WSL USB 准备完成", Some(build_status(&state)), None)
        }
        Ok(out) => action(
            false,
            format!("WSL USB 准备失败: {}", clean_output(&out.stderr)),
            None,
            None,
        ),
        Err(err) => action(false, err.to_string(), None, None),
    }
}

#[tauri::command]
pub fn set_backend_variant(
    app: AppHandle,
    req: SetBackendVariantRequest,
    state: State<'_, AppState>,
) -> ActionResult {
    let variant = match backend_variant_by_id(&req.variant_id) {
        Some(variant) => variant,
        None => {
            return action(
                false,
                format!("未知后端运行体: {}", req.variant_id),
                Some(build_status(&state)),
                None,
            )
        }
    };
    if let Err(err) = desktop_config::save_selected_backend_variant(&app, &variant.id) {
        return action(
            false,
            format!("保存后端运行体选择失败: {err}"),
            Some(build_status(&state)),
            None,
        );
    }
    {
        let mut selected = state
            .selected_backend_variant
            .lock()
            .expect("backend variant mutex poisoned");
        *selected = variant.id.clone();
    }
    state.logs.push(format!(
        "已选择后端运行体: {} {}；停止并重新启动后端后生效",
        variant.name, variant.version
    ));
    action(
        true,
        format!(
            "已选择 {} {}，停止并重新启动后端后生效",
            variant.name, variant.version
        ),
        Some(build_status(&state)),
        None,
    )
}

#[tauri::command]
pub fn start_backend(app: AppHandle, state: State<'_, AppState>) -> ActionResult {
    if check_health().ok {
        let selected = selected_backend_variant_from_app_state(&state);
        if let Some(message) =
            backend_running_variant_guard(&selected, deployed_backend_variant_id())
        {
            return action(false, message, Some(build_status(&state)), None);
        }
        state.logs.push("后端健康检查正常，复用已有 WSL 进程");
        return action(true, "后端已在运行", Some(build_status(&state)), None);
    }

    if let Err(err) = install_or_import(&app, &state) {
        return action(false, err, None, None);
    }
    let mut guard = state.backend.lock().expect("backend mutex poisoned");
    if guard.is_some() {
        return action(true, "后端已在运行", Some(build_status(&state)), None);
    }

    let selected = selected_backend_variant_from_app_state(&state);
    let mut cmd = hidden_command(r"C:\Windows\System32\wsl.exe");
    cmd.args(backend_start_args(&selected));
    cmd.stdout(Stdio::piped()).stderr(Stdio::piped());
    match cmd.spawn() {
        Ok(mut child) => {
            attach_reader(child.stdout.take(), "stdout", &state);
            attach_reader(child.stderr.take(), "stderr", &state);
            let pid = child.id();
            state.logs.push(format!("已启动 WSL 后端 pid={pid}"));
            *guard = Some(child);
            drop(guard);
            action(true, "后端启动中", Some(build_status(&state)), None)
        }
        Err(err) => action(false, err.to_string(), None, None),
    }
}

#[tauri::command]
pub fn stop_backend(state: State<'_, AppState>) -> ActionResult {
    let mut guard = state.backend.lock().expect("backend mutex poisoned");
    if let Some(mut child) = guard.take() {
        let _ = child.kill();
        let _ = child.try_wait();
        state.logs.push("已释放桌面壳持有的 WSL 子进程");
    }
    drop(guard);

    let wsl_running = match wsl::current_distro_running() {
        Ok(running) => running,
        Err(err) => {
            return action(
                false,
                format!("检查 WSL 运行状态失败: {err}"),
                Some(build_status(&state)),
                None,
            )
        }
    };
    if let Err(message) = wsl_required_action_preflight("停止 WSL 后端", wsl_running) {
        return action(false, message, Some(build_status(&state)), None);
    }

    let stop_result = wsl::run_root_shell_timeout(backend_stop_script(), Duration::from_secs(8));

    match stop_result {
        Ok(out) if out.status.success() => {
            let msg = clean_output(&out.stdout);
            if !msg.is_empty() {
                state.logs.push(msg);
            }
            action(true, "后端已停止", Some(build_status(&state)), None)
        }
        Ok(out) => action(
            false,
            format!("停止 WSL 后端失败: {}", clean_output(&out.stderr)),
            Some(build_status(&state)),
            None,
        ),
        Err(err) => action(
            false,
            format!("停止 WSL 后端失败: {err}"),
            Some(build_status(&state)),
            None,
        ),
    }
}

#[tauri::command]
pub fn logs(state: State<'_, AppState>) -> Vec<String> {
    state.logs.snapshot()
}

#[tauri::command]
pub fn open_web() -> ActionResult {
    match hidden_command("cmd")
        .args(["/C", "start", "", WEB_URL])
        .spawn()
    {
        Ok(_) => action(true, "已打开 Web UI", None, None),
        Err(err) => action(false, err.to_string(), None, None),
    }
}

fn build_status(state: &State<'_, AppState>) -> RuntimeStatus {
    let wsl = wsl::detect_wsl();
    let usbipd = usbipd::detect_usbipd();
    let devices = usbipd
        .path
        .as_deref()
        .map(usbipd::list_devices)
        .unwrap_or_default();
    let health = check_health();
    let backend = backend_status(state, health.ok);
    RuntimeStatus {
        route: "wsl2".to_string(),
        wsl,
        usbipd,
        devices,
        backend,
        backend_variants: backend_variants_with_vocat_version(packaged_vocat_version()),
        selected_backend_variant: selected_backend_variant(state),
        health,
    }
}

fn backend_status(state: &State<'_, AppState>, health_ok: bool) -> BackendStatus {
    let mut guard = state.backend.lock().expect("backend mutex poisoned");
    if let Some(child) = guard.as_mut() {
        match child.try_wait() {
            Ok(Some(status)) => {
                let message = format!("后端已退出: {status}");
                state.logs.push(message.clone());
                *guard = None;
                BackendStatus {
                    running: false,
                    pid: None,
                    message: Some(message),
                }
            }
            Ok(None) => BackendStatus {
                running: true,
                pid: Some(child.id()),
                message: None,
            },
            Err(err) => {
                let message = format!("读取后端状态失败: {err}");
                state.logs.push(message.clone());
                *guard = None;
                BackendStatus {
                    running: false,
                    pid: None,
                    message: Some(message),
                }
            }
        }
    } else {
        external_backend_status(health_ok, wsl::managed_backend_pids(Duration::from_secs(3)))
            .unwrap_or(BackendStatus {
                running: false,
                pid: None,
                message: None,
            })
    }
}

fn external_backend_status(
    health_ok: bool,
    wsl_pids: Result<Vec<u32>, String>,
) -> Option<BackendStatus> {
    match wsl_pids {
        Ok(pids) if !pids.is_empty() => {
            let pid = pids[0];
            Some(BackendStatus {
                running: true,
                pid: Some(pid),
                message: Some(format!("检测到 WSL 后端进程 pid={pid}")),
            })
        }
        Ok(_) if health_ok => Some(BackendStatus {
            running: true,
            pid: None,
            message: Some("健康检查正常，后端可能由外部进程提供".to_string()),
        }),
        Err(err) if health_ok => Some(BackendStatus {
            running: true,
            pid: None,
            message: Some(format!("健康检查正常，但读取 WSL 后端进程失败: {err}")),
        }),
        _ => None,
    }
}

fn backend_running_variant_guard(
    selected: &str,
    deployed: Result<Option<String>, String>,
) -> Option<String> {
    match deployed {
        Ok(Some(deployed)) if deployed != selected => {
            let selected_name = backend_variant_by_id(selected)
                .map(|variant| format!("{} {}", variant.name, variant.version))
                .unwrap_or_else(|| selected.to_string());
            let deployed_name = backend_variant_by_id(&deployed)
                .map(|variant| format!("{} {}", variant.name, variant.version))
                .unwrap_or(deployed);
            Some(format!(
                "后端正在运行的是 {deployed_name}，当前选择是 {selected_name}；请先停止后端，再重新启动以切换运行体。"
            ))
        }
        Ok(None) => Some(
            "检测到 7575 已有运行中的未标记后端或外部服务；请先停止后端，再重新启动，让桌面壳接管当前运行体目录。"
                .to_string(),
        ),
        Err(err) => Some(format!(
            "后端正在运行，但读取已部署运行体失败: {err}；请先停止后端，再重新启动以接管当前运行体目录。"
        )),
        Ok(Some(_)) => None,
    }
}

fn install_or_import(app: &AppHandle, state: &State<'_, AppState>) -> Result<(), String> {
    let resource_dir = app
        .path()
        .resource_dir()
        .map_err(|err| format!("读取资源目录失败: {err}"))?;
    let selected = selected_backend_variant_from_app_state(state);
    let variant =
        backend_variant_by_id(&selected).ok_or_else(|| format!("未知后端运行体: {selected}"))?;
    let bin = resource_dir.join(variant_resource_path(&variant));
    let plus_bin = resource_dir.join(format!("resources/vohive/{}", RESOURCE_VOHIVE_PLUS));
    let cfg = resource_dir.join("resources/vohive/config.example.yaml");
    let script = resource_dir.join("resources/vohive/vohive-usb-prepare.sh");
    validate_vohive_resources(&variant, &bin, &plus_bin, &cfg, &script)?;
    let bin_wsl = wsl::sh_quote(&wsl::windows_path_to_wsl(&bin));
    let plus_bin_wsl = wsl::sh_quote(&wsl::windows_path_to_wsl(&plus_bin));
    let cfg_wsl = wsl::sh_quote(&wsl::windows_path_to_wsl(&cfg));
    let script_wsl = wsl::sh_quote(&wsl::windows_path_to_wsl(&script));
    let deploy = backend_deploy_script(&variant.id, &bin_wsl, &plus_bin_wsl, &cfg_wsl, &script_wsl);
    match wsl::run_root_shell(&deploy) {
        Ok(out) if out.status.success() => {
            for log in backend_deploy_success_logs(&variant, &clean_output(&out.stdout)) {
                state.logs.push(log);
            }
            Ok(())
        }
        Ok(out) => Err(format!("部署 WSL 资源失败: {}", clean_output(&out.stderr))),
        Err(err) => Err(err.to_string()),
    }
}

fn backend_deploy_script(
    variant_id: &str,
    bin_wsl: &str,
    plus_bin_wsl: &str,
    cfg_wsl: &str,
    script_wsl: &str,
) -> String {
    let is_vocat = variant_id == VARIANT_VOCAT;
    let variant_id = wsl::sh_quote(variant_id);
    if is_vocat {
        return format!(
            "mkdir -p /opt/vocat/bin /opt/vocat/config /opt/vocat/data /opt/vocat/logs && \
             cp {bin_wsl} /opt/vocat/bin/vocat && \
             printf '%s\\n' {variant_id} > /opt/vocat/config/desktop-backend-variant && \
             chmod +x /opt/vocat/bin/vocat && \
             if [ ! -s /opt/vocat/data/vocat.db ]; then \
               VOCAT_BOOTSTRAP_PASSWORD=$(dd if=/dev/urandom bs=18 count=1 2>/dev/null | base64 | tr -dc 'A-Za-z0-9' | head -c 24); \
               if [ -z \"$VOCAT_BOOTSTRAP_PASSWORD\" ]; then VOCAT_BOOTSTRAP_PASSWORD=\"vocat-$(date +%s)\"; fi; \
               printf '%s\\n' \"$VOCAT_BOOTSTRAP_PASSWORD\" | VOCAT_DATABASE_PATH=/opt/vocat/data/vocat.db /opt/vocat/bin/vocat bootstrap-admin >/opt/vocat/logs/bootstrap-admin.log 2>&1 && \
               printf 'VoCat 初始管理员: admin / %s\\n' \"$VOCAT_BOOTSTRAP_PASSWORD\" | tee /opt/vocat/config/desktop-bootstrap-admin.txt; \
             elif [ -f /opt/vocat/config/desktop-bootstrap-admin.txt ]; then \
               cat /opt/vocat/config/desktop-bootstrap-admin.txt; \
             fi"
        );
    }
    format!(
        "mkdir -p /opt/vohive/bin /opt/vohive/config /opt/vohive/data /opt/vohive/logs && \
         cp {bin_wsl} /opt/vohive/bin/vohive && \
         cp {plus_bin_wsl} /opt/vohive/bin/vohive-plus && \
         cp {script_wsl} /opt/vohive/bin/vohive-usb-prepare.sh && \
         if [ ! -f /opt/vohive/config/config.yaml ]; then cp {cfg_wsl} /opt/vohive/config/config.yaml; fi && \
         printf '%s\\n' {variant_id} > /opt/vohive/config/desktop-backend-variant && \
         chmod +x /opt/vohive/bin/vohive /opt/vohive/bin/vohive-plus /opt/vohive/bin/vohive-usb-prepare.sh"
    )
}

fn deployed_backend_variant_id() -> Result<Option<String>, String> {
    match wsl::current_distro_running() {
        Ok(false) => return Ok(None),
        Ok(true) => {}
        Err(err) => return Err(err),
    }
    let out = wsl::run_root_shell_timeout(
        "marker=''; \
         if pgrep -f '^/opt/vocat/bin/vocat( |$)' >/dev/null; then marker=/opt/vocat/config/desktop-backend-variant; \
         elif pgrep -f '^/opt/vohive/bin/vohive( |$)' >/dev/null; then marker=/opt/vohive/config/desktop-backend-variant; fi; \
         if [ -n \"$marker\" ] && [ -f \"$marker\" ]; then cat \"$marker\"; fi",
        Duration::from_secs(3),
    )
    .map_err(|err| err.to_string())?;
    if !out.status.success() {
        return Err(clean_output(&out.stderr));
    }
    let variant = clean_output(&out.stdout).trim().to_string();
    if variant.is_empty() {
        Ok(None)
    } else {
        Ok(Some(variant))
    }
}

fn backend_start_args(variant_id: &str) -> Vec<&'static str> {
    if variant_id == VARIANT_VOCAT {
        vec![
            "-d",
            wsl::DISTRO,
            "-u",
            "root",
            "--cd",
            "/opt/vocat",
            "--exec",
            "/usr/bin/env",
            "VOCAT_DATABASE_PATH=/opt/vocat/data/vocat.db",
            "/opt/vocat/bin/vocat",
            "serve",
        ]
    } else {
        vec![
            "-d",
            wsl::DISTRO,
            "-u",
            "root",
            "--cd",
            "/opt/vohive",
            "--exec",
            "/opt/vohive/bin/vohive",
            "-c",
            "/opt/vohive/config/config.yaml",
        ]
    }
}

fn variant_install_dir(variant_id: &str) -> &'static str {
    if variant_id == VARIANT_VOCAT {
        "/opt/vocat"
    } else {
        "/opt/vohive"
    }
}

fn variant_resource_path(variant: &BackendVariant) -> String {
    if variant.id == VARIANT_VOCAT {
        format!("resources/vocat/{}", RESOURCE_VOCAT)
    } else {
        format!("resources/vohive/{}", variant.resource_name)
    }
}

fn backend_deploy_success_logs(variant: &BackendVariant, stdout: &str) -> Vec<String> {
    let mut logs = vec![format!(
        "已部署 {} {} 到 WSL {}",
        variant.name,
        variant.version,
        variant_install_dir(&variant.id)
    )];
    let stdout = stdout.trim();
    if !stdout.is_empty() {
        logs.push(stdout.to_string());
    }
    logs
}

fn packaged_vocat_version() -> Option<String> {
    let exe = std::env::current_exe().ok()?;
    let resource = exe.parent()?.join("resources/vocat/VOCAT_VERSION");
    std::fs::read_to_string(resource).ok()
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum UsbAttachStep {
    AlreadyAttached,
    BindThenAttach,
    AttachOnly,
}

fn usb_attach_step(target: &UsbDevice) -> UsbAttachStep {
    match target.state.as_str() {
        "Attached" => UsbAttachStep::AlreadyAttached,
        "Not shared" => UsbAttachStep::BindThenAttach,
        _ => UsbAttachStep::AttachOnly,
    }
}

impl UsbAttachStep {
    fn requires_running_wsl(self) -> bool {
        !matches!(self, UsbAttachStep::AlreadyAttached)
    }
}

fn usb_attach_preflight(step: UsbAttachStep, wsl_running: bool) -> Result<(), String> {
    if step.requires_running_wsl() && !wsl_running {
        return Err("请先点击“启动 WSL”，待 WSL 运行后再连接 USB 到 WSL。".to_string());
    }
    Ok(())
}

fn wsl_required_action_preflight(action_label: &str, wsl_running: bool) -> Result<(), String> {
    if wsl_running {
        return Ok(());
    }
    Err(format!(
        "WSL 未运行，无法执行{action_label}；请先点击“启动 WSL”后再重试。"
    ))
}

fn validate_vohive_resources(
    variant: &BackendVariant,
    bin: &Path,
    plus_bin: &Path,
    cfg: &Path,
    script: &Path,
) -> Result<(), String> {
    let missing = [
        (variant.resource_name.as_str(), bin),
        (RESOURCE_VOHIVE_PLUS, plus_bin),
        ("config.example.yaml", cfg),
        ("vohive-usb-prepare.sh", script),
    ]
    .into_iter()
    .filter_map(|(name, path)| (!path.exists()).then_some(name))
    .collect::<Vec<_>>();

    if missing.is_empty() {
        Ok(())
    } else {
        Err(format!("桌面壳资源不完整，缺少: {}", missing.join(", ")))
    }
}

fn selected_backend_variant(state: &State<'_, AppState>) -> String {
    selected_backend_variant_from_app_state(state)
}

fn selected_backend_variant_from_app_state(state: &AppState) -> String {
    state
        .selected_backend_variant
        .lock()
        .expect("backend variant mutex poisoned")
        .clone()
}

fn ensure_wsl_running(state: &State<'_, AppState>) -> Result<u32, String> {
    let mut guard = state.wsl_keepalive.lock().expect("wsl mutex poisoned");
    if let Some(child) = guard.as_mut() {
        match child.try_wait() {
            Ok(None) => return Ok(child.id()),
            Ok(Some(status)) => {
                state.logs.push(format!("WSL 保活进程已退出: {status}"));
                *guard = None;
            }
            Err(err) => {
                state.logs.push(format!("读取 WSL 保活进程状态失败: {err}"));
                *guard = None;
            }
        }
    }

    let mut cmd = hidden_command(wsl::executable());
    cmd.args(wsl::keepalive_args());
    cmd.stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null());
    let child = cmd
        .spawn()
        .map_err(|err| format!("启动 {distro} 失败: {err}", distro = wsl::DISTRO))?;
    let pid = child.id();
    *guard = Some(child);
    drop(guard);

    if let Err(err) = wsl::wait_until_distro_running(Duration::from_secs(8)) {
        let mut guard = state.wsl_keepalive.lock().expect("wsl mutex poisoned");
        if let Some(mut child) = guard.take() {
            let _ = child.kill();
            let _ = child.try_wait();
        }
        return Err(err);
    }

    state.logs.push(format!("已启动 WSL 保活进程 pid={pid}"));
    Ok(pid)
}

fn suggested_wsl_keepalive_command() -> String {
    let args = wsl::keepalive_args()
        .into_iter()
        .map(|arg| {
            if arg.contains(' ') || arg.contains(';') {
                format!("\"{arg}\"")
            } else {
                arg.to_string()
            }
        })
        .collect::<Vec<_>>()
        .join(" ");
    format!("\"{}\" {args}", wsl::executable())
}

fn attach_reader(
    pipe: Option<impl std::io::Read + Send + 'static>,
    label: &'static str,
    state: &State<'_, AppState>,
) {
    let Some(pipe) = pipe else { return };
    let logs: RingLog = state.logs.clone();
    thread::spawn(move || {
        let reader = BufReader::new(pipe);
        for line in reader.lines().map_while(Result::ok) {
            logs.push(format!("[backend {label}] {line}"));
        }
    });
}

fn action(
    ok: bool,
    message: impl Into<String>,
    status: Option<RuntimeStatus>,
    suggested_admin_command: Option<String>,
) -> ActionResult {
    ActionResult {
        ok,
        message: message.into(),
        status,
        suggested_admin_command,
    }
}

fn backend_stop_script() -> &'static str {
    r#"pids=$({ pgrep -f '^/opt/vohive/bin/vohive( |$)' || true; pgrep -f '^/opt/vocat/bin/vocat( |$)' || true; } | sort -u)
if [ -z "$pids" ]; then
  echo no-process
  exit 0
fi
kill $pids || true
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
  if ! pgrep -f '^/opt/vohive/bin/vohive( |$)' >/dev/null && ! pgrep -f '^/opt/vocat/bin/vocat( |$)' >/dev/null; then
    echo stopped
    exit 0
  fi
  sleep 0.2
done
pids=$({ pgrep -f '^/opt/vohive/bin/vohive( |$)' || true; pgrep -f '^/opt/vocat/bin/vocat( |$)' || true; } | sort -u)
if [ -n "$pids" ]; then
  kill -KILL $pids || true
fi
echo killed"#
}

#[cfg(test)]
mod tests {
    use super::{
        backend_deploy_script, backend_deploy_success_logs, backend_running_variant_guard,
        backend_start_args, backend_stop_script, external_backend_status, usb_attach_preflight,
        usb_attach_step, validate_vohive_resources, wsl_required_action_preflight, UsbAttachStep,
    };
    use crate::backend_variants::{
        by_id as backend_variant_by_id, variants as backend_variants, VARIANT_ORSON, VARIANT_VOCAT,
        VARIANT_VOHIVE_PLUS,
    };
    use crate::models::UsbDevice;
    use std::fs;

    #[test]
    fn backend_stop_script_targets_managed_runtime_processes() {
        let script = backend_stop_script();
        assert!(script.contains("pgrep -f '^/opt/vohive/bin/vohive( |$)'"));
        assert!(script.contains("pgrep -f '^/opt/vocat/bin/vocat( |$)'"));
        assert!(!script.contains("pkill -f"));
    }

    #[test]
    fn external_backend_status_uses_wsl_process_pid() {
        let status = external_backend_status(false, Ok(vec![4242]))
            .expect("WSL process should make backend running");

        assert!(status.running);
        assert_eq!(status.pid, Some(4242));
        assert!(status.message.unwrap_or_default().contains("WSL"));
    }

    #[test]
    fn external_backend_status_uses_health_when_pid_probe_is_unavailable() {
        let status = external_backend_status(true, Err("pgrep timeout".to_string()))
            .expect("healthy backend should be considered running");

        assert!(status.running);
        assert_eq!(status.pid, None);
        let message = status.message.unwrap_or_default();
        assert!(message.contains("健康检查正常"));
        assert!(message.contains("pgrep timeout"));
    }

    #[test]
    fn backend_running_variant_guard_rejects_unmarked_running_slot() {
        let message = backend_running_variant_guard(VARIANT_VOHIVE_PLUS, Ok(None))
            .expect("running unmarked /opt/vohive slot must not be reused silently");

        assert!(message.contains("未标记"));
        assert!(message.contains("停止后端"));
        assert!(message.contains("重新启动"));
    }

    #[test]
    fn usb_attach_step_is_idempotent_for_attached_target() {
        let target = UsbDevice {
            busid: "2-1".to_string(),
            vid_pid: "2ca3:4006".to_string(),
            device: "Baiwang".to_string(),
            state: "Attached".to_string(),
            is_target: true,
        };

        assert_eq!(usb_attach_step(&target), UsbAttachStep::AlreadyAttached);
    }

    #[test]
    fn usb_attach_steps_that_call_usbipd_attach_require_running_wsl() {
        assert!(!UsbAttachStep::AlreadyAttached.requires_running_wsl());
        assert!(UsbAttachStep::AttachOnly.requires_running_wsl());
        assert!(UsbAttachStep::BindThenAttach.requires_running_wsl());
    }

    #[test]
    fn usb_attach_preflight_requires_user_started_wsl_for_attach_steps() {
        let err = usb_attach_preflight(UsbAttachStep::AttachOnly, false)
            .expect_err("attach must not auto-start WSL");

        assert!(err.contains("启动 WSL"));
        assert!(err.contains("再连接 USB 到 WSL"));
    }

    #[test]
    fn usb_attach_preflight_allows_already_attached_without_running_wsl_check() {
        assert!(usb_attach_preflight(UsbAttachStep::AlreadyAttached, false).is_ok());
    }

    #[test]
    fn prepare_usb_preflight_requires_user_started_wsl() {
        let err = wsl_required_action_preflight("准备 WSL USB", false)
            .expect_err("prepare USB must not auto-start WSL");

        assert!(err.contains("启动 WSL"));
        assert!(err.contains("准备 WSL USB"));
    }

    #[test]
    fn stop_backend_preflight_does_not_start_stopped_wsl() {
        let err = wsl_required_action_preflight("停止 WSL 后端", false)
            .expect_err("stop backend must not auto-start WSL");

        assert!(err.contains("WSL 未运行"));
        assert!(err.contains("停止 WSL 后端"));
    }

    #[test]
    fn validate_vohive_resources_rejects_missing_files() {
        let dir =
            std::env::temp_dir().join(format!("vohive-plus-resource-test-{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        fs::create_dir_all(&dir).unwrap();
        let bin = dir.join("vohive-open_linux_amd64");
        let plus_bin = dir.join("vohive-open_linux_amd64");
        let cfg = dir.join("config.example.yaml");
        let script = dir.join("vohive-usb-prepare.sh");

        let variant = backend_variant_by_id(VARIANT_ORSON).unwrap();
        let err = validate_vohive_resources(&variant, &bin, &plus_bin, &cfg, &script)
            .expect_err("missing resources must fail");

        let _ = fs::remove_dir_all(&dir);
        assert!(err.contains("桌面壳资源不完整"));
        assert!(err.contains("vohive-orson-v1.5.5_linux_amd64"));
        assert!(err.contains("config.example.yaml"));
        assert!(err.contains("vohive-usb-prepare.sh"));
    }

    #[test]
    fn backend_deploy_script_installs_selected_runtime_and_keeps_plus_helper() {
        let script = backend_deploy_script(
            VARIANT_ORSON,
            "'/mnt/f/desktop/resources/vohive/vohive-orson-v1.5.5_linux_amd64'",
            "'/mnt/f/desktop/resources/vohive/vohive-open_linux_amd64'",
            "'/mnt/f/desktop/resources/vohive/config.example.yaml'",
            "'/mnt/f/desktop/resources/vohive/vohive-usb-prepare.sh'",
        );

        assert!(script.contains(
            "cp '/mnt/f/desktop/resources/vohive/vohive-orson-v1.5.5_linux_amd64' /opt/vohive/bin/vohive"
        ));
        assert!(script.contains(
            "cp '/mnt/f/desktop/resources/vohive/vohive-open_linux_amd64' /opt/vohive/bin/vohive-plus"
        ));
        assert!(script.contains(
            "printf '%s\\n' 'orson-vohive-155' > /opt/vohive/config/desktop-backend-variant"
        ));
    }

    #[test]
    fn vocat_deploy_script_installs_runtime_to_opt_vocat() {
        let script = backend_deploy_script(
            VARIANT_VOCAT,
            "'/mnt/f/desktop/resources/vocat/vocat-linux-amd64'",
            "'/mnt/f/desktop/resources/vohive/vohive-open_linux_amd64'",
            "'/mnt/f/desktop/resources/vohive/config.example.yaml'",
            "'/mnt/f/desktop/resources/vohive/vohive-usb-prepare.sh'",
        );

        assert!(script.contains(
            "cp '/mnt/f/desktop/resources/vocat/vocat-linux-amd64' /opt/vocat/bin/vocat"
        ));
        assert!(
            script.contains("printf '%s\\n' 'vocat' > /opt/vocat/config/desktop-backend-variant")
        );
        assert!(script.contains("VOCAT_DATABASE_PATH=/opt/vocat/data/vocat.db"));
        assert!(script.contains("bootstrap-admin"));
        assert!(script.contains("desktop-bootstrap-admin.txt"));
        assert!(script.contains("VoCat 初始管理员"));
        assert!(!script.contains("/opt/vohive/bin/vohive &&"));
    }

    #[test]
    fn vocat_deploy_success_logs_bootstrap_credentials_from_stdout() {
        let variant = backend_variant_by_id(VARIANT_VOCAT).unwrap();
        let logs =
            backend_deploy_success_logs(&variant, "VoCat 初始管理员: admin / test-password\n");

        assert!(logs.iter().any(|line| line.contains("已部署 VoCat")));
        assert!(logs
            .iter()
            .any(|line| line.contains("VoCat 初始管理员: admin / test-password")));
    }

    #[test]
    fn backend_start_args_run_vocat_from_opt_vocat() {
        let args = backend_start_args(VARIANT_VOCAT);

        assert!(args.windows(2).any(|pair| pair == ["--cd", "/opt/vocat"]));
        assert!(args.contains(&"/usr/bin/env"));
        assert!(args.contains(&"VOCAT_DATABASE_PATH=/opt/vocat/data/vocat.db"));
        assert!(args.contains(&"/opt/vocat/bin/vocat"));
        assert!(args.contains(&"serve"));
    }

    #[test]
    fn backend_variants_include_default_iniwex_backup_and_vocat_runtime() {
        let variants = backend_variants();

        assert!(variants.iter().any(|variant| {
            variant.id == "vohive-plus" && variant.resource_name == "vohive-open_linux_amd64"
        }));
        assert!(variants.iter().any(|variant| {
            variant.id == VARIANT_ORSON
                && variant.resource_name == "vohive-orson-v1.5.5_linux_amd64"
                && variant.name == "iniwex5/vohive"
                && variant.description == "原项目 iniwex5/vohive 1.5.5 版本备份。"
        }));
        assert!(variants.iter().any(|variant| {
            variant.id == VARIANT_VOCAT
                && variant.resource_name == "vocat-linux-amd64"
                && variant.name == "VoCat"
                && variant.description == "第三方运行体，部署到 /opt/vocat。"
        }));
    }
}
