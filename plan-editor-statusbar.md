# RMTE Collaborative Architecture Plan: Context-Aware Status Bar & Editor Settings

**Target Branch:** `experimental-collab`  
**Role Distribution:**
- **Antigravity (Implementer):** Status bar DOM restructuring, CodeMirror event integrations (`cursorActivity`, `lineWrapping` override precedence, dynamic indentation derivation, mode switcher), context focus tracking, responsive styling, and syntax picker popover.
- **DeepSeek via Kilo Code (Reviewer):** Code review, CodeMirror lifecycle & memory safety audit, touch/desktop responsiveness verification, and edge-case analysis.

---

## 1. Objectives & Scope

Replace the redundant `#sb-copy-link` on the bottom `#status-bar` (which duplicates the topbar share button `🔗` and mobile overflow menu `⋮`) with a **VS Code-style Context-Aware Status Bar & Quick-Settings cluster**:

1. **Context-Aware Information Display:**
   - **Editor Context (`activeContext === 'editor'`):**
     - **Cursor Position:** `Ln X, Col Y` updating in real-time via `cursorActivity`.
     - **Indentation Setting:** Dynamically derived from active CodeMirror instance (initial default: `Tabs: 4`, clickable cycle: `Tabs: 4` ↔ `Spaces: 2` ↔ `Spaces: 4`).
     - **Word Wrap Toggle:** `Wrap: On` / `Wrap: Off` (clickable toggle per-tab with explicit override precedence over mobile viewport rules).
     - **Syntax / Language Mode:** Clickable indicator showing active mode (e.g. `JavaScript`, `TypeScript`, `HTML`, `XML`, `CSS`, `Go`, `Python`, `Shell`, `YAML`, `Markdown`, `Plain Text`) with an instant dropdown/popover to switch mode safely.
   - **Terminal Context (`activeContext === 'terminal'`):**
     - **Terminal Geometry:** `Cols × Rows` indicator (e.g. `80 × 24`), updated on `refitTerminal`, tab switch, and PTY resize.
   - **Media Context (`activeContext === 'media'`):**
     - **Image Details:** Dimensions & byte size (e.g. `1920 × 1080 • 142 KB`), derived on `img.onload` + `blob.size`.
   - **Web Preview Context (`activeContext === 'preview'`):**
     - **Target Host/Port:** Indicator showing active port/host (e.g. `Web: 8080`).

2. **Responsive Layout & Overflow Prevention:**
   - **Desktop (`> 768px`):** Full cluster displayed in bottom right corner with `margin-left: auto`, subtle hover highlights, and popovers.
   - **Mobile (`<= 640px` / down to 360px):** Sembunyikan `#sb-session`, `#sb-user`, dan `Ln, Col` (`#sb-cursor-pos`) agar status bar tidak mengalami wrapping atau horizontal overflow. Prioritaskan `● Connected`, `Wrap`, dan `Syntax`.

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
    <!-- Editor Context (Visible when activeContext === 'editor') -->
    <div id="sb-editor-cluster" class="sb-cluster" style="display:none;">
        <span id="sb-cursor-pos" class="sb-item" title="Current Cursor Line and Column">Ln 1, Col 1</span>
        <button id="sb-indent-btn" class="sb-item sb-interactive" onclick="cycleEditorIndent()" title="Click to toggle indentation size">Tabs: 4</button>
        <button id="sb-wrap-btn" class="sb-item sb-interactive" onclick="toggleActiveEditorWrap()" title="Click to toggle word wrapping">Wrap: Off</button>
        <button id="sb-syntax-btn" class="sb-item sb-interactive" onclick="toggleSyntaxPicker()" title="Click to change language mode">Plain Text</button>
    </div>

    <!-- Terminal Context (Visible when activeContext === 'terminal') -->
    <div id="sb-terminal-cluster" class="sb-cluster" style="display:none;">
        <span id="sb-term-geometry" class="sb-item" title="Terminal Dimensions">80 × 24</span>
    </div>

    <!-- Media Context (Visible when activeContext === 'media') -->
    <div id="sb-media-cluster" class="sb-cluster" style="display:none;">
        <span id="sb-media-info" class="sb-item" title="Media Information"></span>
    </div>

    <!-- Web Preview Context (Visible when activeContext === 'preview') -->
    <div id="sb-preview-cluster" class="sb-cluster" style="display:none;">
        <span id="sb-preview-info" class="sb-item" title="Web Preview Status">Web: 8080</span>
    </div>
</div>

<!-- Floating Language / Syntax Mode Picker Popover -->
<div id="syntax-picker-popover" class="syntax-dropdown" style="display:none;">
    <div class="syntax-dropdown-header">Select Language Mode</div>
    <div id="syntax-picker-list" class="syntax-dropdown-list"></div>
</div>
```

### 2.2 Component State Machine & Technical Rules

1. **Active Context Resolution (Desktop Split & Mobile):**
   - State: `activeContext = 'terminal' | 'editor' | 'media' | 'preview'`.
   - Event Triggers:
     - Editor container click / `cm.on('focus')` -> `setActiveContext('editor')`.
     - Terminal container click / `t.term.onFocus` -> `setActiveContext('terminal')`.
     - Tab click (`activateEditorTab`, `activateTerminalTab`, `openImageTab`) -> sets context immediately.
     - Web preview interaction (toolbar click / toggle button) -> `setActiveContext('preview')`.
   - Cluster Switching:
     - `setActiveContext(ctx)` toggles `display: flex` on the matched cluster element and `display: none` on all others.
     - Fallback: if active context tab is closed, resolve to next available open tab (or hide cluster if empty).

2. **Word Wrap Override Precedence (Resolution of review issue #1):**
   - Problem: `updateEditorLineWrapping()` in `app.js:2008` forces `cm.setOption('lineWrapping', isMobile)` on resize/rotate, which would overwrite manual per-tab toggles.
   - Solution:
     - Each tab in `editorTabs[id]` stores `wrapOverride: true | false | null` (initial: `null`).
     - Effective wrap resolution:
       ```js
       function isTabWrapping(tabId) {
           const et = editorTabs[tabId];
           if (!et || !et.cm) return false;
           if (et.wrapOverride !== null && et.wrapOverride !== undefined) {
               return et.wrapOverride;
           }
           return window.innerWidth <= 768; // default auto-wrap on mobile
       }
       ```
     - `toggleActiveEditorWrap()`:
       - Computes `newVal = !isTabWrapping(activeEditorTab)`.
       - Sets `et.wrapOverride = newVal`.
       - Applies `et.cm.setOption('lineWrapping', newVal)`.
       - Updates button label (`Wrap: On` / `Wrap: Off`) and visual state.
     - `updateEditorLineWrapping()` respects `wrapOverride`:
       - Only tabs with `wrapOverride === null` get updated by breakpoint changes.

3. **Dynamic Indentation Derivation (Resolution of review issue #2):**
   - Initial State: CodeMirror instances in RMTE default to `indentWithTabs: true, indentUnit: 4` (`app.js:846-849`), meaning the initial label is **`Tabs: 4`** (not `Spaces: 4`).
   - Helper function reads `cm.getOption('indentWithTabs')` and `cm.getOption('indentUnit')`:
     ```js
     function getIndentLabel(cm) {
         if (!cm) return 'Tabs: 4';
         const isTabs = cm.getOption('indentWithTabs');
         const unit = cm.getOption('indentUnit') || 4;
         return isTabs ? `Tabs: ${unit}` : `Spaces: ${unit}`;
     }
     ```
   - `cycleEditorIndent()` advances through the cycle:
     - `Tabs: 4` -> `Spaces: 2` (`indentWithTabs: false, indentUnit: 2, tabSize: 2`)
     - `Spaces: 2` -> `Spaces: 4` (`indentWithTabs: false, indentUnit: 4, tabSize: 4`)
     - `Spaces: 4` -> `Tabs: 4` (`indentWithTabs: true, indentUnit: 4, tabSize: 4`)
     - Updates button text immediately.

4. **Syntax Catalog Alignment (Resolution of review issue #3):**
   - Only include CodeMirror modes that are actually loaded in `index.html:16-24` and supported by `getCodeMirrorMode`:
     ```js
     const SYNTAX_MODES = [
         { id: 'javascript', name: 'JavaScript' },
         { id: 'javascript', name: 'TypeScript', extMatch: ['ts', 'tsx'] },
         { id: 'htmlmixed',  name: 'HTML' },
         { id: 'xml',        name: 'XML' },
         { id: 'css',        name: 'CSS' },
         { id: 'go',         name: 'Go' },
         { id: 'python',     name: 'Python' },
         { id: 'shell',      name: 'Shell / Bash' },
         { id: 'yaml',       name: 'YAML' },
         { id: 'markdown',   name: 'Markdown' },
         { id: 'null',       name: 'Plain Text' }
     ];
     ```
   - (Note: `sql` is explicitly omitted because `mode/sql/sql.min.js` is not loaded in the bundle).
   - Changing mode invokes `cm.setOption('mode', targetMode === 'null' ? null : targetMode)`. This preserves dirty tracking, undo history, and document contents without disruption.

5. **Terminal Geometry Hook:**
   - Update `#sb-term-geometry` in:
     - `refitTerminal(tid)` after `t.fitAddon.fit()`.
     - `activateTerminalTab(id)`.
     - Terminal resize listener.
   - Text format: `${t.term.cols} × ${t.term.rows}`.

6. **Media Info Hook:**
   - In `openImageTab`:
     - Attach to `img.onload` to read `img.naturalWidth` and `img.naturalHeight`.
     - Format: `${img.naturalWidth} × ${img.naturalHeight} • ${fmtSize(blob.size)}`.
     - Fallback if load fails: `${fmtSize(blob.size)}`.

7. **CodeMirror Listener Lifecycle & Teardown:**
   - Store listener reference on tab creation: `et.cursorListener = () => updateCursorStatus(cm);`.
   - `cm.on('cursorActivity', et.cursorListener)`.
   - On `closeEditorTab(id)`: detach listener via `cm.off('cursorActivity', et.cursorListener)` prior to `cm.toTextArea()`.

8. **CSS Layout & Responsive Rules:**
   - `#sb-context-cluster` uses `margin-left: auto; display: flex; align-items: center; gap: var(--space-xs);`.
   - `#status-bar` enforces `flex-wrap: nowrap; overflow: hidden;`.
   - Under `@media (max-width: 640px)`:
     - `#sb-session`, `#sb-user`, and `#sb-cursor-pos` receive `display: none;`.
     - Keeps `#sb-connection`, `#sb-wrap-btn`, and `#sb-syntax-btn` visible and cleanly clickable without overflow.

---

## 3. Implementation Phases & Checklist

### Phase 1: Status Bar DOM & CSS Foundation
- [ ] Remove `#sb-copy-link` from `#status-bar` in `rmte/ui/index.html`.
- [ ] Add `#sb-context-cluster` (editor, terminal, media, preview) and `#syntax-picker-popover` in `rmte/ui/index.html`.
- [ ] Add status bar styling (`#sb-context-cluster`, `.sb-cluster`, `.sb-item`, `.sb-interactive`) and popover styles in `rmte/ui/app.css`.
- [ ] Implement responsive rules for `<= 640px` in `rmte/ui/app.css`.

### Phase 2: Active Context & Cursor Position Tracking
- [ ] Implement `activeContext` state machine and `setActiveContext(ctx)` in `rmte/ui/app.js`.
- [ ] Hook focus events for CodeMirror (`focus`), Terminal (`onFocus`), and Preview interactions.
- [ ] Attach `cursorActivity` listener in `openEditorTab` and clean up in `closeEditorTab`.
- [ ] Implement `updateCursorStatus()` to refresh `Ln X, Col Y`.

### Phase 3: Word Wrap & Indentation Quick Toggles
- [ ] Add `wrapOverride` property to `editorTabs[id]`.
- [ ] Update `updateEditorLineWrapping()` to respect `wrapOverride`.
- [ ] Implement `toggleActiveEditorWrap()` with reactive button label update (`Wrap: On` / `Wrap: Off`).
- [ ] Implement `cycleEditorIndent()` starting from `Tabs: 4` -> `Spaces: 2` -> `Spaces: 4` -> `Tabs: 4`.

### Phase 4: Syntax Highlighter Picker Popover
- [ ] Define `SYNTAX_MODES` catalogue (matching loaded modes: JS, TS, HTML, XML, CSS, Go, Py, Shell, YAML, MD, Plain Text).
- [ ] Implement `toggleSyntaxPicker()` and popover positioning near `#sb-syntax-btn`.
- [ ] Implement mode selection via `cm.setOption('mode', mode)`.
- [ ] Implement click-outside dismiss handler for the popover.

### Phase 5: Terminal, Media, & Preview Integration
- [ ] Hook terminal dimensions into `#sb-term-geometry` on `refitTerminal` and tab activation.
- [ ] Hook image dimensions & size into `#sb-media-info` on `img.onload`.
- [ ] Hook web preview port/status into `#sb-preview-info`.
- [ ] Complete cross-browser and mobile verification.

---

## 4. Review Checkpoints for DeepSeek (via Kilo Code)

1. **Scope Boundary:** Changes strictly limited to `rmte/ui/{index.html, app.css, app.js}` + `plan-editor-statusbar.md`. Zero Go changes, zero protocol changes.
2. **Desktop & Mobile Responsiveness:** Status bar items do not wrap or overflow the status bar on screens down to 360px (`<= 640px` collapses non-essential items).
3. **Wrap Override Precedence:** Manual `Wrap: On/Off` per tab persists through resize and mobile/desktop breakpoint transitions.
4. **Indentation Accuracy:** Initial tab indent correctly displays `Tabs: 4` and cycles through 2 spaces, 4 spaces, and tabs seamlessly.
5. **Mode Switching Safety:** Changing syntax mode manually preserves undo stack, dirty state tracking, and does not invoke missing mode scripts.
6. **Context Cleanliness:** Split workbench transitions (clicking editor vs terminal) immediately update the status bar cluster without stale or ghost data.

---

## 5. Review & Collaboration Log
- **Round 1 Draft**: Initial plan proposed (commit `dec6a94`).
- **Round 1 Review (DeepSeek)**: Identified 5 core technical corrections:
  1. Wrap override precedence over `updateEditorLineWrapping()`.
  2. Initial indent label correction (`Tabs: 4` default).
  3. Catalog mode alignment (remove non-existent `sql`).
  4. Missing `#sb-preview-cluster` DOM & preview active context definition.
  5. Context resolution on desktop split view via focus/interaction tracking.
- **Round 1 Revision**: All 5 corrections and polish notes integrated into plan (commit current). Ready for final review and phase execution.
