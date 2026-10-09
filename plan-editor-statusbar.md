# RMTE Collaborative Architecture Plan: Context-Aware Status Bar & Editor Settings

**Target Branch:** `experimental-collab`  
**Role Distribution:**
- **Antigravity (Implementer):** Status bar DOM restructuring, CodeMirror event integrations (`cursorActivity`, `lineWrapping`, mode override), responsive status bar styling, and syntax picker popover.
- **DeepSeek via Kilo Code (Reviewer):** Code review, CodeMirror lifecycle & memory safety audit, touch/desktop responsiveness verification, and edge-case analysis.

---

## 1. Objectives & Scope

Replace the redundant `#sb-copy-link` on the bottom `#status-bar` (which duplicates the topbar share button `🔗` and mobile overflow menu `⋮`) with a **VS Code-style Context-Aware Status Bar & Editor Quick-Settings cluster**:

1. **Context-Aware Information Display:**
   - **When a Code Editor Tab is Active:**
     - **Cursor Position:** `Ln X, Col Y` updating in real-time on cursor movement.
     - **Indentation Setting:** `Spaces: 4` (clickable toggle: `Spaces: 2` ↔ `Spaces: 4` ↔ `Tabs: 4`).
     - **Word Wrap Toggle:** `Wrap: On` / `Wrap: Off` (single-click toggle for active editor).
     - **Syntax / Language Mode:** Clickable indicator showing current mode (e.g. `JavaScript`, `Go`, `Python`, `Markdown`, `JSON`, `HTML`, `CSS`, `Shell`, `Plain Text`) with an instant dropdown/popover to override syntax highlighting manually.
   - **When a Terminal Tab is Active:**
     - **Terminal Geometry:** `Cols × Rows` indicator (e.g. `80 × 24` or active dimensions).
   - **When an Image Tab is Active:**
     - **Image Details:** Dimension & byte size (e.g. `1920 × 1080 • 142 KB`).
   - **When Web Preview is Active:**
     - **Target Host/Port:** `localhost:8080` connection status indicator.

2. **Responsive Layout (Desktop & Mobile):**
   - **Desktop (`> 768px`):** Full cluster displayed in bottom right corner with comfortable spacing and hover effects.
   - **Mobile (`<= 768px`):** Compact display prioritizing `Wrap` and `Language`; hide non-essential items or abbreviate gracefully to prevent status bar overflow.

3. **Zero Regression Guarantee:**
   - WebSocket protocol, relay communication, Go backend, and CLI/TUI client (`rmte join`) remain **100% untouched**.
   - No performance penalty or DOM thrashing (debounced cursor events, clean listener teardown on tab close).

---

## 2. Architecture & Design Decisions

### 2.1 DOM Restructuring (`#status-bar`)

Replace:
```html
<span id="sb-copy-link" onclick="toggleShareModal(true)" title="Click to share session" style="cursor:pointer;margin-left:auto;">🔗 Share Link</span>
```

With a structured context cluster docked to the right of `#status-bar`:
```html
<div id="sb-context-cluster">
    <!-- Editor Context (Visible when activeEditorTab is focused) -->
    <div id="sb-editor-cluster" style="display:none;">
        <span id="sb-cursor-pos" class="sb-item" title="Current Cursor Line and Column">Ln 1, Col 1</span>
        <button id="sb-indent-btn" class="sb-item sb-interactive" onclick="cycleEditorIndent()" title="Click to toggle indentation size">Spaces: 4</button>
        <button id="sb-wrap-btn" class="sb-item sb-interactive" onclick="toggleActiveEditorWrap()" title="Click to toggle word wrapping">Wrap: Off</button>
        <button id="sb-syntax-btn" class="sb-item sb-interactive" onclick="toggleSyntaxPicker()" title="Click to change language mode">Plain Text</button>
    </div>

    <!-- Terminal Context (Visible when activeTerminalTab is focused) -->
    <div id="sb-terminal-cluster" style="display:none;">
        <span id="sb-term-geometry" class="sb-item" title="Terminal Dimensions">80 × 24</span>
    </div>

    <!-- Media Context (Visible when image tab is active) -->
    <div id="sb-media-cluster" style="display:none;">
        <span id="sb-media-info" class="sb-item" title="Media Information"></span>
    </div>
</div>

<!-- Floating Language / Syntax Mode Picker Popover -->
<div id="syntax-picker-popover" class="syntax-dropdown" style="display:none;">
    <div class="syntax-dropdown-header">Select Language Mode</div>
    <div id="syntax-picker-list" class="syntax-dropdown-list"></div>
</div>
```

### 2.2 Component State Machine

1. **Active Context Detection:**
   - On tab activation (`activateEditorTab`, `activateTerminalTab`, `openImageTab`, `toggleWebPreview`):
     - Update visibility of `#sb-editor-cluster`, `#sb-terminal-cluster`, `#sb-media-cluster`.
     - Refresh current tab values.
2. **Cursor Position Tracking:**
   - Hook into CodeMirror `'cursorActivity'` event per tab instance:
     ```js
     cm.on('cursorActivity', () => {
         const pos = cm.getCursor();
         updateCursorStatus(pos.line + 1, pos.ch + 1);
     });
     ```
   - Teardown cleanly when editor tab is closed (`closeEditorTab`).
3. **Indentation Cycle:**
   - Cycle sequence: `Spaces: 2` -> `Spaces: 4` -> `Tabs: 4` -> `Spaces: 2`.
   - Update `cm.setOption('indentUnit', n)` and `cm.setOption('indentWithTabs', bool)`.
4. **Word Wrap Toggle:**
   - Toggle `cm.setOption('lineWrapping', !currentWrap)`.
   - Update status bar button text/style (`active` state).
5. **Language / Syntax Mode Override:**
   - Supported modes map:
     - `javascript` -> JavaScript
     - `go` -> Go
     - `python` -> Python
     - `htmlmixed` -> HTML
     - `css` -> CSS
     - `markdown` -> Markdown
     - `shell` -> Shell / Bash
     - `yaml` -> YAML
     - `sql` -> SQL
     - `xml` -> XML
     - `null` / `text` -> Plain Text
   - Choosing a mode invokes `cm.setOption('mode', selectedMode)` and refreshes display badge.

---

## 3. Implementation Phases & Checklist

### Phase 1: Status Bar DOM & CSS Foundation
- [ ] Remove `#sb-copy-link` from `#status-bar` in `rmte/ui/index.html`.
- [ ] Add `#sb-context-cluster` and `#syntax-picker-popover` DOM in `rmte/ui/index.html`.
- [ ] Style status bar items (`.sb-item`, `.sb-interactive`) and popover in `rmte/ui/app.css`.
- [ ] Responsive rules for mobile `<= 768px` and `<= 640px` (prevent status bar overflow).

### Phase 2: Cursor Position & Line/Col Tracking
- [ ] Attach `cursorActivity` listener in `openEditorTab` for CodeMirror instances.
- [ ] Update `#sb-cursor-pos` on tab switch and cursor move.
- [ ] Clean up listeners on editor tab close to avoid memory leaks.

### Phase 3: Word Wrap & Indentation Quick Toggles
- [ ] Implement `toggleActiveEditorWrap()` in `rmte/ui/app.js`.
- [ ] Sync wrap status indicator (`Wrap: On` / `Wrap: Off`) with dynamic viewport rules.
- [ ] Implement `cycleEditorIndent()` to switch between 2 spaces, 4 spaces, and tabs.

### Phase 4: Syntax Highlighter Picker Popover
- [ ] Implement mode catalogue & display names mapping.
- [ ] Implement `toggleSyntaxPicker()` and popover rendering.
- [ ] Implement manual mode change (`cm.setOption('mode', newMode)`).
- [ ] Add click-outside dismiss listener.

### Phase 5: Terminal & Media Context Integration
- [ ] Update `#sb-term-geometry` from active terminal instance (`t.term.cols × t.term.rows`) on resize/fit.
- [ ] Update `#sb-media-info` when opening image viewer tabs.
- [ ] Comprehensive verification across desktop and mobile screens.

---

## 4. Review Checkpoints for DeepSeek (via Kilo Code)

1. **Scope Boundary:** Changes strictly limited to `rmte/ui/{index.html, app.css, app.js}` + `plan-editor-statusbar.md`. Zero Go changes, zero protocol changes.
2. **Desktop & Mobile Responsiveness:** Status bar items do not wrap or overflow the 22px-28px status bar on screens down to 360px.
3. **CodeMirror Listener Lifecycle:** Verify `cursorActivity` listeners do not cause CPU spikes or leak memory when opening and closing multiple tabs.
4. **Syntax Highlight Resilience:** Switching modes manually preserves dirty tracking, syntax styling, and undo history.
5. **Context Cleanliness:** Switching between Editor, Terminal, Image, and Preview correctly updates status bar clusters without stale text.

---

## 5. Review & Collaboration Log
- **Current**: Initial `plan-editor-statusbar.md` created. Ready for DeepSeek review via Kilo Code.
