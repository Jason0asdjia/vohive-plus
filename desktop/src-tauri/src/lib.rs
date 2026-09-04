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

use tauri::Manager;

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
    tauri::Builder::default()
        .setup(|app| {
            let selected = backend_variants::normalize_variant_id(
                desktop_config::load_selected_backend_variant(app.handle()),
            );
            app.manage(AppState::new(selected));
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
        .run(tauri::generate_context!())
        .expect("failed to run VoHive Plus desktop shell");
}
