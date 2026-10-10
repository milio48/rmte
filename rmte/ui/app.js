// RMTE v0.7.0 — Web Viewer with Split Workbench, Context Status Bar, SVG Icons & Mobile UI
let ws, aesKey, rawAesKeyBytes = null, myUsername = '';
let activeEditorTab = null, activeTerminalTab = 'term-0', activeContext = 'terminal';
let terminalPanelCollapsed = false, terminalPanelMaximized = false, terminalPanelHidden = false;
let mobileActivePane = 'terminal';
const myViewerId = 'v-web-' + Math.random().toString(16).slice(2,10);
let terminals = {}, editorTabs = {};
let fileManagerOpen = false, currentFilePath = './';
let webPreviewOpen = false, previewPort = 8080, previewPath = '', previewMobileMode = false, previewMaximized = false;
// Mobile UI state
let stickyCtrl = 0; // 0 = off, 1 = once, 2 = locked
let stickyAlt = 0;  // 0 = off, 1 = once, 2 = locked
let overflowMenuOpen = false;
let activePeerCount = 0;

// Auto-reconnect state
let isConnected = false, manualDisconnect = false;
let reconnectAttempt = 0, reconnectTimer = null, reconnectCountdown = null;
const RECONNECT_BASE = 2000, RECONNECT_MAX = 30000;
let waitingForFileData = false, pendingFileBytes = null, pendingEditorPath = null;
let pendingDownload = false, pendingDownloadName = null;
let uploadQueue = [], uploadActive = false, fileOpBusy = false;
let pingTimer = null;
const DATA_CH = 255;

const IMAGE_EXT = new Set(['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'ico', 'bmp', 'avif']);
const BINARY_EXT = new Set([
    'exe', 'dll', 'so', 'dylib', 'bin', 'o', 'a', 'lib', 'iso', 'img',
    'zip', 'tar', 'gz', 'tgz', 'bz2', 'tbz2', 'xz', 'txz', '7z', 'rar', 'zst',
    'pdf', 'doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx',
    'mp3', 'wav', 'ogg', 'flac', 'aac', 'm4a',
    'mp4', 'mkv', 'avi', 'mov', 'webm', 'wmv',
    'woff', 'woff2', 'ttf', 'otf', 'eot',
    'wasm', 'class', 'pyc', 'pyo', 'db', 'sqlite', 'sqlite3'
]);

const TEXT_EXT = new Set('go,js,ts,jsx,tsx,py,rb,rs,c,cpp,h,hpp,java,kt,cs,php,html,css,scss,less,json,yaml,yml,toml,xml,sql,md,txt,log,csv,ini,cfg,conf,env,sh,bash,bat,ps1,cmd,mod,sum,lock,editorconfig,gitignore,makefile,dockerfile,license'.split(','));
const LANG_MAP = {go:'Go',js:'JavaScript',ts:'TypeScript',py:'Python',rs:'Rust',html:'HTML',css:'CSS',json:'JSON',md:'Markdown',yaml:'YAML',yml:'YAML',sh:'Shell',sql:'SQL',c:'C',cpp:'C++',java:'Java',rb:'Ruby',php:'PHP',xml:'XML',toml:'TOML',svg:'SVG'};

function basename(p){if(!p)return'';return p.replace(/\\/g,'/').split('/').filter(Boolean).pop()||p;}

function getFileExt(p){
    const fn = basename(p);
    const dot = fn.lastIndexOf('.');
    if(dot <= 0) return '';
    return fn.slice(dot + 1).toLowerCase();
}

function isImageFile(path){
    const ext = getFileExt(path);
    return IMAGE_EXT.has(ext);
}

function isBinaryFile(path){
    const ext = getFileExt(path);
    return BINARY_EXT.has(ext);
}

function isTextFile(path){
    if(isImageFile(path)) return false;
    if(isBinaryFile(path)) return false;
    return true; // Plaintext, LICENSE, Makefile, Dockerfile, configs, etc.
}

function getImageMime(path){
    const ext = getFileExt(path);
    const mimes = {
        svg: 'image/svg+xml', png: 'image/png', jpg: 'image/jpeg', jpeg: 'image/jpeg',
        gif: 'image/gif', webp: 'image/webp', ico: 'image/x-icon', bmp: 'image/bmp', avif: 'image/avif'
    };
    return mimes[ext] || 'image/png';
}

function getCodeMirrorMode(path) {
    const ext = getFileExt(path);
    const map = {
        js: 'javascript', ts: 'javascript', jsx: 'javascript', tsx: 'javascript', json: 'javascript',
        go: 'go', py: 'python',
        html: 'htmlmixed', htm: 'htmlmixed', xml: 'xml', svg: 'xml',
        css: 'css', scss: 'css', less: 'css',
        md: 'markdown', markdown: 'markdown',
        sh: 'shell', bash: 'shell', zsh: 'shell',
        yml: 'yaml', yaml: 'yaml'
    };
    return map[ext] || null;
}

// ═══════════════════════════════════════
// STATUS BAR CONTEXT & CONTROLS
// ═══════════════════════════════════════
const SYNTAX_MODES = [
    { id: 'javascript', name: 'JavaScript / TypeScript' },
    { id: 'json', cmMode: 'javascript', name: 'JSON' },
    { id: 'htmlmixed',  name: 'HTML' },
    { id: 'xml',        name: 'XML / SVG' },
    { id: 'css',        name: 'CSS' },
    { id: 'go',         name: 'Go' },
    { id: 'python',     name: 'Python' },
    { id: 'shell',      name: 'Shell / Bash' },
    { id: 'yaml',       name: 'YAML' },
    { id: 'markdown',   name: 'Markdown' },
    { id: 'null',       name: 'Plain Text' }
];

function setActiveContext(ctx) {
    activeContext = ctx;
    const edCl = document.getElementById('sb-editor-cluster');
    const termCl = document.getElementById('sb-terminal-cluster');
    const medCl = document.getElementById('sb-media-cluster');
    const prevCl = document.getElementById('sb-preview-cluster');

    if (edCl) edCl.style.display = ctx === 'editor' ? 'flex' : 'none';
    if (termCl) termCl.style.display = ctx === 'terminal' ? 'flex' : 'none';
    if (medCl) medCl.style.display = ctx === 'media' ? 'flex' : 'none';
    if (prevCl) prevCl.style.display = ctx === 'preview' ? 'flex' : 'none';

    if (ctx === 'editor') {
        updateStatusBarEditorCluster();
    } else if (ctx === 'terminal') {
        if (activeTerminalTab && activeTerminalTab.startsWith('term-')) {
            const tid = parseInt(activeTerminalTab.slice(5));
            if (terminals[tid] && terminals[tid].term) {
                updateTerminalGeometryStatus(terminals[tid].term.cols, terminals[tid].term.rows);
            }
        }
    } else if (ctx === 'media') {
        if (activeEditorTab && editorTabs[activeEditorTab]) {
            updateMediaStatus(editorTabs[activeEditorTab]);
        }
    } else if (ctx === 'preview') {
        updatePreviewStatus();
    }
}

function updateStatusBarEditorCluster() {
    updateCursorStatus();
    updateStatusBarIndent();
    updateStatusBarWrap();
    updateStatusBarSyntax();
}

function updateCursorStatus() {
    const el = document.getElementById('sb-cursor-pos');
    if (!el) return;
    if (!activeEditorTab || !editorTabs[activeEditorTab]) {
        el.innerText = 'Ln 1, Col 1';
        return;
    }
    const et = editorTabs[activeEditorTab];
    if (et.cm) {
        const pos = et.cm.getCursor();
        el.innerText = `Ln ${pos.line + 1}, Col ${pos.ch + 1}`;
    } else if (et.textarea) {
        const val = et.textarea.value.substring(0, et.textarea.selectionStart || 0);
        const lines = val.split('\n');
        const line = lines.length;
        const col = lines[lines.length - 1].length + 1;
        el.innerText = `Ln ${line}, Col ${col}`;
    }
}

function getIndentLabel(cm) {
    if (!cm) return 'Tabs: 4';
    const isTabs = cm.getOption('indentWithTabs');
    const unit = cm.getOption('indentUnit') || 4;
    return isTabs ? `Tabs: ${unit}` : `Spaces: ${unit}`;
}

function updateStatusBarIndent() {
    const btn = document.getElementById('sb-indent-btn');
    if (!btn) return;
    if (!activeEditorTab || !editorTabs[activeEditorTab] || !editorTabs[activeEditorTab].cm) {
        btn.innerText = 'Tabs: 4';
        return;
    }
    btn.innerText = getIndentLabel(editorTabs[activeEditorTab].cm);
}

function cycleEditorIndent() {
    if (!activeEditorTab || !editorTabs[activeEditorTab]) return;
    const et = editorTabs[activeEditorTab];
    if (!et.cm) return;
    const isTabs = et.cm.getOption('indentWithTabs');
    const unit = et.cm.getOption('indentUnit') || 4;

    // Cycle: Tabs: 4 -> Spaces: 2 -> Spaces: 4 -> Tabs: 4
    if (isTabs) {
        et.cm.setOption('indentWithTabs', false);
        et.cm.setOption('indentUnit', 2);
        et.cm.setOption('tabSize', 2);
    } else if (unit === 2) {
        et.cm.setOption('indentWithTabs', false);
        et.cm.setOption('indentUnit', 4);
        et.cm.setOption('tabSize', 4);
    } else {
        et.cm.setOption('indentWithTabs', true);
        et.cm.setOption('indentUnit', 4);
        et.cm.setOption('tabSize', 4);
    }
    updateStatusBarIndent();
}

function isTabWrapping(tabId) {
    const et = editorTabs[tabId];
    if (!et || !et.cm) return false;
    if (et.wrapOverride !== null && et.wrapOverride !== undefined) {
        return et.wrapOverride;
    }
    return window.innerWidth <= 768;
}

function updateStatusBarWrap() {
    const btn = document.getElementById('sb-wrap-btn');
    if (!btn) return;
    if (!activeEditorTab || !editorTabs[activeEditorTab] || !editorTabs[activeEditorTab].cm) {
        btn.innerText = 'Wrap: Auto';
        btn.classList.remove('active');
        btn.title = 'Word wrapping';
        return;
    }
    const et = editorTabs[activeEditorTab];
    const wrapping = isTabWrapping(activeEditorTab);
    if (et.wrapOverride === null || et.wrapOverride === undefined) {
        btn.innerText = 'Wrap: Auto';
        btn.title = `Word wrapping: Auto (${wrapping ? 'On' : 'Off'} by viewport). Click for forced On.`;
    } else {
        btn.innerText = et.wrapOverride ? 'Wrap: On' : 'Wrap: Off';
        btn.title = `Word wrapping: Forced ${et.wrapOverride ? 'On' : 'Off'}. Click to toggle.`;
    }
    btn.classList.toggle('active', wrapping);
}

function toggleActiveEditorWrap() {
    if (!activeEditorTab || !editorTabs[activeEditorTab]) return;
    const et = editorTabs[activeEditorTab];
    if (!et.cm) return;

    // Strict 3-state cycle: Auto (null) -> Forced On (true) -> Forced Off (false) -> Auto (null)
    if (et.wrapOverride === null || et.wrapOverride === undefined) {
        et.wrapOverride = true;
    } else if (et.wrapOverride === true) {
        et.wrapOverride = false;
    } else {
        et.wrapOverride = null; // Return to Auto
    }

    const effectiveWrap = isTabWrapping(activeEditorTab);
    et.cm.setOption('lineWrapping', effectiveWrap);
    try { et.cm.refresh(); } catch(e){}
    updateStatusBarWrap();
}

function getSyntaxDisplayName(mode, path, syntaxOverride) {
    if (syntaxOverride) {
        const item = SYNTAX_MODES.find(m => m.id === syntaxOverride);
        if (item) return item.name;
    }
    if (typeof mode === 'object' && mode !== null) {
        mode = mode.name || mode.id || mode;
    }
    if (!mode || mode === 'null') return 'Plain Text';
    if (!syntaxOverride && path && (path.endsWith('.json') || getFileExt(path) === 'json')) {
        return 'JSON';
    }
    const item = SYNTAX_MODES.find(m => m.id === mode);
    return item ? item.name : String(mode);
}

function updateStatusBarSyntax() {
    const btn = document.getElementById('sb-syntax-btn');
    if (!btn) return;
    if (!activeEditorTab || !editorTabs[activeEditorTab] || !editorTabs[activeEditorTab].cm) {
        btn.innerText = 'Plain Text';
        return;
    }
    const et = editorTabs[activeEditorTab];
    const currentMode = et.cm.getOption('mode');
    btn.innerText = getSyntaxDisplayName(currentMode, et.path, et.syntaxOverride);
}

function toggleSyntaxPicker(e) {
    if (e) e.stopPropagation();
    const pop = document.getElementById('syntax-picker-popover');
    if (!pop) return;
    if (pop.style.display !== 'none') {
        pop.style.display = 'none';
        return;
    }
    if (!activeEditorTab || !editorTabs[activeEditorTab] || !editorTabs[activeEditorTab].cm) return;

    const et = editorTabs[activeEditorTab];
    let currentMode = et.cm.getOption('mode');
    if (typeof currentMode === 'object' && currentMode !== null) {
        currentMode = currentMode.name || currentMode.id;
    }
    const activeLabel = getSyntaxDisplayName(currentMode, et.path, et.syntaxOverride);
    const list = document.getElementById('syntax-picker-list');
    list.innerHTML = '';
    SYNTAX_MODES.forEach(m => {
        const itemBtn = document.createElement('button');
        const isActive = (m.name === activeLabel);
        itemBtn.className = 'syntax-menu-item' + (isActive ? ' active' : '');
        itemBtn.innerHTML = `<span>${m.name}</span>` + (isActive ? '<span class="syntax-check">✓</span>' : '');
        itemBtn.onclick = (evt) => {
            evt.stopPropagation();
            setEditorSyntaxMode(m.id);
            pop.style.display = 'none';
        };
        list.appendChild(itemBtn);
    });

    pop.style.display = 'flex';
}

function setEditorSyntaxMode(modeId) {
    if (!activeEditorTab || !editorTabs[activeEditorTab]) return;
    const et = editorTabs[activeEditorTab];
    if (!et.cm) return;
    et.syntaxOverride = modeId;
    if (modeId === 'json') {
        et.cm.setOption('mode', 'javascript');
    } else {
        const mode = (modeId === 'null' || !modeId) ? null : modeId;
        et.cm.setOption('mode', mode);
    }
    updateStatusBarSyntax();
}

function updateTerminalGeometryStatus(cols, rows) {
    const el = document.getElementById('sb-term-geometry');
    if (!el) return;
    if (cols && rows) {
        el.innerText = `${cols} × ${rows}`;
    }
}

function updateMediaStatus(et) {
    const el = document.getElementById('sb-media-info');
    if (!el) return;
    if (!et) { el.innerText = ''; return; }
    if (et.naturalWidth && et.naturalHeight) {
        el.innerText = `${et.naturalWidth} × ${et.naturalHeight} • ${fmtSize(et.sizeBytes || 0)}`;
    } else {
        el.innerText = fmtSize(et.sizeBytes || 0);
    }
}

function updatePreviewStatus() {
    const el = document.getElementById('sb-preview-info');
    if (!el) return;
    el.innerText = `Web: ${previewPort || 8080}`;
}


const _log = {
    out(t,d){console.log(`%c[OUT] %c${t}`,'color:#58a6ff;font-weight:bold','color:#8b949e',d)},
    in(t,d){console.log(`%c[IN]  %c${t}`,'color:#3fb950;font-weight:bold','color:#8b949e',d)},
    err(m,d){console.error(`%c[ERR] %c${m}`,'color:#f85149;font-weight:bold','color:#8b949e',d||'')},
    warn(m,d){console.warn(`%c[WARN] %c${m}`,'color:#d29922;font-weight:bold','color:#8b949e',d||'')},
    info(m,d){console.info(`%c[INFO] %c${m}`,'color:#bc8cff;font-weight:bold','color:#8b949e',d||'')},
};

function getLang(name) { return LANG_MAP[getFileExt(name)] || 'Text'; }

const ICONS = {
    // ── 1. WEB & MODERN FRONTEND ──
    langJs: `<rect width="20" height="20" x="2" y="2" rx="3.5" fill="#F7DF1E"/><text x="12" y="12" font-family="'JetBrains Mono', 'Segoe UI', sans-serif" font-size="11" font-weight="900" text-anchor="middle" dominant-baseline="central" fill="#000000">JS</text>`,
    langTs: `<rect width="20" height="20" x="2" y="2" rx="3.5" fill="#3178C6"/><text x="12" y="12" font-family="'JetBrains Mono', 'Segoe UI', sans-serif" font-size="11" font-weight="900" text-anchor="middle" dominant-baseline="central" fill="#FFFFFF">TS</text>`,
    langReact: `<ellipse cx="12" cy="12" rx="9.5" ry="3.8" fill="none" stroke="#61DAFB" stroke-width="1.6"/><ellipse cx="12" cy="12" rx="9.5" ry="3.8" transform="rotate(60 12 12)" fill="none" stroke="#61DAFB" stroke-width="1.6"/><ellipse cx="12" cy="12" rx="9.5" ry="3.8" transform="rotate(120 12 12)" fill="none" stroke="#61DAFB" stroke-width="1.6"/><circle cx="12" cy="12" r="1.8" fill="#61DAFB"/>`,
    langVue: `<polygon points="12,21 1.5,3 6.5,3 12,12.5 17.5,3 22.5,3" fill="#42B883"/><polygon points="12,14 6.5,4.5 9.5,4.5 12,9 14.5,4.5 17.5,4.5" fill="#35495E"/>`,
    langSvelte: `<path fill="#FF3E00" d="M19.4 6.8a4.8 4.8 0 0 0-7.1-.9l-4.6 3.6a4.8 4.8 0 0 0-.8 6.5l1.3-1a3.1 3.1 0 0 1 .5-4.2l4.6-3.6a3.1 3.1 0 0 1 4.5.6 3.1 3.1 0 0 1-.6 4.2l-4.3 3.4a4.8 4.8 0 0 0-.8 6.5 4.8 4.8 0 0 0 7.1.9l4.6-3.6a4.8 4.8 0 0 0 .8-6.5l-1.3 1a3.1 3.1 0 0 1-.5 4.2l-4.6 3.6a3.1 3.1 0 0 1-4.5-.6 3.1 3.1 0 0 1 .6-4.2l4.3-3.4a4.8 4.8 0 0 0 .8-6.5z"/>`,
    langHtml: `<path fill="#E34F26" d="M3 2l1.6 18.2L12 22.5l7.4-2.3L21 2H3z"/><path fill="#EF652A" d="M12 3.8v16.9l5.8-1.8 1.4-15.1H12z"/><text x="12" y="12.5" font-family="'Segoe UI', sans-serif" font-size="11" font-weight="900" text-anchor="middle" dominant-baseline="central" fill="#FFFFFF">5</text>`,
    langCss: `<path fill="#1572B6" d="M3 2l1.6 18.2L12 22.5l7.4-2.3L21 2H3z"/><path fill="#33A9DC" d="M12 3.8v16.9l5.8-1.8 1.4-15.1H12z"/><text x="12" y="12.5" font-family="'Segoe UI', sans-serif" font-size="11" font-weight="900" text-anchor="middle" dominant-baseline="central" fill="#FFFFFF">3</text>`,
    langSass: `<rect width="20" height="20" x="2" y="2" rx="3.5" fill="#CF649A"/><text x="12" y="12" font-family="'Segoe UI', sans-serif" font-size="8" font-weight="900" text-anchor="middle" dominant-baseline="central" fill="#FFFFFF">SASS</text>`,

    // ── 2. BACKEND & SYSTEMS LANGUAGES ──
    langGo: `<rect width="20" height="20" x="2" y="2" rx="3.5" fill="#00ADD8"/><text x="12" y="12" font-family="'JetBrains Mono', 'Segoe UI', sans-serif" font-size="10.5" font-weight="900" letter-spacing="-0.5px" text-anchor="middle" dominant-baseline="central" fill="#FFFFFF">GO</text>`,
    langGoMod: `<rect width="20" height="20" x="2" y="2" rx="3.5" fill="#00ADD8" fill-opacity="0.15" stroke="#00ADD8" stroke-width="1.6"/><text x="12" y="12" font-family="'JetBrains Mono', monospace" font-size="8" font-weight="900" text-anchor="middle" dominant-baseline="central" fill="#00ADD8">MOD</text>`,
    langPy: `<path fill="#3776AB" d="M11.9 2C8.7 2 6.8 2.8 6.8 4.7v2H12v.7H4.5C2.8 7.4 1.5 8.7 1.5 11.2c0 2.3 1.3 3.8 3 3.8h1.8v-1.8c0-1.8 1.5-3.3 3.3-3.3h5.4c1.5 0 2.7-1.2 2.7-2.7V4.7C17.7 2.8 15.1 2 11.9 2zm-1.5 1.5c.5 0 .9.4.9.9s-.4.9-.9.9-.9-.4-.9-.9.4-.9.9-.9z"/><path fill="#FFD43B" d="M12.1 22c3.2 0 5.1-.8 5.1-2.7v-2H12v-.7h7.5c1.7 0 3-1.3 3-3.8 0-2.3-1.3-3.8-3-3.8h-1.8v1.8c0 1.8-1.5 3.3-3.3 3.3H9c-1.5 0-2.7 1.2-2.7 2.7v2.5c0 1.9 2.6 2.7 5.8 2.7zm1.5-1.5c-.5 0-.9-.4-.9-.9s.4-.9.9-.9.9.4.9.9-.4.9-.9.9z"/>`,
    langRs: `<circle cx="12" cy="12" r="9" fill="none" stroke="#DEA584" stroke-width="2.2" stroke-dasharray="2.8 1.4"/><circle cx="12" cy="12" r="6.8" fill="#DEA584" fill-opacity="0.18"/><text x="12" y="12" font-family="'Segoe UI', sans-serif" font-size="10.5" font-weight="900" text-anchor="middle" dominant-baseline="central" fill="#DEA584">R</text>`,
    langJava: `<path d="M4 19h14a2 2 0 0 0 2-2V9H4v8a2 2 0 0 0 2 2z" fill="#E76F00" fill-opacity="0.15" stroke="#E76F00" stroke-width="1.8" stroke-linecap="round"/><path d="M20 11h2a2 2 0 0 1 0 4h-2M2 21h18" stroke="#E76F00" stroke-width="1.8" stroke-linecap="round"/><path d="M8 3c0 2-2 3-2 5M13 2c0 2-2 3-2 5M17 3c0 2-2 3-2 5" stroke="#E11D48" stroke-width="1.8" stroke-linecap="round"/>`,
    langKotlin: `<rect width="20" height="20" x="2" y="2" rx="3.5" fill="#7F52FF"/><polygon points="2,22 22,2 2,2" fill="#E24462"/><polygon points="2,22 12,12 2,2" fill="#22D3EE" opacity="0.35"/><polygon points="22,2 12,12 22,22" fill="#F97316"/>`,
    langC: `<polygon points="12,2 21,7 21,17 12,22 3,17 3,7" fill="#00599C" fill-opacity="0.2" stroke="#00599C" stroke-width="1.6"/><text x="12" y="12" font-family="'Segoe UI', sans-serif" font-size="11" font-weight="900" text-anchor="middle" dominant-baseline="central" fill="#00599C">C</text>`,
    langCpp: `<polygon points="12,2 21,7 21,17 12,22 3,17 3,7" fill="#00599C" fill-opacity="0.2" stroke="#00599C" stroke-width="1.6"/><text x="12" y="12" font-family="'JetBrains Mono', sans-serif" font-size="8.5" font-weight="900" text-anchor="middle" dominant-baseline="central" fill="#00599C">C++</text>`,
    langCs: `<polygon points="12,2 21,7 21,17 12,22 3,17 3,7" fill="#68217A" fill-opacity="0.2" stroke="#68217A" stroke-width="1.6"/><text x="12" y="12" font-family="'JetBrains Mono', sans-serif" font-size="9" font-weight="900" text-anchor="middle" dominant-baseline="central" fill="#A855F7">C#</text>`,
    langPhp: `<ellipse cx="12" cy="12" rx="10" ry="6.5" fill="#777BB4" fill-opacity="0.2" stroke="#777BB4" stroke-width="1.6"/><text x="12" y="12" font-family="'Segoe UI', sans-serif" font-size="8" font-weight="900" letter-spacing="-0.5px" text-anchor="middle" dominant-baseline="central" fill="#777BB4">php</text>`,
    langRuby: `<polygon points="6 3 18 3 22 9 12 21 2 9" fill="#CC342D" fill-opacity="0.15" stroke="#CC342D" stroke-width="1.8" stroke-linejoin="round"/><line x1="2" y1="9" x2="22" y2="9" stroke="#CC342D" stroke-width="1.8"/><line x1="12" y1="21" x2="8" y2="9" stroke="#CC342D" stroke-width="1.8"/><line x1="12" y1="21" x2="16" y2="9" stroke="#CC342D" stroke-width="1.8"/><line x1="6" y1="3" x2="8" y2="9" stroke="#CC342D" stroke-width="1.8"/><line x1="18" y1="3" x2="16" y2="9" stroke="#CC342D" stroke-width="1.8"/>`,
    langSwift: `<path fill="#F05138" d="M21.5 16.5c-2.3 3.5-6.5 5.5-10.7 5.5 3.3-1.6 5.6-4.5 6.2-8.2-3.8 2.5-8.4 2.8-12.7 1 6.5-3.3 11-9.6 11.2-12.8-2 2-4.5 3.5-7.2 4.2 4.8-4 7.2-9 7.2-9s.5 4.8 3.8 9.5c2.6 3.7 6.4 6.8 2.2 9.8z"/>`,
    langDart: `<polygon points="12,2 21,11 15,21 3,9" fill="#0175C2"/><polygon points="12,2 21,11 12,14 3,9" fill="#00E5FF"/><polygon points="12,14 21,11 15,21" fill="#01579B"/>`,
    langLua: `<circle cx="12" cy="12" r="7.5" fill="none" stroke="#51A0D5" stroke-width="2"/><circle cx="15.8" cy="8.2" r="3.2" fill="#51A0D5"/><circle cx="19.2" cy="4.8" r="1.6" fill="#F7DF1E"/>`,

    // ── 3. SHELL & SCRIPTS ──
    langSh: `<rect x="2" y="3" width="20" height="18" rx="3.5" fill="#4EBD32" fill-opacity="0.15" stroke="#4EBD32" stroke-width="1.6"/><polyline points="6 8 10.5 12 6 16" fill="none" stroke="#4EBD32" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/><line x1="13.5" y1="16" x2="18.5" y2="16" stroke="#4EBD32" stroke-width="2" stroke-linecap="round"/>`,
    langPs: `<rect width="20" height="20" x="2" y="2" rx="3.5" fill="#012456" stroke="#2D7DD2" stroke-width="1.5"/><text x="12" y="12" font-family="'JetBrains Mono', monospace" font-size="8.5" font-weight="900" text-anchor="middle" dominant-baseline="central" fill="#58A6FF">PS</text>`,

    // ── 4. DATA, CONFIG & QUERY ──
    langJson: `<text x="12" y="12" font-family="'JetBrains Mono', monospace" font-size="14" font-weight="900" text-anchor="middle" dominant-baseline="central" fill="#CBCB41">{ }</text>`,
    langYaml: `<rect width="20" height="20" x="2" y="2" rx="3.5" fill="#CB171E" fill-opacity="0.18" stroke="#CB171E" stroke-width="1.6"/><text x="12" y="12" font-family="'JetBrains Mono', monospace" font-size="8" font-weight="900" text-anchor="middle" dominant-baseline="central" fill="#CB171E">YML</text>`,
    langToml: `<rect width="20" height="20" x="2" y="2" rx="3.5" fill="#9C4121" fill-opacity="0.18" stroke="#9C4121" stroke-width="1.6"/><text x="12" y="12" font-family="'JetBrains Mono', monospace" font-size="7.5" font-weight="900" text-anchor="middle" dominant-baseline="central" fill="#E06C38">TOML</text>`,
    langSql: `<ellipse cx="12" cy="5" rx="8" ry="3" fill="none" stroke="#336791" stroke-width="1.8" stroke-linecap="round"/><path d="M4 5v6c0 1.66 3.58 3 8 3s8-1.34 8-3V5" fill="none" stroke="#336791" stroke-width="1.8" stroke-linecap="round"/><path d="M4 11v6c0 1.66 3.58 3 8 3s8-1.34 8-3v-6" fill="none" stroke="#336791" stroke-width="1.8" stroke-linecap="round"/>`,
    langGraphql: `<polygon points="12,2 20.6,7 20.6,17 12,22 3.4,17 3.4,7" fill="none" stroke="#E10098" stroke-width="1.6"/><polygon points="12,5 18,15.5 6,15.5" fill="none" stroke="#E10098" stroke-width="1.4"/><circle cx="12" cy="2" r="1.8" fill="#E10098"/><circle cx="20.6" cy="7" r="1.8" fill="#E10098"/><circle cx="20.6" cy="17" r="1.8" fill="#E10098"/><circle cx="12" cy="22" r="1.8" fill="#E10098"/><circle cx="3.4" cy="17" r="1.8" fill="#E10098"/><circle cx="3.4" cy="7" r="1.8" fill="#E10098"/>`,

    // ── 5. DEVOPS, BUILD & SECURITY ──
    langDocker: `<path fill="#2496ED" d="M22.5 10.5c-.3-.2-1.3-.3-2.1.2-.5-1.1-1.4-1.8-2.5-1.8-.1 0-.3 0-.4.1-.7-1.4-2.1-2.4-3.8-2.4-.2 0-.4 0-.6.1V4h-9v5.2c-.7.1-1.4.4-2 .8C1.5 10.5.8 11.5.8 12.5c0 3.3 2.5 6 6.5 6 4.9 0 9.2-2.8 10.7-6.9.9.1 2.3-.2 3.1-1.4.6-.9.6-1.5.4-1.7zm-14.7-1H9.6v1.7H7.8V9.5zm2.3 0h1.8v1.7h-1.8V9.5zm-4.6 0h1.8v1.7H5.5V9.5zm4.6-2.2h1.8V9h-1.8V7.3zm-2.3 0H9.6V9H7.8V7.3zm4.6 2.2h1.8v1.7h-1.8V9.5zm2.3 0h1.8v1.7h-1.8V9.5z"/>`,
    langGit: `<circle cx="6" cy="6" r="2.5" fill="none" stroke="#F05032" stroke-width="2" stroke-linecap="round"/><circle cx="6" cy="18" r="2.5" fill="none" stroke="#F05032" stroke-width="2" stroke-linecap="round"/><circle cx="18" cy="9" r="2.5" fill="none" stroke="#F05032" stroke-width="2" stroke-linecap="round"/><path d="M6 8.5v7M8.2 7.2l7.6 1.8" fill="none" stroke="#F05032" stroke-width="2" stroke-linecap="round"/>`,
    langMakefile: `<path d="M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.77-3.77a6 6 0 0 1-7.94 7.94l-6.91 6.91a2.12 2.12 0 0 1-3-3l6.91-6.91a6 6 0 0 1 7.94-7.94l-3.76 3.76z" fill="none" stroke="#E06C75" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/>`,
    langEnv: `<line x1="4" y1="21" x2="4" y2="14" stroke="#8B949E" stroke-width="1.8" stroke-linecap="round"/><line x1="4" y1="10" x2="4" y2="3" stroke="#8B949E" stroke-width="1.8" stroke-linecap="round"/><line x1="12" y1="21" x2="12" y2="12" stroke="#8B949E" stroke-width="1.8" stroke-linecap="round"/><line x1="12" y1="8" x2="12" y2="3" stroke="#8B949E" stroke-width="1.8" stroke-linecap="round"/><line x1="20" y1="21" x2="20" y2="16" stroke="#8B949E" stroke-width="1.8" stroke-linecap="round"/><line x1="20" y1="12" x2="20" y2="3" stroke="#8B949E" stroke-width="1.8" stroke-linecap="round"/><line x1="1" y1="14" x2="7" y2="14" stroke="#8B949E" stroke-width="1.8" stroke-linecap="round"/><line x1="9" y1="8" x2="15" y2="8" stroke="#8B949E" stroke-width="1.8" stroke-linecap="round"/><line x1="17" y1="16" x2="23" y2="16" stroke="#8B949E" stroke-width="1.8" stroke-linecap="round"/>`,
    langLock: `<rect x="4" y="11" width="16" height="10" rx="2" fill="none" stroke="#E5A00D" stroke-width="1.8" stroke-linecap="round"/><circle cx="12" cy="16" r="1.5" fill="#E5A00D"/><path d="M7 11V7a5 5 0 0 1 10 0v4" fill="none" stroke="#E5A00D" stroke-width="1.8" stroke-linecap="round"/>`,

    // ── 6. DOCUMENTATION & MARKUP ──
    langMd: `<rect x="2" y="4" width="20" height="16" rx="2.5" fill="none" stroke="#42A5F5" stroke-width="1.8"/><text x="8.5" y="12" font-family="'Segoe UI', -apple-system, sans-serif" font-size="10" font-weight="900" text-anchor="middle" dominant-baseline="central" fill="#42A5F5">M</text><polyline points="15 8.5 15 15.5" stroke="#42A5F5" stroke-width="1.8" stroke-linecap="round"/><polyline points="12.5 13 15 15.5 17.5 13" stroke="#42A5F5" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/>`,
    langPdf: `<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" fill="none" stroke="#F43F5E" stroke-width="1.8" stroke-linecap="round"/><polyline points="14 2 14 8 20 8" fill="none" stroke="#F43F5E" stroke-width="1.8" stroke-linecap="round"/><text x="12" y="15" font-family="'JetBrains Mono', sans-serif" font-size="6.8" font-weight="900" text-anchor="middle" dominant-baseline="central" fill="#F43F5E">PDF</text>`,
    langText: `<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" fill="none" stroke="#94A3B8" stroke-width="1.8" stroke-linecap="round"/><polyline points="14 2 14 8 20 8" fill="none" stroke="#94A3B8" stroke-width="1.8" stroke-linecap="round"/><line x1="8" y1="13" x2="16" y2="13" stroke="#94A3B8" stroke-width="1.8" stroke-linecap="round"/><line x1="8" y1="17" x2="14" y2="17" stroke="#94A3B8" stroke-width="1.8" stroke-linecap="round"/>`,

    // ── 7. MEDIA, FONTS & BINARIES ──
    langImage: `<rect x="3" y="3" width="18" height="18" rx="3" fill="none" stroke="#AB47BC" stroke-width="1.8" stroke-linecap="round"/><circle cx="8.5" cy="8.5" r="1.5" fill="#AB47BC"/><polyline points="21 15 16 10 5 21" fill="none" stroke="#AB47BC" stroke-width="1.8" stroke-linecap="round"/>`,
    langAudio: `<circle cx="5.5" cy="17.5" r="3.5" fill="none" stroke="#E91E63" stroke-width="1.8" stroke-linecap="round"/><circle cx="17.5" cy="14.5" r="3.5" fill="none" stroke="#E91E63" stroke-width="1.8" stroke-linecap="round"/><path d="M9 17.5V4l12-3v13.5" fill="none" stroke="#E91E63" stroke-width="1.8" stroke-linecap="round"/>`,
    langVideo: `<polygon points="23 7 16 12 23 17 23 7" fill="none" stroke="#8B5CF6" stroke-width="1.8" stroke-linecap="round"/><rect x="1" y="5" width="15" height="14" rx="2" fill="none" stroke="#8B5CF6" stroke-width="1.8" stroke-linecap="round"/>`,
    langZip: `<path d="M21 8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16Z" fill="none" stroke="#FF9800" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/><line x1="12" y1="22" x2="12" y2="12" stroke="#FF9800" stroke-width="1.8" stroke-linecap="round"/><polyline points="3.27 6.96 12 12.01 20.73 6.96" fill="none" stroke="#FF9800" stroke-width="1.8" stroke-linecap="round"/>`,
    langFont: `<rect width="20" height="20" x="2" y="2" rx="3.5" fill="#00BCD4" fill-opacity="0.15" stroke="#00BCD4" stroke-width="1.5"/><text x="12" y="12" font-family="'Times New Roman', serif" font-size="12" font-weight="900" text-anchor="middle" dominant-baseline="central" fill="#00BCD4">A</text>`,
    langBinary: `<rect x="5" y="5" width="14" height="14" rx="2" fill="none" stroke="#00E5FF" stroke-width="1.8" stroke-linecap="round"/><line x1="9" y1="1" x2="9" y2="5" stroke="#00E5FF" stroke-width="1.8" stroke-linecap="round"/><line x1="15" y1="1" x2="15" y2="5" stroke="#00E5FF" stroke-width="1.8" stroke-linecap="round"/><line x1="9" y1="19" x2="9" y2="23" stroke="#00E5FF" stroke-width="1.8" stroke-linecap="round"/><line x1="15" y1="19" x2="15" y2="23" stroke="#00E5FF" stroke-width="1.8" stroke-linecap="round"/><line x1="1" y1="9" x2="5" y2="9" stroke="#00E5FF" stroke-width="1.8" stroke-linecap="round"/><line x1="1" y1="15" x2="5" y2="15" stroke="#00E5FF" stroke-width="1.8" stroke-linecap="round"/><line x1="19" y1="9" x2="23" y2="9" stroke="#00E5FF" stroke-width="1.8" stroke-linecap="round"/><line x1="19" y1="15" x2="23" y2="15" stroke="#00E5FF" stroke-width="1.8" stroke-linecap="round"/>`,

    // ── COMMON UI ACTIONS & CHROME ──
    folder: `<path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/>`,
    terminal: `<polyline points="4 17 10 11 4 5" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/><line x1="12" y1="19" x2="20" y2="19" stroke="currentColor" stroke-width="2" stroke-linecap="round"/>`,
    save: `<path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/><polyline points="17 21 17 13 7 13 7 21" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/><polyline points="7 3 7 8 15 8" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/>`,
    download: `<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/><polyline points="7 10 12 15 17 10" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/><line x1="12" y1="15" x2="12" y2="3" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/>`,
    edit: `<path d="M12 20h9" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/><path d="M16.5 3.5a2.121 2.121 0 0 1 3 3L7 19l-4 1 1-4L16.5 3.5z" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/>`,
    trash: `<polyline points="3 6 5 6 21 6" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/><line x1="10" y1="11" x2="10" y2="17" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/><line x1="14" y1="11" x2="14" y2="17" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/>`,
    palette: `<circle cx="13.5" cy="6.5" r=".5" fill="currentColor"/><circle cx="17.5" cy="10.5" r=".5" fill="currentColor"/><circle cx="8.5" cy="7.5" r=".5" fill="currentColor"/><circle cx="6.5" cy="12.5" r=".5" fill="currentColor"/><path d="M12 2C6.5 2 2 6.5 2 12s4.5 10 10 10c.926 0 1.648-.746 1.648-1.688 0-.437-.18-.835-.437-1.125-.29-.289-.438-.652-.438-1.125a1.64 1.64 0 0 1 1.668-1.668h1.996c3.051 0 5.563-2.512 5.563-5.563C22 6.5 17.5 2 12 2Z" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/>`,
    maximize: `<polyline points="15 3 21 3 21 9" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/><polyline points="9 21 3 21 3 15" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/><line x1="21" y1="3" x2="14" y2="10" stroke="currentColor" stroke-width="2" stroke-linecap="round"/><line x1="3" y1="21" x2="10" y2="14" stroke="currentColor" stroke-width="2" stroke-linecap="round"/>`,
    minimize: `<polyline points="4 14 10 14 10 20" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/><polyline points="20 10 14 10 14 4" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/><line x1="14" y1="10" x2="21" y2="3" stroke="currentColor" stroke-width="2" stroke-linecap="round"/><line x1="3" y1="21" x2="10" y2="14" stroke="currentColor" stroke-width="2" stroke-linecap="round"/>`,
    chevronUp: `<polyline points="18 15 12 9 6 15" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>`,
    minus: `<line x1="5" y1="12" x2="19" y2="12" stroke="currentColor" stroke-width="2" stroke-linecap="round"/>`,
    check: `<polyline points="20 6 9 17 4 12" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"/>`,
    messageSquare: `<path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/>`,
    cornerDownLeft: `<polyline points="9 10 4 15 9 20" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"/><path d="M20 4v7a4 4 0 0 1-4 4H4" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"/>`
};

function svgIcon(name, opts) {
    const inner = ICONS[name];
    if (!inner) return '';
    const size = (opts && opts.size) || 16;
    const cls = (opts && opts.className) ? 'ui-icon ' + opts.className : 'ui-icon';
    const title = (opts && opts.title) ? String(opts.title).replace(/&/g, '&amp;').replace(/"/g, '&quot;') : '';
    const titleAttr = title ? ` title="${title}"` : '';
    const ariaAttr = title ? '' : ' aria-hidden="true"';
    return `<svg width="${size}" height="${size}" viewBox="0 0 24 24" class="${cls}"${titleAttr}${ariaAttr}>${inner}</svg>`;
}

function fileIcon(name, opts) {
    if (!name) return svgIcon('langText', opts);
    const lower = name.toLowerCase();

    // 1. Exact filename matches
    if (lower === 'dockerfile' || lower.startsWith('docker-compose')) return svgIcon('langDocker', opts);
    if (lower === 'makefile' || lower === 'gnumakefile') return svgIcon('langMakefile', opts);
    if (lower === '.gitignore' || lower === '.gitattributes' || lower === '.gitmodules') return svgIcon('langGit', opts);
    if (lower === 'go.mod' || lower === 'go.sum' || lower === 'go.work') return svgIcon('langGoMod', opts);
    if (lower === 'cargo.toml') return svgIcon('langToml', opts);
    if (lower === 'cargo.lock' || lower === 'package-lock.json' || lower === 'yarn.lock' || lower === 'pnpm-lock.yaml') return svgIcon('langLock', opts);
    if (lower === 'gemfile' || lower === 'gemfile.lock') return svgIcon('langRuby', opts);
    if (lower === '.env' || lower.startsWith('.env.')) return svgIcon('langEnv', opts);

    const ext = getFileExt(lower);
    const EXT_MAP = {
        // Web & Frontend
        js: 'langJs', mjs: 'langJs', cjs: 'langJs',
        ts: 'langTs', mts: 'langTs', cts: 'langTs',
        jsx: 'langReact', tsx: 'langReact',
        vue: 'langVue',
        svelte: 'langSvelte',
        html: 'langHtml', htm: 'langHtml',
        css: 'langCss',
        scss: 'langSass', sass: 'langSass', less: 'langSass',

        // Systems & Backend
        go: 'langGo',
        py: 'langPy', pyw: 'langPy', ipynb: 'langPy', pyi: 'langPy',
        rs: 'langRs',
        java: 'langJava', class: 'langJava', jar: 'langJava',
        kt: 'langKotlin', kts: 'langKotlin',
        c: 'langC', h: 'langC',
        cpp: 'langCpp', cc: 'langCpp', cxx: 'langCpp', hpp: 'langCpp',
        cs: 'langCs', csx: 'langCs',
        php: 'langPhp',
        rb: 'langRuby',
        swift: 'langSwift',
        dart: 'langDart',
        lua: 'langLua',

        // Shell
        sh: 'langSh', bash: 'langSh', zsh: 'langSh',
        ps1: 'langPs', psm1: 'langPs',

        // Data & Config
        json: 'langJson', jsonc: 'langJson', json5: 'langJson',
        yaml: 'langYaml', yml: 'langYaml',
        toml: 'langToml',
        sql: 'langSql', db: 'langSql', sqlite: 'langSql',
        graphql: 'langGraphql', gql: 'langGraphql',
        ini: 'langEnv', conf: 'langEnv', cfg: 'langEnv',

        // Build / DevOps
        dockerfile: 'langDocker',
        mk: 'langMakefile',

        // Docs
        md: 'langMd', markdown: 'langMd', mdx: 'langMd',
        pdf: 'langPdf',
        txt: 'langText', log: 'langText',

        // Media
        png: 'langImage', jpg: 'langImage', jpeg: 'langImage', gif: 'langImage',
        webp: 'langImage', svg: 'langImage', ico: 'langImage', bmp: 'langImage', avif: 'langImage',
        mp3: 'langAudio', wav: 'langAudio', ogg: 'langAudio', flac: 'langAudio',
        mp4: 'langVideo', mkv: 'langVideo', mov: 'langVideo', webm: 'langVideo',

        // Archives
        zip: 'langZip', tar: 'langZip', gz: 'langZip', '7z': 'langZip', rar: 'langZip',

        // Fonts
        woff: 'langFont', woff2: 'langFont', ttf: 'langFont', otf: 'langFont',

        // Binaries
        exe: 'langBinary', dll: 'langBinary', so: 'langBinary', dylib: 'langBinary', bin: 'langBinary'
    };

    const iconKey = EXT_MAP[ext];
    if (iconKey) return svgIcon(iconKey, opts);
    return isTextFile(name) ? svgIcon('langText', opts) : svgIcon('langBinary', opts);
}
function fmtSize(b){if(!b)return'0 B';const u=['B','KB','MB','GB'];const i=Math.floor(Math.log(b)/Math.log(1024));return(b/Math.pow(1024,i)).toFixed(i>0?1:0)+' '+u[i];}
function esc(s){const d=document.createElement('div');d.textContent=s;return d.innerHTML;}

// ===== FILE OPERATION SERIALIZATION =====
// The host keeps a single pending-save slot, so file reads, saves and uploads
// must run one at a time. Uploads queue; reads/saves are rejected while busy.
function tryAcquireFileOp(){if(fileOpBusy)return false;fileOpBusy=true;return true;}
function beginFileOp(){if(!tryAcquireFileOp()){showToast('Another file operation is in progress');return false;}return true;}
function releaseFileOp(){uploadActive=false;fileOpBusy=false;processUploadQueue();}
function finishUpload(){if(!uploadActive)return;releaseFileOp();}
function fileOpDone(){if(uploadActive)return;releaseFileOp();}
// ===== SCOPED SESSION STORAGE =====
// Credentials and autoconnect states are stored per session ID to allow multi-host tabs.
// The display name is global and persistent (localStorage) so it is asked only once per browser.
function getSavedUsername(){
    try{return (localStorage.getItem('rmte_username')||sessionStorage.getItem('rmte_username')||'').trim();}catch(e){return '';}
}
function saveUsername(name){
    name=(name||'').trim();
    if(!name)return;
    try{localStorage.setItem('rmte_username',name);}catch(e){sessionStorage.setItem('rmte_username',name);}
}
function saveSessionCredentials(server, sid, password, username, autoconnect){
    if(server) sessionStorage.setItem('rmte_server', server);
    if(username) saveUsername(username);
    if(sid){
        sessionStorage.setItem('rmte_active_session', sid);
        const creds = {
            password: password || '',
            autoconnect: autoconnect === true
        };
        sessionStorage.setItem('rmte_sess_' + sid, JSON.stringify(creds));
    }
}

function getSessionCredentials(sid){
    if(!sid) return null;
    try {
        const raw = sessionStorage.getItem('rmte_sess_' + sid);
        if(!raw) return null;
        return JSON.parse(raw);
    } catch(e) {
        return null;
    }
}

function clearSessionAutoconnect(sid){
    if(!sid) sid = document.getElementById('sessionId').value || sessionStorage.getItem('rmte_active_session');
    if(!sid) return;
    const creds = getSessionCredentials(sid) || {};
    creds.autoconnect = false;
    sessionStorage.setItem('rmte_sess_' + sid, JSON.stringify(creds));
}

// ===== CONNECTION =====
async function connect() {
    const server=document.getElementById('server').value, sessionId=document.getElementById('sessionId').value;
    const password=document.getElementById('password').value, uname=document.getElementById('username').value.trim();
    hideError(); clearReconnectTimer();
    if(!sessionId||!password){showError('Session ID and Password required');return;}
    const btn=document.getElementById('connect-btn'); btn.innerText='Connecting...'; btn.disabled=true;
    myUsername=uname||('Web-'+Math.random().toString(36).slice(2,6).toUpperCase());
    manualDisconnect=false;
    if(ws){try{ws.close();}catch(e){}}
    try {
        const enc=new TextEncoder();
        const keyHash=await rmteCrypto.sha256(enc.encode(password));
        rawAesKeyBytes=keyHash;
        aesKey=await rmteCrypto.importKey(keyHash);
        const authHash=await rmteCrypto.sha256(enc.encode('rmte-auth:'+password));
        const authToken=Array.from(authHash).map(b=>b.toString(16).padStart(2,'0')).join('');
        ws=new WebSocket(server); ws.binaryType='arraybuffer';
        ws.onopen=()=>{
            _log.info('WS connected');
            sendRaw(JSON.stringify({type:'auth',role:'viewer',session_id:sessionId,viewer_id:myViewerId,viewer_name:myUsername,auth_token:authToken,protocol_version:'0.5',client:'web'}));
            startPing();
        };
        ws.onclose=e=>{
            _log.warn('WS closed',{code:e.code});
            stopPing();
            const s=document.getElementById('sb-connection');
            if(s){s.innerText='● Disconnected';s.style.color='#f85149';}
            if(isConnected&&!manualDisconnect){scheduleReconnect();}
            isConnected=false;
        };
        ws.onerror=()=>{
            _log.err('WS error');
            if(!isConnected){showError('Connection failed');btn.innerText='Establish Connection';btn.disabled=false;}
        };
        ws.onmessage=async e=>{try{typeof e.data==='string'?await onJson(JSON.parse(e.data)):await onBinary(new Uint8Array(e.data));}catch(err){_log.err('msg handler',err);}};
    } catch(e){_log.err('connect',e);showError(e.message);btn.innerText='Establish Connection';btn.disabled=false;}
}

function startPing(){
    stopPing();
    pingTimer=setInterval(()=>{
        if(ws&&ws.readyState===WebSocket.OPEN){
            sendJson({type:'control',action:'ping',t:Date.now()});
        }
    },5000);
}
function stopPing(){
    if(pingTimer){clearInterval(pingTimer);pingTimer=null;}
}

function scheduleReconnect(){
    reconnectAttempt++;
    const delay=Math.min(RECONNECT_BASE*Math.pow(2,reconnectAttempt-1),RECONNECT_MAX);
    _log.info(`Reconnecting in ${delay/1000}s (attempt ${reconnectAttempt})...`);
    const s=document.getElementById('sb-connection');
    let remaining=Math.ceil(delay/1000);
    if(s)s.innerText=`● Reconnecting in ${remaining}s...`;
    clearInterval(reconnectCountdown);
    reconnectCountdown=setInterval(()=>{
        remaining--;
        if(remaining>0&&s)s.innerText=`● Reconnecting in ${remaining}s...`;
    },1000);
    reconnectTimer=setTimeout(()=>{
        clearInterval(reconnectCountdown);reconnectCountdown=null;
        if(!manualDisconnect)connect();
    },delay);
}

function clearReconnectTimer(){
    if(reconnectTimer){clearTimeout(reconnectTimer);reconnectTimer=null;}
    if(reconnectCountdown){clearInterval(reconnectCountdown);reconnectCountdown=null;}
    reconnectAttempt=0;
}

function sendRaw(d){if(ws&&ws.readyState===1)ws.send(d);else _log.err('WS not open');}
function sendJson(m){_log.out('json:'+m.action,m);sendRaw(JSON.stringify(m));}
async function sendBin(tabId,plain){const iv=rmteCrypto.randomBytes(12);const ct=await rmteCrypto.encrypt(iv,aesKey,plain);const p=new Uint8Array(1+12+ct.byteLength);p[0]=tabId;p.set(iv,1);p.set(ct,13);sendRaw(p);}
// Data-channel frames add a 4-byte transfer id so concurrent file operations
// from different viewers can be told apart (the relay broadcasts binary frames).
let currentTransferId=0;
function newTransferId(){return (Math.floor(Math.random()*0xFFFFFFFF)>>>0)||1;}
async function sendDataChannel(plain){
    const id=currentTransferId>>>0;
    const iv=rmteCrypto.randomBytes(12);
    const ct=await rmteCrypto.encrypt(iv,aesKey,plain);
    const p=new Uint8Array(1+4+12+ct.byteLength);
    p[0]=DATA_CH;
    new DataView(p.buffer).setUint32(1,id,false);
    p.set(iv,5);p.set(ct,17);
    sendRaw(p);
}

// ===== MESSAGE HANDLERS =====
async function onJson(msg) {
    _log.in(msg.action||msg.type,msg);
    if(msg.type==='auth_success'){
        isConnected=true; reconnectAttempt=0;
        document.getElementById('setup').style.display='none';
        document.getElementById('terminal-container').style.display='flex';
        if(window.innerWidth <= 768){
            closeAllDrawers();
        }
        document.getElementById('sb-session').innerText='Session: '+(document.getElementById('sessionId').value);
        document.getElementById('sb-user').innerText=myUsername;
        const serverVal=document.getElementById('server').value;
        const sidVal=document.getElementById('sessionId').value;
        const passVal=document.getElementById('password').value;
        // Persist only a name the user actually typed (not the random Web-XXXX fallback).
        const typedName=document.getElementById('username').value.trim();
        saveSessionCredentials(serverVal, sidVal, passVal, typedName, true);
        const s=document.getElementById('sb-connection');if(s){s.innerText='● Connected';s.style.color='#3fb950';}
        // Reset any file-op state left over from a previous connection.
        waitingForFileData=false;pendingFileBytes=null;pendingEditorPath=null;pendingDownload=false;pendingDownloadName=null;fileOpBusy=false;uploadActive=false;currentTransferId=0;
        sendJson({type:'control',action:'get_tabs'});
        sendJson({type:'control',action:'get_events'});
        processUploadQueue();
        setTimeout(initDragAndDrop, 200);
        return;
    }
    if(msg.type==='error'){showError(msg.message);document.getElementById('connect-btn').innerText='Connect';document.getElementById('connect-btn').disabled=false;return;}
    if(msg.type!=='control')return;
    switch(msg.action){
        case'pong':{
            const lat=Math.max(1,Date.now()-(msg.t||Date.now()));
            const s=document.getElementById('sb-connection');
            if(s){
                s.innerText=`● Connected (${lat}ms)`;
                s.style.color=lat<100?'var(--color-green)':(lat<300?'var(--color-orange)':'var(--color-red)');
            }
            break;
        }
        case'tabs_list':{const t=msg.tabs||[];t.forEach(id=>addTermTabBtn(id));if(t.length>0){const firstTab=t.includes(0)?0:t[0];if(!terminals[firstTab])initTerminal(firstTab);setTimeout(()=>switchToTab('term-'+firstTab),50);}break;}
        case'tab_created':addTermTabBtn(msg.tab_id);break;
        case'tab_deleted':removeTermTab(msg.tab_id);break;
        case'sync_data':{const b=Uint8Array.from(atob(msg.data),c=>c.charCodeAt(0));await onBinary(b);}break;
        case'presence':updatePresence(msg.tabs);break;
        case'chat_history':{
            const cm=document.getElementById('chat-messages');
            if(cm){
                cm.innerHTML='';
                if(!msg.history || msg.history.length===0){
                    cm.innerHTML=`<div class="chat-empty-state"><div class="chat-empty-icon">${svgIcon('messageSquare', { size: 32 })}</div><div class="chat-empty-title">Session Chat</div><div class="chat-empty-sub">Messages sent here are encrypted and shared live with all Web and CLI collaborators.</div></div>`;
                } else {
                    msg.history.forEach(m=>appendChat(m.sender,m.message,m.time));
                }
            }
            break;
        }
        case'chat':appendChat(msg.sender,msg.message,msg.time);break;
        case'event_log':appendActivityLog(msg.event);break;
        case'events_history':renderActivityHistory(msg.events);break;
        // File Manager
        case'dir_data':renderFileList(msg.path,msg.files);break;
        case'read_file_start':_log.info('Waiting Tab255',{path:msg.path});waitingForFileData=true;pendingEditorPath=msg.path;break;
        case'ready_for_data':_log.info('Host ready, sending data');await sendPendingFile();break;
        case'file_saved':{
            if(uploadActive){
                showToast(`Uploaded ${basename(msg.path||'')}`);
                requestDir(currentFilePath);
                finishUpload();
                break;
            }
            _log.info('Saved',msg);
            const p=pendingEditorPath||msg.path;
            setEditorStatus(p,'Saved ✓');
            const id='file:'+p;
            if(editorTabs[id]){
                const val=editorTabs[id].cm?editorTabs[id].cm.getValue():(editorTabs[id].textarea?editorTabs[id].textarea.value:'');
                editorTabs[id].original=val;
                checkDirty(id);
            }
            requestDir(currentFilePath);
            fileOpDone();
            break;
        }
        case'file_created':case'dir_created':requestDir(currentFilePath);finishUpload();break;
        case'file_renamed':case'file_deleted':requestDir(currentFilePath);break;
        case'fm_error':_log.err('FM: '+msg.message);showToast(msg.message);if(uploadActive)finishUpload();else fileOpDone();break;
        default:_log.warn('Unhandled: '+msg.action);
    }
}
async function onBinary(raw) {
    const tabId=raw[0];
    try {
        if(tabId===DATA_CH){
            const transferId=new DataView(raw.buffer,raw.byteOffset,raw.byteLength).getUint32(1,false);
            if(transferId!==currentTransferId){_log.warn('Ignoring data-channel frame for another transfer',{transferId,currentTransferId});return;}
            const iv=raw.slice(5,17),ct=raw.slice(17);
            if(ct.length===0){_log.warn('Empty data-channel payload');return;}
            const dec=await rmteCrypto.decrypt(iv,aesKey,ct);
            if(!waitingForFileData){_log.warn('Tab255 data but not waiting');return;}
            waitingForFileData=false;
            if(pendingDownload){
                pendingDownload=false;
                const blob=new Blob([dec],{type:'application/octet-stream'});
                const url=URL.createObjectURL(blob);
                const a=document.createElement('a');
                a.href=url;
                a.download=pendingDownloadName||basename(pendingEditorPath)||'download';
                document.body.appendChild(a);
                a.click();
                document.body.removeChild(a);
                URL.revokeObjectURL(url);
                showToast(`Downloaded ${a.download}`);
            } else if(isImageFile(pendingEditorPath)){
                const mime=getImageMime(pendingEditorPath);
                const blob=new Blob([dec],{type:mime});
                const blobUrl=URL.createObjectURL(blob);
                openImageTab(pendingEditorPath,blobUrl,dec.byteLength,dec);
            } else {
                const txt=new TextDecoder().decode(dec);
                _log.info('File received',{bytes:dec.byteLength});
                openEditorTab(pendingEditorPath,txt);
            }
            pendingDownloadName=null;
            fileOpDone();
            return;
        }
        const iv=raw.slice(1,13),ct=raw.slice(13);
        if(ct.length===0){_log.warn('Empty ct',{tabId});return;}
        const dec=await rmteCrypto.decrypt(iv,aesKey,ct);
        if(!terminals[tabId])initTerminal(tabId);
        terminals[tabId].term.write(new Uint8Array(dec));
    }catch(e){
        _log.err('Decrypt fail',{tabId,e:e.message});
        if(tabId===DATA_CH)fileOpDone();
    }
}

// ===== TAB SYSTEM (WORKBENCH ARCHITECTURE) =====
// Separates Editor Tabs (upper pane) from Terminal Tabs (dock panel)
function activeTabId(){ return activeEditorTab || activeTerminalTab; }

function updateEditorEmptyState(){
    const emptyState = document.getElementById('editor-empty-state');
    if(!emptyState) return;
    const hasFiles = Object.keys(editorTabs).length > 0;
    emptyState.style.display = hasFiles ? 'none' : 'flex';
}

function switchToTab(id){
    if(id.startsWith('file:')){
        activateEditorTab(id);
    } else if(id.startsWith('term-')){
        activateTerminalTab(id);
    }
}

function activateEditorTab(id){
    activeEditorTab = id;
    document.querySelectorAll('#editor-tabs .tab-btn-container').forEach(b => b.classList.remove('active'));
    const el = document.getElementById('tab-' + CSS.escape(id));
    if(el) el.classList.add('active');

    // Show selected editor container in editor-container-wrap
    document.querySelectorAll('#editor-container-wrap > div').forEach(d => d.style.display = 'none');
    const cont = document.getElementById('content-' + CSS.escape(id));
    if(cont) cont.style.display = cont.dataset.type === 'editor' ? 'flex' : 'block';

    updateEditorEmptyState();

    if(window.innerWidth <= 768){
        setMobileWorkbenchPane('editor');
    }

    if(editorTabs[id] && editorTabs[id].type === 'image'){
        setActiveContext('media');
    } else {
        setActiveContext('editor');
    }

    if(editorTabs[id] && editorTabs[id].cm){
        setTimeout(() => {
            try { editorTabs[id].cm.refresh(); } catch(e){}
        }, 20);
    }
}

function activateTerminalTab(id){
    activeTerminalTab = id;
    document.querySelectorAll('#terminal-tabs .tab-btn-container').forEach(b => b.classList.remove('active'));
    const el = document.getElementById('tab-' + CSS.escape(id));
    if(el) el.classList.add('active');

    // Show selected terminal container in terminal-wrapper
    document.querySelectorAll('#terminal-wrapper > div').forEach(d => d.style.display = 'none');
    const cont = document.getElementById('content-' + CSS.escape(id));
    if(cont) cont.style.display = 'block';

    if(window.innerWidth <= 768){
        setMobileWorkbenchPane('terminal');
    }

    setActiveContext('terminal');

    const tid = parseInt(id.slice(5));
    if(terminals[tid]){
        setTimeout(() => {
            refitTerminal(tid);
            try { terminals[tid].term.focus(); } catch(e){}
        }, 20);
        updateTerminalGeometryStatus(terminals[tid].term.cols, terminals[tid].term.rows);
    }
    sendJson({type: 'control', action: 'set_focus', viewer_id: myViewerId, viewer_name: myUsername, tab_id: tid});
    sendJson({type: 'control', action: 'req_sync', tab_id: tid});
}

function focusEditorPane(){
    if(window.innerWidth <= 768){
        setMobileWorkbenchPane('editor');
    }
    if(activeEditorTab && editorTabs[activeEditorTab]){
        const et = editorTabs[activeEditorTab];
        if(et.type === 'image') setActiveContext('media');
        else setActiveContext('editor');
        if(et.cm){
            try { et.cm.focus(); } catch(e){}
        } else if(et.textarea){
            try { et.textarea.focus(); } catch(e){}
        }
    }
}

function focusTerminalPane(){
    if(terminalPanelHidden){
        hideTerminalPanel(false);
    }
    if(terminalPanelCollapsed){
        toggleTerminalPanel(false);
    }
    if(window.innerWidth <= 768){
        setMobileWorkbenchPane('terminal');
    }
    setActiveContext('terminal');
    if(activeTerminalTab){
        const tid = parseInt(activeTerminalTab.slice(5));
        if(terminals[tid] && terminals[tid].term){
            try { terminals[tid].term.focus(); } catch(e){}
            updateTerminalGeometryStatus(terminals[tid].term.cols, terminals[tid].term.rows);
        }
    }
}

function refitActive(){
    if(activeTerminalTab && activeTerminalTab.startsWith('term-')){
        const tid = parseInt(activeTerminalTab.slice(5));
        refitTerminal(tid);
    }
    if(activeEditorTab && editorTabs[activeEditorTab] && editorTabs[activeEditorTab].cm){
        try { editorTabs[activeEditorTab].cm.refresh(); } catch(e){}
    }
}

function refitTerminal(tid){
    const t = terminals[tid];
    if(!t) return;
    const panel = document.getElementById('terminal-panel');
    if(!panel || panel.classList.contains('is-collapsed') || panel.classList.contains('is-hidden')) return;
    const wrap = document.getElementById('terminal-wrapper');
    if(!wrap || wrap.clientWidth <= 0 || wrap.clientHeight <= 0) return;
    try {
        const prevCols = t.term.cols;
        const prevRows = t.term.rows;
        t.fitAddon.fit();
        if(t.term.cols !== prevCols || t.term.rows !== prevRows){
            sendJson({type: 'control', action: 'resize', tab_id: tid, cols: t.term.cols, rows: t.term.rows});
        }
        if(activeTerminalTab === 'term-' + tid){
            updateTerminalGeometryStatus(t.term.cols, t.term.rows);
        }
    } catch(e){}
}

// Terminal tabs
function initTerminal(tabId){
    if(terminals[tabId]) return;
    const id = 'term-' + tabId;
    const cont = document.createElement('div');
    cont.id = 'content-' + CSS.escape(id);
    cont.dataset.type = 'terminal';
    cont.style.cssText = 'height:100%;width:100%;display:' + (activeTerminalTab === id ? 'block' : 'none');
    document.getElementById('terminal-wrapper').appendChild(cont);
    const t = new Terminal({
        cursorBlink: true,
        convertEol: true,
        theme: {background: '#0d1117', foreground: '#e6edf3', cursor: '#58a6ff'},
        fontFamily: "'Consolas','Courier New',monospace",
        fontSize: 14
    });
    t.attachCustomKeyEventHandler(e => {
        if(e.type === 'keydown') {
            // Prevent Alt+ArrowUp/Down from sending escape sequences to shell PTY
            if(e.altKey && (e.key === 'ArrowUp' || e.key === 'ArrowDown')) {
                return false;
            }
            if(e.ctrlKey || e.metaKey) {
                const k = e.key.toLowerCase();
                if(k === 'c') {
                    if(t.hasSelection()){
                        const sel = t.getSelection();
                        if(navigator.clipboard && navigator.clipboard.writeText){
                            navigator.clipboard.writeText(sel).catch(()=>{});
                        }
                        return false;
                    } else {
                        sendBin(tabId, new Uint8Array([3]));
                        return false;
                    }
                }
                // Intercept Workbench & IDE shortcuts so xterm skips sending control bytes
                // to PTY (e.g. Ctrl+S sends 0x13 XOFF freezing shell, Ctrl+B sends 0x02).
                // Only intercept Ctrl+F if File Explorer is open (preserves readline forward-char in shell otherwise).
                if(k === '`' || e.key === '~' || k === 'b' || k === 's' || (k === 'f' && fileManagerOpen)) {
                    return false;
                }
            }
        }
        return true;
    });
    const fa = new FitAddon.FitAddon();
    t.loadAddon(fa);
    terminals[tabId] = {term: t, fitAddon: fa};
    t.open(cont);
    const panel = document.getElementById('terminal-panel');
    const wrap = document.getElementById('terminal-wrapper');
    const canFit = panel && !panel.classList.contains('is-collapsed') && !panel.classList.contains('is-hidden') && wrap && wrap.clientWidth > 0 && wrap.clientHeight > 0;
    if(canFit){
        try { fa.fit(); } catch(e){}
    }
    t.onData(d => handleTerminalData(tabId, d));
    try {
        t.onFocus(() => {
            setActiveContext('terminal');
        });
        t.onBlur(() => {
            setStickyModifier('ctrl', 0);
            setStickyModifier('alt', 0);
        });
    } catch(e){}
    if(t.parser && t.parser.registerCsiHandler){
        try {
            t.parser.registerCsiHandler({ prefix: '?', final: 'h' }, params => {
                if(params && params[0] === 1) terminals[tabId].appCursorKeys = true;
                return false;
            });
            t.parser.registerCsiHandler({ prefix: '?', final: 'l' }, params => {
                if(params && params[0] === 1) terminals[tabId].appCursorKeys = false;
                return false;
            });
        } catch(e){}
    }
    addTermTabBtn(tabId);
    if(!activeTerminalTab || !document.querySelector('#terminal-tabs .tab-btn-container.active')){
        activateTerminalTab(id);
    }
}

// ── Mobile Virtual Key Accessory Bar & Sticky Modifiers ──
function getActiveTerminalTabId(){
    if(!activeTerminalTab || !activeTerminalTab.startsWith('term-')) return 0;
    return parseInt(activeTerminalTab.slice(5), 10) || 0;
}

function setStickyModifier(mod, val){
    if(mod === 'ctrl'){
        stickyCtrl = val;
        const btn = document.getElementById('mkey-ctrl');
        if(btn){
            btn.classList.toggle('active', stickyCtrl === 1);
            btn.classList.toggle('locked', stickyCtrl === 2);
        }
    } else if(mod === 'alt'){
        stickyAlt = val;
        const btn = document.getElementById('mkey-alt');
        if(btn){
            btn.classList.toggle('active', stickyAlt === 1);
            btn.classList.toggle('locked', stickyAlt === 2);
        }
    }
}

function toggleVirtualModifier(mod){
    if(mod === 'ctrl'){
        setStickyModifier('ctrl', (stickyCtrl + 1) % 3);
    } else if(mod === 'alt'){
        setStickyModifier('alt', (stickyAlt + 1) % 3);
    }
}

function isAppCursorMode(tabId){
    const t = terminals[tabId]?.term;
    if(!t) return false;
    if(t.modes && typeof t.modes.applicationCursorKeysMode === 'boolean'){
        return t.modes.applicationCursorKeysMode;
    }
    if(t._core && t._core.coreService && t._core.coreService.decPrivateModes){
        return Boolean(t._core.coreService.decPrivateModes.applicationCursorKeys);
    }
    return Boolean(terminals[tabId]?.appCursorKeys);
}

function sendVirtualKey(key){
    const tid = getActiveTerminalTabId();
    let bytes = null;
    switch(key){
        case 'esc':
            bytes = new Uint8Array([0x1b]);
            if(stickyCtrl === 1) setStickyModifier('ctrl', 0);
            if(stickyAlt === 1) setStickyModifier('alt', 0);
            break;
        case 'tab':
            bytes = new Uint8Array([0x09]);
            break;
        case 'sigint':
            bytes = new Uint8Array([0x03]);
            break;
        case 'up':
            bytes = new TextEncoder().encode(isAppCursorMode(tid) ? '\x1bOA' : '\x1b[A');
            break;
        case 'down':
            bytes = new TextEncoder().encode(isAppCursorMode(tid) ? '\x1bOB' : '\x1b[B');
            break;
        case 'left':
            bytes = new TextEncoder().encode(isAppCursorMode(tid) ? '\x1bOD' : '\x1b[D');
            break;
        case 'right':
            bytes = new TextEncoder().encode(isAppCursorMode(tid) ? '\x1bOC' : '\x1b[C');
            break;
    }
    if(bytes){
        sendBin(tid, bytes);
    }
    if(terminals[tid] && terminals[tid].term){
        try { terminals[tid].term.focus(); } catch(e){}
    }
}

function toggleVirtualKeyRow(){
    const row = document.getElementById('mobile-terminal-keys');
    if(!row) return;
    row.classList.toggle('is-hidden');
    const isHidden = row.classList.contains('is-hidden');
    const toggleBtn = document.getElementById('mkey-toggle');
    if(toggleBtn){
        toggleBtn.classList.toggle('active', !isHidden);
    }
    setTimeout(refitActive, 50);
}

function handleTerminalData(tabId, data){
    if(stickyCtrl > 0){
        if(data.length === 1){
            const code = data.charCodeAt(0);
            if((code >= 65 && code <= 90) || (code >= 97 && code <= 122) || code === 64 || (code >= 91 && code <= 95)){
                data = String.fromCharCode(code & 0x1f);
            }
        }
        if(stickyCtrl === 1) setStickyModifier('ctrl', 0);
    }
    if(stickyAlt > 0){
        if(data.length === 1 || data.startsWith('\x1b')){
            data = '\x1b' + data;
        }
        if(stickyAlt === 1) setStickyModifier('alt', 0);
    }
    sendBin(tabId, new TextEncoder().encode(data));
}

let draggedTabEl = null;
function enableTabDrag(el){
    el.draggable = true;
    el.addEventListener('dragstart', e => {
        draggedTabEl = el;
        el.classList.add('tab-dragging');
        e.dataTransfer.effectAllowed = 'move';
        e.dataTransfer.setData('text/plain', el.id);
    });
    el.addEventListener('dragend', () => {
        el.classList.remove('tab-dragging');
        draggedTabEl = null;
    });
    el.addEventListener('dragover', e => {
        e.preventDefault();
        e.dataTransfer.dropEffect = 'move';
        if(!draggedTabEl || draggedTabEl === el) return;
        const parent = el.parentNode;
        // Strictly scope drag reordering within the same parent tab container!
        if(!parent || draggedTabEl.parentNode !== parent) return;
        const rect = el.getBoundingClientRect();
        const mid = rect.left + rect.width / 2;
        if(e.clientX < mid){
            parent.insertBefore(draggedTabEl, el);
        } else {
            parent.insertBefore(draggedTabEl, el.nextSibling);
        }
    });
}

function addTermTabBtn(tabId){
    const id = 'term-' + tabId;
    if(document.getElementById('tab-' + CSS.escape(id))) return;
    const c = document.createElement('div');
    c.id = 'tab-' + CSS.escape(id);
    c.className = 'tab-btn-container' + (activeTerminalTab === id ? ' active' : '');
    c.onclick = () => activateTerminalTab(id);
    enableTabDrag(c);
    const icon = document.createElement('span'); icon.className = 'tab-icon'; icon.innerHTML = svgIcon('terminal', { size: 14 });
    const content = document.createElement('div'); content.className = 'tab-btn-content';
    const title = document.createElement('span'); title.className = 'tab-title-text'; title.innerText = 'Tab ' + tabId;
    const sub = document.createElement('span'); sub.id = 'tab-subtext-' + tabId; sub.className = 'tab-subtext';
    content.appendChild(title); content.appendChild(sub);
    const close = document.createElement('button'); close.className = 'tab-close-btn'; close.innerText = '×';
    close.onclick = async e => {
        e.stopPropagation();
        if(await uiConfirm('Delete Tab ' + tabId + '?', {okText: 'Delete', danger: true})){
            sendJson({type: 'control', action: 'delete_tab', tab_id: tabId});
        }
    };
    c.appendChild(icon); c.appendChild(content); c.appendChild(close);
    const parentTabs = document.getElementById('terminal-tabs');
    if(parentTabs) parentTabs.appendChild(c);
}

function removeTermTab(tabId){
    const id = 'term-' + tabId;
    const el = document.getElementById('tab-' + CSS.escape(id)); if(el) el.remove();
    const cont = document.getElementById('content-' + CSS.escape(id)); if(cont) cont.remove();
    if(terminals[tabId]){
        try { terminals[tabId].term.dispose(); } catch(e){}
        delete terminals[tabId];
    }
    if(activeTerminalTab === id){
        const k = Object.keys(terminals);
        if(k.length) activateTerminalTab('term-' + k[0]);
        else {
            activeTerminalTab = null;
            if(activeEditorTab && editorTabs[activeEditorTab]){
                setActiveContext(editorTabs[activeEditorTab].type === 'image' ? 'media' : 'editor');
            } else if(webPreviewOpen){
                setActiveContext('preview');
            } else {
                setActiveContext(null);
            }
        }
    }
}
function requestNewTab(){
    sendJson({type: 'control', action: 'request_new_tab'});
    if(terminalPanelHidden){
        hideTerminalPanel(false);
    }
    if(terminalPanelCollapsed){
        toggleTerminalPanel(false);
    }
    if(window.innerWidth <= 768){
        setMobileWorkbenchPane('terminal');
    }
}

// Editor tabs
function checkDirty(id){
    const et = editorTabs[id]; if(!et || (!et.cm && !et.textarea)) return;
    const val = et.cm ? et.cm.getValue() : (et.textarea ? et.textarea.value : '');
    const isDirty = val !== et.original;
    const tabEl = document.getElementById('tab-' + CSS.escape(id));
    if(tabEl) tabEl.classList.toggle('dirty', isDirty);
}

function openEditorTab(path, text){
    const id = 'file:' + path;
    if(editorTabs[id]){
        if(editorTabs[id].cm){
            editorTabs[id].cm.setValue(text);
            setTimeout(() => { try { editorTabs[id].cm.refresh(); } catch(e){} }, 20);
        } else if(editorTabs[id].textarea){
            editorTabs[id].textarea.value = text;
        }
        editorTabs[id].original = text;
        checkDirty(id);
        activateEditorTab(id);
        return;
    }
    const hasBinary = (function(str){
        if(!str) return false;
        const len = Math.min(str.length, 1024);
        for(let i=0; i<len; i++) if(str.charCodeAt(i) === 0) return true;
        return false;
    })(text);
    const isText = !hasBinary && isTextFile(path);
    // Container in editor-container-wrap
    const cont = document.createElement('div');
    cont.id = 'content-' + CSS.escape(id);
    cont.dataset.type = 'editor';
    cont.className = 'editor-container';
    cont.style.display = 'none';

    // Bar
    const bar = document.createElement('div');
    bar.className = 'editor-bar';
    bar.innerHTML = `<span class="editor-path">${esc(path)}</span><span class="editor-lang">${getLang(path)}</span><span class="editor-status" id="estatus-${CSS.escape(id)}"></span>`;
    if(isText){
        const saveBtn = document.createElement('button');
        saveBtn.className = 'editor-save';
        saveBtn.innerHTML = `${svgIcon('save', { size: 14 })}<span>Save</span>`;
        saveBtn.onclick = () => saveEditor(id, path);
        bar.appendChild(saveBtn);
    }
    cont.appendChild(bar);

    let cmInstance = null, taInstance = null;
    if(isText){
        if(typeof CodeMirror !== 'undefined'){
            try {
                cmInstance = CodeMirror(cont, {
                    value: text,
                    mode: getCodeMirrorMode(path),
                    theme: 'material-darker',
                    lineNumbers: true,
                    indentUnit: 4,
                    tabSize: 4,
                    indentWithTabs: true,
                    lineWrapping: window.innerWidth <= 768
                });
                cmInstance.on('change', () => checkDirty(id));
                const cursorListener = () => {
                    if (activeEditorTab === id) updateCursorStatus();
                };
                cmInstance.on('cursorActivity', cursorListener);
                cmInstance.on('focus', () => {
                    setActiveContext('editor');
                });
                editorTabs[id] = {path, cm: cmInstance, textarea: null, original: text, wrapOverride: null, cursorListener, syntaxOverride: null};
            } catch(cmErr){
                console.warn('CodeMirror failed to initialize, falling back to textarea:', cmErr);
                cmInstance = null;
            }
        }
        if(!cmInstance){
            taInstance = document.createElement('textarea');
            taInstance.className = 'editor-textarea';
            taInstance.spellcheck = false;
            taInstance.value = text;
            taInstance.addEventListener('keydown', e => {
                if(e.key === 'Tab'){
                    e.preventDefault();
                    const s = taInstance.selectionStart;
                    taInstance.value = taInstance.value.substring(0, s) + '\t' + taInstance.value.substring(taInstance.selectionEnd);
                    taInstance.selectionStart = taInstance.selectionEnd = s + 1;
                }
            });
            taInstance.addEventListener('input', () => checkDirty(id));
            taInstance.addEventListener('click', () => {
                if (activeEditorTab === id) updateCursorStatus();
            });
            taInstance.addEventListener('keyup', () => {
                if (activeEditorTab === id) updateCursorStatus();
            });
            taInstance.addEventListener('focus', () => {
                setActiveContext('editor');
            });
            cont.appendChild(taInstance);
            editorTabs[id] = {path, cm: null, textarea: taInstance, original: text, wrapOverride: null, cursorListener: null, syntaxOverride: null};
        }
    } else {
        const bp = document.createElement('div');
        bp.className = 'binary-preview';
        bp.innerHTML = `<span class="bp-icon">${fileIcon(path)}</span><span class="bp-msg">Binary file — cannot preview</span><span class="bp-msg" style="font-size:11px;color:#484f58">${fmtSize(text.length)} · ${path}</span>`;
        cont.appendChild(bp);
        editorTabs[id] = {path, cm: null, textarea: null, original: null};
    }
    const wrap = document.getElementById('editor-container-wrap') || document.getElementById('terminal-wrapper');
    wrap.appendChild(cont);

    // Tab button in editor-tabs
    const c = document.createElement('div');
    c.id = 'tab-' + CSS.escape(id);
    c.className = 'tab-btn-container editor-tab';
    c.onclick = () => activateEditorTab(id);
    enableTabDrag(c);
    const icon = document.createElement('span'); icon.className = 'tab-icon'; icon.innerHTML = fileIcon(path);
    const content = document.createElement('div'); content.className = 'tab-btn-content';
    const title = document.createElement('span'); title.className = 'tab-title-text'; title.innerText = basename(path);
    content.appendChild(title);

    const dirtyDot = document.createElement('span'); dirtyDot.className = 'tab-dirty-dot'; dirtyDot.innerText = '●'; dirtyDot.title = 'Unsaved changes';
    const close = document.createElement('button'); close.className = 'tab-close-btn'; close.innerText = '×';
    close.onclick = e => { e.stopPropagation(); closeEditorTab(id); };

    c.appendChild(icon); c.appendChild(content); c.appendChild(dirtyDot); c.appendChild(close);
    const parentEditorTabs = document.getElementById('editor-tabs');
    if(parentEditorTabs) parentEditorTabs.appendChild(c);

    activateEditorTab(id);
    if(cmInstance){
        requestAnimationFrame(() => { try { cmInstance.refresh(); } catch(e){} });
        setTimeout(() => { try { cmInstance.refresh(); } catch(e){} }, 30);
        setTimeout(() => { try { cmInstance.refresh(); } catch(e){} }, 120);
    }
}

function openImageTab(path, blobUrl, sizeBytes, rawBytes){
    const id = 'file:' + path;
    const isSvg = getFileExt(path) === 'svg';

    if(editorTabs[id]){
        if(editorTabs[id].blobUrl && editorTabs[id].blobUrl !== blobUrl){
            try { URL.revokeObjectURL(editorTabs[id].blobUrl); } catch(e){}
        }
        editorTabs[id].blobUrl = blobUrl;
        editorTabs[id].sizeBytes = sizeBytes;
        editorTabs[id].rawBytes = rawBytes;
        const imgEl = document.getElementById('img-el-' + CSS.escape(id));
        if(imgEl) imgEl.src = blobUrl;
        activateEditorTab(id);
        return;
    }

    const cont = document.createElement('div');
    cont.id = 'content-' + CSS.escape(id);
    cont.dataset.type = 'image';
    cont.className = 'editor-container image-preview-container';
    cont.style.display = 'none';

    const bar = document.createElement('div');
    bar.className = 'editor-bar';
    bar.innerHTML = `
        <span class="editor-path">${esc(path)}</span>
        <span class="editor-size">${fmtSize(sizeBytes)}</span>
        <span class="image-dimensions" id="img-dim-${CSS.escape(id)}">Measuring...</span>
    `;

    const actions = document.createElement('div');
    actions.className = 'image-bar-actions';

    if(isSvg && rawBytes){
        const editSvgBtn = document.createElement('button');
        editSvgBtn.className = 'editor-btn-secondary';
        editSvgBtn.innerHTML = `${svgIcon('palette', { size: 14 })}<span>Edit as Code</span>`;
        editSvgBtn.title = 'Edit SVG code in editor';
        editSvgBtn.onclick = async () => {
            const txt = new TextDecoder().decode(rawBytes);
            await closeEditorTab(id);
            openEditorTab(path, txt);
        };
        actions.appendChild(editSvgBtn);
    }

    const dlBtn = document.createElement('button');
    dlBtn.className = 'editor-btn-secondary';
    dlBtn.innerHTML = `${svgIcon('download', { size: 14 })}<span>Download</span>`;
    dlBtn.title = 'Download file';
    dlBtn.onclick = () => {
        const a = document.createElement('a');
        a.href = blobUrl;
        a.download = basename(path);
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
    };
    actions.appendChild(dlBtn);
    bar.appendChild(actions);
    cont.appendChild(bar);

    const viewport = document.createElement('div');
    viewport.className = 'image-viewport';
    const img = document.createElement('img');
    img.id = 'img-el-' + CSS.escape(id);
    img.className = 'image-preview-img';
    img.alt = basename(path);
    img.src = blobUrl;
    img.onload = () => {
        const dimEl = document.getElementById('img-dim-' + CSS.escape(id));
        if(dimEl && img.naturalWidth) {
            dimEl.innerText = `${img.naturalWidth} × ${img.naturalHeight} px`;
        }
        if(editorTabs[id]){
            editorTabs[id].naturalWidth = img.naturalWidth;
            editorTabs[id].naturalHeight = img.naturalHeight;
            if(activeEditorTab === id && activeContext === 'media'){
                updateMediaStatus(editorTabs[id]);
            }
        }
    };
    img.onerror = () => {
        const dimEl = document.getElementById('img-dim-' + CSS.escape(id));
        if(dimEl) dimEl.innerText = 'Image load failed';
        if(editorTabs[id] && activeEditorTab === id && activeContext === 'media'){
            updateMediaStatus(editorTabs[id]);
        }
    };
    viewport.appendChild(img);
    cont.appendChild(viewport);

    const wrap = document.getElementById('editor-container-wrap') || document.getElementById('terminal-wrapper');
    wrap.appendChild(cont);

    const c = document.createElement('div');
    c.id = 'tab-' + CSS.escape(id);
    c.className = 'tab-btn-container editor-tab';
    c.onclick = () => activateEditorTab(id);
    enableTabDrag(c);
    const icon = document.createElement('span'); icon.className = 'tab-icon'; icon.innerHTML = fileIcon(path);
    const content = document.createElement('div'); content.className = 'tab-btn-content';
    const title = document.createElement('span'); title.className = 'tab-title-text'; title.innerText = basename(path);
    content.appendChild(title);

    const close = document.createElement('button'); close.className = 'tab-close-btn'; close.innerText = '×';
    close.onclick = e => { e.stopPropagation(); closeEditorTab(id); };

    c.appendChild(icon); c.appendChild(content); c.appendChild(close);
    const parentEditorTabs = document.getElementById('editor-tabs');
    if(parentEditorTabs) parentEditorTabs.appendChild(c);

    editorTabs[id] = { path, type: 'image', blobUrl, sizeBytes, rawBytes, cm: null, textarea: null, original: null, naturalWidth: 0, naturalHeight: 0 };
    activateEditorTab(id);
}

async function closeEditorTab(id){
    const et = editorTabs[id];
    if(et){
        if(et.type === 'image' && et.blobUrl){
            try { URL.revokeObjectURL(et.blobUrl); } catch(e){}
        }
        if(et.cm || et.textarea){
            const val = et.cm ? et.cm.getValue() : et.textarea.value;
            if(val !== et.original && !await uiConfirm('File has unsaved changes. Close anyway?', {title: 'Unsaved Changes', okText: 'Close', danger: true})) return;
            if(et.cm){
                if(et.cursorListener){
                    try { et.cm.off('cursorActivity', et.cursorListener); } catch(e){}
                }
                try { et.cm.toTextArea && et.cm.toTextArea(); } catch(e){}
            }
        }
    }
    const el = document.getElementById('tab-' + CSS.escape(id)); if(el) el.remove();
    const cont = document.getElementById('content-' + CSS.escape(id)); if(cont) cont.remove();
    delete editorTabs[id];
    if(activeEditorTab === id){
        const et2 = Object.keys(editorTabs);
        if(et2.length){
            activateEditorTab(et2[et2.length - 1]);
        } else {
            activeEditorTab = null;
            updateEditorEmptyState();
            if(activeTerminalTab){
                setActiveContext('terminal');
            } else if(webPreviewOpen){
                setActiveContext('preview');
            } else {
                setActiveContext(null);
            }
        }
    }
}

function saveEditor(id,path){
    const et=editorTabs[id];if(!et || (!et.cm && !et.textarea))return;
    if(!beginFileOp())return;
    currentTransferId=newTransferId();
    const val=et.cm?et.cm.getValue():(et.textarea?et.textarea.value:'');
    pendingFileBytes=new TextEncoder().encode(val);
    pendingEditorPath=path;
    setEditorStatus(path,'Saving...');
    sendJson({type:'control',action:'prepare_save',path,transfer_id:currentTransferId});
}

function setEditorStatus(path,text){
    const id='file:'+path;
    const el=document.getElementById('estatus-'+CSS.escape(id));
    if(el){el.innerText=text;if(text)setTimeout(()=>{if(el.innerText===text)el.innerText='';},3000);}
    if(text==='Saved ✓'&&editorTabs[id]){
        const val=editorTabs[id].cm?editorTabs[id].cm.getValue():(editorTabs[id].textarea?editorTabs[id].textarea.value:'');
        editorTabs[id].original=val;
        checkDirty(id);
    }
}
async function sendPendingFile(){if(!pendingFileBytes){_log.err('No pending data');if(uploadActive)finishUpload();else fileOpDone();return;}const d=pendingFileBytes;pendingFileBytes=null;await sendDataChannel(d);}

// ===== FILE MANAGER =====
function toggleFileManager(force){
    fileManagerOpen=typeof force==='boolean'?force:!fileManagerOpen;
    const fe = document.getElementById('file-explorer');
    const isMobile = window.innerWidth <= 768;
    if(fe){
        fe.classList.toggle('drawer-open', isMobile && fileManagerOpen);
        if(!isMobile){
            fe.style.display = fileManagerOpen ? 'flex' : 'none';
        } else {
            fe.style.display = '';
        }
    }
    const resizer=document.getElementById('fe-resizer');
    if(resizer)resizer.style.display=(!isMobile && fileManagerOpen)?'block':'none';
    const b=document.getElementById('toggle-files-btn');
    if(b)b.classList.toggle('active',fileManagerOpen);
    if(fileManagerOpen)requestDir(currentFilePath);
    updateDrawerBackdrop();
    setTimeout(refitActive,100);
}

function updateDrawerBackdrop(){
    const backdrop = document.getElementById('drawer-backdrop');
    if(!backdrop) return;
    const isMobile = window.innerWidth <= 768;
    const fe = document.getElementById('file-explorer');
    const sb = document.getElementById('users-sidebar');
    const feOpen = isMobile && fe && fe.classList.contains('drawer-open');
    const sbOpen = isMobile && sb && sb.classList.contains('drawer-open');
    if(feOpen || sbOpen){
        backdrop.style.display = 'block';
    } else {
        backdrop.style.display = 'none';
    }
}

function closeSidebarDrawer(){
    const sb = document.getElementById('users-sidebar');
    if(sb) sb.classList.remove('drawer-open');
    const ws = document.getElementById('workspace');
    if(ws) ws.classList.add('sidebar-collapsed');
    const collabBtn = document.getElementById('toggle-sidebar-btn');
    const chatBtn = document.getElementById('toggle-chat-btn');
    const actBtn = document.getElementById('toggle-activity-btn');
    if(collabBtn) collabBtn.classList.remove('active');
    if(chatBtn) chatBtn.classList.remove('active');
    if(actBtn) actBtn.classList.remove('active');
    updateDrawerBackdrop();
    setTimeout(refitActive, 50);
}

function closeAllDrawers(){
    const fe = document.getElementById('file-explorer');
    const sb = document.getElementById('users-sidebar');
    if(fe) fe.classList.remove('drawer-open');
    if(sb) sb.classList.remove('drawer-open');
    const backdrop = document.getElementById('drawer-backdrop');
    if(backdrop) backdrop.style.display = 'none';

    if(window.innerWidth <= 768){
        fileManagerOpen = false;
        const b = document.getElementById('toggle-files-btn');
        if(b) b.classList.remove('active');
        closeSidebarDrawer();
    } else {
        if(fe) fe.style.display = fileManagerOpen ? 'flex' : 'none';
        const resizer = document.getElementById('fe-resizer');
        if(resizer) resizer.style.display = fileManagerOpen ? 'block' : 'none';
        const b = document.getElementById('toggle-files-btn');
        if(b) b.classList.toggle('active', fileManagerOpen);
    }
}
function requestDir(p){currentFilePath=p;sendJson({type:'control',action:'req_dir',path:p});}

function renderFileList(path,files){
    renderBreadcrumb(path);
    hideInlineInput();
    const filterInput=document.getElementById('fe-filter');
    if(filterInput)filterInput.value='';
    const list=document.getElementById('fe-list');list.innerHTML='';
    const parentPath = path.endsWith('/') ? (path + '..') : (path + '/..');
    list.appendChild(mkItem(svgIcon('folder', { size: 18 }),'..','',parentPath,true,()=>requestDir(parentPath)));
    if(!files||!files.length){list.appendChild(Object.assign(document.createElement('div'),{className:'fe-empty',innerText:'Empty directory'}));return;}
    files.forEach(f=>{
        const fp=(path.endsWith('/')?path:path+'/')+f.name;
        if(f.is_dir){list.appendChild(mkItem(svgIcon('folder', { size: 18 }),f.name,'',fp,true,()=>requestDir(fp)));}
        else{list.appendChild(mkItem(fileIcon(f.name, { size: 18 }),f.name,fmtSize(f.size),fp,false,()=>{
            _log.info('Open file',{path:fp,isImage:isImageFile(fp),isText:isTextFile(fp)});
            if(isImageFile(fp)){
                openImageForView(fp);
            } else if(isBinaryFile(fp)){
                downloadFile(fp,f.name);
            } else {
                openFileForEdit(fp);
            }
        }));}
    });
}

function filterFiles(query){
    const q=(query||'').toLowerCase().trim();
    const items=document.querySelectorAll('#fe-list .fe-item');
    items.forEach(it=>{
        const name=it.querySelector('.fe-name')?.innerText||'';
        if(name==='..'){it.style.display='flex';return;}
        it.style.display=(!q||name.toLowerCase().includes(q))?'flex':'none';
    });
}

function downloadFile(path,name,e){
    if(e)e.stopPropagation();
    if(!beginFileOp())return;
    currentTransferId=newTransferId();
    pendingDownload=true;
    pendingDownloadName=name||basename(path);
    showToast(`Downloading ${pendingDownloadName}...`);
    sendJson({type:'control',action:'req_read_file',path,transfer_id:currentTransferId});
}

function openImageForView(path){
    if(!beginFileOp())return;
    currentTransferId=newTransferId();
    pendingDownload=false;
    pendingDownloadName=null;
    sendJson({type:'control',action:'req_read_file',path,transfer_id:currentTransferId});
}

function openFileForEdit(path){
    if(!beginFileOp())return;
    currentTransferId=newTransferId();
    pendingDownload=false;
    pendingDownloadName=null;
    sendJson({type:'control',action:'req_read_file',path,transfer_id:currentTransferId});
}

function mkItem(icon,name,size,fullPath,isDir,onclick){
    const item=document.createElement('div');item.className='fe-item'+(isDir?' is-dir':'');
    const iconEl=document.createElement('span');iconEl.className='fe-icon';iconEl.innerHTML=icon;
    const nameEl=document.createElement('span');nameEl.className='fe-name';nameEl.innerText=name;
    const sizeEl=document.createElement('span');sizeEl.className='fe-size';sizeEl.innerText=size;
    const aDiv=document.createElement('div');aDiv.className='fe-item-actions';
    // Download action for files
    if(!isDir){
        const dlBtn=document.createElement('button');dlBtn.innerHTML=svgIcon('download', { size: 13 });dlBtn.title='Download';dlBtn.className='fe-download';
        dlBtn.onclick=e=>{e.stopPropagation();downloadFile(fullPath,name,e);};
        aDiv.appendChild(dlBtn);
    }
    // Rename
    const renBtn=document.createElement('button');renBtn.innerHTML=svgIcon('edit', { size: 13 });renBtn.title='Rename';
    renBtn.onclick=e=>{e.stopPropagation();startInlineRename(item,nameEl,fullPath,name);};
    // Delete
    const delBtn=document.createElement('button');delBtn.innerHTML=svgIcon('trash', { size: 13 });delBtn.title='Delete';delBtn.className='fe-delete';
    delBtn.onclick=e=>{e.stopPropagation();startInlineDelete(item,fullPath,name,isDir);};
    aDiv.appendChild(renBtn);aDiv.appendChild(delBtn);
    item.appendChild(iconEl);item.appendChild(nameEl);item.appendChild(sizeEl);item.appendChild(aDiv);
    item.onclick=onclick;return item;
}

// ===== BREADCRUMB (interactive & editable) =====
function renderBreadcrumb(path){
    const bc=document.getElementById('fe-breadcrumb');if(!bc)return;
    bc.innerHTML='';
    bc.dataset.editing='false';
    const norm=path.replace(/\\/g,'/');
    const isWindowsDrive=/^[a-zA-Z]:/.test(norm);
    const segs=norm.split('/').filter(Boolean);
    
    let accum='';
    if(norm.startsWith('/')){
        accum='/';
        const rootSeg=document.createElement('span');
        rootSeg.className='bc-seg';
        rootSeg.innerText='/';
        rootSeg.title='Jump to /';
        rootSeg.onclick=e=>{e.stopPropagation();requestDir('/');};
        bc.appendChild(rootSeg);
    }
    
    segs.forEach((seg,idx)=>{
        if(idx>0||norm.startsWith('/')){
            const sep=document.createElement('span');
            sep.className='bc-sep';
            sep.innerText='›';
            bc.appendChild(sep);
        }
        if(!accum||accum==='/'){
            accum=(accum==='/'?'/':'')+seg;
        }else{
            accum=accum+'/'+seg;
        }
        if(isWindowsDrive&&idx===0&&!accum.endsWith('/')){
            accum+='/';
        }
        const targetPath=accum;
        const segEl=document.createElement('span');
        segEl.className='bc-seg';
        segEl.innerText=seg;
        segEl.title='Jump to '+targetPath;
        segEl.onclick=e=>{e.stopPropagation();requestDir(targetPath);};
        bc.appendChild(segEl);
    });

    bc.onclick=e=>{if(e.target===bc)editBreadcrumb();};
}
function editBreadcrumb(){
    const bc=document.getElementById('fe-breadcrumb');if(!bc)return;
    if(bc.dataset.editing==='true')return;
    bc.dataset.editing='true';bc.innerHTML='';
    const inp=document.createElement('input');inp.className='bc-edit-input';inp.value=currentFilePath;
    const hint=document.createElement('span');hint.className='bc-hint';hint.innerHTML=`Enter ${svgIcon('cornerDownLeft', { size: 10 })}`;
    inp.onkeydown=e=>{if(e.key==='Enter'){e.preventDefault();const v=inp.value.trim();if(v)requestDir(v);else renderBreadcrumb(currentFilePath);}if(e.key==='Escape'){renderBreadcrumb(currentFilePath);}};
    inp.onblur=()=>{setTimeout(()=>renderBreadcrumb(currentFilePath),150);};
    bc.appendChild(inp);bc.appendChild(hint);inp.focus();inp.select();
}

// ===== INLINE INPUT (new file/folder) =====
function showInlineInput(type){
    const el=document.getElementById('fe-inline');el.style.display='flex';el.innerHTML='';
    const icon=document.createElement('span');icon.className='inline-icon';icon.innerHTML=type==='dir'?svgIcon('folder', { size: 14 }):svgIcon('langText', { size: 14 });
    const inp=document.createElement('input');inp.placeholder=type==='dir'?'Folder name...':'File name...';
    inp.onkeydown=e=>{
        if(e.key==='Enter'){const n=inp.value.trim();if(!n)return;const p=(currentFilePath.endsWith('/')?currentFilePath:currentFilePath+'/')+n;sendJson({type:'control',action:type==='dir'?'create_dir':'create_file',path:p});hideInlineInput();}
        if(e.key==='Escape')hideInlineInput();
    };
    el.appendChild(icon);el.appendChild(inp);inp.focus();
}
function hideInlineInput(){const el=document.getElementById('fe-inline');el.style.display='none';el.innerHTML='';}

// ===== INLINE RENAME =====
function startInlineRename(item,nameEl,fullPath,oldName){
    const origText=nameEl.innerText;
    nameEl.style.display='none';
    const inp=document.createElement('input');inp.className='fe-rename-input';inp.value=oldName;
    inp.onkeydown=e=>{
        if(e.key==='Enter'){const n=inp.value.trim();if(n&&n!==oldName){const dir=fullPath.replace(/\\/g,'/').replace(/\/[^/]+$/,'');sendJson({type:'control',action:'rename_file',old_path:fullPath,new_path:dir+'/'+n});}endRename();}
        if(e.key==='Escape')endRename();
    };
    inp.onblur=()=>endRename();
    function endRename(){nameEl.style.display='';if(inp.parentNode)inp.remove();}
    item.insertBefore(inp,nameEl.nextSibling);inp.focus();inp.select();
}

// ===== INLINE DELETE =====
function startInlineDelete(item,path,name,isDir){
    // Remove existing actions, show confirm strip
    const acts=item.querySelector('.fe-item-actions');if(acts)acts.style.display='none';
    const c=document.createElement('div');c.className='fe-confirm';
    c.innerHTML=`<span>Delete?</span>`;
    const yes=document.createElement('button');yes.className='fc-yes';yes.innerText='Yes';
    yes.onclick=e=>{e.stopPropagation();sendJson({type:'control',action:'delete_file',path});};
    const no=document.createElement('button');no.className='fc-no';no.innerText='No';
    no.onclick=e=>{e.stopPropagation();c.remove();if(acts)acts.style.display='';};
    c.appendChild(yes);c.appendChild(no);
    item.appendChild(c);
}

// ===== TOAST =====
function showToast(msg){
    const el=document.getElementById('fe-toast');el.style.display='flex';
    el.innerHTML=`<span>ℹ ${esc(msg)}</span>`;
    const btn=document.createElement('button');btn.className='toast-close';btn.innerText='×';btn.onclick=()=>{el.style.display='none';};
    el.appendChild(btn);
    setTimeout(()=>{el.style.display='none';},4000);
}

async function handleFileUpload(e){
    const files=Array.from(e.target.files||[]);
    e.target.value='';
    enqueueUploads(files);
}

function enqueueUploads(files){
    for(const f of files||[])uploadQueue.push(f);
    processUploadQueue();
}

function processUploadQueue(){
    if(uploadActive)return;
    const f=uploadQueue.shift();
    if(!f)return;
    if(!tryAcquireFileOp()){uploadQueue.unshift(f);return;}
    uploadActive=true;
    uploadSingleFile(f);
}

function uploadSingleFile(f){
    const r=new FileReader();
    r.onerror=()=>{showToast(`Failed to read ${f.name}`);finishUpload();};
    r.onload=()=>{
        pendingFileBytes=new Uint8Array(r.result);
        currentTransferId=newTransferId();
        const p=(currentFilePath.endsWith('/')?currentFilePath:currentFilePath+'/')+f.name;
        showToast(`Uploading ${f.name}...`);
        sendJson({type:'control',action:'prepare_upload',path:p,transfer_id:currentTransferId});
    };
    r.readAsArrayBuffer(f);
}

function initDragAndDrop(){
    const fe=document.getElementById('file-explorer');
    if(!fe||fe.dataset.dndInit)return;
    fe.dataset.dndInit='true';
    ['dragenter','dragover'].forEach(n=>fe.addEventListener(n,e=>{e.preventDefault();e.stopPropagation();fe.classList.add('drag-active');}));
    ['dragleave','drop'].forEach(n=>fe.addEventListener(n,e=>{e.preventDefault();e.stopPropagation();fe.classList.remove('drag-active');}));
    fe.addEventListener('drop',e=>{
        const files=e.dataTransfer?.files;
        if(files&&files.length){
            enqueueUploads(Array.from(files));
        }
    });
}

function toggleShareModal(show){
    const m=document.getElementById('share-modal');
    if(!m)return;
    const isVisible=m.style.display==='flex';
    const nextShow=typeof show==='boolean'?show:!isVisible;
    m.style.display=nextShow?'flex':'none';
    if(nextShow){
        const serverVal=document.getElementById('server').value||(location.protocol==='https:'?'wss://':'ws://')+location.host+'/ws-rmte';
        const sid=document.getElementById('sessionId').value;
        const creds=getSessionCredentials(sid);
        const pass=document.getElementById('password').value||(creds?creds.password:'')||'';
        const url=new URL(window.location.href);
        url.hash='';
        url.searchParams.set('server',serverVal);
        if(sid)url.searchParams.set('session',sid);
        // Password goes in the URL fragment (#pass=...): browsers never send it to the relay.
        if(pass)url.hash=new URLSearchParams({pass}).toString();
        const webLink=url.toString();
        const linkInp=document.getElementById('share-link-input');
        if(linkInp)linkInp.value=webLink;

        const cliCmd=`rmte join --server-relay="${serverVal}" --id="${sid}" --pass="${pass||'<your-password>'}"`;
        const cliInp=document.getElementById('share-cli-input');
        if(cliInp)cliInp.value=cliCmd;
    }
}

function copyWebLink(){
    const inp=document.getElementById('share-link-input');
    if(!inp)return;
    copyToClip(inp.value,document.getElementById('copy-web-link-btn'));
}

function copyCliCmd(){
    const inp=document.getElementById('share-cli-input');
    if(!inp)return;
    copyToClip(inp.value,document.getElementById('copy-cli-cmd-btn'));
}

function copyToClip(text,btn){
    const flash=()=>{
        if(!btn)return;
        const old=btn.innerHTML;
        btn.innerHTML=`${svgIcon('check', { size: 12 })}<span>Copied!</span>`;
        btn.classList.add('copied');
        setTimeout(()=>{btn.innerHTML=old;btn.classList.remove('copied');},2000);
    };
    // Works on HTTP (non-secure) origins where navigator.clipboard is unavailable.
    const legacyCopy=()=>{
        try{
            const ta=document.createElement('textarea');
            ta.value=text;
            ta.setAttribute('readonly','');
            ta.style.cssText='position:fixed;top:-1000px;left:-1000px;opacity:0;';
            document.body.appendChild(ta);
            ta.select();
            ta.setSelectionRange(0,text.length);
            const ok=document.execCommand('copy');
            document.body.removeChild(ta);
            return ok;
        }catch(e){return false;}
    };
    if(navigator.clipboard&&window.isSecureContext){
        navigator.clipboard.writeText(text).then(flash).catch(()=>{if(!legacyCopy())prompt('Copy text:',text);});
    } else if(legacyCopy()){
        flash();
    } else {
        prompt('Copy text:',text);
    }
}

function copyShareableLink(){
    toggleShareModal(true);
}

function toggleHelpModal(){
    const m=document.getElementById('help-modal');
    if(!m)return;
    m.style.display=m.style.display==='none'?'flex':'none';
}

// ===== CONFIRM MODAL =====
let confirmResolver=null;
function uiConfirm(message,opts){
    opts=opts||{};
    const modal=document.getElementById('confirm-modal');
    if(!modal)return Promise.resolve(window.confirm(message));
    document.getElementById('confirm-title').textContent=opts.title||'Confirm';
    document.getElementById('confirm-message').textContent=message;
    const ok=document.getElementById('confirm-ok'),cancel=document.getElementById('confirm-cancel');
    ok.textContent=opts.okText||'Confirm';
    cancel.textContent=opts.cancelText||'Cancel';
    ok.classList.toggle('danger',!!opts.danger);
    modal.style.display='flex';
    return new Promise(resolve=>{confirmResolver=resolve;});
}
function closeConfirm(result){
    const modal=document.getElementById('confirm-modal');
    if(modal)modal.style.display='none';
    if(confirmResolver){const r=confirmResolver;confirmResolver=null;r(result);}
}

// ===== PRESENCE + CHAT =====
function updatePresence(tabs){
    document.querySelectorAll('.tab-subtext').forEach(el=>el.innerText='');
    Object.keys(tabs||{}).forEach(tid=>{const s=document.getElementById('tab-subtext-'+tid);if(s&&tabs[tid]&&tabs[tid].length)s.innerText=tabs[tid].join(', ');});
    const list=document.getElementById('users-list');list.innerHTML='';
    const peers = new Set();
    const selfName = (myUsername || '').trim();
    Object.keys(tabs||{}).forEach(tid=>(tabs[tid]||[]).forEach(name=>{
        const trimmed = (name || '').trim();
        const i=document.createElement('div');i.className='user-item';i.innerHTML=`<span class="dot"></span><span class="user-name">${esc(trimmed)}</span><span class="user-tab-badge">Tab ${tid}</span>`;list.appendChild(i);
        if(trimmed && selfName && trimmed !== selfName) peers.add(trimmed);
    }));
    const collabBadge = document.getElementById('collab-badge-top');
    if(collabBadge){
        if(peers.size > 0){
            collabBadge.innerText = peers.size > 99 ? '99+' : peers.size;
            collabBadge.style.display = 'inline-flex';
            collabBadge.title = `${peers.size} active collaborator(s)`;
        } else {
            collabBadge.style.display = 'none';
        }
    }
    activePeerCount = peers.size;
    updateOverflowBadge();
}
function handleChatKey(e){if(e.key==='Enter')sendChatMessage();}
function sendChatMessage(){
    const i=document.getElementById('chat-input'),t=(i.value||'').trim();
    if(!t)return;
    sendJson({type:'control',action:'chat',sender:myUsername,message:t,time:new Date().toLocaleTimeString([],{hour:'2-digit',minute:'2-digit'})});
    i.value='';
}
function appendChat(sender,msg,time){
    const c=document.getElementById('chat-messages');
    if(!c)return;
    const empty=c.querySelector('.chat-empty-state');
    if(empty)empty.remove();

    const isSelf=sender===myUsername;
    const el=document.createElement('div');
    el.className=`chat-msg ${isSelf?'self':'other'}`;
    el.innerHTML=`
        <div class="chat-msg-header">
            <span class="chat-msg-sender${isSelf?' self':''}">${esc(sender)}</span>
            <span class="chat-msg-time">${esc(time||'')}</span>
        </div>
        <div class="chat-msg-body">${esc(msg)}</div>
    `;
    c.appendChild(el);
    c.scrollTop=c.scrollHeight;

    const ws=document.getElementById('workspace');
    const isSidebarHidden=ws&&ws.classList.contains('sidebar-collapsed');
    if(currentSidebarTab!=='chat'||isSidebarHidden){
        unreadChatCount++;
        const badge=document.getElementById('chat-badge');
        if(badge){
            badge.innerText=unreadChatCount>99?'99+':unreadChatCount;
            badge.style.display='inline-block';
        }
        const topBadge=document.getElementById('chat-badge-top');
        if(topBadge){
            topBadge.innerText=unreadChatCount>99?'99+':unreadChatCount;
            topBadge.style.display='inline-flex';
        }
    }
}

// ===== UI =====
let currentSidebarTab = 'collab';
let eventsLog = [];
let unreadActivityCount = 0;
let unreadChatCount = 0;

function switchSidebarTab(tabName){
    currentSidebarTab = tabName;
    ['collab','chat','activity'].forEach(t => {
        const btn = document.getElementById('sb-tab-' + t);
        if(btn) btn.classList.toggle('active', t === tabName);
    });
    const uSec = document.getElementById('users-section');
    const cSec = document.getElementById('chat-section');
    const aSec = document.getElementById('activity-section');
    if(uSec) uSec.style.display = tabName === 'collab' ? 'flex' : 'none';
    if(cSec) cSec.style.display = tabName === 'chat' ? 'flex' : 'none';
    if(aSec) aSec.style.display = tabName === 'activity' ? 'flex' : 'none';
    
    const collabBtn = document.getElementById('toggle-sidebar-btn');
    const chatBtn = document.getElementById('toggle-chat-btn');
    const actBtn = document.getElementById('toggle-activity-btn');
    const isCollapsed = document.getElementById('workspace').classList.contains('sidebar-collapsed');
    if(collabBtn) collabBtn.classList.toggle('active', !isCollapsed && tabName === 'collab');
    if(chatBtn) chatBtn.classList.toggle('active', !isCollapsed && tabName === 'chat');
    if(actBtn) actBtn.classList.toggle('active', !isCollapsed && tabName === 'activity');

    if(tabName === 'activity'){
        unreadActivityCount = 0;
        const b = document.getElementById('activity-badge');
        if(b) b.style.display = 'none';
        const tb = document.getElementById('activity-badge-top');
        if(tb) tb.style.display = 'none';
    }
    if(tabName === 'chat'){
        unreadChatCount = 0;
        const b = document.getElementById('chat-badge');
        if(b) b.style.display = 'none';
        const tb = document.getElementById('chat-badge-top');
        if(tb) tb.style.display = 'none';
        const ci = document.getElementById('chat-input');
        if(ci) setTimeout(() => ci.focus(), 60);
    }
    updateOverflowBadge();
    updateDrawerBackdrop();
    setTimeout(refitActive, 50);
}

function toggleSidebar(preferredTab){
    const ws = document.getElementById('workspace');
    const sb = document.getElementById('users-sidebar');
    const isMobile = window.innerWidth <= 768;
    if(!preferredTab) preferredTab = 'collab';

    if(isMobile){
        const isOpen = sb && sb.classList.contains('drawer-open');
        if(!isOpen){
            if(sb) sb.classList.add('drawer-open');
            if(ws) ws.classList.remove('sidebar-collapsed');
            switchSidebarTab(preferredTab);
        } else if(currentSidebarTab === preferredTab){
            if(sb) sb.classList.remove('drawer-open');
            if(ws) ws.classList.add('sidebar-collapsed');
            const collabBtn = document.getElementById('toggle-sidebar-btn');
            const chatBtn = document.getElementById('toggle-chat-btn');
            const actBtn = document.getElementById('toggle-activity-btn');
            if(collabBtn) collabBtn.classList.remove('active');
            if(chatBtn) chatBtn.classList.remove('active');
            if(actBtn) actBtn.classList.remove('active');
        } else {
            switchSidebarTab(preferredTab);
        }
    } else {
        const isCollapsed = ws.classList.contains('sidebar-collapsed');
        if(isCollapsed){
            ws.classList.remove('sidebar-collapsed');
            switchSidebarTab(preferredTab);
        } else if(currentSidebarTab === preferredTab){
            ws.classList.add('sidebar-collapsed');
            const collabBtn = document.getElementById('toggle-sidebar-btn');
            const chatBtn = document.getElementById('toggle-chat-btn');
            const actBtn = document.getElementById('toggle-activity-btn');
            if(collabBtn) collabBtn.classList.remove('active');
            if(chatBtn) chatBtn.classList.remove('active');
            if(actBtn) actBtn.classList.remove('active');
        } else {
            switchSidebarTab(preferredTab);
        }
    }
    updateOverflowBadge();
    updateDrawerBackdrop();
    setTimeout(refitActive, 200);
}

// ── Topbar Overflow Menu for Mobile (<= 640px) ──
function toggleOverflowMenu(force){
    const menu = document.getElementById('topbar-overflow-menu');
    const btn = document.getElementById('topbar-overflow-btn');
    overflowMenuOpen = typeof force === 'boolean' ? force : !overflowMenuOpen;
    if(menu) menu.style.display = overflowMenuOpen ? 'flex' : 'none';
    if(btn) btn.classList.toggle('active', overflowMenuOpen);
    if(overflowMenuOpen) updateOverflowBadge();
}

function handleOverflowAction(action){
    toggleOverflowMenu(false);
    switch(action){
        case 'collab':
            toggleSidebar('collab');
            break;
        case 'activity':
            toggleSidebar('activity');
            break;
        case 'share':
            toggleShareModal(true);
            break;
        case 'help':
            toggleHelpModal();
            break;
        case 'disconnect':
            disconnectSession();
            break;
    }
}

function updateOverflowBadge(){
    const ovBadge = document.getElementById('overflow-badge-top');
    const collabBadge = document.getElementById('collab-badge-overflow');
    const actBadge = document.getElementById('activity-badge-overflow');

    const collabCount = activePeerCount || 0;
    const actCount = unreadActivityCount || 0;

    if(collabBadge){
        collabBadge.innerText = collabCount > 99 ? '99+' : collabCount;
        collabBadge.style.display = collabCount > 0 ? 'inline-flex' : 'none';
    }
    if(actBadge){
        actBadge.innerText = actCount > 99 ? '99+' : actCount;
        actBadge.style.display = actCount > 0 ? 'inline-flex' : 'none';
    }
    if(ovBadge){
        // Badge on ⋮ only indicates unread notifications
        if(actCount > 0){
            ovBadge.innerText = actCount > 99 ? '99+' : actCount;
            ovBadge.style.display = 'inline-flex';
        } else {
            ovBadge.style.display = 'none';
        }
    }
}

function createActivityItem(evt){
    const it = document.createElement('div');
    it.className = 'activity-item';
    const typeClass = (evt.type || 'INFO').replace(/[^a-zA-Z0-9_]/g, '');
    it.innerHTML = `
        <div class="activity-item-header">
            <span class="activity-type ${esc(typeClass)}">${esc(evt.type || 'EVENT')}</span>
            <span class="activity-time">${esc(evt.time || '')}</span>
        </div>
        <div class="activity-msg">${esc(evt.message || '')}</div>
    `;
    return it;
}

function appendActivityLog(evt){
    if(!evt) return;
    eventsLog.push(evt);
    const list = document.getElementById('activity-list');
    if(list){
        list.appendChild(createActivityItem(evt));
        list.scrollTop = list.scrollHeight;
    }

    const ws = document.getElementById('workspace');
    const isSidebarHidden = ws && ws.classList.contains('sidebar-collapsed');
    if(currentSidebarTab !== 'activity' || isSidebarHidden){
        unreadActivityCount++;
        const badge = document.getElementById('activity-badge');
        if(badge){
            badge.innerText = unreadActivityCount > 99 ? '99+' : unreadActivityCount;
            badge.style.display = 'inline-block';
        }
        const topBadge = document.getElementById('activity-badge-top');
        if(topBadge){
            topBadge.innerText = unreadActivityCount > 99 ? '99+' : unreadActivityCount;
            topBadge.style.display = 'inline-flex';
        }
        updateOverflowBadge();
    }
}

function renderActivityHistory(events){
    eventsLog = events || [];
    const list = document.getElementById('activity-list');
    if(!list) return;
    list.innerHTML = '';
    eventsLog.forEach(evt => list.appendChild(createActivityItem(evt)));
    list.scrollTop = list.scrollHeight;
}

function exportActivityLog(){
    if(!eventsLog.length){
        showToast('No events logged yet');
        return;
    }
    const lines = eventsLog.map(e => `[${e.date || e.time}] [${e.type || 'EVENT'}] [${e.user || 'system'}] ${e.message || ''}`);
    const blob = new Blob([lines.join('\n')], {type: 'text/plain'});
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    const sid = document.getElementById('sessionId').value || 'session';
    a.download = `rmte-${sid}.log`;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    showToast('Activity log exported!');
}
function disconnectSession(){
    manualDisconnect=true;
    clearReconnectTimer();
    stopPing();
    const sid=document.getElementById('sessionId').value||sessionStorage.getItem('rmte_active_session');
    if(sid)clearSessionAutoconnect(sid);
    if(ws)ws.close();
    location.reload();
}
function showError(m){const e=document.getElementById('setup-error');e.style.display='block';e.innerText=m;}
function hideError(){document.getElementById('setup-error').style.display='none';}

// ===== JOIN MODAL (first-time display name prompt) =====
function showJoinModal(){
    const m=document.getElementById('join-modal');
    if(!m)return;
    const sid=document.getElementById('sessionId').value;
    const lbl=document.getElementById('join-session-label');
    if(lbl)lbl.innerText=sid;
    const inp=document.getElementById('join-name-input');
    if(inp)inp.value=document.getElementById('username').value||'';
    const err=document.getElementById('join-error');
    if(err)err.style.display='none';
    m.style.display='flex';
    if(inp)setTimeout(()=>inp.focus(),30);
}
function closeJoinModal(){
    const m=document.getElementById('join-modal');
    if(m)m.style.display='none';
}
function submitJoinModal(){
    const inp=document.getElementById('join-name-input');
    const name=(inp?inp.value:'').trim();
    if(!name){
        const err=document.getElementById('join-error');
        if(err){err.innerText='Please enter a display name';err.style.display='block';}
        if(inp)inp.focus();
        return;
    }
    saveUsername(name);
    document.getElementById('username').value=name;
    closeJoinModal();
    connect();
}

// Reads #pass=... from the URL fragment, then wipes the fragment from the
// address bar and browser history so the password never lingers.
function consumeHashPassword(){
    const raw=window.location.hash.slice(1);
    if(!raw)return '';
    const pass=new URLSearchParams(raw).get('pass')||'';
    history.replaceState(null,'',window.location.pathname+window.location.search);
    return pass;
}

let windowResizeTimer = null;
window.addEventListener('resize', () => {
    if(windowResizeTimer) clearTimeout(windowResizeTimer);
    windowResizeTimer = setTimeout(() => {
        windowResizeTimer = null;
        refitActive();
    }, 100);
});
window.addEventListener('DOMContentLoaded',async()=>{
    // URL params take priority (sharable link: ?server=...&session=...)
    const params=new URLSearchParams(window.location.search);
    const paramServer=params.get('server');
    const paramSession=params.get('session');

    // Server
    if(paramServer){
        document.getElementById('server').value=paramServer;
    } else {
        const savedServer=sessionStorage.getItem('rmte_server');
        if(savedServer)document.getElementById('server').value=savedServer;
    }

    // Password from URL fragment (#pass=...). Read once, then removed from the URL.
    const hashPass=consumeHashPassword();

    // Username (global, persistent across sessions)
    const savedUser=getSavedUsername();
    if(savedUser&&!document.getElementById('username').value){
        document.getElementById('username').value=savedUser;
    }

    // Session ID: URL param takes priority, otherwise last active session in this tab
    let targetSession='';
    if(paramSession){
        targetSession=paramSession.trim();
        document.getElementById('sessionId').value=targetSession;
    } else {
        targetSession=(sessionStorage.getItem('rmte_active_session')||'').trim();
        if(targetSession)document.getElementById('sessionId').value=targetSession;
    }

    // A #pass= link overrides any stored password for this session.
    let fromHashLink=false;
    if(hashPass){
        document.getElementById('password').value=hashPass;
        if(targetSession){
            const prev=getSessionCredentials(targetSession)||{};
            saveSessionCredentials(null,targetSession,hashPass,null,prev.autoconnect===true&&prev.password===hashPass);
            fromHashLink=true;
        }
    }

    // Load credentials specifically for targetSession (no cross-session leaks)
    let shouldAutoconnect=false;
    if(targetSession){
        const creds=getSessionCredentials(targetSession);
        if(creds){
            if(creds.password&&!document.getElementById('password').value){
                document.getElementById('password').value=creds.password;
            }
            if(creds.autoconnect===true){
                shouldAutoconnect=true;
            }
        }
    }

    // Load relay config once: used for the WS URL fallback and the version badge.
    let wsPath='/ws-rmte';
    try{
        const r=await fetch('config.json',{cache:'no-store'});
        if(r.ok){
            const c=await r.json();
            if(c.ws_path)wsPath=c.ws_path;
            if(c.version){const b=document.getElementById('brand-version');if(b)b.innerText=c.version.startsWith('v')?c.version:(c.version==='dev'?'dev':'v'+c.version);}
        }
    }catch(e){}

    // Fallback: derive WS URL from the page origin + relay config (ws_path)
    if(!document.getElementById('server').value){
        document.getElementById('server').value=(location.protocol==='https:'?'wss://':'ws://')+location.host+wsPath;
    }

    // Focus password field if server+session already filled but password is empty
    if(document.getElementById('server').value&&document.getElementById('sessionId').value&&!document.getElementById('password').value){
        document.getElementById('password').focus();
    }

    const ready=document.getElementById('server').value&&document.getElementById('sessionId').value&&document.getElementById('password').value;
    if(ready&&fromHashLink){
        // One-click link: join immediately if we know the user's name, otherwise ask once.
        if(document.getElementById('username').value.trim())connect();
        else showJoinModal();
    } else if(ready&&shouldAutoconnect){
        // Only autoconnect if this specific session has autoconnect flag set!
        connect();
    }

    document.addEventListener('keydown', e => {
        if((e.ctrlKey || e.metaKey) && e.key === 's'){
            e.preventDefault();
            if(activeEditorTab && editorTabs[activeEditorTab]){
                saveEditor(activeEditorTab, editorTabs[activeEditorTab].path);
            }
        }
        if((e.ctrlKey || e.metaKey) && (e.key === '`' || e.key === '~')){
            e.preventDefault();
            toggleTerminalPanel();
        }
        if((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'b'){
            e.preventDefault();
            toggleFileManager();
        }
        if((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'f'){
            const fi = document.getElementById('fe-filter');
            if(fi && fileManagerOpen){
                e.preventDefault();
                fi.focus();
                fi.select();
            }
        }
        if(e.altKey && (e.key === 'ArrowUp' || e.key === 'ArrowDown')){
            const tag = document.activeElement ? document.activeElement.tagName.toLowerCase() : '';
            if(tag === 'input' || (tag === 'textarea' && !document.activeElement.closest('.CodeMirror'))){
                return;
            }
            const hasModal = Array.from(document.querySelectorAll('.modal-overlay')).some(m => getComputedStyle(m).display !== 'none');
            if(hasModal) return;

            e.preventDefault();
            if(e.key === 'ArrowUp'){
                focusEditorPane();
            } else {
                focusTerminalPane();
            }
        }
        if(e.key === 'Escape'){
            const jm = document.getElementById('join-modal');
            if(jm && jm.style.display !== 'none') closeJoinModal();
            const cf = document.getElementById('confirm-modal');
            if(cf && cf.style.display !== 'none') closeConfirm(false);
            const hm = document.getElementById('help-modal');
            if(hm && hm.style.display !== 'none') toggleHelpModal();
            const sm = document.getElementById('share-modal');
            if(sm && sm.style.display !== 'none') toggleShareModal(false);
            const sp = document.getElementById('syntax-picker-popover');
            if(sp && sp.style.display !== 'none') sp.style.display = 'none';
        }
    });

    // Dismiss syntax picker on outside click
    document.addEventListener('click', e => {
        const pop = document.getElementById('syntax-picker-popover');
        if (pop && pop.style.display !== 'none') {
            if (!pop.contains(e.target) && e.target.id !== 'sb-syntax-btn') {
                pop.style.display = 'none';
            }
        }
    });

    // Pane click focus context tracking
    const ecWrap = document.getElementById('editor-container-wrap');
    if (ecWrap) {
        ecWrap.addEventListener('click', () => {
            if (activeEditorTab && editorTabs[activeEditorTab]) {
                setActiveContext(editorTabs[activeEditorTab].type === 'image' ? 'media' : 'editor');
            }
        });
    }
    const twWrap = document.getElementById('terminal-wrapper');
    if (twWrap) {
        twWrap.addEventListener('click', () => {
            if (activeTerminalTab) {
                setActiveContext('terminal');
            }
        });
    }
    const ptBar = document.getElementById('preview-toolbar');
    if (ptBar) {
        ptBar.addEventListener('click', () => {
            if (webPreviewOpen) {
                setActiveContext('preview');
            }
        });
    }

    initFileExplorerResizer();
    initWorkbenchResizer();
    initPreviewResizer();
    setActiveContext('terminal');
});

function initWorkbenchResizer() {
    const resizer = document.getElementById('workbench-resizer');
    const panel = document.getElementById('terminal-panel');
    const mainArea = document.getElementById('main-area');
    const shield = document.getElementById('preview-drag-shield');
    if (!resizer || !panel || !mainArea) return;

    // Load saved height, collapse and hidden state from localStorage
    try {
        const savedHeight = localStorage.getItem('rmte_terminal_height');
        if (savedHeight) {
            const h = parseFloat(savedHeight);
            if (h >= 15 && h <= 80) {
                document.getElementById('workspace').style.setProperty('--terminal-panel-height', h + '%');
            }
        }
        const savedCollapsed = localStorage.getItem('rmte_terminal_collapsed');
        if (savedCollapsed === 'true') {
            toggleTerminalPanel(true, true);
        }
        const savedHidden = localStorage.getItem('rmte_terminal_hidden');
        if (savedHidden === 'true') {
            hideTerminalPanel(true);
        }
    } catch(e) {}

    // Responsive mobile listener
    if (window.innerWidth <= 768) {
        setMobileWorkbenchPane('terminal');
        closeAllDrawers();
    }
    function updateEditorLineWrapping(){
        const isMobile = window.innerWidth <= 768;
        Object.values(editorTabs).forEach(et => {
            if(et && et.cm){
                const shouldWrap = (et.wrapOverride !== null && et.wrapOverride !== undefined) ? et.wrapOverride : isMobile;
                et.cm.setOption('lineWrapping', shouldWrap);
                try { et.cm.refresh(); } catch(e){}
            }
        });
        updateStatusBarWrap();
    }

    const mobileQuery = window.matchMedia('(max-width: 768px)');
    const handleMobileQuery = e => {
        if (!mainArea) return;
        if (e.matches) {
            setMobileWorkbenchPane(mobileActivePane);
            closeAllDrawers();
        } else {
            mainArea.classList.remove('mobile-view-editor', 'mobile-view-terminal');
            closeAllDrawers();
        }
        updateEditorLineWrapping();
        refitActive();
    };
    try {
        mobileQuery.addEventListener('change', handleMobileQuery);
    } catch(err) {
        mobileQuery.addListener(handleMobileQuery);
    }

    let isDragging = false;
    let startY = 0;
    let startHeightPx = 0;
    let rafId = null;
    let lastHeightPct = 35;

    resizer.addEventListener('pointerdown', e => {
        if (panel.classList.contains('is-collapsed') || panel.classList.contains('is-maximized')) return;
        isDragging = true;
        startY = e.clientY;
        startHeightPx = panel.getBoundingClientRect().height;
        try { resizer.setPointerCapture(e.pointerId); } catch(err) {}
        resizer.classList.add('is-dragging');
        document.body.style.cursor = 'row-resize';
        document.body.style.userSelect = 'none';
        if (shield) shield.style.display = 'block';
        e.preventDefault();
    });

    resizer.addEventListener('pointermove', e => {
        if (!isDragging) return;
        const dy = e.clientY - startY;
        const mainRect = mainArea.getBoundingClientRect();
        if (mainRect.height <= 0) return;

        let newHeightPx = startHeightPx - dy;
        const minHeightPx = 80;
        const maxHeightPx = mainRect.height - 120; // preserve at least 120px for editor

        if (newHeightPx < minHeightPx) newHeightPx = minHeightPx;
        if (newHeightPx > maxHeightPx) newHeightPx = maxHeightPx;

        const heightPct = (newHeightPx / mainRect.height) * 100;
        lastHeightPct = Math.round(heightPct * 10) / 10;

        if (!rafId) {
            rafId = requestAnimationFrame(() => {
                document.getElementById('workspace').style.setProperty('--terminal-panel-height', lastHeightPct + '%');
                rafId = null;
            });
        }
    });

    const endDrag = e => {
        if (!isDragging) return;
        isDragging = false;
        try { resizer.releasePointerCapture(e.pointerId); } catch(err) {}
        resizer.classList.remove('is-dragging');
        document.body.style.cursor = '';
        document.body.style.userSelect = '';
        if (shield) shield.style.display = 'none';
        if (rafId) {
            cancelAnimationFrame(rafId);
            rafId = null;
        }

        try {
            localStorage.setItem('rmte_terminal_height', lastHeightPct);
        } catch(err) {}

        // Send exactly 1 resize message on drag finish if cols/rows changed
        refitActive();
    };

    resizer.addEventListener('pointerup', endDrag);
    resizer.addEventListener('pointercancel', endDrag);

    // Double-click resets split to default 35%
    resizer.addEventListener('dblclick', () => {
        document.getElementById('workspace').style.setProperty('--terminal-panel-height', '35%');
        try { localStorage.setItem('rmte_terminal_height', 35); } catch(err) {}
        refitActive();
    });

    // Keyboard navigation (ArrowUp / ArrowDown) when resizer is focused
    resizer.addEventListener('keydown', e => {
        if (e.key === 'ArrowUp' || e.key === 'ArrowDown') {
            e.preventDefault();
            const step = e.shiftKey ? 10 : 5;
            const currentPct = parseFloat(getComputedStyle(document.getElementById('workspace')).getPropertyValue('--terminal-panel-height')) || 35;
            const nextPct = e.key === 'ArrowUp' ? Math.min(80, currentPct + step) : Math.max(15, currentPct - step);
            document.getElementById('workspace').style.setProperty('--terminal-panel-height', nextPct + '%');
            try { localStorage.setItem('rmte_terminal_height', nextPct); } catch(err) {}
            refitActive();
        }
    });
}

function initPreviewResizer() {
    const resizer = document.getElementById('preview-resizer');
    const wbTop = document.getElementById('workbench-top');
    const editorWb = document.getElementById('editor-workbench');
    const previewPanel = document.getElementById('preview-panel');
    const shield = document.getElementById('preview-drag-shield');
    if (!resizer || !wbTop || !editorWb || !previewPanel) return;

    // Load saved split ratio from localStorage
    try {
        const savedRatio = localStorage.getItem('rmte_preview_split_ratio');
        if (savedRatio) {
            const r = parseFloat(savedRatio);
            if (r >= 20 && r <= 80) {
                wbTop.style.setProperty('--preview-split-ratio', r + '%');
                resizer.setAttribute('aria-valuenow', Math.round(r));
            }
        }
    } catch(e) {}

    let isDragging = false;
    let startX = 0;
    let startRatio = 50;
    let rafId = null;
    let lastRatio = 50;

    resizer.addEventListener('pointerdown', e => {
        if (previewMaximized || window.innerWidth <= 768) return;
        isDragging = true;
        startX = e.clientX;
        const wbRect = wbTop.getBoundingClientRect();
        const editorRect = editorWb.getBoundingClientRect();
        if (wbRect.width > 0) {
            startRatio = (editorRect.width / wbRect.width) * 100;
        }
        try { resizer.setPointerCapture(e.pointerId); } catch(err) {}
        resizer.classList.add('is-dragging');
        document.body.style.cursor = 'col-resize';
        document.body.style.userSelect = 'none';
        if (shield) shield.style.display = 'block';
        e.preventDefault();
    });

    resizer.addEventListener('pointermove', e => {
        if (!isDragging) return;
        const dx = e.clientX - startX;
        const wbRect = wbTop.getBoundingClientRect();
        if (wbRect.width <= 0) return;

        const currentWidthPx = (startRatio / 100) * wbRect.width;
        let newWidthPx = currentWidthPx + dx;

        const minWidthPx = 200;
        const maxWidthPx = wbRect.width - 240;

        if (newWidthPx < minWidthPx) newWidthPx = minWidthPx;
        if (newWidthPx > maxWidthPx) newWidthPx = maxWidthPx;

        const ratio = (newWidthPx / wbRect.width) * 100;
        lastRatio = Math.round(ratio * 10) / 10;

        if (!rafId) {
            rafId = requestAnimationFrame(() => {
                wbTop.style.setProperty('--preview-split-ratio', lastRatio + '%');
                rafId = null;
            });
        }
    });

    const endDrag = e => {
        if (!isDragging) return;
        isDragging = false;
        try { resizer.releasePointerCapture(e.pointerId); } catch(err) {}
        resizer.classList.remove('is-dragging');
        document.body.style.cursor = '';
        document.body.style.userSelect = '';
        if (shield) shield.style.display = 'none';
        if (rafId) {
            cancelAnimationFrame(rafId);
            rafId = null;
        }
        resizer.setAttribute('aria-valuenow', Math.round(lastRatio));
        try {
            localStorage.setItem('rmte_preview_split_ratio', lastRatio);
        } catch(err) {}
        setTimeout(refitActive, 50);
    };

    resizer.addEventListener('pointerup', endDrag);
    resizer.addEventListener('pointercancel', endDrag);

    // Double-click resets split to default 50%
    resizer.addEventListener('dblclick', () => {
        wbTop.style.setProperty('--preview-split-ratio', '50%');
        resizer.setAttribute('aria-valuenow', 50);
        try { localStorage.setItem('rmte_preview_split_ratio', 50); } catch(err) {}
        setTimeout(refitActive, 50);
    });

    // Keyboard navigation (ArrowLeft / ArrowRight) when resizer is focused
    resizer.addEventListener('keydown', e => {
        if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') {
            e.preventDefault();
            const step = e.shiftKey ? 10 : 5;
            const currentRatio = parseFloat(getComputedStyle(wbTop).getPropertyValue('--preview-split-ratio')) || 50;
            const nextRatio = e.key === 'ArrowRight' ? Math.min(80, currentRatio + step) : Math.max(20, currentRatio - step);
            wbTop.style.setProperty('--preview-split-ratio', nextRatio + '%');
            resizer.setAttribute('aria-valuenow', Math.round(nextRatio));
            try { localStorage.setItem('rmte_preview_split_ratio', nextRatio); } catch(err) {}
            setTimeout(refitActive, 50);
        }
    });
}

function setMobileWorkbenchPane(pane) {
    mobileActivePane = pane;
    const mainArea = document.getElementById('main-area');
    if (!mainArea) return;

    if (window.innerWidth <= 768) {
        mainArea.classList.toggle('mobile-view-editor', pane === 'editor');
        mainArea.classList.toggle('mobile-view-terminal', pane === 'terminal');
    } else {
        mainArea.classList.remove('mobile-view-editor', 'mobile-view-terminal');
    }

    const switchEditorBtn = document.getElementById('mobile-switch-editor-btn');
    const switchTermBtn = document.getElementById('mobile-switch-term-btn');
    if (switchEditorBtn) switchEditorBtn.classList.toggle('active', pane === 'editor');
    if (switchTermBtn) switchTermBtn.classList.toggle('active', pane === 'terminal');

    const toggleBtn = document.getElementById('toggle-terminal-btn');
    if (toggleBtn && window.innerWidth <= 768) {
        toggleBtn.classList.toggle('active', pane === 'terminal');
    }

    setTimeout(refitActive, 50);
}

function hideTerminalPanel(hidden) {
    const panel = document.getElementById('terminal-panel');
    const mainArea = document.getElementById('main-area');
    const toggleBtn = document.getElementById('toggle-terminal-btn');
    if (!panel || !mainArea) return;

    if (hidden === undefined) {
        terminalPanelHidden = !terminalPanelHidden;
    } else {
        terminalPanelHidden = !!hidden;
    }

    panel.classList.toggle('is-hidden', terminalPanelHidden);
    mainArea.classList.toggle('terminal-hidden', terminalPanelHidden);
    if (toggleBtn) {
        toggleBtn.classList.toggle('active', !terminalPanelHidden && !terminalPanelCollapsed);
    }

    try {
        localStorage.setItem('rmte_terminal_hidden', terminalPanelHidden ? 'true' : 'false');
    } catch(e) {}

    setTimeout(refitActive, 50);
}

function toggleTerminalPanel(collapse, silent) {
    const panel = document.getElementById('terminal-panel');
    const toggleBtn = document.getElementById('toggle-terminal-btn');
    const collapseBtn = document.getElementById('term-collapse-btn');
    if (!panel) return;

    // Mobile single pane toggle
    if (window.innerWidth <= 768) {
        setMobileWorkbenchPane(mobileActivePane === 'terminal' ? 'editor' : 'terminal');
        return;
    }

    // Unhide if hidden
    if (terminalPanelHidden) {
        hideTerminalPanel(false);
        if (collapse === true) {
            terminalPanelCollapsed = true;
            panel.classList.add('is-collapsed');
            if (collapseBtn) collapseBtn.innerHTML = svgIcon('chevronUp', { size: 12 });
            if (toggleBtn) toggleBtn.classList.remove('active');
        }
        return;
    }

    if (collapse === undefined) {
        terminalPanelCollapsed = !terminalPanelCollapsed;
    } else {
        terminalPanelCollapsed = collapse;
    }

    panel.classList.toggle('is-collapsed', terminalPanelCollapsed);
    if (toggleBtn) toggleBtn.classList.toggle('active', !terminalPanelCollapsed);
    if (collapseBtn) collapseBtn.innerHTML = terminalPanelCollapsed ? svgIcon('chevronUp', { size: 12 }) : svgIcon('minus', { size: 12 });

    try {
        localStorage.setItem('rmte_terminal_collapsed', terminalPanelCollapsed);
    } catch(e) {}

    if (!silent) {
        setTimeout(refitActive, 50);
    }
}

function toggleTerminalMaximize() {
    const panel = document.getElementById('terminal-panel');
    const mainArea = document.getElementById('main-area');
    const maxBtn = document.getElementById('term-maximize-btn');
    if (!panel || !mainArea) return;

    if (!terminalPanelMaximized && previewMaximized) {
        togglePreviewMaximize();
    }

    terminalPanelMaximized = !terminalPanelMaximized;
    panel.classList.toggle('is-maximized', terminalPanelMaximized);
    mainArea.classList.toggle('terminal-maximized', terminalPanelMaximized);
    if (maxBtn) maxBtn.innerHTML = terminalPanelMaximized ? svgIcon('minimize', { size: 12 }) : svgIcon('maximize', { size: 12 });

    setTimeout(refitActive, 50);
}

function initFileExplorerResizer() {
    const resizer = document.getElementById('fe-resizer');
    const fe = document.getElementById('file-explorer');
    const shield = document.getElementById('preview-drag-shield');
    if (!resizer || !fe) return;

    try {
        const saved = localStorage.getItem('rmte_fe_width');
        if (saved) {
            const w = parseInt(saved, 10);
            if (w >= 180 && w <= window.innerWidth * 0.6) {
                fe.style.width = w + 'px';
            }
        }
    } catch(e) {}

    let isDragging = false;
    let startX = 0;
    let startWidth = 0;

    resizer.addEventListener('mousedown', e => {
        isDragging = true;
        startX = e.clientX;
        startWidth = fe.getBoundingClientRect().width;
        resizer.classList.add('is-dragging');
        document.body.style.cursor = 'col-resize';
        document.body.style.userSelect = 'none';
        if (shield) shield.style.display = 'block';
        e.preventDefault();
    });

    window.addEventListener('mousemove', e => {
        if (!isDragging) return;
        const dx = e.clientX - startX;
        let newWidth = startWidth + dx;
        const minW = 180;
        const maxW = Math.max(minW, Math.floor(window.innerWidth * 0.6));
        if (newWidth < minW) newWidth = minW;
        if (newWidth > maxW) newWidth = maxW;
        fe.style.width = newWidth + 'px';
        // Note: Do NOT call refitActive() here to avoid spamming PTY resize messages during drag
    });

    window.addEventListener('mouseup', () => {
        if (isDragging) {
            isDragging = false;
            resizer.classList.remove('is-dragging');
            document.body.style.cursor = '';
            document.body.style.userSelect = '';
            if (shield) shield.style.display = 'none';
            try {
                localStorage.setItem('rmte_fe_width', parseInt(fe.style.width, 10));
            } catch(e) {}
            // Send exactly 1 resize message after drag finishes if cols/rows changed
            refitActive();
        }
    });
}

// ═══════════════════════════════════════
// MINI BROWSER PREVIEW CONTROLLER
// ═══════════════════════════════════════

function toggleWebPreview(force) {
    if (typeof force === 'boolean') {
        webPreviewOpen = force;
    } else {
        if (window.innerWidth <= 768 && webPreviewOpen && mobileActivePane === 'terminal') {
            // Already open in background, user tapped 🌐 to view it
            setMobileWorkbenchPane('editor');
            setTimeout(refitActive, 50);
            return;
        }
        webPreviewOpen = !webPreviewOpen;
    }
    const panel = document.getElementById('preview-panel');
    const btn = document.getElementById('toggle-preview-btn');
    const mainArea = document.getElementById('main-area');
    const wbTop = document.getElementById('workbench-top');
    const maxBtn = document.getElementById('preview-maximize-btn');

    if (!webPreviewOpen) {
        if (previewMaximized) {
            previewMaximized = false;
            if (panel) panel.classList.remove('is-maximized');
            if (mainArea) mainArea.classList.remove('preview-maximized');
            if (wbTop) wbTop.classList.remove('preview-maximized');
            if (maxBtn) {
                maxBtn.innerHTML = svgIcon('maximize', { size: 14 });
                maxBtn.title = 'Maximize / Restore Preview (Full width)';
            }
        }
    } else {
        // Smart adaptive check on opening:
        // If window or workbench width < 1024px (medium / half-screen desktop),
        // auto-maximize preview so it takes full width cleanly instead of cramming both panels!
        const wbWidth = wbTop ? wbTop.getBoundingClientRect().width : window.innerWidth;
        if ((window.innerWidth < 1024 || wbWidth < 960) && window.innerWidth > 768) {
            if (!previewMaximized) {
                togglePreviewMaximize(true);
            }
        }
    }

    if (panel) panel.style.display = webPreviewOpen ? 'flex' : 'none';
    if (btn) btn.classList.toggle('active', webPreviewOpen);
    if (mainArea) mainArea.classList.toggle('preview-active', webPreviewOpen);
    if (wbTop) wbTop.classList.toggle('preview-active', webPreviewOpen);

    // On mobile, opening preview switches view to workbench-top (preview pane)
    if (window.innerWidth <= 768) {
        if (webPreviewOpen) {
            setMobileWorkbenchPane('editor');
        } else if (Object.keys(editorTabs).length === 0) {
            setMobileWorkbenchPane('terminal');
        }
    }

    if (webPreviewOpen) {
        setActiveContext('preview');
        updatePreviewStatus();
        const portInput = document.getElementById('preview-port-input');
        if (portInput && !portInput.value) {
            portInput.value = previewPort || 8080;
        }
        const frame = document.getElementById('preview-frame');
        if (frame && (!frame.src || frame.src === 'about:blank')) {
            previewNavigate();
        }
    } else {
        if (activeContext === 'preview') {
            if (activeEditorTab && editorTabs[activeEditorTab]) {
                setActiveContext(editorTabs[activeEditorTab].type === 'image' ? 'media' : 'editor');
            } else if (activeTerminalTab) {
                setActiveContext('terminal');
            } else {
                setActiveContext(null);
            }
        }
    }
    setTimeout(refitActive, 50);
}

async function getPreviewTicket(sessionId, port) {
    if (!rawAesKeyBytes) return '';
    const expiresAt = Math.floor(Date.now() / 1000) + 60;
    const nonce = Math.random().toString(36).slice(2, 10);
    const enc = new TextEncoder();
    const subKey = await rmteCrypto.hmacSha256(rawAesKeyBytes, enc.encode("rmte-preview-subauth"));
    const msg = `${sessionId}:${port}:${expiresAt}:${nonce}`;
    const sig = await rmteCrypto.hmacSha256(subKey, enc.encode(msg));
    const sigHex = Array.from(sig).map(b => b.toString(16).padStart(2, '0')).join('');
    return `${expiresAt}:${nonce}:${sigHex}`;
}

async function previewNavigate() {
    const portInput = document.getElementById('preview-port-input');
    const pathInput = document.getElementById('preview-path-input');
    const frame = document.getElementById('preview-frame');
    if (!portInput || !frame) return;

    let port = parseInt(portInput.value) || 8080;
    if (port < 1) port = 1;
    if (port > 65535) port = 65535;
    portInput.value = port;
    previewPort = port;
    updatePreviewStatus();

    let subPath = (pathInput ? pathInput.value.trim() : '');
    if (subPath && !subPath.startsWith('/')) {
        subPath = '/' + subPath;
    }
    previewPath = subPath;

    const sid = document.getElementById('sessionId').value || sessionStorage.getItem('rmte_active_session') || '';
    if (!sid) {
        showToast('No active session for preview');
        return;
    }

    try {
        const ticket = await getPreviewTicket(sid, port);
        let targetUrl = `/p/${encodeURIComponent(sid)}/${port}${subPath ? subPath : '/'}`;
        if (ticket) {
            targetUrl += (targetUrl.includes('?') ? '&' : '?') + 'ticket=' + encodeURIComponent(ticket);
        }
        frame.src = targetUrl;
        _log.info('Preview navigate', { port, subPath, targetUrl });
    } catch (err) {
        _log.err('Failed to generate preview ticket', err);
        showToast('Failed to authorize preview: ' + err.message);
    }
}

function previewGoBack() {
    const frame = document.getElementById('preview-frame');
    try {
        if (frame && frame.contentWindow) frame.contentWindow.history.back();
    } catch(e) {}
}

function previewGoForward() {
    const frame = document.getElementById('preview-frame');
    try {
        if (frame && frame.contentWindow) frame.contentWindow.history.forward();
    } catch(e) {}
}

function previewReload() {
    const frame = document.getElementById('preview-frame');
    try {
        if (frame && frame.contentWindow) frame.contentWindow.location.reload();
        else previewNavigate();
    } catch(e) {
        previewNavigate();
    }
}

async function previewOpenInNewTab() {
    const port = parseInt(document.getElementById('preview-port-input')?.value) || 8080;
    const subPath = document.getElementById('preview-path-input')?.value.trim() || '';
    const sid = document.getElementById('sessionId').value || sessionStorage.getItem('rmte_active_session') || '';
    if (!sid) return;

    const ticket = await getPreviewTicket(sid, port);
    let targetUrl = `/p/${encodeURIComponent(sid)}/${port}${subPath ? (subPath.startsWith('/') ? subPath : '/' + subPath) : '/'}`;
    if (ticket) {
        targetUrl += (targetUrl.includes('?') ? '&' : '?') + 'ticket=' + encodeURIComponent(ticket);
    }
    window.open(targetUrl, '_blank');
}

function togglePreviewMobile() {
    previewMobileMode = !previewMobileMode;
    const wrap = document.getElementById('preview-viewport-wrap');
    const btn = document.getElementById('preview-mobile-btn');
    if (wrap) wrap.classList.toggle('mobile-view', previewMobileMode);
    if (btn) btn.classList.toggle('active', previewMobileMode);
}

function togglePreviewMaximize(force) {
    const previewPanel = document.getElementById('preview-panel');
    const mainArea = document.getElementById('main-area');
    const wbTop = document.getElementById('workbench-top');
    const maxBtn = document.getElementById('preview-maximize-btn');
    if (!previewPanel || !mainArea) return;

    if (typeof force === 'boolean') {
        previewMaximized = force;
    } else {
        previewMaximized = !previewMaximized;
    }

    if (previewMaximized && terminalPanelMaximized) {
        toggleTerminalMaximize();
    }

    previewPanel.classList.toggle('is-maximized', previewMaximized);
    mainArea.classList.toggle('preview-maximized', previewMaximized);
    if (wbTop) wbTop.classList.toggle('preview-maximized', previewMaximized);

    if (maxBtn) {
        maxBtn.innerHTML = previewMaximized ? svgIcon('minimize', { size: 14 }) : svgIcon('maximize', { size: 14 });
        maxBtn.title = previewMaximized ? 'Restore Preview' : 'Maximize / Restore Preview (Full width)';
    }
    setTimeout(refitActive, 50);
}

function handlePreviewPortKey(e) {
    if (e.key === 'Enter') {
        previewNavigate();
    }
}

function handlePreviewPathKey(e) {
    if (e.key === 'Enter') {
        previewNavigate();
    }
}

// ── Mobile Global Listeners ──
document.addEventListener('click', e => {
    if(!overflowMenuOpen) return;
    const menu = document.getElementById('topbar-overflow-menu');
    const btn = document.getElementById('topbar-overflow-btn');
    if(menu && !menu.contains(e.target) && btn && !btn.contains(e.target)){
        toggleOverflowMenu(false);
    }
});

function updateMobileViewportOffset(){
    if (!window.visualViewport) return;
    const isMobile = window.innerWidth <= 768;
    const panel = document.getElementById('terminal-panel');
    if (!panel) return;
    if (isMobile) {
        const vv = window.visualViewport;
        const overlap = Math.max(0, Math.round(window.innerHeight - (vv.height + vv.offsetTop)));
        panel.style.paddingBottom = overlap > 0 ? `${overlap}px` : '';
    } else {
        panel.style.paddingBottom = '';
    }
    refitActive();
}

if (window.visualViewport) {
    let vpTimer = null;
    const onVpChange = () => {
        if (window.innerWidth > 768) return;
        if (vpTimer) clearTimeout(vpTimer);
        vpTimer = setTimeout(updateMobileViewportOffset, 50);
    };
    window.visualViewport.addEventListener('resize', onVpChange);
    window.visualViewport.addEventListener('scroll', onVpChange);
}


