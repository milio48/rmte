# RMTE Collaborative Architecture Plan: Mobile-First Responsive Web UI

**Target Branch:** `experimental-collab`  
**Role Distribution:**
- **Antigravity (Implementer):** Mobile UX/DOM architecture, responsive CSS tokens, virtual keyboard accessory bar, drawer overlays, and implementation.
- **DeepSeek via Kilo Code (Reviewer):** Code review, touch-interaction audit, virtual keyboard edge-case analysis, and responsiveness verification.

---

## 1. Objectives & Scope

Transform RMTE's single-file web interface (`rmte/ui/*`) to be genuinely **usable, comfortable, and productive on smartphones and tablets** without creating a separate mobile website:

1. **Single Responsive Architecture:**
   - **Strictly single-file web app:** No `mobile.html` or `/m` redirects. One codebase, one URL, one share link (`#pass=...`) across all form factors.
   - Seamless adaptation: Switching orientation (portrait ↔ landscape) or resizing desktop windows transitions smoothly without WebSocket reconnections or state loss.
   - Reuse existing mobile workbench switcher (`setMobileWorkbenchPane`, `.mobile-view-editor`, `.mobile-view-terminal`) established in `plan-collab.md`.
2. **Terminal Mobile Productivity (Termux-Style Touch Controls):**
   - Provide an on-screen **Virtual Key Row** (`#mobile-terminal-keys`) above the virtual keyboard (`ESC`, `TAB`, `CTRL`, `ALT`, `▲`, `▼`, `◀`, `▶`, `^C`).
   - Sticky modifier keys (`CTRL` and `ALT`) intercept native mobile keyboard inputs (`term.onData`) so users can type shortcuts (`Ctrl+C`, `Ctrl+D`, `Ctrl+Z`, `nano`/`vim` commands) with automatic reset and double-tap lock.
3. **Slide-Over Drawer Overlays (Full-Width / Backdrop):**
   - File Explorer (`#file-explorer`) and Sidebar (`#users-sidebar`) must **NOT** squish the terminal/editor side-by-side on mobile screens (`<= 768px`).
   - Transition from conflicting inline `display:flex/none` to a class-based overlay (`.drawer-open` / `.is-open`) with `position: fixed`, touch backdrop, and explicit `✕` close buttons. `#fe-resizer` hidden on mobile.
4. **Compact Topbar & Mobile Overflow Menu (`⋮`):**
   - Prevent 11-button crowding on `< 400px` screens.
   - Keep primary actions visible (`Logo`, `📁 Files`, `💻 Term`, `🌐 Web`, `💬 Chat`), and route secondary actions (`👥 Collab`, `📜 Activity`, `🔗 Share`, `❓ Help`, `🐙 GitHub`, `⏻ Disconnect`) to an adaptive overflow dropdown menu with aggregated badge indicator.
5. **Dynamic Viewport Height (`100dvh` & Mobile Keyboard Safety):**
   - Fix mobile browser address bar clipping and virtual keyboard overlap using `100dvh` on `body`, `#terminal-container`, and `#setup`. Replace `width: 100vw` with `100%` to prevent horizontal safe-area scrolling.
   - Viewport meta: remove `user-scalable=no` / `maximum-scale=1.0` for a11y, preserve `viewport-fit=cover, interactive-widget=resizes-content`.
6. **Zero Regression for Desktop & Backend:**
   - Desktop layout (`> 768px`) remains 100% identical and unaffected.
   - CLI/TUI client (`rmte join`), WebSocket protocol, and Go backend remain **100% untouched**.

---

## 2. Architecture & Design Decisions

### 2.1 Viewport & Keyboard Resizing Strategy
- **Meta Tag:** `<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover, interactive-widget=resizes-content">`
- **Dynamic Viewport Height & Width:**
  - `body`: `min-height: 100dvh; width: 100%; overflow: hidden;`
  - `#terminal-container`: `height: 100dvh; width: 100%;`
  - `#setup`, `.setup-split`: replace `100vw`/`100vh` with `width: 100%; min-height: 100dvh;`
- **iOS Virtual Viewport Handling:** Listen to `window.visualViewport` resize/scroll events (debounced) to adjust terminal height dynamically so input lines and the key accessory row remain anchored right above the iOS on-screen keyboard.

### 2.2 Component Hierarchy & Mobile States

```
Desktop (> 768px)                       Mobile (<= 768px)
┌─────────────────────────────────┐     ┌─────────────────────────────────┐
│ Topbar (All 11 buttons visible) │     │ Topbar (Compact + [⋮] Overflow) │
├──────┬───────────────────┬──────┤     ├─────────────────────────────────┤
│ File │ Editor Workbench  │Users │     │ Active Single-Pane Viewport     │
│ Tree │ (CodeMirror)      │Side  │     │ • Full-screen Terminal OR       │
│      ├───────────────────┤bar   │     │ • Full-screen Editor            │
│      │ Terminal Dock     │      │     ├─────────────────────────────────┤
│      │ (xterm.js)        │      │     │ Virtual Touch Key Row (Termux)  │
│      │                   │      │     │ [ESC][TAB][CTRL][ALT][▲][▼][^C] │
└──────┴───────────────────┴──────┘     └─────────────────────────────────┘
                                        [#file-explorer & #users-sidebar
                                         open as slide-over overlay drawers]
```

### 2.3 Virtual Key Row & Sticky Modifier Architecture

- **DOM Container:** `#mobile-terminal-keys` docked inside `#terminal-panel`, positioned directly under `#terminal-wrapper` (visible only on mobile `<= 768px`).
- **Binary Sending Pipeline:**
  - Send bytes via existing `sendBin(tabId, plain: Uint8Array)` where `tabId = parseInt(activeTerminalTab.replace('term-', ''), 10) || 0`.
- **Modifier State Machine:**
  - `stickyCtrl`: `0` = off, `1` = once (next key), `2` = locked (double-tap).
  - `stickyAlt`: `0` = off, `1` = once (next key), `2` = locked (double-tap).
  - Reset modifiers on `Escape`, terminal blur, or when tapping the active modifier button again.
- **Interception in `term.onData(data)`:**
  ```js
  // When native keyboard sends input:
  if (stickyCtrl > 0) {
      if (data.length === 1) {
          const code = data.charCodeAt(0);
          if ((code >= 65 && code <= 90) || (code >= 97 && code <= 122) || code === 64 || (code >= 91 && code <= 95)) {
              data = String.fromCharCode(code & 0x1f);
          }
      }
      if (stickyCtrl === 1) setStickyCtrl(0);
  }
  if (stickyAlt > 0) {
      data = '\x1b' + data;
      if (stickyAlt === 1) setStickyAlt(0);
  }
  sendBin(activeTabNum, new TextEncoder().encode(data));
  ```
- **Direct Escape Sequences & DECCKM (Cursor Keys Mode):**
  - `ESC` -> `\x1b`
  - `TAB` -> `\x09`
  - `^C`  -> `\x03` (SIGINT)
  - Arrow Keys:
    - Check active terminal instance: `const isAppMode = terminals[tid]?.term.modes?.applicationCursorKeysMode;`
    - `▲`: `isAppMode ? '\x1bOA' : '\x1b[A'`
    - `▼`: `isAppMode ? '\x1bOB' : '\x1b[B'`
    - `◀`: `isAppMode ? '\x1bOD' : '\x1b[D'`
    - `▶`: `isAppMode ? '\x1bOC' : '\x1b[C'`
- **Key Row Collapsible Toggle:** Chevron button `⌨` to collapse/uncollapse the key row. When toggled, invoke `refitActive()` (1 debounced call, 0 WS message if cols/rows unchanged).

### 2.4 Slide-Over Drawers (#file-explorer & #users-sidebar)
- **Class-based Overlay:**
  - Replace conflicting inline `display` changes on mobile with `.drawer-open`:
    - Desktop: normal flex side-pane layout.
    - Mobile (`<= 768px`):
      `#file-explorer, #users-sidebar { position: fixed; top: 42px; left: 0; bottom: 0; width: 85vw; max-width: 360px; z-index: 100; transform: translateX(-100%); transition: transform var(--dur-normal) var(--ease-out); }`
      `#users-sidebar { left: auto; right: 0; transform: translateX(100%); }`
      `.drawer-open { transform: translateX(0) !important; }`
  - Backdrop: `#drawer-backdrop` (`position: fixed; inset: 42px 0 0 0; background: rgba(0,0,0,0.5); z-index: 90;`).
  - `#fe-resizer`: `display: none !important;` on `<= 768px`.
  - PTY safety: opening/closing overlay drawers does not resize `#main-area` and does not trigger PTY resize spam.

### 2.5 Compact Topbar & Overflow Menu (`⋮`)
- **Structure:**
  - Primary buttons visible on mobile: `Logo`, `📁 Files`, `💻 Term`, `🌐 Web`, `💬 Chat` (with badge).
  - Secondary buttons moved to adaptive dropdown `#topbar-overflow-menu`:
    - `👥 Collaborators` (with count badge)
    - `📜 Activity Log` (with unread badge)
    - `🔗 Share Session`
    - `❓ Help & Shortcuts`
    - `🐙 GitHub`
    - `⏻ Disconnect`
  - Overflow button `#topbar-overflow-btn` (`⋮`) displays an aggregated badge if any hidden item has an unread notification.

### 2.6 Dynamic CodeMirror `lineWrapping`
- Set `lineWrapping: window.innerWidth <= 768` on editor initialization.
- On `window.matchMedia('(max-width: 768px)')` change or orientation change:
  - Loop open editors: `et.cm.setOption('lineWrapping', isMobile); et.cm.refresh();`

---

## 3. Implementation Phases & Checklist

### Phase 1: Viewport, dvh & Accessibility Foundation
- [ ] Update `viewport` meta in `rmte/ui/index.html` to remove zoom-disabling tags: `width=device-width, initial-scale=1, viewport-fit=cover, interactive-widget=resizes-content`.
- [ ] Replace `100vh` and `100vw` with `100dvh` and `100%` across `body`, `#terminal-container`, `#setup`, and `.setup-split` in `rmte/ui/app.css`.
- [ ] Ensure all buttons have minimum touch target dimensions (`min-height: 38px`, `min-width: 38px`).

### Phase 2: Slide-Over Drawers for #file-explorer & #users-sidebar
- [ ] Add `#drawer-backdrop` in `rmte/ui/index.html`.
- [ ] Add drawer header close buttons (`✕`) in File Explorer and Sidebar.
- [ ] Update `toggleFileManager` and `toggleSidebar` in `rmte/ui/app.js` to manage class-based drawer state on mobile without breaking desktop inline display.
- [ ] Hide `#fe-resizer` in mobile media query (`app.css`).
- [ ] Add click-backdrop-to-dismiss handler.

### Phase 3: Compact Topbar & Mobile Overflow Menu (`⋮`)
- [ ] Add `#topbar-overflow-btn` and `#topbar-overflow-menu` dropdown in `rmte/ui/index.html`.
- [ ] Style compact topbar for `<= 640px` and style dropdown menu in `rmte/ui/app.css`.
- [ ] Implement toggle and click-outside dismissal in `rmte/ui/app.js`.
- [ ] Aggregate badges on `#topbar-overflow-btn` when secondary items have unread notifications.

### Phase 4: Virtual Key Accessory Bar (Termux-Style)
- [ ] Add `#mobile-terminal-keys` DOM in `rmte/ui/index.html` inside `#terminal-panel` with toggle button.
- [ ] Implement key dispatch via `sendBin(tabId, bytes)` in `rmte/ui/app.js`:
  - Direct keys: `ESC`, `TAB`, `^C`.
  - Arrow keys with DECCKM mode check (`term.modes?.applicationCursorKeysMode`).
  - Sticky modifiers `CTRL` and `ALT` with single-use and double-tap lock.
- [ ] Intercept native keyboard in `term.onData` when modifiers are active.
- [ ] Debounced `visualViewport` listener on iOS to position the accessory bar above the on-screen keyboard.
- [ ] Refit terminal smoothly on virtual key row toggle without resize spam.

### Phase 5: CodeMirror Dynamic `lineWrapping` & Touch Panning
- [ ] Set `lineWrapping: window.innerWidth <= 768` on tab creation.
- [ ] Add dynamic media query listener to toggle `lineWrapping` and trigger `cm.refresh()` across open editor tabs.
- [ ] Add momentum scrolling (`-webkit-overflow-scrolling: touch`) to `#editor-tabs-bar` and `#terminal-tabs`.

---

## 4. Review Checkpoints for DeepSeek (via Kilo Code)

1. **Scope Boundary:** Changes strictly limited to `rmte/ui/{index.html, app.css, app.js}` + `plan-mobile-ui.md`. Zero Go changes, zero TUI changes.
2. **Desktop Non-Regression:** Resizing window to `> 768px` preserves standard desktop workbench, split panels, inline sidebars, and full topbar.
3. **Sticky Modifier Input:** Verify typing letter 'c' with sticky `CTRL` transmits byte `0x03` to PTY and resets modifier; verify typing 'x' with `ALT` transmits `\x1bx`.
4. **ANSI & DECCKM Arrows:** Verify arrow keys emit `\x1bOA` in application mode (vim/nano) and `\x1b[A` in shell.
5. **PTY Stability on Drawers:** Verify opening/closing overlay drawers does not emit spurious PTY resize messages.
6. **Listener & Memory Safety:** Verify `visualViewport` and click-outside listeners are clean and do not leak on reconnect or tab switches.

---

## 5. Review & Collaboration Log
- **b9967ce**: Initial `plan-mobile-ui.md` draft created.
- **Current**: DeepSeek Review #1 integrated (corrected `#users-sidebar` ID, `sendBin` API signature, class-based drawer overlays, `100dvh`/`100%` targets, sticky modifier state machine & `term.onData` interception, DECCKM arrow sequences, a11y viewport tag, dynamic CodeMirror `lineWrapping`).
