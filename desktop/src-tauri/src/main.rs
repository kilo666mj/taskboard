#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use serde::{Deserialize, Serialize};
use std::collections::HashSet;
use std::fmt::Write;
use std::net::IpAddr;
use std::process::Command;
use tauri::{
    image::Image,
    menu::{CheckMenuItem, Menu, MenuItem, PredefinedMenuItem, Submenu},
    tray::{TrayIconBuilder, TrayIconEvent},
    AppHandle, Emitter, Manager, RunEvent, WebviewWindow, WindowEvent,
};
use tauri_plugin_autostart::ManagerExt;
use tauri_plugin_notification::NotificationExt;
use tauri_plugin_store::StoreExt;

const STORE_FILE: &str = "taskboard.json";
const ORIGIN_KEY: &str = "origin";
const SERVERS_KEY: &str = "servers";
const SELECTED_ORIGIN_KEY: &str = "selected_origin";
const MAIN_WINDOW: &str = "main";
const MANAGER_WINDOW: &str = "servers";
const TRAY_ICON: &[u8] = include_bytes!("../icons/icon.png");
const TRAY_ATTENTION_ICON: &[u8] = include_bytes!("../icons/icon-attention.png");

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
struct ServerConfig {
    name: String,
    origin: String,
}

#[derive(Debug, Serialize)]
struct ServerState {
    servers: Vec<ServerConfig>,
    selected_origin: Option<String>,
}

#[derive(Debug, Deserialize)]
struct AlertPayload {
    id: String,
    title: String,
    body: String,
    urgent: bool,
}

fn normalize_origin(origin: &str) -> Result<String, String> {
    let parsed = url::Url::parse(origin.trim()).map_err(|_| "not a valid URL".to_string())?;
    if parsed.scheme() != "http" && parsed.scheme() != "https" {
        return Err("address must use HTTP or HTTPS".into());
    }
    if parsed.host_str().is_none() || !parsed.username().is_empty() || parsed.password().is_some() {
        return Err("address must be a plain host without credentials".into());
    }
    if parsed.query().is_some() || parsed.fragment().is_some() || parsed.path() != "/" {
        return Err("address must not contain a path, query, or fragment".into());
    }
    let host = parsed.host_str().ok_or("address must include a host")?;
    let address_host = host.trim_start_matches('[').trim_end_matches(']');
    if parsed.scheme() == "http"
        && !host.eq_ignore_ascii_case("localhost")
        && !address_host
            .parse::<IpAddr>()
            .is_ok_and(|address| address.is_loopback())
    {
        return Err("HTTP addresses are allowed only on loopback".into());
    }
    Ok(parsed.origin().ascii_serialization())
}

fn normalize_name(name: &str) -> Result<String, String> {
    let name = name.trim();
    if name.is_empty() {
        return Err("name is required".into());
    }
    if name.chars().count() > 80 {
        return Err("name must be at most 80 characters".into());
    }
    if name.chars().any(char::is_control) {
        return Err("name must not contain control characters".into());
    }
    Ok(name.to_string())
}

fn default_server_name(origin: &str) -> String {
    url::Url::parse(origin)
        .ok()
        .and_then(|parsed| parsed.host_str().map(str::to_string))
        .unwrap_or_else(|| "Taskboard".into())
}

fn sanitize_servers(servers: Vec<ServerConfig>) -> Vec<ServerConfig> {
    let mut names = HashSet::new();
    let mut origins = HashSet::new();
    servers
        .into_iter()
        .filter_map(|server| {
            let name = normalize_name(&server.name).ok()?;
            let origin = normalize_origin(&server.origin).ok()?;
            if !names.insert(name.to_lowercase()) || !origins.insert(origin.clone()) {
                return None;
            }
            Some(ServerConfig { name, origin })
        })
        .collect()
}

fn load_server_state(app: &AppHandle) -> Result<ServerState, String> {
    let store = app.store(STORE_FILE).map_err(|error| error.to_string())?;
    let mut servers = store
        .get(SERVERS_KEY)
        .and_then(|value| serde_json::from_value(value).ok())
        .map(sanitize_servers)
        .unwrap_or_default();

    let legacy_origin = store
        .get(ORIGIN_KEY)
        .and_then(|value| value.as_str().map(str::to_string))
        .and_then(|origin| normalize_origin(&origin).ok());
    if servers.is_empty() {
        if let Some(origin) = legacy_origin.clone() {
            servers.push(ServerConfig {
                name: default_server_name(&origin),
                origin,
            });
        }
    }

    let requested = store
        .get(SELECTED_ORIGIN_KEY)
        .and_then(|value| value.as_str().map(str::to_string))
        .and_then(|origin| normalize_origin(&origin).ok())
        .or(legacy_origin);
    let selected_origin = requested
        .filter(|origin| servers.iter().any(|server| server.origin == *origin))
        .or_else(|| servers.first().map(|server| server.origin.clone()));

    Ok(ServerState {
        servers,
        selected_origin,
    })
}

fn save_server_state(app: &AppHandle, state: &ServerState) -> Result<(), String> {
    let store = app.store(STORE_FILE).map_err(|error| error.to_string())?;
    store.set(
        SERVERS_KEY,
        serde_json::to_value(&state.servers).map_err(|error| error.to_string())?,
    );
    match &state.selected_origin {
        Some(origin) => {
            store.set(SELECTED_ORIGIN_KEY, origin.clone());
            store.set(ORIGIN_KEY, origin.clone());
        }
        None => {
            store.delete(SELECTED_ORIGIN_KEY);
            store.delete(ORIGIN_KEY);
        }
    }
    store.save().map_err(|error| error.to_string())
}

fn selected_server(state: &ServerState) -> Option<&ServerConfig> {
    let selected = state.selected_origin.as_deref()?;
    state
        .servers
        .iter()
        .find(|server| server.origin == selected)
}

fn stored_origin(app: &AppHandle) -> Option<String> {
    load_server_state(app).ok()?.selected_origin
}

fn allow_origin(app: &AppHandle, origin: &str) -> Result<(), String> {
    let mut encoded_origin = String::with_capacity(origin.len() * 2);
    for byte in origin.bytes() {
        write!(&mut encoded_origin, "{byte:02x}").expect("writing to a string cannot fail");
    }
    let capability = format!(
        r#"{{
        "identifier":"taskboard-remote-{}",
        "windows":["main"],
        "remote":{{"urls":["{}/*"]}},
        "permissions":["allow-set-attention","allow-alert","allow-begin-oidc-login","allow-open-server-manager","allow-desktop-server-state","allow-select-desktop-server","core:event:default","core:window:allow-set-focus"]
    }}"#,
        encoded_origin, origin
    );
    app.add_capability(capability)
        .map_err(|error| error.to_string())
}

fn allow_all_origins(app: &AppHandle, state: &ServerState) {
    for server in &state.servers {
        if let Err(error) = allow_origin(app, &server.origin) {
            eprintln!(
                "taskboard: cannot trust {} ({}): {error}",
                server.name, server.origin
            );
        }
    }
}

fn show_window(window: &WebviewWindow) {
    let _ = window.show();
    let _ = window.unminimize();
    let _ = window.set_focus();
}

fn show_main_window(app: &AppHandle) {
    if let Some(window) = app.get_webview_window(MAIN_WINDOW) {
        show_window(&window);
    }
}

fn show_server_manager(app: &AppHandle, focus_form: bool) {
    let window = app
        .get_webview_window(MANAGER_WINDOW)
        .or_else(|| app.get_webview_window(MAIN_WINDOW));
    if let Some(window) = window {
        show_window(&window);
        if focus_form {
            let _ = app.emit_to(window.label(), "taskboard://add-server", ());
        } else {
            let _ = app.emit_to(window.label(), "taskboard://manage-servers", ());
        }
    }
}

fn navigate(window: &WebviewWindow, target: &str) -> Result<(), String> {
    let parsed = url::Url::parse(target).map_err(|error| error.to_string())?;
    window.navigate(parsed).map_err(|error| error.to_string())
}

fn reset_attention(app: &AppHandle) -> Result<(), String> {
    if let Some(tray) = app.tray_by_id("taskboard") {
        tray.set_icon(Some(
            Image::from_bytes(TRAY_ICON).map_err(|error| error.to_string())?,
        ))
        .map_err(|error| error.to_string())?;
        tray.set_tooltip(Some("Taskboard"))
            .map_err(|error| error.to_string())?;
    }
    if let Some(window) = app.get_webview_window(MAIN_WINDOW) {
        let _ = window.set_badge_count(None);
    }
    Ok(())
}

fn activate_selected_server(app: &AppHandle, state: &ServerState) -> Result<(), String> {
    reset_attention(app)?;
    let Some(server) = selected_server(state) else {
        if let Some(window) = app.get_webview_window(MAIN_WINDOW) {
            let _ = window.set_title("Taskboard");
            // Unload a removed remote server so it cannot keep a hidden live
            // connection after the final configured server is deleted.
            let _ = navigate(&window, "tauri://localhost");
            let _ = window.hide();
        }
        show_server_manager(app, true);
        return Ok(());
    };
    allow_origin(app, &server.origin)?;
    let window = app
        .get_webview_window(MAIN_WINDOW)
        .ok_or("main window is unavailable")?;
    window
        .set_title(&format!("Taskboard — {}", server.name))
        .map_err(|error| error.to_string())?;
    navigate(&window, &server.origin)?;
    show_window(&window);
    Ok(())
}

fn requesting_active_server(window: &WebviewWindow, app: &AppHandle) -> Result<String, String> {
    let configured = stored_origin(app).ok_or("server address is not configured")?;
    let page = window.url().map_err(|error| error.to_string())?;
    if page.origin().ascii_serialization() != configured {
        return Err("request came from an inactive server".into());
    }
    Ok(configured)
}

fn tray_menu(app: &AppHandle, state: &ServerState) -> tauri::Result<Menu<tauri::Wry>> {
    let servers = Submenu::new(app, "Servers", true)?;
    for (index, server) in state.servers.iter().enumerate() {
        let item = CheckMenuItem::with_id(
            app,
            format!("server:{index}"),
            &server.name,
            true,
            state.selected_origin.as_deref() == Some(server.origin.as_str()),
            None::<&str>,
        )?;
        servers.append(&item)?;
    }
    if !state.servers.is_empty() {
        servers.append(&PredefinedMenuItem::separator(app)?)?;
    }
    servers.append(&MenuItem::with_id(
        app,
        "add-server",
        "Add Server…",
        true,
        None::<&str>,
    )?)?;
    servers.append(&MenuItem::with_id(
        app,
        "manage-servers",
        "Manage Servers…",
        true,
        None::<&str>,
    )?)?;

    let open = MenuItem::with_id(app, "open", "Open Taskboard", true, None::<&str>)?;
    let refresh = MenuItem::with_id(app, "refresh", "Refresh", true, None::<&str>)?;
    let quit = MenuItem::with_id(app, "quit", "Quit", true, None::<&str>)?;
    Menu::with_items(app, &[&open, &servers, &refresh, &quit])
}

fn rebuild_tray_menu(app: &AppHandle, state: &ServerState) -> Result<(), String> {
    if let Some(tray) = app.tray_by_id("taskboard") {
        tray.set_menu(Some(
            tray_menu(app, state).map_err(|error| error.to_string())?,
        ))
        .map_err(|error| error.to_string())?;
    }
    Ok(())
}

fn select_server(app: &AppHandle, origin: &str) -> Result<ServerState, String> {
    let origin = normalize_origin(origin)?;
    let mut state = load_server_state(app)?;
    if !state.servers.iter().any(|server| server.origin == origin) {
        return Err("server is not configured".into());
    }
    state.selected_origin = Some(origin);
    save_server_state(app, &state)?;
    rebuild_tray_menu(app, &state)?;
    activate_selected_server(app, &state)?;
    Ok(state)
}

#[tauri::command]
fn configured_origin(app: AppHandle) -> Option<String> {
    stored_origin(&app)
}

#[tauri::command]
fn server_state(app: AppHandle) -> Result<ServerState, String> {
    let state = load_server_state(&app)?;
    save_server_state(&app, &state)?;
    Ok(state)
}

#[tauri::command]
fn configure(app: AppHandle, origin: String) -> Result<String, String> {
    let normalized = normalize_origin(&origin)?;
    save_server(
        app,
        default_server_name(&normalized),
        normalized.clone(),
        None,
    )?;
    Ok(normalized)
}

#[tauri::command]
fn save_server(
    app: AppHandle,
    name: String,
    origin: String,
    previous_origin: Option<String>,
) -> Result<ServerState, String> {
    let name = normalize_name(&name)?;
    let origin = normalize_origin(&origin)?;
    let previous_origin = previous_origin
        .as_deref()
        .map(normalize_origin)
        .transpose()?;
    let mut state = load_server_state(&app)?;
    let editing = previous_origin.as_ref().and_then(|previous| {
        state
            .servers
            .iter()
            .position(|server| &server.origin == previous)
    });
    if previous_origin.is_some() && editing.is_none() {
        return Err("server to edit no longer exists".into());
    }
    if state.servers.iter().enumerate().any(|(index, server)| {
        Some(index) != editing
            && (server.origin == origin || server.name.eq_ignore_ascii_case(&name))
    }) {
        return Err("another server already uses that name or address".into());
    }

    let server = ServerConfig {
        name,
        origin: origin.clone(),
    };
    match editing {
        Some(index) => state.servers[index] = server,
        None => state.servers.push(server),
    }
    let was_selected = previous_origin
        .as_ref()
        .is_some_and(|previous| state.selected_origin.as_ref() == Some(previous));
    if state.selected_origin.is_none() || was_selected {
        state.selected_origin = Some(origin.clone());
    }

    save_server_state(&app, &state)?;
    allow_origin(&app, &origin)?;
    rebuild_tray_menu(&app, &state)?;
    if state.selected_origin.as_deref() == Some(origin.as_str()) {
        activate_selected_server(&app, &state)?;
    }
    if let Err(error) = app.autolaunch().enable() {
        eprintln!("taskboard: cannot enable autostart: {error}");
    }
    let _ = app.emit("taskboard://servers-changed", ());
    Ok(state)
}

#[tauri::command]
fn remove_server(app: AppHandle, origin: String) -> Result<ServerState, String> {
    let origin = normalize_origin(&origin)?;
    let mut state = load_server_state(&app)?;
    let original_len = state.servers.len();
    state.servers.retain(|server| server.origin != origin);
    if state.servers.len() == original_len {
        return Err("server is not configured".into());
    }
    let removed_selected = state.selected_origin.as_deref() == Some(origin.as_str());
    if removed_selected {
        state.selected_origin = state.servers.first().map(|server| server.origin.clone());
    }
    save_server_state(&app, &state)?;
    rebuild_tray_menu(&app, &state)?;
    if removed_selected {
        activate_selected_server(&app, &state)?;
    }
    let _ = app.emit("taskboard://servers-changed", ());
    Ok(state)
}

#[tauri::command]
fn switch_server(app: AppHandle, origin: String) -> Result<ServerState, String> {
    let state = select_server(&app, &origin)?;
    let _ = app.emit("taskboard://servers-changed", ());
    Ok(state)
}

#[tauri::command]
fn desktop_server_state(app: AppHandle, window: WebviewWindow) -> Result<ServerState, String> {
    requesting_active_server(&window, &app)?;
    load_server_state(&app)
}

#[tauri::command]
fn select_desktop_server(
    app: AppHandle,
    window: WebviewWindow,
    origin: String,
) -> Result<ServerState, String> {
    requesting_active_server(&window, &app)?;
    switch_server(app, origin)
}

#[tauri::command]
fn open_server_manager(app: AppHandle, window: WebviewWindow) -> Result<(), String> {
    requesting_active_server(&window, &app)?;
    let manager = app
        .get_webview_window(MANAGER_WINDOW)
        .ok_or("server manager is unavailable")?;
    manager.show().map_err(|error| error.to_string())?;
    manager.unminimize().map_err(|error| error.to_string())?;
    manager.set_focus().map_err(|error| error.to_string())
}

#[tauri::command]
fn begin_oidc_login(app: AppHandle, window: WebviewWindow, handoff: String) -> Result<(), String> {
    if handoff.len() != 64
        || !handoff
            .chars()
            .all(|character| character.is_ascii_hexdigit())
    {
        return Err("desktop sign-in handoff is invalid".into());
    }
    let origin = requesting_active_server(&window, &app)?;
    let target = format!("{origin}/api/v1/auth/oidc/start?desktop={handoff}");
    open_in_system_browser(&target)
}

fn open_in_system_browser(target: &str) -> Result<(), String> {
    let parsed = url::Url::parse(target).map_err(|_| "not a valid URL".to_string())?;
    if parsed.scheme() != "http" && parsed.scheme() != "https" {
        return Err("sign-in URL must use HTTP or HTTPS".into());
    }
    #[cfg(target_os = "macos")]
    let mut command = Command::new("open");
    #[cfg(target_os = "linux")]
    let mut command = Command::new("xdg-open");
    #[cfg(target_os = "windows")]
    let mut command = {
        let mut command = Command::new("rundll32");
        command.arg("url.dll,FileProtocolHandler");
        command
    };
    command
        .arg(parsed.as_str())
        .spawn()
        .map_err(|error| error.to_string())?;
    Ok(())
}

#[tauri::command]
fn set_attention(app: AppHandle, window: WebviewWindow, count: u32) -> Result<(), String> {
    requesting_active_server(&window, &app)?;
    if let Some(tray) = app.tray_by_id("taskboard") {
        let icon = if count > 0 {
            TRAY_ATTENTION_ICON
        } else {
            TRAY_ICON
        };
        tray.set_icon(Some(
            Image::from_bytes(icon).map_err(|error| error.to_string())?,
        ))
        .map_err(|error| error.to_string())?;
        let server_name = load_server_state(&app)
            .ok()
            .and_then(|state| selected_server(&state).map(|server| server.name.clone()))
            .unwrap_or_else(|| "Taskboard".into());
        tray.set_tooltip(Some(if count > 0 {
            format!("{server_name} — {count} need attention")
        } else {
            server_name
        }))
        .map_err(|error| error.to_string())?;
    }
    if let Some(window) = app.get_webview_window(MAIN_WINDOW) {
        let _ = window.set_badge_count((count > 0).then_some(count as i64));
    }
    Ok(())
}

#[tauri::command]
fn alert(app: AppHandle, window: WebviewWindow, payload: AlertPayload) -> Result<(), String> {
    requesting_active_server(&window, &app)?;
    let mut notification = app
        .notification()
        .builder()
        .title(payload.title)
        .body(payload.body)
        .group(format!("taskboard-{}", payload.id));
    if !payload.urgent {
        notification = notification.silent();
    }
    notification.show().map_err(|error| error.to_string())
}

fn handle_tray_menu(app: &AppHandle, id: &str) {
    match id {
        "open" => show_main_window(app),
        "add-server" => show_server_manager(app, true),
        "manage-servers" => show_server_manager(app, false),
        "refresh" => {
            let _ = app.emit("taskboard://refresh", ());
            show_main_window(app);
        }
        "quit" => app.exit(0),
        _ => {
            if let Some(index) = id
                .strip_prefix("server:")
                .and_then(|value| value.parse::<usize>().ok())
            {
                if let Ok(state) = load_server_state(app) {
                    if let Some(server) = state.servers.get(index) {
                        if let Err(error) = select_server(app, &server.origin) {
                            eprintln!("taskboard: cannot switch server: {error}");
                            show_server_manager(app, false);
                        }
                    }
                }
            }
        }
    }
}

fn build_tray(app: &AppHandle, icon: Image<'_>, state: &ServerState) -> tauri::Result<()> {
    TrayIconBuilder::with_id("taskboard")
        .icon(icon)
        .tooltip("Taskboard")
        .menu(&tray_menu(app, state)?)
        .show_menu_on_left_click(false)
        .on_menu_event(|app, event| handle_tray_menu(app, event.id.as_ref()))
        .on_tray_icon_event(|tray, event| {
            if let TrayIconEvent::Click { .. } = event {
                show_main_window(tray.app_handle());
            }
        })
        .build(app)?;
    Ok(())
}

fn main() {
    #[cfg(target_os = "linux")]
    if std::env::var_os("GDK_BACKEND").is_none() {
        std::env::set_var("GDK_BACKEND", "x11");
    }
    #[cfg(target_os = "linux")]
    if std::env::var_os("WEBKIT_DISABLE_DMABUF_RENDERER").is_none() {
        std::env::set_var("WEBKIT_DISABLE_DMABUF_RENDERER", "1");
    }

    tauri::Builder::default()
        .plugin(tauri_plugin_single_instance::init(|app, _, _| {
            show_main_window(app)
        }))
        .plugin(tauri_plugin_store::Builder::default().build())
        .plugin(tauri_plugin_window_state::Builder::default().build())
        .plugin(tauri_plugin_notification::init())
        .plugin(tauri_plugin_autostart::init(
            tauri_plugin_autostart::MacosLauncher::LaunchAgent,
            None,
        ))
        .invoke_handler(tauri::generate_handler![
            configured_origin,
            server_state,
            configure,
            save_server,
            remove_server,
            switch_server,
            open_server_manager,
            desktop_server_state,
            select_desktop_server,
            begin_oidc_login,
            set_attention,
            alert
        ])
        .setup(|app| {
            let handle = app.handle().clone();
            let icon = handle
                .default_window_icon()
                .expect("bundle icon configured")
                .clone();
            for label in [MAIN_WINDOW, MANAGER_WINDOW] {
                if let Some(window) = handle.get_webview_window(label) {
                    window.set_icon(icon.clone())?;
                }
            }
            if let Some(window) = handle.get_webview_window(MANAGER_WINDOW) {
                let _ = window.hide();
            }

            let state = load_server_state(&handle).map_err(std::io::Error::other)?;
            save_server_state(&handle, &state).map_err(std::io::Error::other)?;
            allow_all_origins(&handle, &state);
            build_tray(&handle, Image::from_bytes(TRAY_ICON)?, &state)?;
            if state.selected_origin.is_some() {
                if let Err(error) = handle.autolaunch().enable() {
                    eprintln!("taskboard: cannot enable autostart: {error}");
                }
                if let Err(error) = activate_selected_server(&handle, &state) {
                    eprintln!("taskboard: cannot open selected server: {error}");
                    show_server_manager(&handle, false);
                }
            }
            Ok(())
        })
        .on_window_event(|window, event| {
            if let WindowEvent::CloseRequested { api, .. } = event {
                api.prevent_close();
                let _ = window.hide();
            }
        })
        .build(tauri::generate_context!())
        .expect("build Taskboard desktop client")
        .run(|_, event| {
            if let RunEvent::ExitRequested { api, code, .. } = event {
                if code.is_none() {
                    api.prevent_exit();
                }
            }
        });
}

#[cfg(test)]
mod tests {
    use super::{
        default_server_name, normalize_name, normalize_origin, sanitize_servers, ServerConfig,
    };

    #[test]
    fn origin_requires_https_except_on_loopback() {
        for origin in [
            "https://taskboard.example.com",
            "http://localhost:8095",
            "http://127.0.0.1:8095",
            "http://[::1]:8095",
        ] {
            assert!(normalize_origin(origin).is_ok(), "rejected {origin}");
        }
        for origin in [
            "http://taskboard.internal",
            "http://192.168.1.20:8095",
            "http://10.0.0.2",
            "https://taskboard.example.com/path",
        ] {
            assert!(normalize_origin(origin).is_err(), "accepted {origin}");
        }
    }

    #[test]
    fn names_are_trimmed_and_validated() {
        assert_eq!(normalize_name(" Work ").unwrap(), "Work");
        assert!(normalize_name(" ").is_err());
        assert!(normalize_name("bad\nname").is_err());
    }

    #[test]
    fn invalid_and_duplicate_stored_servers_are_dropped() {
        let servers = sanitize_servers(vec![
            ServerConfig {
                name: "Work".into(),
                origin: "https://work.example.com".into(),
            },
            ServerConfig {
                name: "work".into(),
                origin: "https://duplicate.example.com".into(),
            },
            ServerConfig {
                name: "Invalid".into(),
                origin: "http://remote.example.com".into(),
            },
        ]);
        assert_eq!(servers.len(), 1);
    }

    #[test]
    fn migrated_server_name_uses_the_host() {
        assert_eq!(
            default_server_name("https://taskboard.example.com"),
            "taskboard.example.com"
        );
    }
}
