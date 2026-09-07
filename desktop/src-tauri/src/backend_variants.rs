use crate::models::BackendVariant;

pub const VARIANT_VOHIVE_PLUS: &str = "vohive-plus";
pub const VARIANT_ORSON: &str = "orson-vohive-155";
pub const VARIANT_VOCAT: &str = "vocat";
pub const RESOURCE_VOHIVE_PLUS: &str = "vohive-open_linux_amd64";
pub const RESOURCE_ORSON: &str = "vohive-orson-v1.5.5_linux_amd64";
pub const RESOURCE_VOCAT: &str = "vocat-linux-amd64";

pub fn default_variant_id() -> String {
    VARIANT_VOHIVE_PLUS.to_string()
}

pub fn variants() -> Vec<BackendVariant> {
    vec![
        BackendVariant {
            id: VARIANT_VOHIVE_PLUS.to_string(),
            name: "VoHive Plus".to_string(),
            version: env!("CARGO_PKG_VERSION").to_string(),
            description: "本项目默认后端，适合日常使用。".to_string(),
            resource_name: RESOURCE_VOHIVE_PLUS.to_string(),
        },
        BackendVariant {
            id: VARIANT_ORSON.to_string(),
            name: "iniwex5/vohive".to_string(),
            version: "v1.5.5-10-gf9eb85d".to_string(),
            description: "原项目 iniwex5/vohive 1.5.5 版本备份。".to_string(),
            resource_name: RESOURCE_ORSON.to_string(),
        },
        BackendVariant {
            id: VARIANT_VOCAT.to_string(),
            name: "VoCat".to_string(),
            version: "latest release".to_string(),
            description: "第三方运行体，部署到 /opt/vocat。".to_string(),
            resource_name: RESOURCE_VOCAT.to_string(),
        },
    ]
}

pub fn variants_with_vocat_version(vocat_version: Option<String>) -> Vec<BackendVariant> {
    let mut variants = variants();
    let Some(vocat_version) = vocat_version.map(|version| version.trim().to_string()) else {
        return variants;
    };
    if vocat_version.is_empty() {
        return variants;
    }
    if let Some(variant) = variants
        .iter_mut()
        .find(|variant| variant.id == VARIANT_VOCAT)
    {
        variant.version = vocat_version;
    }
    variants
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
    use super::{
        normalize_variant_id, variants, variants_with_vocat_version, VARIANT_ORSON,
        VARIANT_VOCAT, VARIANT_VOHIVE_PLUS,
    };

    #[test]
    fn normalize_variant_id_keeps_known_saved_variant() {
        assert_eq!(
            normalize_variant_id(Some(VARIANT_ORSON.to_string())),
            VARIANT_ORSON
        );
        assert_eq!(
            normalize_variant_id(Some(VARIANT_VOCAT.to_string())),
            VARIANT_VOCAT
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

    #[test]
    fn variants_with_vocat_version_displays_packaged_vocat_release() {
        let variants = variants_with_vocat_version(Some("v0.2.28\n".to_string()));
        let vocat = variants
            .iter()
            .find(|variant| variant.id == VARIANT_VOCAT)
            .unwrap();

        assert_eq!(vocat.version, "v0.2.28");
    }

    #[test]
    fn variant_descriptions_are_short_ui_notes() {
        let variants = variants();

        let plus = variants
            .iter()
            .find(|variant| variant.id == VARIANT_VOHIVE_PLUS)
            .unwrap();
        let backup = variants
            .iter()
            .find(|variant| variant.id == VARIANT_ORSON)
            .unwrap();
        let vocat = variants
            .iter()
            .find(|variant| variant.id == VARIANT_VOCAT)
            .unwrap();

        assert_eq!(plus.description, "本项目默认后端，适合日常使用。");
        assert_eq!(
            backup.description,
            "原项目 iniwex5/vohive 1.5.5 版本备份。"
        );
        assert_eq!(vocat.description, "第三方运行体，部署到 /opt/vocat。");
    }
}
