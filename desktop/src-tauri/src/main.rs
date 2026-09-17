#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use serde::Deserialize;
use std::net::IpAddr;
use std::process::Command;
use tauri::{
    image::Image,
    menu::{Menu, MenuItem},
    tray::{TrayIconBuilder, TrayIconEvent},
    AppHandle, Emitter, Manager, RunEvent, WebviewWindow, WindowEvent,
};
use tauri_plugin_autostart::ManagerExt;
use tauri_plugin_notification::NotificationExt;
use tauri_plugin_store::StoreExt;

const STORE_FILE: &str = "taskboard.json";
const ORIGIN_KEY: &str = "origin";
const MAIN_WINDOW: &str = "main";
const TRAY_ICON: &[u8] = include_bytes!("../icons/icon.png");
const TRAY_ATTENTION_ICON: &[u8] = include_bytes!("../icons/icon-attention.png");

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
        return Err("origin must use HTTP or HTTPS".into());
    }
    if parsed.host_str().is_none() || !parsed.username().is_empty() || parsed.password().is_some() {
        return Err("origin must be a plain host without credentials".into());
    }
    if parsed.query().is_some() || parsed.fragment().is_some() || parsed.path() != "/" {
        return Err("origin must not contain a path, query, or fragment".into());
    }
    let host = parsed.host_str().ok_or("origin must include a host")?;
    let address_host = host.trim_start_matches('[').trim_end_matches(']');
    if parsed.scheme() == "http"
        && !host.eq_ignore_ascii_case("localhost")
        && !address_host
            .parse::<IpAddr>()
            .is_ok_and(|address| address.is_loopback())
    {
        return Err("HTTP origins are allowed only on loopback".into());
    }
    Ok(parsed.origin().ascii_serialization())
}

#[cfg(test)]
mod tests {
    use super::normalize_origin;

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
        ] {
            assert!(normalize_origin(origin).is_err(), "accepted {origin}");
        }
    }
}

fn stored_origin(app: &AppHandle) -> Option<String> {
    let origin = app
        .store(STORE_FILE)
        .ok()?
        .get(ORIGIN_KEY)?
        .as_str()
        .map(str::to_string)?;
    normalize_origin(&origin).ok()
}

fn allow_origin(app: &AppHandle, origin: &str) -> Result<(), String> {
    let capability = format!(
        r#"{{
        "identifier":"taskboard-remote-{}",
        "windows":["main"],
        "remote":{{"urls":["{}/*"]}},
        "permissions":["allow-set-attention","allow-alert","allow-begin-oidc-login","core:event:default","core:window:allow-set-focus"]
    }}"#,
        origin.replace([':', '/', '.'], "-"),
        origin
    );
    app.add_capability(capability)
        .map_err(|error| error.to_string())
}

fn show_main_window(app: &AppHandle) {
    if let Some(window) = app.get_webview_window(MAIN_WINDOW) {
        let _ = window.show();
        let _ = window.unminimize();
        let _ = window.set_focus();
    }
}

fn navigate(window: &WebviewWindow, target: &str) -> Result<(), String> {
    let parsed = url::Url::parse(target).map_err(|error| error.to_string())?;
    window.navigate(parsed).map_err(|error| error.to_string())
}

#[tauri::command]
fn configured_origin(app: AppHandle) -> Option<String> {
    stored_origin(&app)
}

#[tauri::command]
fn configure(app: AppHandle, origin: String) -> Result<String, String> {
    let origin = normalize_origin(&origin)?;
    let store = app.store(STORE_FILE).map_err(|error| error.to_string())?;
    store.set(ORIGIN_KEY, origin.clone());
    store.save().map_err(|error| error.to_string())?;
    allow_origin(&app, &origin)?;
    app.autolaunch()
        .enable()
        .map_err(|error| error.to_string())?;
    let window = app
        .get_webview_window(MAIN_WINDOW)
        .ok_or("main window is unavailable")?;
    navigate(&window, &origin)?;
    Ok(origin)
}

#[tauri::command]
fn begin_oidc_login(app: AppHandle, handoff: String) -> Result<(), String> {
    if handoff.len() != 64
        || !handoff
            .chars()
            .all(|character| character.is_ascii_hexdigit())
    {
        return Err("desktop sign-in handoff is invalid".into());
    }
    let origin = stored_origin(&app).ok_or("server origin is not configured")?;
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
fn set_attention(app: AppHandle, count: u32) -> Result<(), String> {
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
        tray.set_tooltip(Some(if count > 0 {
            format!("Taskboard — {count} need attention")
        } else {
            "Taskboard".into()
        }))
        .map_err(|error| error.to_string())?;
    }
    if let Some(window) = app.get_webview_window(MAIN_WINDOW) {
        let _ = window.set_badge_count((count > 0).then_some(count as i64));
    }
    Ok(())
}

#[tauri::command]
fn alert(app: AppHandle, payload: AlertPayload) -> Result<(), String> {
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

fn build_tray(app: &AppHandle, icon: Image<'_>) -> tauri::Result<()> {
    let open = MenuItem::with_id(app, "open", "Open Taskboard", true, None::<&str>)?;
    let refresh = MenuItem::with_id(app, "refresh", "Refresh", true, None::<&str>)?;
    let quit = MenuItem::with_id(app, "quit", "Quit", true, None::<&str>)?;
    let menu = Menu::with_items(app, &[&open, &refresh, &quit])?;
    TrayIconBuilder::with_id("taskboard")
        .icon(icon)
        .tooltip("Taskboard")
        .menu(&menu)
        .show_menu_on_left_click(false)
        .on_menu_event(|app, event| match event.id.as_ref() {
            "open" => show_main_window(app),
            "refresh" => {
                let _ = app.emit("taskboard://refresh", ());
                show_main_window(app);
            }
            "quit" => app.exit(0),
            _ => {}
        })
        .on_tray_icon_event(|tray, event| {
            if let TrayIconEvent::Click { .. } = event {
                show_main_window(tray.app_handle());
            }
        })
        .build(app)?;
    Ok(())
}

fn main() {
    // Fedora/KWin can terminate GTK WebKit clients on the explicit-sync Wayland
    // path. XWayland is stable on Plasma and preserves tray/notification support.
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
            configure,
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
            if let Some(window) = handle.get_webview_window(MAIN_WINDOW) {
                window.set_icon(icon.clone())?;
            }
            build_tray(&handle, Image::from_bytes(TRAY_ICON)?)?;
            if let Some(origin) = stored_origin(&handle) {
                if let Err(error) = handle.autolaunch().enable() {
                    eprintln!("taskboard: cannot enable autostart: {error}");
                }
                match allow_origin(&handle, &origin) {
                    Ok(()) => {
                        if let Some(window) = handle.get_webview_window(MAIN_WINDOW) {
                            let _ = navigate(&window, &origin);
                        }
                    }
                    Err(error) => eprintln!("taskboard: cannot trust {origin}: {error}"),
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
