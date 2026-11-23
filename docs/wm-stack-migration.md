# WM stack migration: Hyprland and Noctalia out, KDE Plasma in

Status: **decided 2026-11-22, not built.** Work starts after the 2TB move.
This page is the plan of record: why, what each part becomes, the security
work, the build order and the tickets (Plane 390 and its children). The
2026-11-20 niri plan is kept at the end as history; where it disagrees with
the 2026-11-22 decisions, the 2026-11-22 decisions win.

The political/money analysis also exists as a local mockup
(`~/.vibe/serve-mockup/index.html`, "political audit, niri migration &
waybar battle plan"). Its "KDE later" and "Hyprland behind a git flag"
lines are superseded by this page.

## Why we are moving

- 2022–2023: a Hyprland Discord moderator stripped a trans woman's pronouns
  from her nickname. Drew DeVault documented "Hyprland is a toxic community";
  Vaxry's own community poll showed ~40% of members experienced harassment.
- 2024: freedesktop.org banned Vaxry from all fdo projects (wlroots,
  wayland, …) for CoC violations. He can no longer push wlroots patches.
- Aug 2026: DHH's Omacom Foundation ($8M→$18.5M+, donors incl. DigitalOcean
  $3M, 1Password $300k, 37signals, Dell, Dorsey …) made **Hyprland its
  exclusive 3-year sponsor** and **Quickshell + Mise "premier sponsors"**.
  Omarchy (DHH's Arch spin) runs a Quickshell shell with a Noctalia plugin
  compat layer (omacom/omarchy PR #5856).
- Refusers of Omarchy/DHH money: **Arch Linux project** and **AUR maintainers**
  (building their own package management), Framework walked back after an
  Oct 2025 backlash. Boycott lists: stopomarchy.org. 1Password and
  DigitalOcean took customer backlash for pledges.
- GNOME (Petridis/alatiera) and sr.ht/DeVault publicly distanced; KDE e.V.
  has an enforced CoC and no Omarchy link. KDE Plasma is Wayland-default and
  goes Wayland-only with Plasma 6.8 (Oct 2026).

Correction that matters: **Noctalia v5 does not use Quickshell.** v4 was
Quickshell/QML (now unmaintained); v5 (what this repo pins, flake.lock rev
`125a30d4f13e`) is a native C++/OpenGL ES core with Luau plugins and TOML
config. So the Omacom→Quickshell money reaches Omarchy's own shell, DMS and
noctalia-v4 — not our v5 shell. The move off Noctalia is driven by **build
time and shared-config needs**, not by that chain.

## Decisions 2026-11-22 (current)

1. **One session: KDE Plasma (KWin).** niri is logged for later (Plane
   ticket "niri session, later"), not built now. Reason: one session means
   one security review, and niri's scrolling columns are not the split-tile
   layout Hyprland has today.
2. **Tiling: Krohnkite** (KWin script), spiral layout as the closest match
   to Hyprland's split tiles, narrow gaps. HyprKwin is out: its main feature
   is importing `hyprland.conf`, which is deleted. CoC/maintenance check of
   Krohnkite before use.
3. **Keys: one source.** `user/wm/hyprland/keybinds.json` moves to a
   compositor-neutral place and renders to KWin global shortcuts and
   Krohnkite shortcuts. Same keys as today. Super+F1 help reads the
   generated list, not `hyprctl binds`.
4. **Shell: Plasma's own panel**, floating, current look as the target. A
   gjallar **control-centre widget** (QML plasmoid) recreates Noctalia's
   control centre look (Wi-Fi, BT, volume, brightness, power profile, quick
   keys, disk protection). Notifications, OSD, launcher (KRunner) and
   screenshots (Spectacle) are Plasma's own.
5. **Lock: kscreenlocker.**
6. **Greeter: replaced by decision 15.** tuigreet was the first pick, but it
   has no on-screen keyboard.
7. **Theme: Plasma colour schemes** generated from our palettes. The
   greeter takes new colours **at rebuild only**: the live pkexec greeter
   sync is removed (fewer root paths; Jade: "whatever is securer and
   cleaner").
8. **Portal: xdg-desktop-portal-kde only** (never mixed, fdo#969). The gtk
   portal and its file dialog go too: KDE file dialog only (Jade 2026-11-22).
9. **No rollback, no migration prompt.** fw13 is the only install (Jade
   2026-11-22); Hyprland/Noctalia are deleted once KDE works on e2e-fw13.
10. **gjallarctl stays the one control tool.** `hyprland-rotate` → `rotate`,
    `hyprland-render-gpu` → `render-gpu`, no aliases.
11. All day-1 features block the switch (2026-11-20 answer 5 stands):
    rotation, render GPU, greeter OSK/Plymouth wiring, disk-protection
    notice, desktop OSK.
12. **Displays: kscreen** (Jade 2026-11-22). monique goes; KDE's own
    display settings and saved layouts replace it.
13. **Rotation: KWin auto-rotate, ODDC decides per model** (Jade
    2026-11-22). `gjallarctl rotate` only calls KWin's output setting.
14. **Clipboard: Klipper off, paste-once built for KDE** (Jade 2026-11-22),
    shipped from the store. No history, nothing on disk.
15. **Greeter: open again.** tuigreet has no on-screen keyboard. greetd +
    a KWin greeter vs Plasma Login Manager is compared on e2e-fw13 before
    the build (Jade 2026-11-22): root parts each adds, whether PLM's
    settings helper can be switched off, on-screen keyboard, Plymouth
    handover, fingerprint. Jade picks with the results.
16. **Secrets: KWallet replaces gnome-keyring** (Jade 2026-11-22: the
    native choice). ksecretd answers `org.freedesktop.secrets`, so
    `secret-tool` callers keep working. One-off move of the user's secrets
    from gnome-keyring; gnome-keyring goes. kwallet-pam gets a loophole pass
    first (it runs in the root PAM stack and works in the user's folders,
    L178). Wi-Fi/VPN secrets stay system-side.
17. **Lid and power keys: PowerDevil config, locked** (Jade 2026-11-22).
    Generated from the installer answer, Kiosk `[$i]`, so the user session
    cannot change it.
18. **Automount: `gjallarctl media automount`** (Jade 2026-11-22). Own user
    unit, same binary as `media notice`. Mounts only media detected less
    than 5 s ago (the rule of our udiskie patch, like GNOME); unlocked LUKS
    and drive-less devices always mount; only on usbguard installs, as
    today; only signals from the real udisks count. KDE's automounter is
    locked off (Kiosk) and its settings page hidden: it matches "known"
    devices by device name (`block_devices/sdb1`, not the stick), mounts
    them again at login and has no new-media check (plasma-desktop 6.6.6
    `solid-device-automounter`). udiskie, its overlay and patch go at the
    switch. The new-media rule is proposed to KDE (book 753).
19. **Noctalia does not stay on KDE** (Jade 2026-11-23). Noctalia 5.6
    (C++, no Quickshell) runs on KWin only as a second shell on top of
    plasmashell: it uses Plasma's notification server and OSD. On KWin its
    lock does not work (no ext-session-lock; lock through kscreenlocker is
    still an open pull request, #5296), and clipboard history, paste,
    screenshots, night light and the keyboard-layout widget have no
    protocol to use. Its taskbar writes `kwin-active-window.js` into the
    user's state folder and loads it into KWin, which breaks the sealed
    KWin scripts (L166). Plugins are unsandboxed and update themselves
    from git every 6 hours by default. The only saving would be the
    rewrite of our 7 plugins, so decision 4 stands: Plasma's own panel,
    and Noctalia with its 23 patches (and the 8 greeter patches) goes at
    the switch.
20. **Base and versions** (Jade 2026-11-23). Build the switch now on
    nixos-26.05 (Plasma 6.6.6, KF 6.26.0) with our patches; move to
    nixos-26.11 once it is out (nixos-unstable has Plasma 6.8.3 / KF 6.32
    today) and re-check every patch and finding against the new versions.
    Patches upstream has fixed by then are dropped, not kept.
21. **Per-screen workspaces** come native with KWin 6.7 or later, so at
    the 26.11 move. Until then one shared set of workspaces on all
    screens; no interim script (no throwaway code). Logged as a known
    limit until 26.11.
22. **On-screen keyboard.** KWin patch: show the keyboard on text focus
    when no hardware keyboard is attached. Key preview through a patch to
    plasma-keyboard's style; the same patch reads layouts only from the
    store (L189).
23. **KWin realtime capability.** Patch KWin to drop `cap_sys_nice` after
    its realtime thread is set up (L172).
24. **Remote input dropped for now** (L186). Portals RemoteDesktop and
    InputCapture set to `none`, the dialog skip patched out, KWin's
    remote-input interface closed. An allow-list by program path is not a
    boundary against code in the user's session, so it is not used. Later
    review after the move: remote session for IT on Genbu-joined machines
    (needs grants only to programs that root or KWin start).
25. **Clipboard and config.** Primary selection (middle-click paste) off.
    KConfig patch: locks count only from `/etc/xdg` and the store (L188);
    user nix profiles keep working.
26. **Removed or locked.** kio-admin removed (module change). KRunner
    D-Bus runners only from a store list (L169). Dictionary and unit
    converter runners off and locked (L191). Lock screen media controls
    off.
27. **Screen share asks every time** (L177): patch the portal so the
    restore box is gone and saved restore data is ignored.
28. **No previews on removable media** (L191): KIO patch makes removable
    media count as slow, so Dolphin builds no previews there. The USB
    guard gates devices, not the files on them.
29. **Wallet re-key.** A password change through gjallarctl also re-keys
    the KWallet (old and new password asked, nothing printed).
30. **Tiling.** Krohnkite 0.9.11.0 as our own store package (reviewed
    diff), dropped when nixpkgs catches up. Super+P toggles floating
    (Krohnkite has no pseudo-tile).
31. **Every patch gets an upstream write-up** (Jade 2026-11-23): one page
    on the private upstream book (753) per patch with the full diff, the
    problem, why upstream's behaviour falls short, how the patch fixes it,
    the VM proof and notes on fitting it upstream, written in the same
    step as the patch. The patch file links its page.

## What Noctalia and Hyprland do today, and where each part goes

| Today | Where it lives | On KDE |
|---|---|---|
| Compositor | Hyprland + aquamarine overlay, 4 Hyprland patches, xdph patch | KWin (nixpkgs, no patches planned) |
| Window rules, settings, env | `user/wm/hyprland/{rules,settings,env}.nix` | kwinrc + KWin window rules, generated |
| Keybinds + Super+F1 help | `keybinds.json`, `binds.nix`, `keybinds-help.py` (reads `hyprctl binds`) | same JSON → kglobalshortcutsrc + Krohnkite; help reads the generated list |
| Touch gestures | hyprgrass plugin + 2 patches, `touch.nix` | KWin touchscreen edge swipes / touchpad gestures; parity check needed |
| Privileged Wayland access | `permissions.nix`: screencopy only for portal, lock, shell; plugin allowlist; "log out to finish" notice | KWin's own restriction of privileged interfaces; must be mapped and tested |
| Bar | Noctalia | Plasma panel, floating |
| Control centre | Noctalia | gjallar control-centre plasmoid |
| Notifications, OSD | Noctalia | Plasma |
| Launcher | Noctalia | KRunner / Kickoff |
| Polkit agent | Noctalia (`polkit_agent = true`) | polkit-kde-agent |
| Lock | Noctalia lock screen (+ hyprlock config) | kscreenlocker |
| Wallpaper | Noctalia, from ~/Pictures | Plasma slideshow wallpaper, from ~/Pictures |
| Battery toasts | Noctalia off when battery-guard runs | PowerDevil toasts off when battery-guard runs |
| Plugin: disk protection | shield until disk protection is set up | plasmoid |
| Plugin: developer mode | shows developer mode is on | plasmoid |
| Plugin: VPN block | shield while VPN blocks/pauses the network | plasmoid |
| Plugin: time | clock while NTS has not confirmed the time | plasmoid |
| Plugin: quick keys | active bezel Quick Keys preset | plasmoid |
| Plugin: OSK | show/hide keyboard, visible in tablet mode | Plasma virtual keyboard or wvkbd (research) + plasmoid toggle |
| Plugin: weather city | asks once whether the city is right | plasmoid or Plasma weather widget |
| Theme sync | see next section | see next section |
| Greeter | noctalia-greeter (8 patches), live look sync via pkexec | greeter of decision 15, colours at rebuild |
| Session target | `hyprland-session.target` | `graphical-session.target` |
| gjallarctl rotate / render GPU / reload watch | `hyprctl`, socket2 | KWin output config (kscreen-doctor / D-Bus), `KWIN_DRM_DEVICES` |
| Active window for resource QoS | `user/services/resource-qos.nix` uses `hyprctl activewindow` | KWin D-Bus or KWin script |
| Session diagnostics | `system/tools/default.nix` (hyprctl, `noctalia msg status`) | rewritten for KWin/plasmashell |

## Full inventory (repo sweep 2026-11-22)

The table above was the first cut. A sweep of every tracked file that still
names Hyprland, Noctalia, hyprctl, wvkbd, grim/slurp, wl-copy or monique
(outside the folders deleted in CP-W8) found these on top.

### Not in the first cut

| Part | Where | On KDE |
|---|---|---|
| Monitor layouts (monique, submodule `pkgs/monique`, vard's repo) | `flake.nix`, `system/wm/hyprland/default.nix`, `user/wm/hyprland/default.nix` (monitors.lua), `rules.nix` (OSD rule), `.gitmodules`, `scripts/installation/install.sh`, `internal/installercheck/check.go`, installer tests (`flakesource`, `systemcheckout`), `scripts/testing/test-installer-bootstrap.py` | monique has Hyprland, niri and sway backends, no KWin. KDE's display settings (kscreen keeps a layout per set of connected screens), decision 12. monique goes. |
| On-screen keyboard (wvkbd + our overlay: close key, swipe down, no key preview) | `pkgs/overlays/wvkbd.nix`, `user/wm/hyprland/touch.nix` (two user units, tablet mode, bezel gesture), Noctalia OSK button, greeter | KWin offers the virtual-keyboard protocol to no normal client (KDE bug 512996), so wvkbd cannot type on KWin. Plasma's own keyboard (plasma-keyboard, Plasma 6.6+) runs as KWin's input method and shows on text focus. Our overlay patches do not carry over; the no-key-preview rule (password privacy) must be checked on plasma-keyboard. |
| Login screen OSK | `system/wm/common/wayland.nix` runs wvkbd inside noctalia-greeter on touch machines | The greeter of decision 15 must show plasma-keyboard; the VM comparison checks it. Until then a touch-only device cannot log in. |
| GTK and Qt theming | `user/wm/shells/noctalia/gtk.nix` (adw-gtk3, CSS from semantic tokens), `qt.nix` (qtct + Kvantum "Noctalia", writes `Noctalia.colors`) | Lives inside the Noctalia folder, so it moves, not deleted. Qt: Plasma's own platform theme + generated colour scheme replaces qtct/Kvantum. Stylix's NixOS Qt target already picks platform `kde` + Breeze when Plasma 6 is on; the Home Manager `qt.nix` `mkForce` of qtct/Kvantum goes, so no `QT_QPA_PLATFORMTHEME`/`QT_STYLE_OVERRIDE` session value overrides Plasma. GTK: adw-gtk3 + our CSS stays, written by Stylix at rebuild (one GTK writer, as today). KDE's GTK sync (kded `gtkconfig`) writes the same files Home Manager owns, so it is off and locked. VM: no qtct value in the session environment, Qt and GTK apps in the palette. |
| Stylix KDE target | Not set today (`themes/lib/home.nix` turns other targets off) | Turns itself on with Plasma: puts a `kdeglobals` with an immutable colour scheme, look-and-feel and fonts into `XDG_CONFIG_DIRS` and runs `plasma-apply-lookandfeel` at every login and Home Manager switch. That would block the palette pick in System Settings and fight the colour watcher. `stylix.targets.kde.enable = false`; our own `kdeglobals` locks (guards table) are the only system copy. VM: picking a palette in System Settings sticks after logout. |
| qt6ct stack | `pkgs/overlays/qt6ct-kde-integration.nix`, `pkgs/patches/qt6ct-kde-fixed-font-key.patch`, `tests/nix/qt6ct-kde-style.nix`, flake.nix qt6ct entries, `apps/dolphin/home.nix` (Darkly + `qt6ctSettings`), `themes/apps/dolphin/darkly.nix`, `themes/lib/semantic.nix` Kvantum roles, `pkgs/gjallar-settings` (Fusion palette from qt6ct) | All go with qtct. Dolphin and the settings app use the Plasma style and scheme. Darkly vs Breeze is one choice at CP-W5 (one style for all Qt apps, not Dolphin only). The Nextcloud palette patch (`apps/nextcloud/patches/nextcloud-palette-theme.patch`, test `nextcloud-client-patches.nix`) is re-checked: QML windows must follow the Plasma palette, no white window with light text. |
| Ghostty overlay | `pkgs/overlays/ghostty.nix`: touch cancel from a gesture left a button held (hyprgrass sent `wl_touch.cancel`); portal dialog scope app id | Both fixes stay (the scope id is compositor-neutral). VM: KWin touch gesture over Ghostty leaves no held button. |
| Installer stick font size | `system/installer-media/stick.nix` imports `fontSize` from `themes/noctalia.nix` | The stick (cage + foot) does not use Hyprland, but CP-W8 deletes that file. The value moves to a neutral theme file in CP-W1; the stick build runs again after CP-W8. |
| Live app themes | Noctalia templates for Ghostty (`user/apps/ghostty.nix`), VSCodium (`user/editors/vscodium/default.nix`), btop (`apps/btop/home.nix`), hyprlock colours | The colour watcher writes all of them, not only Ghostty. hyprlock goes. |
| Clipboard paste-once (patched wl-copy, Hyprland paste binds) | `user/wm/hyprland/settings.nix`, `wl-copy-paste-once-keep-owner.patch` | KWin has no "send shortcut" step to hand the paste over. Klipper owns the clipboard and keeps a history by default. Paste-once needs a new design or is lost; Klipper history off either way. |
| Primary selection off | `settings.nix` | KWin setting. |
| Wine sandbox display | `internal/winesandbox/wayland.go` needs `wp_security_context_v1`, fails closed without it | KWin has it (6.8 tested by others). VM proof that a sandboxed Wine app gets no screen capture, input or clipboard snooping. |
| Fingerprint at lock | `system/hardware/fingerprint.nix` (hyprlock, swaylock) | kscreenlocker PAM (`kde` service + fingerprint). |
| Window rules by title | Nextcloud dialogs (`apps/nextcloud/home.nix` calls `hyprctl dispatch`), USB and security notices, Firefox extension pop-ups (title changes after map, Plane 188), monique OSD | KWin window rules; title-change case may need a KWin script. |
| Nextcloud login window | `apps/nextcloud/home.nix`: `hyprctl activeworkspace` picks the workspace, `hyprctl dispatch` starts the login browser there, `window_rule` floats, centres and sizes class `gjallar-nextcloud-login` (`user/browsers/login.nix` recipe sets the class) and lets the client take focus | KWin opens new windows on the current desktop, so the workspace lookup and dispatch go; float, centre, size and focus become KWin rules on the same window class. The `HYPRLAND_INSTANCE_SIGNATURE` branches are deleted, not kept as a fallback. VM: login window opens centred on the current desktop. |
| Links from apps (`user/url-handler.nix`) | `gjallar-open-url` routes exact https hosts to their app, other http(s) and `file://` to the browser, refuses every other scheme; `mimeapps.list` points `x-scheme-handler/http(s)` at it, `$BROWSER` too | KIO (`OpenUrlJob`, also behind `kde-open` and `xdg-open` on Plasma) asks `x-scheme-handler/https` first, so our handler keeps winning. Default Applications in System Settings writes `mimeapps.list` only when the file is writable; ours is a read-only Home Manager link, so the page cannot change it (same as today). KIO's last fallback, kdeglobals `[General] BrowserApplication`, gets our handler and is locked (guards table). VM: https link from Dolphin and from a KDE notification goes through `gjallar-open-url`, a routed host opens its app. |
| Volume, brightness, mic and media keys | `keybinds.json` sends the XF86 keys to Noctalia IPC; volume and brightness step 5 (ODDC 26.2: held keys repeat about 7 times a second) | plasma-pa and PowerDevil own the XF86 keys on Plasma and show their own OSD. The generator leaves XF86 keys out of the KWin shortcuts, so one key press never fires twice; the Super+Alt combos map to the plasma-pa and media actions. `plasmaparc [General] VolumeStep` is set from the same step (upstream default 5). Brightness step comes from PowerDevil (config locked, decision 17). VM: one press = one step, held key behaves like today. |
| Boot console prompts | `installer-resume.nix` and `measured-boot.nix` quit Plymouth, `chvt 1` and prompt on tty1; `console-lockdown.nix` keeps every other VT off | Unchanged with greetd on VT1. If the greeter comparison (decision 15) picks Plasma Login Manager, it must start on VT1 after these units, or the prompts end up on a VT nobody sees. VM: installer resume and TPM setup prompt visible with the chosen greeter. |
| Settings app colours | `pkgs/gjallar-settings/gjallar_settings.py` reads `Noctalia.colors` | Reads the Plasma colour scheme, or drops its own palette code since Qt apps get it natively. |
| Dolphin app menu fix | `apps/dolphin/home.nix` (no applications menu in a Hyprland session) | Not needed on Plasma; delete. |
| Bar shields | `apps/tailscale/home.nix`, `system/security/disk-unlock.nix` (+ session target) | Plasmoids (W9). |
| Installer and config | `app.go` (`profileDefault`, `render.WMs`), `config.go` (`Theme: "noctalia"`, wallpaper note), `default.user.config.json`, many tests with `Theme: "noctalia"` | New values; Jade's own `user.config.json` gets one hand edit at the switch (no migration code). |
| JODS desktop profile | `internal/installer/config` accepts `DesktopProfile = "hyprland"` | Contract change with the JODS agent (jods-21) before the value changes. |
| Local AI notes | `apps/ai/nixos.nix` lists Hyprland/Noctalia as context | Text only; AI is benched. |
| Nix tests | `tests/nix/noctalia-notification-app-name.nix`, `theme-palettes.nix` (Noctalia 4.5:1 pre-hook), `nextcloud-client-patches.nix` (Noctalia palette) | First goes, second keeps the contrast rule for Plasma schemes, third gets the new palette source. |

### gjallarctl

Only two commands talk to Hyprland. Everything else (usb, thunderbolt,
luks, vpn, rebuild, secure-boot, oddc, move, apps, settings, snapshots,
wine, fan, ...) does not touch the compositor.

- `hyprland-rotate` (main.go 3516-3780: `hyprctl monitors/eval/reload`,
  socket2 reload watch, scale fix, touch/pen mapping). KWin rotates by
  itself from iio-sensor-proxy and maps touch and pen to the panel, in
  tablet mode or always. Likely delete instead of rewrite; the ODDC sensor
  proof decides whether KWin's auto-rotate is switched on.
- `hyprland-render-gpu` (`rendergpu.go`: socket2, exit dispatch). KWin takes
  `KWIN_DRM_DEVICES` at session start; the "new screen wants another GPU,
  log in again" offer needs a KWin output signal instead of socket2.
- Palette: `internal/noctalia` → `internal/palette`, env
  `GJALLAR_NOCTALIA_COLORS` renamed (`system_rebuild.go`, `main.go`,
  `palette.go`, `cmd/gjallar-settingsd` (`bus.go` writes the rebuild env),
  `themes/lib/common.nix` (Nix reads the value with `builtins.getEnv`),
  and the root rebuild script in
  `system/tools/commands/default.nix`, which passes only this value
  through `env -i`). Root code: the rename gets the loophole pass, and the
  root side keeps re-checking the value (16 base16 values, size cap).
- zsh completion `pkgs/gjallarctl/_gjallarctl`, usage text.
- Behaviour behind comments that must still hold: notice windows float on
  top (`usb.go`, `security_notice.go`), tablet switch reload
  (`tabletswitch.go`), time widget (`system_timewatch.go`), disk-protection
  button text (`luks_remind.go`), keyboard layout reaches the compositor
  (`internal/input/xkb`).

### Patches and custom code that go

Hyprland 4 patches + aquamarine overlay, xdph 1 + overlay, Noctalia 24,
noctalia-greeter 8, hyprgrass 2, wl-copy 1, wvkbd overlay; the pkexec greeter
sync helper and its polkit rule; the Hyprland Lua config generator and the
hyprlang migration code; Hyprland permission bookkeeping; probably the
rotate service. About 40 patches we carry today, none planned for KDE.

## What changes, what breaks, what to watch

### Better than today

- Far fewer patches and source builds we own (list above). KDE packages
  come from the nixpkgs binary cache.
- One root path less (greeter sync via pkexec).
- No app gets the virtual-keyboard protocol at all; input injection needs
  the portal.
- Rotation, touch/pen mapping, per-screen layouts and fractional scaling are
  KWin features with years of use, not our code.
- The lock survives a crash of the desktop shell (KWin holds it).
- Qt and KDE apps follow the colour scheme natively; the Dolphin menu fix
  and the settings app colour reader go away.

### Changes you will see

- Bar, control centre, notifications, OSD, launcher (KRunner) look like
  Plasma styled with our palette, not Noctalia.
- Palette is picked in System Settings → Colours; no palette made from the
  wallpaper (accent only).
- On-screen keyboard is Plasma's, with its own look and gestures.
- Login screen: depends on the greeter decision.
- Monitor tool: depends on the monique decision.
- First login writes fresh KDE config files.
- Workspaces stay per screen (Jade 2026-11-23), through our own KWin
  script; KWin alone would switch every screen together.
- Animations use KWin's effects and speed setting; our own Hyprland curves
  do not carry over.
- Scratchpad (Super+S) needs our own KWin script; KWin has no special
  workspace.

### Breaks unless we build it

- Touch-only login needs the greeter's on-screen keyboard (decision 15).
- Paste-once clipboard.
- Our wvkbd fixes (close key, swipe down).
- Bezel gestures (hyprgrass) until mapped to KWin edge swipes.
- Firefox pop-up rule when the title changes after the window opens.
- Hyprland dispatchers without a 1:1 KWin action in keybinds.json.

### Watch out

- KDE writes its own config over files we manage (plasma-manager vs
  generated files decides how).
- Two portals at once (keep -kde only).
- Baloo file indexing load and what it indexes; KDE Connect, Discover,
  browser integration, kwallet prompts.
- Klipper keeping copied secrets in its history.
- PowerDevil vs battery-guard and our power profiles; toasts twice.
- Units that waited for `hyprland-session.target` must start in every
  session (security notices).
- RAM/VRAM use of plasmashell on small machines (4 GiB Galaxy Book class).
- Fractional scaling and the 1/120 scale case.
- Fingerprint and USB keyboard gate in kscreenlocker.
- Exec-guard probes and the regression list after the switch.
- Any new root code gets the root loophole pass.

## Feel: same look and hand, or better (pass 3, 2026-11-23)

Every look and input setting gjallarOS sets today, from
`user/wm/hyprland/settings.nix`, `keybinds.json`, `rules.nix` and
`themes/lib/common.nix`, with its KDE home. CP-W7 compares both sessions side
by side in the VM (screenshots, a short screen recording of window open,
close and desktop switch, login-to-desktop time) against `hyprland-final`.
A row that cannot match is listed under "Changes you will see" or "Limits".

| Today | Value today | On KDE | Check |
|---|---|---|---|
| Gaps between windows / to the screen edge | 8 px / 16 px (`gaps_in`, `gaps_out`) | Krohnkite tile gap 8, screen gaps 16 | VM screenshot |
| Window border | 2 px, palette colour (`border_size`) | Palette colour through the colour scheme; Krohnkite tiles without title bars, so the border comes from the decoration (Darkly vs Breeze at CP-W5) | VM screenshot |
| Rounded corners, shadow | per theme (`themeDetails.rounding`, `.shadow`, range 20, offset 2,2) | Decoration rounding and shadow set from the same theme values; full-window rounding beyond the title bar needs a KWin effect built from source (research; none rather than another source build without a reason) | VM screenshot |
| Blur behind panels and pop-ups | on, 3 passes, size 10, noise 0.02 | KWin Blur effect, strength and noise from the same values | VM screenshot |
| Window and workspace animations | slide with our own curves; scratchpad slides vertical; border fade | KWin Slide for desktops, Scale or Glide for windows, one speed factor; own curves not possible | VM recording |
| Scratchpad | Super+S show/hide, Super+Shift+S move; rest of the screen dimmed to 0.5 (`dim_special`) | No KWin equivalent. Our own small KWin script shipped in the store (never from the user dir, see the KWin scripts seal) plus the Dim Inactive effect (Jade 2026-11-23: keep it, secure). Seal: script in the store, loaded through the same locked KWin script list as every KWin script, no user copy can shadow it; it only moves and hides windows, no shell or file access; loophole pass on its build | VM: keys, dim, script loads only from the store, a planted user script of the same name does not load |
| Pseudo tile | Super+P keeps the window size inside its tile | Krohnkite float-in-place or none (research) | VM |
| Workspaces | 1-10 per screen (`focusworkspaceoncurrentmonitor`) | Jade 2026-11-23: per screen, like today. KWin desktops span all screens, so this needs our own KWin script from the store, sealed like the scratchpad script (research at CP-W3: how far a script can switch one screen alone in the pinned KWin; if it cannot, back to Jade before any other route) | VM with two screens: Super+2 changes only the screen under the mouse |
| Cursor hides when idle | after 5 s (`cursor.inactive_timeout`) | KWin hide-cursor effect if the pinned KWin has it (research), else a limit | VM |
| Tearing for games | allowed when the app asks (`allow_tearing`) | KWin allows tearing for full-screen apps that ask (tearing-control); check the default in the pinned KWin | VM with a test client |
| Resize from the window edge | on, drag threshold 10 | KWin resize area on borderless tiles, Meta+right-drag (research) | VM |
| Focus follows mouse | on (`follow_mouse`) | kwinrc `FocusPolicy=FocusFollowsMouse`, click raises | VM |
| Touchpad | natural scroll on | KWin libinput settings are per device in `kcminputrc`; research a default for every touchpad | VM |
| Layout switch | Alt+Shift (`grp:alt_shift_toggle`), layout from the installer | `kxkbrc` same option, layout from the installer | VM |
| Pen tablet | absolute (`relative_input = false`) | KWin default absolute; screen mapping through ODDC where known | ODDC device check |
| Touchpad desktop swipe | 3 fingers sideways when `touchpadWorkspaceSwipe` is on; cancel ratio 0.15, distance 200, carries on past the last desktop | KWin built-in swipe: finger count and tuning are fixed by KWin; check whether it can be turned off for the setting | VM |
| Window opacity | per theme (`details.opacity`); terminals opaque | KWin window rules from the same values | VM screenshot |
| Cursor, icons, fonts | phinger-cursors-light 32 (`themes/lib/common.nix:50`), Papirus, Fira Sans/Code, emoji font | Same theme through `kcminputrc` and the colour/font settings (CP-W5); Breeze cursor only as nixpkgs fallback (`plasma6.nix:292`) | VM screenshot |
| Sounds | none | Plasma plays event sounds (ocean theme, a required package) | Off by default, the user may turn them on (taste, not security) |
| Splash after login | none; the bar appears | ksplash off | VM: login-to-desktop time, measured on both sessions |
| Launcher | Noctalia launcher, apps | KRunner apps + calculator; network runners off (security table) | VM |
| Area screenshot | Super+Shift+Z and Print | Spectacle region mode on the same keys, same save folder | VM |
| Floating terminal, window rules | `rules.nix` | KWin window rules, generated | VM |

Can be better than today (proposals, decided at CP-W3 and CP-W5):
Overview with search (Meta+W), window thumbnails in Alt+Tab, Night Light on
a time schedule (no location service), KWin per-screen scaling and HDR,
screen-edge actions.

## Security on KDE: same level, sealed (Jade 2026-11-22)

Rule: KDE ships only when every row below has its seal built and a VM proof.
No row may silently become weaker than today. Where KDE is weaker by
default, we seal it (system config marked immutable through KDE's Kiosk
`[$i]` keys in `/etc/xdg`, package excludes, polkit rules, or our own patch
under the upstream rules). Each finding gets a loophole number on the
private review page; attack detail stays there.

### Today's guards and what replaces them

| Today | KDE default | Seal | Proof |
|---|---|---|---|
| Screen capture only for store-path binaries (Hyprland permissions, `permissions.nix`), unknown clients get a prompt | KWin hands its privileged protocols (screencast, fake input, window management, output management, ScreenShot2 over D-Bus) to any executable that a desktop file names, and it also reads user-writable desktop files. No prompt. ([D22571](https://phabricator.kde.org/D22571), [kwin-input](https://github.com/SamuelCGilbert/kwin-input)) | Grants only from desktop files in the system profile with an executable in `/nix/store`; our KWin patch if no setting exists. Interpreters never get a grant. | VM: a user-made desktop file grants nothing. |
| Compositor plugins only from the store (Hyprland plugin rule) | KWin loads scripts and effects from the user data dir; scripts can also be loaded over D-Bus | Research first; user-dir scripts/effects off or limited | VM probe |
| XWayland apps see no keys of other windows | Default passes modifier combos to every X11 app; one layout bug leaked all keys ([bug 500032](https://www.mail-archive.com/kde-bugs-dist@kde.org/msg1026249.html)) | `XwaylandEavesdrops=None`, mouse off, immutable | VM: X11 key reader sees nothing |
| Lock asks for the password at once | `LockGrace` 5 s unlock without password ([kcfg](https://github.com/KDE/kscreenlocker/blob/master/settings/kscreenlockersettings.kcfg)); grace path also breaks later locks ([bug 528681](http://www.mail-archive.com/kde-bugs-dist@kde.org/msg1231117.html)) | `LockGrace=0`, `LockOnResume=true`, autolock on, immutable | VM: lock, wait 1 s, no unlock without password |
| Lid, power key and clamshell follow the installer answer (`power-keys.nix` writes `/run/systemd/logind.conf.d/50-gjallar-power.conf`) | PowerDevil takes logind's lid and power-key inhibitors and applies its own per-user settings, so our logind file stops applying | PowerDevil config generated from the same answer, system-side and locked with Kiosk `[$i]` (decision 17); lock before sleep on | VM: lid close on battery, AC and docked; power key; clamshell with external screen |
| `loginctl lock` and resume lock | kscreenlocker follows logind Lock | keep; `LockOnResume` immutable (row above) | VM |
| Idle lock (no idle daemon today, lid and key only) | kscreenlocker autolock with user-set timeout | Autolock on, timeout and lock-on-resume immutable | VM: idle past timeout locks |
| Security, USB, Thunderbolt and media notices always show (`internal/notice`) | Plasma "Do Not Disturb", also switched on by itself while a screen is mirrored, hides normal notices | Security notices sent with urgency critical (shown through DND); DND-on-mirror off and immutable | VM: DND on, plug USB, notice shows |
| Lock-screen PAM: password, nullok off (`local-hardening.nix`), finger only where we allow it (`fingerprint.nix`) | nixpkgs `kde` PAM service has `allowNullPassword = true`; finger on a separate `kde-fingerprint` service | `kde.allowNullPassword = mkForce false` like greetd/login; finger moves from hyprlock/swaylock to `kde-fingerprint` only; `polkit-1`, `kde` and the greeter stay without finger | VM: empty-password user cannot unlock; `pam.d/kde` has no `nullok` |
| Time zone change needs admin (polkit default) | nixpkgs `plasma6.nix` allows `set-timezone` for any active user when a time zone is set, and we set one | Our polkit rule ahead of it: `auth_admin` for set-timezone | VM: `timedatectl set-timezone` as user asks for admin |
| No capability wrappers for desktop tools | nixpkgs adds wrappers: `kwin_wayland` cap_sys_nice, `ksystemstats_intel_helper` cap_perfmon, `ksgrd_network_helper` cap_net_raw | Remove the two system-monitor wrappers; KWin must drop its capability after start | VM: `/run/wrappers` list, `CapEff` of kwin_wayland is 0 |
| pkexec refused for users (`local-hardening.nix`); root menu goes through our helper (Q253) | `kio-admin` (a required Plasma package) opens any file as root from Dolphin behind one password, the same reason pkexec is refused | polkit NO for the kio-admin action unless root | VM: Dolphin "Open as administrator" refused |
| Removable media: udiskie mounts only what is there at plug-in (`udiskie.nix` patch), foreign file systems read-only (`foreign-media-readonly.nix`), "Allow writing" notice | KDE device automounter in kded can mount on its own | One automounter only: `gjallarctl media automount` (decision 18) with the same new-media rule; KDE automount off and immutable, its KCM hidden; read-only defaults and the notice carry over unchanged | VM (ported udiskie test + new cases): stick writer writes an image or makes a file system, nothing mounts it; partition made later on an internal disk is not mounted; stick plugged in at login is not mounted; unlocked LUKS stick mounts; forged udisks signal is ignored |
| No Bluetooth file receive | Bluedevil ships OBEX receive (openobex, obexftp) | OBEX receive off, immutable | VM: OBEX push refused |
| Wi-Fi/VPN secrets system-side; Noctalia's secret agent answers only NetworkManager (`network-secret-agent.patch`) | plasma-nm secret agent stores secrets per user in KWallet | Research whether the plasma-nm agent checks the caller; patch if not; secrets stay system-owned | VM: secret request from a non-NM client gets nothing |
| Shell and lock screen run only store code (Noctalia from the store, plugin rule) | plasmashell and the lock screen load widgets, wallpaper plugins, look-and-feel packages and KRunner runners from the user data dir | Research; user-dir QML packages for shell and lock off or limited; lock screen look only from the store | VM probe (private review page) |
| Global shortcuts only from our keybinds | kglobalacceld lets session clients register global shortcuts | Research; limit to known clients if possible | VM probe |
| Screen share asks every time ("Allow once" does not carry over, `hyprland-permission-client-destroy.patch`) | xdg-desktop-portal-kde can keep restore tokens in the permission store | Restore tokens off; only `-kde` portal, gtk portal dropped (decision 8) | VM: share, close, reopen asks again |
| Hyprland permission rules live in the user's Home Manager config | KWin grants (row 1) | System-side seal, stronger than today | VM |
| No background push service | `kunifiedpush` (required package) | Off, immutable; listed under network calls | VM network log |
| Firefox native hosts are only the ones we ship | `plasma6.nix` adds `plasma-browser-integration` as a Firefox native host even when the package is excluded | `nativeMessagingHosts.packages` without it (mkForce) | VM: Firefox native host list |
| Firmware updates only through our dbx/fwupd flow (PCR and shim aware) | Discover offers fwupd firmware updates; `services.fwupd.enable` pulls it in by itself | `environment.plasma6.excludePackages` drops Discover; fwupd polkit defaults checked (CLI path exists today too) | `which plasma-discover` empty |
| Lock OSK only without a real keyboard, no key preview (`lock-osk.patch`, wvkbd `--no-popup`) | plasma-keyboard at the lock screen | Same rule on plasma-keyboard; key preview off | VM with and without USB keyboard |
| Wi-Fi password and Bluetooth pairing requests never take keyboard focus (Noctalia patches) | plasma-nm and Bluedevil open dialogs | Check focus behaviour; patch or notification-only if they steal focus | VM: pairing request while typing |
| polkit prompts from Noctalia's agent | polkit-kde-agent | Shows the requesting program and action; no auto-fill | VM |
| No root helpers from the desktop besides our own (greeter sync goes) | Many KAuth root helpers (System Settings pages, info centre, disks, login manager, Discover). SUSE found root bugs in some ([KDE6 review](https://security.opensuse.org/2024/04/02/kde6-dbus-polkit.html), [plasma-login-manager CVE-2026-25710](https://security.opensuse.org/2026/04/27/plasma-login-manager.html), smb4k CVE-2025-66002/66003) | Only helpers we need; each gets the root loophole pass; polkit `auth_admin` for all KAuth actions; smb4k never | Inventory of `/run/current-system/sw/share/dbus-1/system-services` before/after; planted-link tests where a helper takes paths |
| Nothing runs code from an online store | Global themes, widgets and KNewStuff downloads run code ([KDE warning 2024](https://www.bleepingcomputer.com/news/linux/kde-advises-extreme-caution-after-theme-wipes-linux-users-files/)) | Kiosk `ghns=false`, "Get New" buttons gone; our look ships from the store path | VM |
| Paste-once clipboard, no history, no primary selection | Klipper keeps history and writes it to disk; clipboard actions can run commands | Klipper off or history 0, not saved, actions off; paste-once redesign | VM: copy secret, log out, no trace in `~/.local/share` |
| No file index | Baloo indexes names and contents; on Nextcloud online-only files a read downloads them | Baloo off | VM: no index dir, no VFS hydration |
| No "recent files" database | Activity manager records opened files | Off, immutable | VM |
| Coredumps / crash reports stay local | DrKonqi crash reporting, KUserFeedback; `plasma6.nix:288-289` also hooks DrKonqi's processor into every `systemd-coredump` run, and `excludePackages` does not remove that | Not installed / off; `drkonqi-coredump-processor@` `wantedBy = mkForce [ ]` | VM: crash a test program, no DrKonqi unit runs |
| No phone link, no remote desktop | KDE Connect (opens LAN ports), KRdp | Not installed | port scan in VM |
| Secrets in gnome-keyring (`system/security/keyring.nix`) | ksecretd and kwallet-pam, also a Secret Service provider | One provider only: KWallet (decision 16), gnome-keyring removed; kwallet-pam loophole pass (L178); Wi-Fi/VPN secrets stay system-side as today | VM: ksecretd owns `org.freedesktop.secrets`; `secret-tool` lookup works; nothing else claims the name; planted link in the wallet folder before login is refused |
| Network calls only from apps the user opens | Weather, picture of the day, Welcome Center, KNewStuff fetches | Not installed or off | VM with network log |
| Session not restored on login | Plasma restores the last session's apps | Empty session at login | VM |
| Security notices, disk-unlock shield, build-source, VPN, USB and Thunderbolt units start with the desktop | They wait for `hyprland-session.target` (10 files) and would never start | Move to `graphical-session.target` | VM: each unit active after login |
| Wine sandbox: no display without security context | KWin supports `wp_security_context_v1` | keep fail-closed | VM: sandboxed app gets no capture/input/clipboard of others |
| USB keyboard gate (usbguard, `removable-media.nix`) | compositor-independent | keep | regression list |
| Links open only through `gjallar-open-url` (exact host routes, unknown schemes refused) | KIO falls back to kdeglobals `BrowserApplication` when no scheme handler is set; that entry may name any command | `BrowserApplication` = our handler's desktop file, immutable in `/etc/xdg/kdeglobals`; `mimeapps.list` stays a read-only link | VM: entry cannot be changed from the session, links still routed |
| No account service as root | `plasma6.nix:268-270` turns on accounts-daemon (root) and turns its `PrivateTmp` off so it can read an account picture that System Settings puts in `/tmp` | Off unless the greeter decision needs it; if kept, `PrivateTmp` stays on and the root loophole pass runs (L180) | VM: service absent, or `PrivateTmp=yes` |
| No location service (Firefox `geo.provider.use_geoclue` off) | `plasma6.nix:277` turns on geoclue2 | `mkForce false`; Night Light runs on times; the weather city stays our own question | VM: no geoclue on the bus, network log |
| No accessibility bus, no speech daemon | `plasma6.nix:304` turns on Orca, which turns on at-spi2-core and speechd (`orca.nix:24-25`); the AT-SPI bus lets clients read other apps' text | `mkForce false` for all three (Jade 2026-11-23); accessibility later as an opt-in with its own security pass | VM: no `org.a11y.Bus` |
| No mail or calendar database | `plasma6.nix:264` turns on kde-pim (Akonadi) | `mkForce false`; Thunderbird stays | VM process list |
| SSH asks for passphrases in the terminal | `plasma6.nix:265` sets ksshaskpass as askpass; it can keep passphrases in KWallet | Jade 2026-11-23: keep ksshaskpass only if its save-to-KWallet choice can be locked off; otherwise `askPassword` unset and the terminal asks as today (research CP-W2) | VM: `ssh-add` behaviour |
| Session start: today Hyprland reads its config from the user's home (`HYPRLAND_CONFIG`, `system/wm/hyprland/default.nix:48,66`) | Plasma's session start also reads files from the home folder before KWin starts | Same class of gap on both; KDE must close it, not carry it. Research and seal on the private review page (L181) | VM probe (private) |
| No desktop autostart (Hyprland runs none; Nextcloud's entry is removed, `apps/nextcloud/home.nix:473,632`) | Plasma starts every entry in `/etc/xdg/autostart` and `~/.config/autostart` | System entries inventoried, unwanted ones hidden system-side; user entries part of L181 | VM: started programs equal the list we ship |
| Only our OSK gets the input-method protocol | kwinrc `[Wayland] InputMethod` names the program KWin starts as input method | Set to plasma-keyboard from the store, immutable | VM: entry cannot be changed from the session |
| No remote control, no input capture | xdg-desktop-portal-kde offers RemoteDesktop and InputCapture (input injection through libei) | Both off for the KDE portal, or asked every time with nothing saved (research what the pinned portal allows) | VM: portal call refused |
| Lock screen shows nothing of the session | kscreenlocker shows media controls; notifications can show on the lock screen | Media controls off, notification text hidden while locked, immutable | VM |
| Launcher makes no network calls | KRunner runners for web shortcuts, dictionary and others go online while typing | Network runners off, immutable | VM network log while typing |
| No previews of files on remote folders (Dolphin `EnableRemoteFolderThumbnail=false`, `MaximumRemoteSize=0`) | KDE thumbnailers treat a mounted stick as local and parse its files | Keep today's settings; previews on removable media off unless the VM shows they stay sandboxed (research) | VM: stick with test files, no thumbnailer runs |
| No listening ports from the desktop | ksmserver, KDE Connect, KRdp, file sharing | Generalises the KDE Connect row: nothing new listens after login | VM: `ss -lntup` before and after login equal, port scan |
| No vault tool, no online accounts | Plasma Vault (FUSE), KAccounts | Not installed | `which` empty |
| Root from the desktop only through sudo as set (`local-hardening.nix`) | kdesu | follows the sudo policy, keeps no root session | VM |
| USB devices pass usbguard first | `plasma6.nix:282-284` adds libmtp and media-player-info udev rules | usbguard still decides first; MTP only after allow | VM: blocked phone stays blocked |

Checked in the coverage pass (2026-11-23) and needing no change: OOM
handling (only nix-daemon and the AI service get a penalty; no session
process is protected today, so KWin and plasmashell need nothing new),
night light, HDR, VRR and screen power-off (gjallarOS sets none of them
today; Plasma's own defaults are new features, not a parity gap), Qt and
GTK theming (rows above, CP-W5).

Checked in pass 3 (2026-11-23), no change: pinentry-qt (`plasma6.nix:263`,
same as today), power-profiles-daemon, udisks2 and upower (used today),
xdg-user-dirs, kde-inotify-survey and kwrited (local only), the
`nixos-rebuild-sycoca` user cache service.

### Root code added by the switch

Every KAuth helper and every root unit KDE brings is root code under the
standing root loophole rule. Plasma Login Manager's helper had a
service-user-to-root bug (CVE-2026-25710); if it is the greeter, our pin
must carry the fix and the pass is done before the switch.

## Theme sync

Today Noctalia does four things:

1. You pick palette and wallpaper in Noctalia's settings.
2. Live: a template writes Hyprland border colours (`hyprctl reload`); a
   second writes the Ghostty theme behind a contrast guard and reloads
   Ghostty.
3. Greeter: every theme/wallpaper change runs
   `pkexec gjallar-greeter-sync-privilege` to copy the look to the greeter.
4. Rebuild: Noctalia writes `~/.local/state/noctalia/stylix-override.json`;
   gjallarctl reads it on the user side (`internal/noctalia`, O_NOFOLLOW,
   4 KiB cap), passes 16 base16 values as `GJALLAR_NOCTALIA_COLORS`,
   gjallar-settingsd checks them again (`noctalia.CheckColors`), Stylix
   bakes GTK, Qt, Dolphin, btop, Plymouth.

On KDE:

1. Each palette in `themes/palettes/` is generated as a Plasma colour scheme
   at build time. You pick it in System Settings → Colours.
2. KWin borders, Qt apps and Dolphin follow the scheme natively; GTK apps
   follow at the next rebuild through Stylix, as today (KDE's GTK sync
   stays off, see the theming row). A small user service sees the colour change, writes the
   Ghostty theme through the same contrast guard and reloads Ghostty.
3. Greeter: no live root step. The greeter takes its colours from the system
   build, so a new palette reaches it with the next `gjallarctl rebuild`.
   `gjallar-greeter-sync-privilege` and its pkexec rule are deleted.
4. The same user service writes the 16 colours to a gjallar state file.
   `internal/noctalia` becomes `internal/palette` (new path, new env name,
   same checks, no compat reader). Stylix and Plymouth stay as they are.
5. Lost: a full palette made from the wallpaper. Plasma only derives the
   accent colour. matugen can add it later (rated secondary 2026-11-20).

## Security work

Every item gets a loophole pass (CLAUDE.md rule) and a VM proof before the
switch.

- **Session target.** These user units bind to `hyprland-session.target`
  today: `system/security/disk-unlock.nix`, `security-notice.nix`,
  `build-source.nix`, `system/services/removable-media.nix`,
  `system/hardware/thunderbolt.nix`, the session diagnostics in
  `system/tools/default.nix`. They move to `graphical-session.target`.
  Check: each security notice still starts in every session, and none can
  be skipped by a session that never reaches the target.
- **Privileged Wayland access.** Today only the portal, the lock and the
  shell may screencopy, and Hyprland plugins are allowlisted. KWin's
  equivalent must allow the same set and nothing more; test that a plain
  client cannot screencopy or inject input.
- **KDE services off unless chosen:** KDE Connect, Discover/PackageKit
  updates (Nix owns updates), Plasma browser integration (Firefox and
  Passbolt use their own stores). KWallet stays as the one secret store. Each one
  that stays gets a reason in this page.
- **Lock.** kscreenlocker PAM must match today: password on `kde` without
  nullok, finger only on `kde-fingerprint`, USB keyboard gate, PIN rules.
  Test: lock cannot be bypassed by VT switch or by killing plasmashell
  (KWin keeps the lock).
- **Polkit agent.** polkit-kde-agent replaces Noctalia's agent; root menu
  and helpers (Q253) still work without sudo.
- **Greeter.** The greeter of decision 15 keeps the uaccess, Plymouth hand-over, OSK
  auto-show and measured-boot prompt order from
  `system/wm/common/wayland.nix`. The pkexec greeter-sync path goes away.
- **Portal.** Only -kde. Screen sharing asks every time.
- **No new root code is planned.** If any appears (e.g. a rotate helper),
  it gets the full root checklist and a planted-link test.

## Build order (checkpoints)

Each checkpoint: VM proof on e2e-fw13, exec-guard probes, regression list
(`.vm/installer-e2e/REGRESSION.md`), wiki 1169 + Plane + memory.

1. **CP-W0 Prep.** Commit this page. Plane children of 390. CoC and
   maintenance check: Krohnkite, any plasmoid dependency, plasma-manager if
   used.
2. **CP-W1 Groundwork, still on Hyprland.** Session units →
   `graphical-session.target`; `internal/noctalia` → `internal/palette`;
   keybinds.json to a neutral path with a generated list for Super+F1;
   installer defaults (`app.go` `profileDefault`, `render.WMs`,
   `config.go` `Theme`); stick `fontSize` to a neutral theme file. Ships
   alone, so the big switch has fewer moving parts.
3. **CP-W2 KDE base session.** Plasma 6 module, greetd → startplasma-wayland,
   portal -kde, polkit-kde-agent, kscreenlocker with our PAM, KDE services
   off, privileged-access mapping, KWallet + kwallet-pam + secret move,
   PowerDevil locked config, `gjallarctl media automount` + its VM test.
   `plasma6.nix` defaults forced off (accounts-daemon, geoclue2,
   Orca/AT-SPI/speechd, kde-pim, DrKonqi coredump hook), autostart
   inventory, `InputMethod` lock, portal RemoteDesktop/InputCapture off,
   L180/L181 seals. Loophole pass.
4. **CP-W3 Keys and tiling.** keybinds.json → KWin + Krohnkite, window
   rules, touch gestures, Super+F1. Input rows of the Feel table, workspace
   model, scratchpad script from the store.
5. **CP-W4 Shell.** Panel layout, control-centre plasmoid, the 7 plugin
   widgets, OSK, notifications/OSD, battery toast dedupe.
6. **CP-W5 Theme.** Palettes → Plasma schemes, colour watcher (Ghostty +
   snapshot), greeter colours at rebuild, Plymouth unchanged. Stylix KDE
   target off, kded `gtkconfig` off and locked, Qt platform `kde`, one
   widget style (Darkly or Breeze) for all Qt apps. Look rows of the Feel
   table (gaps, border, rounding, blur, animations, opacity, cursor,
   sounds, splash).
7. **CP-W6 gjallarctl.** `rotate`, `render-gpu`, reload watch on KWin; zsh
   completion; resource-qos active window; session diagnostics.
8. **CP-W7 Switch.** fw13 rebuilds onto KDE (Jade runs it). Full VM pass
   first, on a `REGRESSION.md` rewritten for KDE: each Hyprland/Noctalia
   check (about 175 lines name them) gets its KDE check or is retired with
   the reason. Feel comparison against `hyprland-final` (screenshots,
   recording, login time). Just before the switch merges to dev: annotated tag
   `hyprland-final` on the last Hyprland release commit (the newest `v*`
   tag before the merge), pushed, then a repo ruleset on
   `refs/tags/hyprland-final` that blocks update and delete with no bypass.
   Tag only, no branch (Jade 2026-11-23).
9. **CP-W8 Delete.** `pkgs/overlays/{hyprland,aquamarine,xdg-desktop-portal-hyprland}.nix`,
   the 4 Hyprland + xdph patches, `user/wm/hyprland/` (incl. hyprgrass and
   wl-copy patches), `user/wm/shells/noctalia/`, `pkgs/noctalia/` (24
   patches), `pkgs/noctalia-greeter/` (8), flake inputs, `themes/noctalia.nix`,
   `tests/nix/noctalia-notification-app-name.nix`, hyprland/noctalia refs
   in themes, apps and docs. `pkgs/overlays/udiskie.nix`,
   `pkgs/patches/udiskie-mount-new-media-only.patch`,
   `tests/nix/udiskie-new-media-only.nix` (its cases live on in the
   automount test), `services.udiskie`, gnome-keyring in
   `system/security/keyring.nix`. Also `system/wm/hyprland/`; in
   `system/wm/common/wayland.nix` the noctalia-greeter parts (the pkexec
   appearance helper, its polkit rule, greeter wvkbd);
   `pkgs/overlays/wvkbd.nix` and the flake wvkbd entries; the qt6ct stack
   (theming rows); `hyprlock`/`swaylock` `fprintAuth` in
   `system/hardware/fingerprint.nix`. Comment sweep (no stale text): every
   code comment that names Hyprland, Noctalia, hyprctl or qtct is rewritten
   for KDE or removed (today in `luks_remind.go`, `security_notice.go`,
   `usb.go`, `tabletswitch.go`, `system_timewatch.go`,
   `internal/input/xkb`, `config.go`, `themes/lib/contrast.nix`,
   `themes/lib/openjade-shape.css`, `system/maintenance/storage.nix`,
   Nextcloud patch and source comments). Gate: `git grep -i -E
   'hypr|noctalia|qt6ct|kvantum|wvkbd|udiskie'` outside this page and the
   changelog finds nothing. Installer stick and ISO build again after the
   delete.
10. **CP-W9 Docs.** Public stance in docs (`docs/maintainers.md`
    patched-packages list), README, ROADMAP and
    `scripts/installation/README.md` (wallpaper and dialog notes), wiki.

## Tickets (children of Plane 390)

- W1 (Plane 396) groundwork: session units → graphical-session.target (security)
- W2 (Plane 397) groundwork: internal/noctalia → internal/palette, settingsd + settings app
- W3 (Plane 398) groundwork: keybinds.json neutral + generated Super+F1 list
- W4 (Plane 399) installer defaults: profile, WMs, theme
- W5 (Plane 400) KDE base session + greeter start (decision 15) + portal -kde
- W6 (Plane 401) KDE hardening: privileged access, KDE services off, polkit agent, kscreenlocker PAM
- W7 (Plane 402) keys + Krohnkite + window rules + touch gestures
- W8 (Plane 403) Plasma panel + control-centre plasmoid
- W9 (Plane 404) plugin widgets: disk protection, developer mode, VPN block, time, quick keys, OSK, weather city
- W10 (Plane 405) theme: Plasma schemes, colour watcher, greeter at rebuild
- W11 (Plane 406) gjallarctl rotate / render-gpu / reload watch + zsh + resource-qos + diagnostics
- W12 (Plane 407) switch fw13 + REGRESSION.md for KDE + `hyprland-final` tag and ruleset
- W13 (Plane 408) delete Hyprland/Noctalia
- W14 (Plane 409) docs + public stance
- Later (Plane 410): niri session (logged, not planned)

## Research results (2026-11-23, pin 8ce73368)

Read from source at the pin (Plasma 6.6.6, KF 6.26.0, KWin 6.6.6); not
yet proven in a VM. Security details for every item marked L are on the
private review page; the VM probes at the end are still owed.

- **Krohnkite** (codeberg.org/anametologin/Krohnkite, MIT, kept up, no
  code-of-conduct file). The pin has 0.9.9.2, upstream 0.9.11.0. Spiral
  layout yes, gaps settable (default 0), no pseudo-tile. Its per-screen
  desktops need KWin 6.7.
- **Per-screen workspaces and scratchpad.** The KWin 6.6.6 script API has
  one global current desktop; a script can only fake per-screen
  workspaces by moving windows. A scratchpad script looks doable (window
  desktops, minimise, shortcuts), no prototype yet. Scripts load only from
  the store if we patch the lookup: a `[Plugins][$i]` list alone does not
  hold (L166).
- **plasma-keyboard** (6.6.6, in the pin). No setting for the key preview
  (needs a patch to its style or our own style). KWin shows it after the
  last input was touch or pen, or always with `KWIN_IM_SHOW_ALWAYS`; a
  "no hardware keyboard" rule needs a KWin patch. Closing: only the hide
  key, no swipe. Config: kwinrc `[Wayland] InputMethod` /
  `VirtualKeyboardEnabled`. Layouts are read from the user data dir first
  (L189).
- **Greeter.** Plasma Login Manager 6.6.6: daemon as root, greeter as user
  `plasmalogin`, its KWin runs with `--inputmethod plasma-keyboard` (the
  OSK button in the UI is commented out), starts after plymouth-quit (no
  smooth hand-over), PAM includes login. Its KAuth helper (save, sync,
  reset; auth_admin) cannot be left out of the build; CVE-2026-25710 is
  fixed in the pin, the later helper hardening is on master only (L165).
  greetd: only the daemon is root; regreet and qtgreet are in the pin, a
  KWin wrapper with `--inputmethod` would be our own code. Neither hands
  over from Plymouth smoothly. Greeter pick = decision 15 VM comparison.
- **KWin grants** (L164, L184, L185, L187). Desktop files name the
  privileged globals, the user data dir counts, and only five globals are
  restricted at all; clipboard (data control), screen layout and plasma
  shell are open to every client. Plan: root-owned allow-list in our KWin
  patch, more globals restricted.
- **kglobalacceld** (L176): no caller check on registering or changing
  shortcuts. Plan: caller allow-list patch, lock and VT keys outside it.
- **plasma-nm secret agent:** no caller check in code, but the agent is
  reachable only through NetworkManager's own bus policy (root only). Keep
  that policy as shipped; secrets that must stay out of user reach (VPN,
  802.1X) stay system-owned as today. Closes the question of L168.
- **User QML and runners** (L169): KRunner D-Bus runners load from the user
  data dir with no Kiosk key. Plan: store-only lookup for shell, lock
  screen, look-and-feel, wallpaper and runners.
- **Kiosk** (L188): `[$i]` in `/etc/xdg` is only a lock if nothing earlier
  in the config search order can be written by the user. Every lock below
  depends on this.
- **Capability wrappers** (L172): each wrapper has an `enable` option;
  set it false for `ksystemstats_intel_helper` and `ksgrd_network_helper`.
  KWin seems to keep `cap_sys_nice` after start.
- **Polkit** (L173, L192): one rule file: NO for kio-admin, fontinst,
  kcmclock, charge-threshold set and the ktexteditor buffer helper;
  brightness YES; wakeup count and dmidecode NO. Partition manager,
  Plymouth KCM and SDDM KCM are not pulled in and stay out. kio-admin is a
  required package, so removing it needs a module change.
- **Session start files** (L181): KDE adds start paths (env scripts,
  shutdown scripts, autostart through systemd). L181 does not close by
  switching; it needs a startplasma patch, and systemd user units stay a
  known limit.
- **KWallet:** `ksecretd` owns `org.freedesktop.secrets` in KF 6.26
  (`kwalletd6` is a compatibility client). Cipher Blowfish-CBC with
  PBKDF2-SHA512 (50000 rounds). `pam_kwallet` opens only when a session
  type or display is set (greetd: VM probe). Fingerprint login leaves the
  wallet locked until its first use asks for the password. `passwd` does
  not re-key the wallet. No importer from gnome-keyring: the one-off move
  is our own tool (libsecret read, write to `org.kde.secretservicecompat`,
  nothing printed). Daemon points in L190.
- **PowerDevil:** `powerdevilrc` `[AC|Battery|LowBattery][SuspendAndShutdown]`
  `PowerButtonAction`, `LidAction`,
  `InhibitLidActionWhenExternalMonitorPresent` (docked, default true),
  `AutoSuspendAction`, `AutoSuspendIdleTimeoutSec`, `SleepMode`. Lock
  before sleep is kscreenlockerrc `[Daemon] LockOnResume` (default true).
  `[RunScript]` actions must be locked empty (L193).
- **Portal:** screen-share restore is on by default and skips the dialog;
  no setting turns it off (L177). RemoteDesktop and InputCapture can be
  set to `none` in portals.conf (L186).
- **Feel:** hide-cursor effect `hidecursor` (off by default,
  `HideOnTyping` default true); `[Compositing] AllowTearing` default true,
  only for the active fullscreen window that asks; touchpad defaults for
  every device in kcminputrc `[Libinput][Defaults][Touchpad]`; swipe
  fingers are fixed (3/4 left-right desktops, 4 up overview, 4 down grid);
  Breeze keeps an invisible resize strip on borderless windows.
- **Lock screen:** shows no desktop notifications in 6.6.6; it shows the
  media title and artist (`showMediaControls`, default on).
- **Online runners** (L191): dictionary (plain TCP to dict.org, on by
  default) and the unit converter (daily currency download). USB sticks
  count as local disks, so Dolphin builds thumbnails for them.
- **Greeter and accounts-daemon:** no accounts-daemon use found in Plasma
  Login Manager; L180 can stay off.

Still owed in a VM (CP-W0/CP-W2): greeter comparison (decision 15), real
KConfig search order, lock-screen package shadowing, kglobalaccel bare-key
grabs, greetd session type for `pam_kwallet`, KWin `cap_sys_nice` after
start, PowerDevil Kiosk lock, KCMs that start root helpers one by one
(date/time, print-manager, Thunderbolt).

## Behaviour carried from our patches

Each one gets a KDE check before CP-W7; where KWin or Plasma already does
it, we note that and drop the patch.

- Hyprland: focus back to the right window after unlock; floating windows
  open on screen; touchpad axis direction; no stale cursor after hotplug;
  shortcuts sent unshifted (paste-once).
- Noctalia: battery notice dedupe; lock OSK only without a real keyboard;
  settings per release (rollback must not break on newer KDE config
  versions); notification actions stay internal; app name "GjallarOS" on
  our notices.
- Time bar plugin reads `/run/user/UID/gjallar/time` (`system_timewatch.go`);
  its plasmoid reads the same file. Same for the other 6 plugin widgets (W9).

## Limits on KDE

- pkexec stays refused for users. KDE features that call pkexec do not
  work, by design. README and wiki get this line when the switch ships.
- KWin `wp_security_context_v1`: what a sandboxed client loses (Wine).
- KWin privileged-interface restriction: how it is configured, what it
  covers compared with Hyprland's permissions.
- Touchscreen gestures on KWin compared with hyprgrass.
- Writing KDE config: plasma-manager (extra flake input) vs generated files.
- Plasma panel: floating, gap and corner settings reachable from config.
- Own animation curves (Hyprland beziers) do not carry over.
- Accessibility (screen reader) stays off until its opt-in is built.

## History: niri plan of 2026-11-20 (superseded)

### Verified target facts

- **niri**: v26.04 released 25 Apr 2026 (Phoronix-covered), releases
  25.01→26.04 + hotfixes, active triage, ~22k+ stars, LWN article, official
  `programs.niri` module + `pkgs.niri` in nixpkgs 26.05 plus niri-flake.
  Funded by community GitHub Sponsors @YaLTeR — **not** Omacom. Risk: single
  maintainer; mitigations: nixpkgs packaging survives upstream stalls,
  Hyprland config stays in git, KDE session as stable fallback.
- **waybar stack**: waybar, fuzzel, swaync, mako, swaylock, wvkbd, cliphist,
  matugen, avizo, wlogout, polkit-gnome — all small packages in the nixpkgs
  binary cache. No patches → zero local compilation (waybar ≈1–2 min even
  built from source). Current Noctalia pin carries 24 patches
  (`pkgs/noctalia/`) + 8 on the greeter (`pkgs/noctalia-greeter/`), so any
  drift rebuilds a C++/GLES shell from source.

### Repo audit (Hyprland/Noctalia surface)

| Area | Files | Size | Fate |
|---|---|---|---|
| System session | `system/wm/hyprland/default.nix` | 200 ln | → `programs.niri`; keep udev/tablet wiring |
| User WM config | `user/wm/hyprland/` (binds, settings, rules, touch, lua, permissions, plugins, env, hyprlock) | ~1.9k ln | → niri KDL; hyprlock→swaylock; hyprgrass→niri gestures |
| Shell | `user/wm/shells/noctalia/` + `pkgs/noctalia/` (24 patches) + flake inputs | ~1.3k ln | → `user/wm/shells/waybar/` stack; drop pins/patches |
| Greeter | `pkgs/noctalia-greeter/` (8 patches), wiring in `system/wm/common/wayland.nix` | — | greetd stays; greeter app → tuigreet (or SDDM); look sync → matugen |
| Overlays/patches | `pkgs/overlays/{hyprland,aquamarine,xdg-desktop-portal-hyprland}.nix` + 4 patches | ~150 ln | drop; portal → xdg-desktop-portal-wlr (niri) / -kde (KDE) |
| gjallarctl | `cmd/gjallarctl` hyprland-rotate, hyprland-render-gpu, socket2 watch | ~400 ln Go | → `niri msg`; `pkgs/monique` already auto-detects niri (`NIRI_SOCKET`) |
| Shared | `system/wm/common/wayland.nix` (minus greeter parts), theming, `apps/*` | — | survive; matugen replaces Noctalia palette sync |

### Feature parity (Noctalia → waybar stack)

| Noctalia feature | Replacement | Effort | Works on KDE |
|---|---|---|---|
| Bar | waybar (layer-shell + CSS) | 1–2 d | yes (KWin layer-shell) |
| Launcher | fuzzel / wofi | <1 d | yes |
| Control center | swaync (closest) or nwg-panel; else pavucontrol/blueman/nm-applet/brightnessctl | 2–3 d | partial on KWin; System Settings on KDE |
| Notifications | mako / dunst (swaync bundles) | <1 d | layer-shell OK; KDE has own |
| Lock screen | swaylock (ext-session-lock) | <1 d | mostly; kscreenlocker native on KDE |
| Login greeter | greetd + tuigreet (or SDDM) | 1–2 d | yes |
| OSD vol/brightness | avizo / wob / waybar custom module | <1 d | yes |
| Clipboard history | cliphist + fuzzel | <1 d | yes |
| OSK (gjallar/osk plugin) | wvkbd — already used by the greeter (`gjallar-greeter-osk`) | 1 d | yes |
| Power menu | wlogout | <1 d | yes |
| Polkit | polkit-gnome | <1 d | yes (KDE agent on KDE) |
| Wallpaper + palette sync | waypaper/swaybg + matugen → waybar CSS/GTK/terminal | 1–2 d | yes |
| Tray | waybar `tray` | <1 d | yes; avoid double tray with Plasma |
| Media | waybar `mpris` + playerctl | <1 d | yes |
| quick-keys / disk-protection plugins | waybar custom modules / systemd+notify | 1–2 d | yes |
| **Workspace overview w/ live previews** | **LOST** | — | no (Noctalia-on-KDE lacks it too) |
| **Settings GUI** | **LOST** — text config | — | no |
| **Plugin API** | **LOST** — 3 gjallar plugins ported | — | no |
| **Rich lock/greeter UI** | **degraded** — swaylock/tuigreet simpler | 1 d | no |

KDE session adjustments (later phase): portal per session (never mix -kde
with -wlr/-hyprland, fdo#969), tray single source, notifications/lock native
vs mako/swaylock, blur via KWin, HyprKwin (imports hyprland.conf, has
hyprkwinctl) or Krohnkite for the Hyprland-like tiling feel. One shared
`user/wm/shells/waybar/` config for both sessions; per-compositor autostart
via detection like `pkgs/monique/src/monique/detect.py` + `XDG_CURRENT_DESKTOP`.

### Ideas list (backlog)

1. Compositor-agnostic shell layer: one waybar config, compositor detected at
   session start; per-compositor override dir merged on top.
2. Keep the current Noctalia look as the CSS target so the feel does not
   change (same bar layout, rounded corners, palette).
3. matugen everywhere (waybar CSS, GTK, terminal, SDDM/tuigreet) replaces
   Noctalia's palette sync incl. the `hyprland-colors.txt` template hack
   (unneeded on niri/KDE).
4. OSK: wvkbd for desktop + greeter (already proven in greeter wiring).
5. gjallarctl: keep the Go tool, swap IPC backend per compositor (niri msg /
   KWin D-Bus), same CLI (`hyprland-rotate` → generic `rotate`).
6. monique already multi-backend — use it as the detection library instead
   of new code.
7. Tablet rotation: niri `output transform` via `niri msg`; port the
   ODDC/scale logic from `applyHyprlandRotation`.
8. Render-GPU selection: niri/DRM env instead of hyprctl socket read.
9. KDE tiling: HyprKwin first (hyprland.conf import = least re-learning),
   Krohnkite as fallback.
10. Rollback: Hyprland + Noctalia stay in git behind a disabled option until
    the KDE session ships.
11. Drop pinned noctalia/noctalia-greeter inputs and all 32 patches; rely on
    the nixpkgs binary cache; port anything still needed upstream.
12. Security wiring (greeter uaccess, plymouth hand-over, greeter OSK
    auto-show, measured-boot prompt ordering) re-created for tuigreet/swaylock
    in `system/wm/common/wayland.nix`.

### Ticket list ("plane tickets")

- T0: installer + USB maker: session choice niri/KDE (question drop) — both day-1
- T1: add niri + KDE sessions via `programs.niri` / plasma module (login-switchable)
- T2: `user/wm/shells/waybar/` shared shell stack (waybar+fuzzel+swaync/mako+swaylock+wvkbd+cliphist+matugen+wlogout+polkit-gnome)
- T3: niri KDL port of `user/wm/hyprland/` (binds/rules/settings/touch)
- T4: gjallarctl IPC port → `niri msg` (rotate, render-gpu, reload watch)
- T5: plugin ports: osk→wvkbd, quick-keys→waybar module, disk-protection→systemd
- T6: greeter swap (greetd+tuigreet or SDDM) + swaylock + matugen look sync; re-do security wiring
- T7: theming via matugen; remove hyprland-colors template on niri
- T8: portal per session (wlr on niri, kde on KDE)
- T9: KDE session with shared waybar config + HyprKwin (later phase)
- T10: cleanup — remove `pkgs/overlays/hyprland*`, 4 hyprland patches, `user/wm/hyprland/`, noctalia inputs + 32 patches (after one stable release)
- T11: tests — `cmd/gjallarctl/main_test.go` equivalents for niri IPC; monique tests already cover niri backend
- T12: docs — this page + README/ROADMAP notes

### Questionnaire — answers recorded 2026-11-20

1. Session strategy: **both day-1**, choosable in the gjallar installer and
   the USB maker (new ticket T0 below).
2. KDE feel: native Plasma preferred *if* it can match the current feel,
   otherwise waybar+HyprKwin (option 1). Recommendation: **waybar + HyprKwin
   on KDE** — a native Plasma panel cannot reproduce the floating bar look,
   so option 1 is the only way to keep the same feel on both. When picking
   replacement projects, check CoC/political record first (this page's §Why).
3. Look: current Noctalia look, **narrower gaps** (more screen real estate),
   keep the floating look; auto palette (matugen) **secondary / nice-to-have**
   but still needed for terminal, dolphin etc.
4. Rollback: **none** — Hyprland/Noctalia get deleted once the niri session
   works (git history only). T10 loses its "disabled option" variant.

All answered 2026-11-20:

5. Day-1 blocking features: **all of them** — tablet rotation, render-GPU,
   greeter OSK/plymouth wiring, disk-protection notify, desktop OSK. Full
   port, no feature phasing.
6. gjallarctl CLI: **`gjallarctl` stays the single control point** (it has
   ~30 subcommands: usb, installer, check, oddc, hyprland-rotate, …).
   Subcommands `hyprland-rotate`/`hyprland-render-gpu` become `rotate` and
   `render-gpu`, with no aliases (Jade 2026-11-22, no-stale-code rule); zsh
   completion and docs move with them.
   Timing (Jade 2026-11-22): the migration starts after the 2TB move.
7. Greeter: **recommend greetd + tuigreet** — all the security wiring in
   `system/wm/common/wayland.nix` (plymouth hand-over, greeter uaccess,
   OSK auto-show, measured-boot prompt ordering) is greetd-based; SDDM
   would redo PAM/theme/plymouth and adds attack surface. SDDM with a
   corners/sugar-candy theme is an option *later* if a fancier login is
   wanted. Decision: greetd + tuigreet.
8. monique: keep as the shared compositor-detection lib (already speaks
   niri/sway/hyprland IPC).
9. Noctalia patches: drop all 32; re-implement only what visibly breaks.
10. Public stance: **public in docs, not README** — update the
    hyprland/noctalia mentions (e.g. `docs/maintainers.md` patched-packages
    list) and state plainly that those projects no longer fit our ethical
    line after the documented facts above.

## Sources

- Incident/ban: drewdevault.com/blog/Hyprland-toxicity,
  /blog/FDO-conduct-enforcement, HN 39976713, blog.vaxry.net/articles/2024-fdo-and-redhat,
  hexbear.net/post/2255089
- Omarchy money: omarchy.org/news 2026/08 (Hyprland exclusive; Quickshell+Mise
  premier), itsfoss.com, linuxiac.com, techtimes.com, blog.openreplay.com,
  omacom/omarchy PR #5856
- Refusals/boycotts: stopomarchy.org, HN 49509876 (AUR), theverge.com/tech/988536
  (1Password), theregister.com + sgued.fr (Framework), brennan.day
- niri: github.com/niri-wm/niri releases, phoronix.com/news/Niri-26.04-Released,
  lwn.net/Articles/1025866, github.com/sponsors/YaLTeR, nixpkgs
  programs/wayland/niri.nix release-26.05, sodiboo/niri-flake
- Noctalia v5 ≠ Quickshell: noctalia.dev/blog/announcing-noctalia-v5, deepwiki
  noctalia-docs (v4 QML/Quickshell legacy, v5 C++/Luau), byteiota/linuxcompatible
  v5.0.1, CachyOS/distribution#551 (v4 qs dep)
- Greeter: noctalia-dev/noctalia-greeter README (greetd + bundled wlroots),
  docs.noctalia.dev/greeter
- KDE: blogs.kde.org/2025/11/26 (Wayland-only 6.8), discuss.kde.org CoC,
  DGBooth/HyprKwin, krohnkite.com, flatpak/xdg-desktop-portal#969
- Dual niri+KDE shared waybar/fuzzel/matugen precedent: github.com/notflask/.config PR #7
