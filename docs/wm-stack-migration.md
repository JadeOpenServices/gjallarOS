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
6. **Greeter: greetd + tuigreet** (decision 7 of 2026-11-20 stands). greetd
   starts `startplasma-wayland`.
7. **Theme: Plasma colour schemes** generated from our palettes. The
   greeter takes new colours **at rebuild only**: the live pkexec greeter
   sync is removed (fewer root paths; Jade: "whatever is securer and
   cleaner").
8. **Portal: xdg-desktop-portal-kde only** (never mixed, fdo#969).
9. **No rollback, no migration prompt.** fw13 is the only install (Jade
   2026-11-22); Hyprland/Noctalia are deleted once KDE works on e2e-fw13.
10. **gjallarctl stays the one control tool.** `hyprland-rotate` → `rotate`,
    `hyprland-render-gpu` → `render-gpu`, no aliases.
11. All day-1 features block the switch (2026-11-20 answer 5 stands):
    rotation, render GPU, greeter OSK/Plymouth wiring, disk-protection
    notice, desktop OSK.

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
| Greeter | noctalia-greeter (8 patches), live look sync via pkexec | tuigreet, colours at rebuild |
| Session target | `hyprland-session.target` | `graphical-session.target` |
| gjallarctl rotate / render GPU / reload watch | `hyprctl`, socket2 | KWin output config (kscreen-doctor / D-Bus), `KWIN_DRM_DEVICES` |
| Active window for resource QoS | `user/services/resource-qos.nix` uses `hyprctl activewindow` | KWin D-Bus or KWin script |
| Session diagnostics | `system/tools/default.nix` (hyprctl, `noctalia msg status`) | rewritten for KWin/plasmashell |

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
2. KWin borders, Qt apps and Dolphin follow the scheme natively; GTK apps via
   KDE's GTK sync. A small user service sees the colour change, writes the
   Ghostty theme through the same contrast guard and reloads Ghostty.
3. Greeter: no live root step. tuigreet takes its colours from the system
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
  updates (Nix owns updates), Plasma browser integration, kwallet
  auto-unlock decision (Firefox and Passbolt use their own stores). Each one
  that stays gets a reason in this page.
- **Lock.** kscreenlocker PAM must match today: fingerprint step, USB
  keyboard gate, PIN rules. Test: lock cannot be bypassed by VT switch or by
  killing plasmashell (KWin keeps the lock).
- **Polkit agent.** polkit-kde-agent replaces Noctalia's agent; root menu
  and helpers (Q253) still work without sudo.
- **Greeter.** greetd + tuigreet keeps the uaccess, Plymouth hand-over, OSK
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
   `config.go` `Theme`). Ships alone, so the big switch has fewer moving
   parts.
3. **CP-W2 KDE base session.** Plasma 6 module, greetd → startplasma-wayland,
   portal -kde, polkit-kde-agent, kscreenlocker with our PAM, KDE services
   off, privileged-access mapping. Loophole pass.
4. **CP-W3 Keys and tiling.** keybinds.json → KWin + Krohnkite, window
   rules, touch gestures, Super+F1.
5. **CP-W4 Shell.** Panel layout, control-centre plasmoid, the 7 plugin
   widgets, OSK, notifications/OSD, battery toast dedupe.
6. **CP-W5 Theme.** Palettes → Plasma schemes, colour watcher (Ghostty +
   snapshot), greeter colours at rebuild, Plymouth unchanged.
7. **CP-W6 gjallarctl.** `rotate`, `render-gpu`, reload watch on KWin; zsh
   completion; resource-qos active window; session diagnostics.
8. **CP-W7 Switch.** fw13 rebuilds onto KDE (Jade runs it). Full VM pass
   first.
9. **CP-W8 Delete.** `pkgs/overlays/{hyprland,aquamarine,xdg-desktop-portal-hyprland}.nix`,
   the 4 Hyprland + xdph patches, `user/wm/hyprland/` (incl. hyprgrass and
   wl-copy patches), `user/wm/shells/noctalia/`, `pkgs/noctalia/` (24
   patches), `pkgs/noctalia-greeter/` (8), flake inputs, `themes/noctalia.nix`,
   `tests/nix/noctalia-notification-app-name.nix`, hyprland/noctalia refs
   in themes, apps and docs.
10. **CP-W9 Docs.** Public stance in docs (`docs/maintainers.md`
    patched-packages list), README mentions, wiki.

## Tickets (children of Plane 390)

- W1 (Plane 396) groundwork: session units → graphical-session.target (security)
- W2 (Plane 397) groundwork: internal/noctalia → internal/palette, settingsd + settings app
- W3 (Plane 398) groundwork: keybinds.json neutral + generated Super+F1 list
- W4 (Plane 399) installer defaults: profile, WMs, theme
- W5 (Plane 400) KDE base session + greetd/tuigreet start + portal -kde
- W6 (Plane 401) KDE hardening: privileged access, KDE services off, polkit agent, kscreenlocker PAM
- W7 (Plane 402) keys + Krohnkite + window rules + touch gestures
- W8 (Plane 403) Plasma panel + control-centre plasmoid
- W9 (Plane 404) plugin widgets: disk protection, developer mode, VPN block, time, quick keys, OSK, weather city
- W10 (Plane 405) theme: Plasma schemes, colour watcher, greeter at rebuild
- W11 (Plane 406) gjallarctl rotate / render-gpu / reload watch + zsh + resource-qos + diagnostics
- W12 (Plane 407) switch fw13
- W13 (Plane 408) delete Hyprland/Noctalia
- W14 (Plane 409) docs + public stance
- Later (Plane 410): niri session (logged, not planned)

## Open research (to check before the matching checkpoint)

- Krohnkite: maintainer, CoC, Plasma 6 support, spiral layout behaviour.
- OSK on KWin: Plasma's virtual keyboard vs wvkbd (KWin input-method
  rules); greeter keeps wvkbd.
- KWin privileged-interface restriction: how it is configured, what it
  covers compared with Hyprland's permissions.
- Touchscreen gestures on KWin compared with hyprgrass.
- Writing KDE config: plasma-manager (extra flake input) vs generated files.
- Plasma panel: floating, gap and corner settings reachable from config.

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
