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
   - Sticky modifier keys (`CTRL` and `ALT`) integrated into the **single existing `term.onData` handler** so users can type shortcuts (`Ctrl+C`, `Ctrl+D`, `Ctrl+Z`, `nano`/`vim` commands) with automatic reset and double-tap lock.
3. **Slide-Over Drawer Overlays (Full-Width / Backdrop):**
   - File Explorer (`#file-explorer`) and Sidebar (`#users-sidebar`) must **NOT** squish the terminal/editor side-by-side on mobile screens (`<= 768px`).
   - Transition from conflicting inline `display:flex/none` to a class-based overlay (`.drawer-open` / `.is-open`) with `position: fixed`, touch backdrop, and explicit `✕` close buttons. `#fe-resizer` hidden on mobile.
4. **Compact Topbar & Mobile Overflow Menu (`⋮`):**
   - Prevent button crowding on screens `<= 640px`.
   - Keep primary actions visible (`Logo`, `📁 Files`, `💻 Term`, `🌐 Web`, `💬 Chat`), and route secondary actions (`👥 Collab`, `📜 Activity`, `🔗 Share`, `❓ Help`, `🐙 GitHub`, `⏻ Disconnect`) to an adaptive overflow dropdown menu (calling identical controller functions without DOM element cloning or ID duplication) with aggregated badge indicator.
5. **Dynamic Viewport Height (`100dvh` & Mobile Keyboard Safety):**
   - Fix mobile browser address bar clipping and virtual keyboard overlap using `100dvh` on `body`, `#terminal-container`, and `#setup`. Replace `width: 100vw` with `100%` to prevent horizontal safe-area scrolling.
   - Viewport meta: remove `user-scalable=no` / `maximum-scale=1.0` for a11y, preserve `viewport-fit=cover, interactive-widget=resizes-content`.
6. **Zero Regression for Desktop & Backend:**
   - Desktop layout (`> 768px`) remains 100% identical and unaffected (touch-target enlargement strictly scoped to mobile media query, preserving 24×24 desktop buttons).
   - CLI/TUI client (`rmte join`), WebSocket protocol, and Go backend remain **100% untouched**.

---

## 2. Architecture & Design Decisions

### 2.1 Viewport & Keyboard Resizing Strategy
- **Meta Tag:** `<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover, interactive-widget=resizes-content">`
- **Dynamic Viewport Height & Width:**
  - `body`: `min-height: 100dvh; width: 100%; overflow: hidden;`
  - `#terminal-container`: `height: 100dvh; width: 100%;`
  - `#setup`, `.setup-split`: replace `100vw`/`100vh` with `width: 100%; min-height: 100dvh;`
- **Scoped Touch Targets:** Mobile touch-target enlargement (`min-height: 38px`, `min-width: 38px`) must be **strictly scoped** to `@media (max-width: 768px)` or `@media (pointer: coarse)`. Desktop buttons (like `.term-action-btn`, `.tab-close-btn`) remain compact at 24×24px.
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
- **Single Handler Interception in `initTerminal`:**
  - Update the single existing `term.onData` handler in `initTerminal` (`app.js:462`) to bake modifier logic directly into it (never attach duplicate handlers):
  ```js
  // When native keyboard or paste sends input:
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
      if (data.length === 1) {
          data = '\x1b' + data;
      }
      if (stickyAlt === 1) setStickyAlt(0);
  }
  sendBin(tabId, new TextEncoder().encode(data));
  ```
- **Direct Escape Sequences & DECCKM (Cursor Keys Mode):**
  - `ESC` -> `\x1b`
  - `TAB` -> `\x09`
  - `^C`  -> `\x03` (SIGINT)
  - Arrow Keys:
    - Check terminal mode: `const isAppMode = terminals[tabId]?.term.modes?.applicationCursorKeysMode;`
    - Fallback safely to `\x1b[A` if `modes` is unavailable in current xterm bundle.
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
      `#file-explorer, #users-sidebar { position: fixed; top: 42px; left: 0; bottom: 0; width: 85vw; max-width: 360px; z-index: 100; transform: translateX(-100%); transition: transform var(--dur-med) var(--ease-out); }`
      `#users-sidebar { left: auto; right: 0; transform: translateX(100%); }`
      `.drawer-open { transform: translateX(0) !important; }`
  - Backdrop: `#drawer-backdrop` (`position: fixed; inset: 42px 0 0 0; background: rgba(0,0,0,0.5); z-index: 90;`).
  - `#fe-resizer`: `display: none !important;` on `<= 768px`.
  - PTY safety: opening/closing overlay drawers does not resize `#main-area` and does not trigger PTY resize spam.

### 2.5 Compact Topbar & Overflow Menu (`⋮`)
- **Structure (Breakpoint `<= 640px`):**
  - Primary buttons visible on mobile: `Logo`, `📁 Files`, `💻 Term`, `🌐 Web`, `💬 Chat` (with badge).
  - Secondary actions handled via a dedicated dropdown `#topbar-overflow-menu` that invokes the same handler functions:
    - `👥 Collaborators` -> `toggleSidebar('collab')`
    - `📜 Activity Log` -> `toggleSidebar('activity')`
    - `🔗 Share Session` -> `toggleShareModal(true)`
    - `❓ Help & Shortcuts` -> `toggleHelpModal()`
    - `🐙 GitHub` -> opens repository link
    - `⏻ Disconnect` -> `disconnect()`
  - Zero duplicate element IDs; dropdown menu items are styled as clean list rows.
  - Overflow button `#topbar-overflow-btn` (`⋮`) displays an aggregated badge if any hidden item (Collab or Activity) has an unread notification.

### 2.6 Dynamic CodeMirror `lineWrapping`
- Set `lineWrapping: window.innerWidth <= 768` on editor initialization.
- On `window.matchMedia('(max-width: 768px)')` change or orientation change:
  - Loop open editors: `et.cm.setOption('lineWrapping', isMobile); et.cm.refresh();`

---

## 3. Implementation Phases & Checklist

### Phase 1: Viewport, dvh & Accessibility Foundation
- [x] Update `viewport` meta in `rmte/ui/index.html`: `width=device-width, initial-scale=1, viewport-fit=cover, interactive-widget=resizes-content`.
- [x] Replace `100vh` and `100vw` with `100dvh` and `100%` across `body`, `#terminal-container`, `#setup`, and `.setup-split` in `rmte/ui/app.css`.
- [x] Add touch target sizing (`min-height: 38px`, `min-width: 38px`) **strictly scoped** under `@media (max-width: 768px)` or `@media (pointer: coarse)` to protect desktop 24×24 buttons.

### Phase 2: Slide-Over Drawers for #file-explorer & #users-sidebar
- [x] Add `#drawer-backdrop` in `rmte/ui/index.html`.
- [x] Add drawer header close buttons (`✕`) in File Explorer and Sidebar.
- [x] Update `toggleFileManager` and `toggleSidebar` in `rmte/ui/app.js` to manage class-based drawer state on mobile without breaking desktop inline display.
- [x] Use `var(--dur-med)` for drawer transitions.
- [x] Hide `#fe-resizer` in mobile media query (`app.css`).
- [x] Add click-backdrop-to-dismiss handler.

### Phase 3: Compact Topbar & Mobile Overflow Menu (`⋮`)
- [x] Add `#topbar-overflow-btn` and `#topbar-overflow-menu` dropdown in `rmte/ui/index.html`.
- [x] Style compact topbar for `<= 640px` and style dropdown menu in `rmte/ui/app.css`.
- [x] Implement toggle and click-outside dismissal in `rmte/ui/app.js`.
- [x] Aggregate badges on `#topbar-overflow-btn` when secondary items have unread notifications.

### Phase 4: Virtual Key Accessory Bar (Termux-Style)
- [x] Add `#mobile-terminal-keys` DOM in `rmte/ui/index.html` inside `#terminal-panel` with toggle button.
- [x] Implement key dispatch via `sendBin(tabId, bytes)` in `rmte/ui/app.js`:
  - Direct keys: `ESC`, `TAB`, `^C`.
  - Arrow keys with DECCKM mode check (`term.modes?.applicationCursorKeysMode`) and safe fallback.
  - Sticky modifiers `CTRL` and `ALT` with single-use and double-tap lock.
- [x] Intercept native keyboard within the single existing `term.onData` handler in `initTerminal`.
- [x] Debounced `visualViewport` listener on iOS to position the accessory bar above the on-screen keyboard.
- [x] Refit terminal smoothly on virtual key row toggle without resize spam.

### Phase 5: CodeMirror Dynamic `lineWrapping` & Touch Panning
- [x] Set `lineWrapping: window.innerWidth <= 768` on tab creation.
- [x] Add dynamic media query listener to toggle `lineWrapping` and trigger `cm.refresh()` across open editor tabs.
- [x] Add momentum scrolling (`-webkit-overflow-scrolling: touch`) to `#editor-tabs-bar` and `#terminal-tabs`.

---

## 4. Review Checkpoints for DeepSeek (via Kilo Code)

1. **Scope Boundary:** Changes strictly limited to `rmte/ui/{index.html, app.css, app.js}` + `plan-mobile-ui.md`. Zero Go changes, zero TUI changes.
2. **Desktop Non-Regression:** Resizing window to `> 768px` preserves standard desktop workbench, split panels, inline sidebars, 24×24 compact buttons, and full topbar.
3. **Sticky Modifier Input:** Verify typing letter 'c' with sticky `CTRL` transmits byte `0x03` to PTY and resets modifier; verify typing 'x' with `ALT` transmits `\x1bx`.
4. **ANSI & DECCKM Arrows:** Verify arrow keys emit `\x1bOA` in application mode (vim/nano) and `\x1b[A` in shell.
5. **PTY Stability on Drawers:** Verify opening/closing overlay drawers does not emit spurious PTY resize messages.
6. **Listener & Memory Safety:** Verify `visualViewport` and click-outside listeners are clean and do not leak on reconnect or tab switches.

---

## 5. Review & Collaboration Log
- **b9967ce**: Initial `plan-mobile-ui.md` draft created.
- **1995bc2**: DeepSeek Review #1 integrated (corrected `#users-sidebar` ID, `sendBin` API signature, class-based drawer overlays, `100dvh`/`100%` targets, sticky modifier state machine, DECCKM arrow sequences, a11y viewport tag, dynamic CodeMirror `lineWrapping`).
- **Current**: DeepSeek Review #2 integrated (`var(--dur-med)` token fix, touch target 38px strictly scoped to mobile media query, single `term.onData` handler replacement specification, consistent 640px breakpoint, overflow menu ID safety).
