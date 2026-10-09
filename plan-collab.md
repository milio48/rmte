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
5. **Zero Regression:** Existing E2EE encryption, binary protocols, Web Preview, file manager operations, and CLI viewer compatibility must remain fully intact.

---

## 2. Architecture & Design Decisions

### 2.1 Local Viewport vs Shared State
- **Shared State (Relay WebSocket):**
  - Terminal PTY data stream (`websocket.BinaryMessage`)
  - File tree and file contents (`req_read_file`, `save_file`)
  - Web Preview reverse-proxy tunnel (`/ws-rmte-proxy`)
  - Chat history and activity logs
- **Local Viewport (Browser DOM / LocalStorage):**
  - Bottom panel height (`localStorage: rmte_terminal_height`, default `35%`)
  - Bottom panel collapsed/open state (`localStorage: rmte_terminal_collapsed`)
  - Active editor tab vs active terminal tab in each pane

### 2.2 DOM & Layout Hierarchy
```
#workspace
├── #file-explorer (Left sidebar - existing)
├── #fe-resizer (Vertical drag handle - existing)
├── #main-area (Flex column container)
│   ├── #editor-workbench (Upper Pane - Flex 1)
│   │   ├── #editor-tabs-bar (Tabs for open files only: main.go, app.js, etc.)
│   │   └── #editor-container-wrap (CodeMirror instances & file empty state)
│   │
│   ├── #workbench-resizer (Horizontal split drag handle)
│   │
│   └── #terminal-panel (Bottom Pane - Collapsible / Resizable)
│       ├── #terminal-panel-header (Tabs: Tab 0, Tab 1, [+] Terminal, Actions: Minimize, Maximize, Close)
│       └── #terminal-wrapper (xterm.js instances)
│
├── #preview-panel (Web preview iframe - can dock right or overlay)
└── #users-sidebar (Right sidebar: Users, Chat, Activity - existing)
```

### 2.3 Resizer & Responsiveness Constraints
- **Min Height Editor:** `150px` (guarantees readable code).
- **Min Height Terminal Panel:** `100px` (guarantees at least 3-4 visible terminal rows).
- **Default Height:** `35%` of `#main-area`.
- **Keyboard Shortcuts:**
  - `` Ctrl + ` ``: Toggle Terminal Panel (open/close).
  - `Ctrl + B`: Toggle File Explorer sidebar.
  - `Ctrl + S`: Save currently focused editor file.
  - `Alt + Up / Down`: Focus between Editor and Terminal.

---

## 3. Implementation Phases & Checklist

### Phase 1: DOM Restructuring & CSS Flex Layout
- [ ] Refactor `#main-area` in `rmte/ui/index.html` to support `#editor-workbench` and `#terminal-panel`.
- [ ] Implement responsive Flexbox layout in `rmte/ui/app.css` with clean CSS custom properties.
- [ ] Add `#workbench-resizer` with hover effects and dragging cursor styles.

### Phase 2: Separate Tab Bars & State Logic
- [ ] Separate File Tabs from Terminal Tabs in `rmte/ui/app.js`.
  - `#editor-tabs-bar`: Only renders open file tabs (`file:path`). Shows empty state if no file is open.
  - `#terminal-panel-header`: Only renders terminal tabs (`term-0`, `term-1`, `+ Terminal`).
- [ ] Update `switchToTab(id)` to independently track `activeEditorTab` and `activeTerminalTab`.
- [ ] Ensure `refitActive()` properly resizes terminal `xterm.js` instances when panel height changes.

### Phase 3: Split Resizer & Panel Controls
- [ ] Implement smooth mouse-drag resizing with pointer events and iframe overlay shield (preventing mouse traps on preview iframe).
- [ ] Add panel header action buttons:
  - `_` Minimize / Collapse
  - `□` Maximize (fullscreen terminal)
  - `✕` Hide panel
- [ ] Persist user's preferred layout height in `localStorage`.

### Phase 4: Polish & Integration
- [ ] Auto-expand terminal panel when a new terminal tab is opened or CLI commands execute.
- [ ] Auto-open editor pane when a file is clicked in File Explorer.
- [ ] Add `` Ctrl + ` `` keyboard shortcut listener.
- [ ] Verify Web Preview panel compatibility (side-by-side or tabbed).

---

## 4. Review Checkpoints for DeepSeek (via Kilo Code)

When reviewing implementations, please evaluate against these key criteria:
1. **Bandwidth & Performance:** Are there any unintentional WebSocket payloads generated during split pane resize or layout changes? (Must be strictly 0).
2. **Terminal Fitting (xterm FitAddon):** Does `fitAddon.fit()` trigger cleanly on drag end without spamming PTY resize messages?
3. **DOM Leak / Clean Disconnect:** Are editor and terminal DOM elements properly destroyed when tabs or sessions are closed?
4. **Mobile & Small Screen Fallback:** Does the layout gracefully degrade to single-pane or stacked view on viewport width `< 768px`?
5. **Accessibility & Usability:** Are keyboard shortcuts conflict-free across Windows/Linux/macOS browsers?
