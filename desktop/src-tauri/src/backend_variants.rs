use crate::models::BackendVariant;

pub const VARIANT_VOHIVE_PLUS: &str = "vohive-plus";
pub const VARIANT_ORSON: &str = "orson-vohive-155";
pub const RESOURCE_VOHIVE_PLUS: &str = "vohive-open_linux_amd64";
pub const RESOURCE_ORSON: &str = "vohive-orson-v1.5.5_linux_amd64";

pub fn default_variant_id() -> String {
    VARIANT_VOHIVE_PLUS.to_string()
}

pub fn variants() -> Vec<BackendVariant> {
    vec![
        BackendVariant {
            id: VARIANT_VOHIVE_PLUS.to_string(),
            name: "VoHive Plus".to_string(),
            version: env!("CARGO_PKG_VERSION").to_string(),
            description:
                "本项目默认后端，包含 Windows/WSL USB 准备、国家前置代理和项目内修复。".to_string(),
            resource_name: RESOURCE_VOHIVE_PLUS.to_string(),
        },
        BackendVariant {
            id: VARIANT_ORSON.to_string(),
            name: "Orson/Vohive-155".to_string(),
            version: "v1.5.5-10-gf9eb85d".to_string(),
            description:
                "备用诊断后端，实机已验证可拉起 VOXI/Vodafone UK WiFi Calling；不包含本项目新增的 prepare-usb 命令和 SOCKS5 UDP 前置代理。"
                    .to_string(),
            resource_name: RESOURCE_ORSON.to_string(),
        },
    ]
}

pub fn by_id(id: &str) -> Option<BackendVariant> {
    variants().into_iter().find(|variant| variant.id == id)
}

pub fn normalize_variant_id(saved: Option<String>) -> String {
    saved
        .filter(|id| by_id(id).is_some())
        .unwrap_or_else(default_variant_id)
}

#[cfg(test)]
mod tests {
    use super::{normalize_variant_id, VARIANT_ORSON, VARIANT_VOHIVE_PLUS};

    #[test]
    fn normalize_variant_id_keeps_known_saved_variant() {
        assert_eq!(
            normalize_variant_id(Some(VARIANT_ORSON.to_string())),
            VARIANT_ORSON
        );
    }

    #[test]
    fn normalize_variant_id_falls_back_to_default_for_unknown_variant() {
        assert_eq!(
            normalize_variant_id(Some("removed-backend".to_string())),
            VARIANT_VOHIVE_PLUS
        );
    }

    #[test]
    fn normalize_variant_id_falls_back_to_default_when_missing() {
        assert_eq!(normalize_variant_id(None), VARIANT_VOHIVE_PLUS);
    }
}
