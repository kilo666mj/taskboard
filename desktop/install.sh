#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

app_id="com.kilo666.taskboard"
binary_name="taskboard-desktop"

install_macos() {
    local applications_dir="${TASKBOARD_APPLICATIONS_DIR:-${HOME}/Applications}"
    local app_bundle="src-tauri/target/release/bundle/macos/Taskboard.app"
    local installed_app="${applications_dir}/Taskboard.app"

    if ! cargo tauri --version >/dev/null 2>&1; then
        echo "error: cargo-tauri is required (cargo install tauri-cli --locked)" >&2
        exit 1
    fi

    (cd src-tauri && cargo tauri build --bundles app -- --locked)
    mkdir -p "$applications_dir"
    /usr/bin/ditto "$app_bundle" "$installed_app"
    /usr/bin/codesign --force --deep --sign - "$installed_app"

    echo "Installed Taskboard at ${installed_app}."
}

install_linux() {
    local bin_dir="${XDG_BIN_HOME:-${HOME}/.local/bin}"
    local data_dir="${XDG_DATA_HOME:-${HOME}/.local/share}"
    local target="src-tauri/target/release/${binary_name}"
    local desktop_file="${data_dir}/applications/${app_id}.desktop"

    if ! pkg-config --exists glib-2.0 webkit2gtk-4.1; then
        echo "error: Tauri's GTK/WebKit development libraries are required" >&2
        exit 1
    fi

    (cd src-tauri && cargo build --release --locked)
    install -Dm755 "$target" "${bin_dir}/${binary_name}"
    install -Dm644 src-tauri/icons/icon.png "${data_dir}/icons/hicolor/512x512/apps/${app_id}.png"
    mkdir -p "${data_dir}/applications"

    {
        echo '[Desktop Entry]'
        echo 'Type=Application'
        echo 'Name=Taskboard'
        echo 'Comment=Live checklist for agent work'
        echo "Exec=/usr/bin/env GDK_BACKEND=x11 WEBKIT_DISABLE_DMABUF_RENDERER=1 ${bin_dir}/${binary_name}"
        echo "Icon=${app_id}"
        echo "StartupWMClass=${app_id}"
        echo 'Terminal=false'
        echo 'Categories=Office;ProjectManagement;Utility;'
        echo 'StartupNotify=true'
    } > "$desktop_file"
    chmod 644 "$desktop_file"

    command -v update-desktop-database >/dev/null && update-desktop-database "${data_dir}/applications" >/dev/null 2>&1 || true
    command -v gtk-update-icon-cache >/dev/null && gtk-update-icon-cache -f -t "${data_dir}/icons/hicolor" >/dev/null 2>&1 || true
    command -v kbuildsycoca6 >/dev/null && kbuildsycoca6 --noincremental >/dev/null 2>&1 || true
    echo "Installed Taskboard for the current Plasma user."
}

case "$(uname -s)" in
    Darwin) install_macos ;;
    Linux) install_linux ;;
    *)
        echo "error: desktop installation is supported on macOS and Linux" >&2
        exit 1
        ;;
esac
