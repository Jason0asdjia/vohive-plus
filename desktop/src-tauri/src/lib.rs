mod backend_variants;
mod commands;
mod desktop_config;
mod health;
mod logs;
mod models;
mod process;
mod usbipd;
mod wsl;

use std::process::Child;
use std::sync::Mutex;
use std::thread;
use std::time::Duration;

use tauri::{AppHandle, Manager, RunEvent, Runtime, WindowEvent};

pub struct AppState {
    backend: Mutex<Option<Child>>,
    selected_backend_variant: Mutex<String>,
    wsl_keepalive: Mutex<Option<Child>>,
    logs: logs::RingLog,
}

impl Default for AppState {
    fn default() -> Self {
        Self::new(backend_variants::default_variant_id())
    }
}

impl AppState {
    fn new(selected_backend_variant: String) -> Self {
        Self {
            backend: Mutex::new(None),
            selected_backend_variant: Mutex::new(selected_backend_variant),
            wsl_keepalive: Mutex::new(None),
            logs: logs::RingLog::new(400),
        }
    }
}

pub fn run() {
    let app = tauri::Builder::default()
        .setup(|app| {
            let selected = backend_variants::normalize_variant_id(
                desktop_config::load_selected_backend_variant(app.handle()),
            );
            app.manage(AppState::new(selected));
            start_exit_watchdog(app.handle().clone());
            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            commands::detect,
            commands::status,
            commands::start_wsl,
            commands::stop_wsl,
            commands::attach_usb,
            commands::prepare_usb,
            commands::set_backend_variant,
            commands::start_backend,
            commands::stop_backend,
            commands::logs,
            commands::open_web
        ])
        .build(tauri::generate_context!())
        .expect("failed to build VoHive Plus desktop shell");

    app.run(|app, event| match event {
        RunEvent::WindowEvent {
            event: WindowEvent::CloseRequested { .. },
            ..
        }
        | RunEvent::WindowEvent {
            event: WindowEvent::Destroyed,
            ..
        } => {
            exit_desktop_process(app);
        }
        RunEvent::ExitRequested { .. } => {
            if let Some(state) = app.try_state::<AppState>() {
                cleanup_managed_children(&state);
            }
        }
        RunEvent::MainEventsCleared => {
            if app.webview_windows().is_empty() {
                exit_desktop_process(app);
            }
        }
        _ => {}
    });
}

fn start_exit_watchdog<R: Runtime>(app: AppHandle<R>) {
    thread::spawn(move || loop {
        thread::sleep(Duration::from_secs(1));
        if app.webview_windows().is_empty() {
            exit_desktop_process(&app);
        }
    });
}

fn exit_desktop_process<R: Runtime>(app: &AppHandle<R>) -> ! {
    if let Some(state) = app.try_state::<AppState>() {
        cleanup_managed_children(&state);
    }
    app.cleanup_before_exit();
    std::process::exit(0);
}

fn cleanup_managed_children(state: &AppState) {
    cleanup_child_slot(&state.backend, "WSL 后端", &state.logs);
    cleanup_child_slot(&state.wsl_keepalive, "WSL 保活", &state.logs);
}

fn cleanup_child_slot(slot: &Mutex<Option<Child>>, label: &str, logs: &logs::RingLog) {
    let mut guard = slot.lock().expect("child mutex poisoned");
    if let Some(mut child) = guard.take() {
        let pid = child.id();
        let _ = child.kill();
        let _ = child.wait();
        logs.push(format!("关闭桌面壳时已释放{label}进程 pid={pid}"));
    }
}

#[cfg(test)]
mod tests {
    use super::{cleanup_managed_children, AppState};
    use crate::process::hidden_command;
    use std::process::Stdio;

    #[test]
    fn cleanup_managed_children_releases_child_handles_on_exit() {
        let state = AppState::default();
        let backend = hidden_command("powershell.exe")
            .args(["-NoProfile", "-Command", "Start-Sleep -Seconds 30"])
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .expect("spawn backend child");
        let keepalive = hidden_command("powershell.exe")
            .args(["-NoProfile", "-Command", "Start-Sleep -Seconds 30"])
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .expect("spawn keepalive child");

        *state.backend.lock().expect("backend mutex poisoned") = Some(backend);
        *state
            .wsl_keepalive
            .lock()
            .expect("wsl mutex poisoned") = Some(keepalive);

        cleanup_managed_children(&state);

        assert!(state
            .backend
            .lock()
            .expect("backend mutex poisoned")
            .is_none());
        assert!(state
            .wsl_keepalive
            .lock()
            .expect("wsl mutex poisoned")
            .is_none());
        assert!(state
            .logs
            .snapshot()
            .iter()
            .any(|line| line.contains("关闭桌面壳时已释放WSL 后端进程")));
    }
}
