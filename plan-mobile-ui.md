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
2. **Terminal Mobile Productivity (Termux-Style Touch Controls):**
   - Provide an on-screen **Virtual Key Row** above the virtual keyboard (`ESC`, `TAB`, `CTRL`, `ALT`, `▲`, `▼`, `◀`, `▶`, `^C`).
   - Support sticky modifier keys (`CTRL` and `ALT`) so users can execute CLI shortcuts (`Ctrl+C`, `Ctrl+D`, `Ctrl+Z`, `nano`/`vim` commands) from standard mobile touch keyboards.
3. **Slide-Over Drawer Overlays (Full-Width / Backdrop):**
   - File Explorer (`#file-explorer`) and Sidebar (`#sidebar`) must **NOT** squish the terminal/editor side-by-side on mobile screens (`<= 768px`).
   - Instead, they open as full slide-over drawers with touch backdrop and `✕` close button, leaving the terminal/editor 100% full-width when closed.
4. **Compact Topbar & Overflow Menu (`⋮`):**
   - Prevent 11-button crowding on `< 400px` screens.
   - Keep primary actions visible (`Logo`, `📁 Files`, `💻 Term`, `🌐 Web`, `💬 Chat`), and neatly collapse secondary actions (`👥 Collab`, `📜 Activity`, `🔗 Share`, `❓ Help`, `🐙 GitHub`, `⏻ Disconnect`) into an adaptive dropdown menu.
5. **Dynamic Viewport Height (`100dvh` & Mobile Keyboard Safety):**
   - Fix mobile browser address bar clipping and virtual keyboard overlap using `100dvh` and `interactive-widget=resizes-content`.
6. **Zero Regression for Desktop & Backend:**
   - Desktop layout (`> 768px`) remains 100% identical and unaffected.
   - CLI/TUI client (`rmte join`), WebSocket protocol, and Go backend remain **100% untouched**.

---

## 2. Architecture & Design Decisions

### 2.1 Viewport & Keyboard Resizing Strategy
- **Meta Tag:** `<meta name="viewport" content="width=device-width, initial-scale=1.0, maximum-scale=1.0, user-scalable=no, viewport-fit=cover, interactive-widget=resizes-content">`
- **Dynamic Viewport Height:** Replace `#terminal-container { height: 100vh; }` with `height: 100dvh;` (with `100vh` fallback).
- **Virtual Keyboard Detection:** Use `visualViewport` API listener where available to dynamically adjust terminal rows and keep active cursor/input visible above software keyboards.

### 2.2 Component Hierarchy & Layout States

```
Desktop (> 768px)                       Mobile (<= 768px)
┌─────────────────────────────────┐     ┌─────────────────────────────────┐
│ Topbar (All 11 buttons visible) │     │ Topbar (Compact + [⋮] Overflow) │
├──────┬───────────────────┬──────┤     ├─────────────────────────────────┤
│ File │ Editor Workbench  │ Side │     │ Active Single-Pane Viewport     │
│ Tree │ (CodeMirror)      │ bar  │     │ • Full-screen Terminal OR       │
│      ├───────────────────┤      │     │ • Full-screen Editor            │
│      │ Terminal Dock     │      │     ├─────────────────────────────────┤
│      │ (xterm.js)        │      │     │ Virtual Touch Key Row (Termux)  │
│      │                   │      │     │ [ESC][TAB][CTRL][ALT][▲][▼][^C] │
└──────┴───────────────────┴──────┘     └─────────────────────────────────┘
                                        [File Explorer & Sidebar open as
                                         overlay drawers with backdrops]
```

### 2.3 Virtual Key Accessory Bar Architecture
- **DOM Container:** `#mobile-terminal-keys` docked inside `#terminal-panel`, positioned directly below `#terminal-wrapper` (visible only on mobile `<= 768px`).
- **Modifier State:**
  - `activeCtrlModifier`: boolean flag. When tapped, button highlights. Next keypress sends `String.fromCharCode(charCode & 0x1f)` to active terminal instance, then automatically resets modifier (unless double-tapped for lock).
  - `activeAltModifier`: boolean flag. When tapped, sends `\x1b` prefix before next keypress.
- **Direct Escape Sequences:**
  - `ESC` -> `\x1b`
  - `TAB` -> `\t`
  - `▲` -> `\x1b[A`
  - `▼` -> `\x1b[B`
  - `◀` -> `\x1b[D`
  - `▶` -> `\x1b[C`
  - `^C` -> `\x03` (SIGINT)
- **Send Key Helper:** `sendTerminalInput(str)` dispatches to active terminal's backend WebSocket channel using existing `sendData(tabId, str)` pipeline.
- **Collapsible Toggle:** A subtle chevron/pill toggle button to hide/show the virtual keys row if the user wants maximum terminal height.

### 2.4 Mobile Drawers (File Explorer & Sidebar)
- **CSS Rule for `<= 768px`:**
  - `#file-explorer` and `#sidebar`: `position: fixed; top: 42px; left: 0; right: 0; bottom: 0; width: 100% !important; z-index: 100;`
  - Slide-in animation using `transform: translateX(-100%)` (file explorer) and `translateX(100%)` (sidebar).
  - Darkened backdrop shield (`rgba(0,0,0,0.6)`) to dismiss drawer on outside tap.
  - Distinct `✕` Close header button in both drawers.

### 2.5 CodeMirror Mobile Adaptations
- Mobile touch typing is difficult with horizontal scrolling: enable `lineWrapping: true` dynamically when `window.innerWidth <= 768px`.
- Tab bar: `-webkit-overflow-scrolling: touch;` and hide scrollbar for fluid momentum panning.

---

## 3. Implementation Phases & Checklist

### Phase 1: Viewport & Layout Foundation
- [ ] Update `viewport` meta tag in `rmte/ui/index.html` with `interactive-widget=resizes-content` and `viewport-fit=cover`.
- [ ] Adopt `100dvh` for root containers in `rmte/ui/app.css` (`#terminal-container`, `#setup-modal`).
- [ ] Add touch targets minimum sizing (`min-height: 38px`, `min-width: 38px`) to prevent fat-finger issues on touchscreens.

### Phase 2: Compact Topbar & Mobile Overflow Menu (`⋮`)
- [ ] Add `#topbar-overflow-btn` (`⋮`) and `#topbar-overflow-menu` dropdown in `rmte/ui/index.html`.
- [ ] Route secondary actions into overflow menu: Collab (`👥`), Activity (`📜`), Share Link (`🔗`), Shortcuts (`❓`), GitHub (`🐙`), Disconnect (`⏻`).
- [ ] Style topbar and overflow popup in `rmte/ui/app.css` with clean click-outside dismissal in `rmte/ui/app.js`.
- [ ] Sync notification badges onto the `⋮` button if unread items belong to hidden menu entries.

### Phase 3: Slide-Over Drawers & Backdrop
- [ ] Transform `#file-explorer` into a slide-over overlay drawer on `<= 768px` with clean close button and transition.
- [ ] Transform `#sidebar` into a slide-over overlay drawer on `<= 768px` with backdrop.
- [ ] Ensure opening a drawer does NOT resize or crush terminal PTY dimensions.
- [ ] Add backdrop click handlers to close active drawers.

### Phase 4: Virtual Key Accessory Bar for Terminal
- [ ] Add `#mobile-terminal-keys` DOM structure in `rmte/ui/index.html` inside `#terminal-panel`.
- [ ] Implement key handlers in `rmte/ui/app.js`:
  - Direct keys: `ESC`, `TAB`, `^C`, Arrow keys (`▲`, `▼`, `◀`, `▶`).
  - Sticky modifiers: `CTRL`, `ALT` with visual active state.
  - Custom input helper: sends directly to active terminal tab via WebSocket.
- [ ] Add toggle button to hide/show virtual key row on demand (persisted in `localStorage`).
- [ ] Style buttons with dark tactile mobile aesthetic in `rmte/ui/app.css`.

### Phase 5: CodeMirror Touch Optimizations
- [ ] Enable `lineWrapping: true` on mobile viewports for CodeMirror instances.
- [ ] Ensure editor tab bar has momentum scrolling (`-webkit-overflow-scrolling: touch`).

---

## 4. Review Checkpoints for DeepSeek (via Kilo Code)

1. **Scope Boundary:** Changes strictly limited to `rmte/ui/{index.html, app.css, app.js}` + `plan-mobile-ui.md`. Zero Go changes, zero TUI changes.
2. **Desktop Non-Regression:** Resizing browser window to desktop size (`> 768px`) preserves normal desktop workbench, split panels, and topbar exactly as before.
3. **Virtual Keys PTY Transmission:** Verify escape sequences (`\x1b`, `\t`, `\x03`, arrows) match standard ANSI/VT100 specs and dispatch correctly without duplicate echo.
4. **PTY Resizing on Drawer Toggle:** Verify opening/closing overlay drawers does not trigger unwanted PTY resize spam.
5. **Memory & Listener Cleanup:** Verify backdrop tap listeners, visualViewport listeners, and overflow menu events clean up without leaks.

---

## 5. Review & Collaboration Log
*(To be updated after each implementation step and review cycle)*
