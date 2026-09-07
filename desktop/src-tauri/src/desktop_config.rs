use serde::{Deserialize, Serialize};
use std::fs;
use std::path::{Path, PathBuf};
use tauri::{AppHandle, Manager};

const CONFIG_FILE_NAME: &str = "desktop-config.json";

#[derive(Debug, Default, Deserialize, Serialize)]
struct DesktopConfig {
    selected_backend_variant: Option<String>,
}

pub fn config_path(app: &AppHandle) -> Result<PathBuf, String> {
    app.path()
        .app_config_dir()
        .map(|dir| dir.join(CONFIG_FILE_NAME))
        .map_err(|err| format!("读取桌面配置目录失败: {err}"))
}

pub fn load_selected_backend_variant(app: &AppHandle) -> Option<String> {
    config_path(app)
        .ok()
        .and_then(|path| load_selected_backend_variant_from_path(&path).ok())
        .flatten()
}

pub fn save_selected_backend_variant(app: &AppHandle, variant_id: &str) -> Result<(), String> {
    let path = config_path(app)?;
    save_selected_backend_variant_to_path(&path, variant_id)
}

fn load_selected_backend_variant_from_path(path: &Path) -> Result<Option<String>, String> {
    if !path.exists() {
        return Ok(None);
    }
    let text = fs::read_to_string(path).map_err(|err| format!("读取桌面配置失败: {err}"))?;
    let config = serde_json::from_str::<DesktopConfig>(&text)
        .map_err(|err| format!("解析桌面配置失败: {err}"))?;
    Ok(config.selected_backend_variant)
}

fn save_selected_backend_variant_to_path(path: &Path, variant_id: &str) -> Result<(), String> {
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent).map_err(|err| format!("创建桌面配置目录失败: {err}"))?;
    }
    let config = DesktopConfig {
        selected_backend_variant: Some(variant_id.to_string()),
    };
    let text = serde_json::to_string_pretty(&config)
        .map_err(|err| format!("序列化桌面配置失败: {err}"))?;
    fs::write(path, text).map_err(|err| format!("写入桌面配置失败: {err}"))
}

#[cfg(test)]
mod tests {
    use super::{load_selected_backend_variant_from_path, save_selected_backend_variant_to_path};
    use std::fs;

    #[test]
    fn selected_backend_variant_round_trips_through_config_file() {
        let path = std::env::temp_dir().join(format!(
            "vohive-desktop-config-{}-{}.json",
            std::process::id(),
            "round-trip"
        ));
        let _ = fs::remove_file(&path);

        save_selected_backend_variant_to_path(&path, "orson-vohive-155").unwrap();
        let selected = load_selected_backend_variant_from_path(&path).unwrap();

        let _ = fs::remove_file(&path);
        assert_eq!(selected.as_deref(), Some("orson-vohive-155"));
    }

    #[test]
    fn missing_config_file_has_no_saved_backend_variant() {
        let path = std::env::temp_dir().join(format!(
            "vohive-desktop-config-{}-{}.json",
            std::process::id(),
            "missing"
        ));
        let _ = fs::remove_file(&path);

        let selected = load_selected_backend_variant_from_path(&path).unwrap();

        assert_eq!(selected, None);
    }
}
