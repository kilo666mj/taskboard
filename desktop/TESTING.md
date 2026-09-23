# Desktop server switching checks

Run from `desktop/src-tauri`:

```sh
cargo fmt --check
cargo test --locked
cargo clippy --locked --all-targets -- -D warnings
cargo build --locked
node --check ../src/app.js
```

For runtime checks, use a separate app identifier/profile and two local HTTP
fixtures on different hostnames (`127.0.0.1` and `localhost`). Cookies are scoped
by hostname, not port. Do not use a personal profile or enable test autostart.

1. Seed the profile's `taskboard.json` with a legacy `origin` entry. Launch and
   verify migration to a named server and selection of that server.
2. Add a second server, reject duplicate addresses and addresses containing a
   path, and edit its name. Open the tray's Add Server action and check input
   focus. Check that Manage Servers remains available.
3. Switch through the tray and manager. Verify the native window title, one
   checked tray entry, and that only the selected origin accepts attention
   commands. Set attention before switching and check that it resets.
4. Give each fixture a distinct session cookie. Switch away and back, checking
   that each hostname retains its own session. This checks browser session
   storage; it does not replace an end-to-end identity-provider login test.
5. Select an unavailable loopback address, reopen Manage Servers, and recover
   by selecting or removing it. Remove the active server and check fallback.
6. Restart and verify the saved server name and last selection. At the manager's
   minimum width (420 pixels), verify that content does not overflow. Check
   keyboard focus, input labels, error announcements, and reduced-motion CSS.
7. Remove the final server. Verify that the remote page unloads, selection and
   attention clear, and the manager opens. On macOS the local URL may serialize
   as `tauri://localhost` without a trailing slash.

Verification on 2026-09-23: the recovered implementation passed the commands
above on macOS 27.0 (26A428). An isolated native Tauri/WKWebView run exercised
the manager form, validation, tray action handler and menu state, switching,
titles, per-host fixture cookies, unavailable-server recovery, restart
persistence, 420-pixel layout, inactive-origin rejection, and final removal.
The Taskboard task records the preceding Linux runtime and static checks as
passed. No production identity-provider login was performed in this macOS run.
