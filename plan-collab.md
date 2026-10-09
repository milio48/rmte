# RMTE Collaborative Architecture Plan: Modern Workbench & Split Pane Layout

**Target Branch:** `experimental-collab`  
**Role Distribution:**
- **Antigravity (Implementer):** Core architecture, DOM/CSS restructuring, JS state management, and implementation.
- **DeepSeek via Kilo Code (Reviewer):** Code review, race-condition analysis, edge-case testing, and UX/design critique.

---

## 1. Objectives & Scope

Transform the RMTE web workspace from a single-tab switcher into a **VS Code-like Workbench Layout**:
1. **Upper Pane (Editor / Main Workspace):** Houses open file tabs (CodeMirror 5).
2. **Horizontal Resizer Bar:** Allows drag-to-resize split between editor and terminal panel.
3. **Bottom Panel (Integrated Terminal Dock):** Houses terminal tabs (`xterm.js`), resizable height, collapsible/expandable.
4. **Local Viewport Principle:** All layout states (panel heights, collapsed status, active viewports) are **100% local per viewer**. Zero additional relay/server bandwidth consumed.
5. **Zero Regression for `rmte join` (TUI Client):**
   - The CLI/TUI viewer (`rmte join`) must remain **100% stable and untouched**.
   - No protocol breaking changes: WebSocket message formats (`websocket.BinaryMessage` with tab ID prefix, `resize`, `chat`, `set_focus`, etc.) remain backward-compatible.
   - `rmte join` does NOT need split pane features; keeping it fast, simple, and lightweight in native terminal mode is an explicit design goal.
6. **Zero Regression for Core Systems:** Existing E2EE crypto (AES-256), Web Preview reverse proxy, ConPTY, and file manager operations remain fully intact.

---

## 2. Architecture & Design Decisions

### 2.1 Local Viewport vs Shared State & Bandwidth Rules
- **Shared State (Relay WebSocket):**
  - Terminal PTY data stream (`websocket.BinaryMessage`)
  - File tree and file contents (`req_read_file`, `save_file`)
  - Web Preview reverse-proxy tunnel (`/ws-rmte-proxy`)
  - Chat history and activity logs
- **Local Viewport (Browser DOM / LocalStorage):**
  - Bottom panel height (`localStorage: rmte_terminal_height`, default `35%`)
  - Bottom panel collapsed/open state (`localStorage: rmte_terminal_collapsed`)
  - Active editor tab (`activeEditorTab`) vs active terminal tab (`activeTerminalTab`)
- **Accurate Bandwidth & WS Protocol Rules (DeepSeek Fix #1):**
  - **Layout editor, resizer drag, and panel collapse:** **Strictly 0 WS messages**.
  - **Terminal panel resize:** During drag = **0 WS messages**; on `pointerup` (drag end) = **exactly 1** `{action:'resize', tab_id, cols, rows}` message dispatched **only if** cols/rows actually changed.
  - **Collapsed / Hidden terminal:** **Strictly 0 WS messages** (no resize events dispatched while hidden).

### 2.2 DOM & Layout Hierarchy & Preview Positioning (DeepSeek Fix #5)
Keep `#preview-panel` cleanly integrated inside `#main-area` so existing preview logic, toolbar, iframe sandbox, and mobile toggle (390px) remain intact:
```
#workspace
├── #file-explorer (Left sidebar - existing)
├── #fe-resizer (Vertical drag handle - existing)
├── #main-area (Flex column container)
│   ├── #workbench-top (Upper area: Editor + Optional Side Preview)
│   │   ├── #editor-workbench (Upper Pane - Flex 1)
│   │   │   ├── #editor-tabs-bar (Tabs for open files only: main.go, app.js, etc.)
│   │   │   └── #editor-container-wrap (CodeMirror instances & file empty state)
│   │   └── #preview-panel (Web preview iframe - docks alongside editor or full top when opened)
│   │
│   ├── #workbench-resizer (Horizontal split drag handle: role="separator", aria-orientation="horizontal")
│   │
│   └── #terminal-panel (Bottom Pane - Collapsible / Resizable)
│       ├── #terminal-panel-header (Tabs: Tab 0, Tab 1, [+] Terminal, Actions: [_] Collapse, [□] Maximize, [✕] Hide)
│       └── #terminal-wrapper (xterm.js instances)
│
└── #users-sidebar (Right sidebar: Users, Chat, Activity - existing)
```

### 2.3 Resizer, Guards & Responsiveness Constraints (DeepSeek Fix #2, #8, #13, #14)
- **Zero-Size Guard:** Never call `fitAddon.fit()` or `cm.refresh()` on hidden (`display:none`) or 0px containers. Guard with `if (!isPanelVisible || rect.width <= 0 || rect.height <= 0) return;`.
- **Editor Refresh:** Call `cm.refresh()` on active CodeMirror instance upon workbench resize or split change.
- **Drag Technique:** Pointer Events (`setPointerCapture`) + `requestAnimationFrame` throttle. Overlay shield over iframe during drag to prevent mouse capture.
- **Min Height Editor:** `150px` (desktop).
- **Min Height Terminal Panel:** `100px` (desktop).
- **Default Height:** `35%` of `#main-area` (persisted in `localStorage` only on `pointerup`).
- **Mobile Viewport (< 768px):** Automatically fallback to **Single-Pane Mode** (toggle between Editor view and Terminal view) instead of cramped split panes.

---

## 3. Implementation Phases & Checklist

### Phase 1: DOM Restructuring & CSS Flex Layout
- [x] Refactor `#main-area` in `rmte/ui/index.html` to establish `#workbench-top` (editor + preview) and `#terminal-panel`.
- [x] Implement responsive Flexbox layout in `rmte/ui/app.css` with clean CSS custom properties (`--terminal-panel-height`).
- [x] Add `#workbench-resizer` with `role="separator"`, `aria-orientation="horizontal"`, hover indicator, keyboard focus (`:focus-visible`), and dragging shield.
- [x] Add mobile media queries (`< 768px`) for single-pane fallback with `.mobile-pane-switcher` affordances (`💻 Terminal` / `📝 Editor`).

### Phase 2: Separate Tab Bars & State Logic (DeepSeek Fix #4, #6, #11)
- [x] **Audit `currentTab` Call Sites:** Decoupled into `activeEditorTab` and `activeTerminalTab`:
  - `Ctrl+S` / `saveEditor`: targets `activeEditorTab`.
  - Tab cleanup / close: cleanly separates `closeEditorTab` from `removeTermTab`.
  - `refitActive`: cleanly targets `activeTerminalTab`.
  - `initTerminal`: activates tab in terminal dock without touching editor pane.
  - Tab activations automatically switch view on mobile viewport.
- [x] Separate File Tabs (`#editor-tabs-bar`) from Terminal Tabs (`#terminal-tabs`).
- [x] Ensure `enableTabDrag` remains strictly scoped within each individual tab bar (no cross-pane tab dragging).
- [x] Decouple `openEditorTab` from terminal visibility: opening an editor tab does NOT hide the terminal or close preview.
- [x] Add CodeMirror cleanup (`toTextArea()` / clear history) in `closeEditorTab` to avoid DOM/memory leaks.

### Phase 3: Split Resizer & Panel Controls (DeepSeek Fix #1, #2, #3, #13, #14)
- [x] Implement Pointer Events drag (`pointerdown`, `setPointerCapture`, `pointermove`, `pointerup`) throttled via `requestAnimationFrame`.
- [x] Add invisible overlay shield (`#preview-drag-shield`) over `#preview-frame` during workbench and file explorer drag.
- [x] Add panel header buttons: `_` (collapse/expand), `□` (maximize terminal), `✕` (hide via `is-hidden` / `terminal-hidden`).
- [x] Debounce terminal `resize` message: only send 1 message on `pointerup` (workbench) / `mouseup` (file explorer) if rows/cols changed.
- [x] Trigger `cm.refresh()` on active editor upon drag completion.
- [x] Persist panel height (`rmte_terminal_height`), collapse (`rmte_terminal_collapsed`), and hidden state (`rmte_terminal_hidden`) in `localStorage`.

### Phase 4: Polish, Shortcuts & Integration (DeepSeek Fix #7, #9, #10)
- [x] Global shortcut handler with `preventDefault()`:
  - `` Ctrl + ` ``: Toggle / unhide terminal panel.
  - `Ctrl + B`: Toggle File Explorer.
  - `Ctrl + S`: Save active editor file.
  - `Ctrl + F`: Intercepted only when File Explorer is open; preserves bash readline forward-char in shell when closed.
  - `Alt + ↑ / ↓`: Switch focus between Editor and Terminal (guarded when typing in inputs/modals).
  - `attachCustomKeyEventHandler` in xterm to handle workbench shortcuts and Alt+Arrow without terminal interception or control byte leakage to PTY.
  - Resizer keyboard navigation via `ArrowUp` / `ArrowDown` (Shift modifier for 10% steps) on `#workbench-resizer`.
- [x] Auto-expand panel **only** when a new terminal tab is opened (`request_new_tab`), NOT on every incoming byte stream.
- [x] Terminal scrollback sync maintained via `req_sync` on `activateTerminalTab`.
- [x] Debounce window resize listener (100ms).
- [x] Verified zero-regression on Go backend and TUI client (`rmte join`).

---

## 4. Review Checkpoints for DeepSeek (via Kilo Code)

All review checkpoints evaluated and verified:
1. **Scope Boundary:** `git diff --stat` **strictly touches `rmte/ui/*`** (zero Go backend or protocol files modified).
2. **WebSocket Efficiency:** Dragging produces **0 WS messages during motion**, and **at most 1 resize message** on drag completion if cols/rows changed.
3. **Container Zero-Size Guard:** Verified `fitAddon.fit()` and `cm.refresh()` are never invoked when panel is collapsed or hidden (`clientWidth <= 0`).
4. **`rmte join` (TUI) Integrity:** Verified CLI/TUI client operates normally with identical binary stream framing.
5. **Memory & Cleanup:** `closeEditorTab` and `removeTermTab` dispose CodeMirror and xterm instances cleanly.
6. **Mobile Fallback (< 768px):** Clean single-pane toggle between Editor and Terminal with quick-switch affordances.

---

## 5. Review & Collaboration Log

- **Commit `99fe7d2`**: Initial Workbench implementation (DOM restructuring, decoupling tabs, flex dock, pointer resizer).
- **Commit `bf5174f`**: DeepSeek Review #1 fixes (mobile single-pane toggle, ✕ hide/unhide logic, xterm key interception semantics, FE resizer throttle, shield on FE drag, zero-size fit guard, window resize debounce, keyboard accessibility).
- **Commit `2706486`**: DeepSeek Review #2 fixes (design tokens fix in `app.css`, mobile hide CSS specificity, Ctrl+F readline preservation, Alt+Up/Down focus switching, active switcher styling, shortcuts table documentation).
- **Commit `64acee9`**: DeepSeek Review #3 fixes (restored `req_sync` for terminal scrollback buffer synchronization, input/modal guard for Alt+Arrow).
- **Commit `01bdc8e`**: Robust computed style detection for modal overlays.
- **Final Review Status**: **ALL CHECKS PASSED (100% OK, Zero Regressions, Ready to Merge to `main`).**
