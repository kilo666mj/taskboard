fn main() {
    tauri_build::try_build(tauri_build::Attributes::new().app_manifest(
        tauri_build::AppManifest::new().commands(&[
            "configured_origin",
            "server_state",
            "configure",
            "save_server",
            "remove_server",
            "switch_server",
            "open_server_manager",
            "desktop_server_state",
            "select_desktop_server",
            "begin_oidc_login",
            "set_attention",
            "alert",
        ]),
    ))
    .expect("failed to run tauri-build");
}
