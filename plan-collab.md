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
- [ ] Refactor `#main-area` in `rmte/ui/index.html` to establish `#workbench-top` (editor + preview) and `#terminal-panel`.
- [ ] Implement responsive Flexbox layout in `rmte/ui/app.css` with clean CSS custom properties.
- [ ] Add `#workbench-resizer` with `role="separator"`, `aria-orientation="horizontal"`, hover indicator, and dragging shield.
- [ ] Add mobile media queries (`< 768px`) for single-pane fallback.

### Phase 2: Separate Tab Bars & State Logic (DeepSeek Fix #4, #6, #11)
- [ ] **Audit `currentTab` Call Sites:** Refactor `currentTab` into `activeEditorTab` and `activeTerminalTab`:
  - `Ctrl+S` / `saveEditor`: targets `activeEditorTab`.
  - Tab cleanup / close: cleanly separates `closeEditorTab` from `removeTermTab`.
  - `refitActive`: cleanly targets `activeTerminalTab`.
  - `initTerminal`: activates tab in terminal dock without touching editor pane.
- [ ] Separate File Tabs (`#editor-tabs-bar`) from Terminal Tabs (`#terminal-tabs`).
- [ ] Ensure `enableTabDrag` remains strictly scoped within each individual tab bar (no cross-pane tab dragging).
- [ ] Decouple `openEditorTab` from terminal visibility: opening an editor tab does NOT hide the terminal or close preview.
- [ ] Add CodeMirror cleanup (`toTextArea()` / clear history) in `closeEditorTab` to avoid DOM/memory leaks.

### Phase 3: Split Resizer & Panel Controls (DeepSeek Fix #1, #2, #3, #13, #14)
- [ ] Implement Pointer Events drag (`pointerdown`, `setPointerCapture`, `pointermove`, `pointerup`) throttled via `requestAnimationFrame`.
- [ ] Add invisible overlay shield over `#preview-frame` while dragging.
- [ ] Add panel header buttons: `_` (collapse/expand), `□` (maximize terminal), `✕` (hide).
- [ ] Debounce terminal `resize` message: only send 1 message on `pointerup` if rows/cols changed.
- [ ] Trigger `cm.refresh()` on active editor upon drag completion.
- [ ] Persist panel height and collapse state in `localStorage` on `pointerup`.

### Phase 4: Polish, Shortcuts & Integration (DeepSeek Fix #7, #9, #10)
- [ ] Global shortcut handler with `preventDefault()`:
  - `` Ctrl + ` ``: Toggle terminal panel.
  - `Ctrl + B`: Toggle File Explorer.
  - `Ctrl + S`: Save active editor file.
  - `attachCustomKeyEventHandler` in xterm to handle pane navigation without terminal swallowing.
- [ ] Auto-expand panel **only** when a new terminal tab is opened (`request_new_tab`), NOT on every incoming byte stream.
- [ ] Validate persisted `activeEditorTab` / `activeTerminalTab` on session load with fallback to defaults.
- [ ] Manual verification matrix:
  - Terminal output streaming while both panes are visible.
  - Preview iframe open + split resize + mobile toggle.
  - Host reconnecting while split pane is open.
  - Independent layout test across two browser windows.

---

## 4. Review Checkpoints for DeepSeek (via Kilo Code)

When reviewing implementations, please evaluate against these key criteria:
1. **Scope Boundary:** Verify `git diff --stat` **strictly touches `rmte/ui/*`** (no Go backend or protocol files modified).
2. **WebSocket Efficiency:** Verify drag produces **0 WS messages during motion**, and **at most 1 resize message** on `pointerup`.
3. **Container Zero-Size Guard:** Confirm `fitAddon.fit()` and `cm.refresh()` are never invoked when panel is collapsed (`display: none` or 0px).
4. **`rmte join` (TUI) Integrity:** Verify CLI/TUI client operates normally with identical binary stream framing and zero breaking changes.
5. **Memory & Cleanup:** Confirm `closeEditorTab` and `removeTermTab` dispose CodeMirror and xterm instances cleanly.
6. **Mobile Fallback (< 768px):** Confirm layout does not overflow or clip on narrow screens.
