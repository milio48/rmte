# RMTE Collaborative Architecture Plan: Native SVG Icon System & Emoji Deprecation

**Target Branch:** `experimental-collab`  
**Role Distribution:**
- **Antigravity (Implementer):** SVG icon dictionary & helper development, DOM icon replacements across topbar/workbench/modals, file type badge generator, and responsive alignment styling.
- **DeepSeek via Kilo Code (Reviewer):** Visual consistency audit, SVG viewBox/stroke quality verification, bundle size impact analysis, cross-browser rendering checks, and edge-case review.

---

## 1. Objectives & Scope

Replace all fragmented OS/browser-dependent emoji characters (`📁`, `💻`, `🌐`, `👥`, `📜`, `🔗`, `❓`, `🐙`, `💾`, `🔷`, `🐍`, etc.) with a **crisp, unified, theme-aware native SVG icon system**:

1. **Visual Consistency Across All Platforms:**
   - Eliminate platform-dependent emoji rendering variances (Windows Segoe UI Emoji vs macOS Apple Color Emoji vs Android/Linux Noto).
   - Ensure pixel-perfect baseline alignment, uniform stroke weight, and optical sizing.

2. **Full Theme & State Responsiveness:**
   - Icons inherit CSS colors via `currentColor` / CSS design tokens (`var(--color-ink-2)`, `var(--color-accent)`, `var(--color-green)`, etc.).
   - Interactive hover, focus, and active states seamlessly glow and highlight with zero image swapping or filter hacks.

3. **Zero External Runtime Dependencies:**
   - RMTE remains 100% offline-capable and single-bundle friendly (no FontAwesome, Lucide CDN, or Google Fonts icons via HTTP).
   - All SVGs embedded as an ultra-compact JS dictionary + HTML vector templates (estimated total footprint < 12 KB uncompressed, ~3.5 KB gzipped).

4. **Zero Regression Guarantee:**
   - WebSocket protocols, Go relay/host backend, CLI/TUI client (`rmte join`), and keyboard shortcuts remain **100% untouched**.
   - No performance penalty or DOM thrashing (lightweight string templates or SVG `<use>` references).

---

## 2. Architecture & Design Decisions

### 2.1 Icon Design Specification

- **Grid & ViewBox:** 
  - Standard UI icons: `viewBox="0 0 24 24"` (rendered at `16×16px` for compact controls / status bar, `18×18px` or `20×20px` for topbar).
  - Compact tree/tab icons: `viewBox="0 0 16 16"`.
- **Stroke & Geometry:**
  - `fill="none"`
  - `stroke="currentColor"`
  - `stroke-width="1.75"` (or `2.0` on 24px grid)
  - `stroke-linecap="round"`
  - `stroke-linejoin="round"`
- **File Type Icons:**
  - Modern geometric file badge with language emblem (inspired by VS Code Codicons & Seti UI).
  - Subtle language tinting classes (e.g. `.icon-go` = Go cyan, `.icon-js` = JS yellow, `.icon-py` = Python yellow/blue, `.icon-md` = Markdown blue, etc.) that harmonize with Midnight theme tokens.

---

### 2.2 Technical Delivery: Hybrid SVG System

To combine runtime efficiency and dynamic rendering in RMTE's vanilla JS architecture:

1. **JavaScript SVG Dictionary (`ICONS` map in `app.js`):**
   ```js
   const ICONS = {
       // Navigation & Actions
       files: '<svg ...><path .../></svg>',
       terminal: '<svg ...><path .../></svg>',
       preview: '<svg ...><path .../></svg>',
       users: '<svg ...><path .../></svg>',
       activity: '<svg ...><path .../></svg>',
       share: '<svg ...><path .../></svg>',
       help: '<svg ...><path .../></svg>',
       github: '<svg ...><path .../></svg>',
       close: '<svg ...><path .../></svg>',
       more: '<svg ...><path .../></svg>',
       plus: '<svg ...><path .../></svg>',
       save: '<svg ...><path .../></svg>',
       download: '<svg ...><path .../></svg>',
       upload: '<svg ...><path .../></svg>',
       refresh: '<svg ...><path .../></svg>',
       newFile: '<svg ...><path .../></svg>',
       newFolder: '<svg ...><path .../></svg>',
       folder: '<svg ...><path .../></svg>',
       folderOpen: '<svg ...><path .../></svg>',

       // Preview Controls
       chevronLeft: '<svg ...><path .../></svg>',
       chevronRight: '<svg ...><path .../></svg>',
       arrowRight: '<svg ...><path .../></svg>',
       externalLink: '<svg ...><path .../></svg>',
       deviceMobile: '<svg ...><path .../></svg>',
       maximize: '<svg ...><path .../></svg>',
       restore: '<svg ...><path .../></svg>',

       // File Badges (Seti / Codicon style)
       fileCode: '<svg ...><path .../></svg>',
       fileText: '<svg ...><path .../></svg>',
       fileImage: '<svg ...><path .../></svg>',
       fileArchive: '<svg ...><path .../></svg>',
       fileGear: '<svg ...><path .../></svg>',
       fileGo: '<svg ...><path .../></svg>',
       fileJs: '<svg ...><path .../></svg>',
       fileTs: '<svg ...><path .../></svg>',
       filePy: '<svg ...><path .../></svg>',
       fileHtml: '<svg ...><path .../></svg>',
       fileCss: '<svg ...><path .../></svg>',
       fileJson: '<svg ...><path .../></svg>',
       fileMd: '<svg ...><path .../></svg>'
   };

   function svgIcon(name, { size = 16, className = '', title = '' } = {}) {
       const svg = ICONS[name] || ICONS.fileText;
       // Injects dimensions and classes cleanly
       return svg.replace('<svg', `<svg width="${size}" height="${size}" class="ui-icon ${className}" ${title ? `title="${title}"` : ''}`);
   }
   ```

2. **Static Markup Replacement in `index.html`:**
   - Static topbar buttons use crisp SVG markup directly in `index.html` for instant first-paint without hydration lag.

3. **CSS Utilities & Token Integration (`app.css`):**
   ```css
   .ui-icon {
       display: inline-flex;
       align-self: center;
       flex-shrink: 0;
       vertical-align: middle;
       stroke: currentColor;
       fill: none;
       pointer-events: none;
   }
   
   /* File type accent colors */
   .ui-icon.lang-go    { color: #00add8; }
   .ui-icon.lang-js    { color: #f7df1e; }
   .ui-icon.lang-ts    { color: #3178c6; }
   .ui-icon.lang-py    { color: #3776ab; }
   .ui-icon.lang-html  { color: #e34f26; }
   .ui-icon.lang-css   { color: #1572b6; }
   .ui-icon.lang-json  { color: #cbcb41; }
   .ui-icon.lang-yaml  { color: #cb171e; }
   .ui-icon.lang-md    { color: #42a5f5; }
   .ui-icon.lang-sh    { color: #4ebd32; }
   .ui-icon.lang-img   { color: #ab47bc; }
   .ui-icon.lang-zip   { color: #ff9800; }
   ```

---

### 2.3 Inventory of All Emoji Targets to Replace

| Target Area | Current Emoji | Replacement SVG Name |
| :--- | :--- | :--- |
| **Topbar Buttons** | 📁 `toggle-files-btn` | `folder` |
| | 💻 `toggle-terminal-btn` | `terminal` |
| | ＋ `new-tab-btn` | `plus` |
| | 🌐 `toggle-preview-btn` | `globe` |
| | 👥 `toggle-sidebar-btn` | `users` |
| | 📜 `toggle-activity-btn` | `history` / `scroll` |
| | 🔗 `copy-link-btn` | `link` |
| | ❓ `help-btn` | `helpCircle` |
| | 🐙 `github-link-btn` | `github` |
| | ✕ `disconnect-btn` | `power` / `logOut` |
| | ⋮ `topbar-overflow-btn` | `moreVertical` |
| **Overflow Menu Items** | 👥, 📜, 🔗, ❓, 🐙, ✕ | Matching vector icons |
| **File Explorer Actions** | 📄 `promptNewFile` | `filePlus` |
| | 📁 `promptNewFolder` | `folderPlus` |
| | ⬆ `triggerFileUpload` | `upload` |
| | 🔄 `refreshFiles` | `refreshCw` |
| **File Tree & Tabs** | 📁 `fe-folder-closed` | `folder` |
| | 📂 `fe-folder-open` | `folderOpen` |
| | 25+ file extensions (`fileIcon`) | Dedicated language & category vector badges |
| **Editor Workbench** | 💾 `saveBtn` | `save` |
| | ⬇ `dlBtn` | `download` |
| | 🎨 `editSvgBtn` | `edit` / `palette` |
| **Preview Toolbar** | ◀ `preview-back-btn` | `arrowLeft` |
| | ▶ `preview-forward-btn` | `arrowRight` |
| | ⟳ `preview-reload-btn` | `rotateCw` |
| | 🌐 `preview-address-icon` | `globe` |
| | ↵ `preview-go-btn` | `cornerDownLeft` |
| | ↗ `preview-open-tab-btn` | `externalLink` |
| | 📱 `preview-mobile-btn` | `smartphone` |
| | □ `preview-maximize-btn` | `maximize2` / `minimize2` |
| | ✕ `preview-close-btn` | `x` |
| **Sidebar Tabs** | 👥 Users, 💬 Chat, 📜 Log | `users`, `messageSquare`, `activity` |
| **Modals & Dialogs** | 🔗 Share, 🌐 Link, 💻 CLI, 📥 Export | Clean vector iconography |

---

## 3. Implementation Phases & Checklist

### Phase 1: SVG Foundation & Topbar Icons
- [ ] Define `.ui-icon` base styles and layout utilities in `rmte/ui/app.css`.
- [ ] Replace emoji characters in `rmte/ui/index.html` topbar buttons with static SVGs.
- [ ] Replace emoji characters in `#topbar-overflow-menu` items in `rmte/ui/index.html`.
- [ ] Validate button alignment, touch target sizes, and hover colors across desktop and mobile.

### Phase 2: Workbench, Editor Toolbar & Web Preview Toolbar
- [ ] Replace emoji in Mini Browser Preview toolbar (`#preview-toolbar`) with clean navigation SVGs.
- [ ] Replace `💾 Save`, `⬇ Download`, and `🎨 Edit SVG` in `openEditorTab` and `openImageTab`.
- [ ] Update maximize/restore toggle logic to switch SVG icons smoothly instead of text `□`.

### Phase 3: File Explorer Tree & File Type Badges
- [ ] Replace folder emojis (`📁` / `📂`) in file tree rendering with SVG `folder` / `folderOpen`.
- [ ] Replace action buttons (`📄`, `📁`, `⬆`, `🔄`) in File Explorer toolbar.
- [ ] Re-engineer `fileIcon(name)` in `rmte/ui/app.js` to return SVG file badges with subtle language classes.
- [ ] Update tab header rendering (`editor-tabs`) to cleanly display the new SVG file icons.

### Phase 4: Sidebar Tabs, Log Badges & Modals
- [ ] Replace emoji in sidebar navigation tabs (`#sb-tab-collab`, `#sb-tab-chat`, `#sb-tab-activity`).
- [ ] Replace emoji in `#share-modal`, `#join-modal`, and `#help-modal` headers & input labels.
- [ ] Replace `📥 Export` in Activity Log toolbar with an SVG download/export icon.

### Phase 5: Testing, Accessibility & Verification
- [ ] Verify `aria-label` and `title` attributes on all icon buttons for screen reader accessibility.
- [ ] Verify high-DPI (Retina) crispness and subpixel rendering across Chrome, Firefox, Safari, and mobile WebKit.
- [ ] Run test suite (`node --check rmte/ui/app.js`, `go test -count=1 ./...`, `go vet ./...`).
- [ ] Measure total asset footprint to ensure zero bloat.

---

## 4. Review Checkpoints for DeepSeek (via Kilo Code)

1. **Scope Boundary:** Strict UI layer only (`rmte/ui/{index.html, app.css, app.js}` + `plan-svg-icons.md`). Zero Go backend changes, zero protocol changes.
2. **Offline Independence:** Confirm all SVGs are embedded; no external network requests or third-party web font references.
3. **Optical Sizing & Alignment:** Icons align perfectly to font baseline with no vertical jumping or misaligned text pairings.
4. **Theme Reactivity:** Icons inherit `currentColor` properly, adapting immediately to hover, active, and focus states.
5. **No Regressions on Touch/Mobile:** Touch targets on mobile (`min-height: 38px` where applicable) remain fully accessible and responsive.

---

## 5. Review & Collaboration Log
- **Initial Proposal**: Brainstormed and drafted `plan-svg-icons.md` to transition RMTE from emoji-based UI to a native, theme-aware SVG icon system. Ready for review and phased execution.
