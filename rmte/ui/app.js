// RMTE v0.6 — Web Viewer with Editor Tabs + File Manager + Web Preview
let ws, aesKey, rawAesKeyBytes = null, currentTab = 'term-0', myUsername = '';
const myViewerId = 'v-web-' + Math.random().toString(16).slice(2,10);
let terminals = {}, editorTabs = {};
let fileManagerOpen = false, currentFilePath = './';
let webPreviewOpen = false, previewPort = 8080, previewPath = '', previewMobileMode = false;

// Auto-reconnect state
let isConnected = false, manualDisconnect = false;
let reconnectAttempt = 0, reconnectTimer = null, reconnectCountdown = null;
const RECONNECT_BASE = 2000, RECONNECT_MAX = 30000;
let waitingForFileData = false, pendingFileBytes = null, pendingEditorPath = null;
let pendingDownload = false, pendingDownloadName = null;
let uploadQueue = [], uploadActive = false, fileOpBusy = false;
let pingTimer = null;
const DATA_CH = 255;

const TEXT_EXT = new Set('go,js,ts,jsx,tsx,py,rb,rs,c,cpp,h,hpp,java,kt,cs,php,html,css,scss,less,json,yaml,yml,toml,xml,sql,md,txt,log,csv,ini,cfg,conf,env,sh,bash,bat,ps1,cmd,mod,sum,lock,editorconfig,gitignore,makefile,dockerfile'.split(','));
const LANG_MAP = {go:'Go',js:'JavaScript',ts:'TypeScript',py:'Python',rs:'Rust',html:'HTML',css:'CSS',json:'JSON',md:'Markdown',yaml:'YAML',yml:'YAML',sh:'Shell',sql:'SQL',c:'C',cpp:'C++',java:'Java',rb:'Ruby',php:'PHP',xml:'XML',toml:'TOML'};

function getCodeMirrorMode(path) {
    const ext = (path.split('.').pop() || '').toLowerCase();
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

const _log = {
    out(t,d){console.log(`%c[OUT] %c${t}`,'color:#58a6ff;font-weight:bold','color:#8b949e',d)},
    in(t,d){console.log(`%c[IN]  %c${t}`,'color:#3fb950;font-weight:bold','color:#8b949e',d)},
    err(m,d){console.error(`%c[ERR] %c${m}`,'color:#f85149;font-weight:bold','color:#8b949e',d||'')},
    warn(m,d){console.warn(`%c[WARN] %c${m}`,'color:#d29922;font-weight:bold','color:#8b949e',d||'')},
    info(m,d){console.info(`%c[INFO] %c${m}`,'color:#bc8cff;font-weight:bold','color:#8b949e',d||'')},
};

function isTextFile(name) {
    const ext = (name.split('.').pop()||'').toLowerCase();
    const base = name.toLowerCase();
    return TEXT_EXT.has(ext) || ['makefile','dockerfile','readme','license','changelog','.gitignore','.env'].includes(base);
}
function getLang(name) { return LANG_MAP[(name.split('.').pop()||'').toLowerCase()] || 'Text'; }
function fileIcon(name) {
    const ext = (name.split('.').pop()||'').toLowerCase();
    return {go:'🔷',js:'🟨',ts:'🔷',py:'🐍',rs:'🦀',html:'🌐',css:'🎨',json:'📋',yaml:'📋',yml:'📋',md:'📝',txt:'📄',log:'📜',sh:'⚙️',bat:'⚙️',png:'🖼️',jpg:'🖼️',svg:'🖼️',zip:'📦',tar:'📦',gz:'📦',mod:'📦',sum:'🔒',exe:'💠',dll:'💠'}[ext]||'📄';
}
function fmtSize(b){if(!b)return'0 B';const u=['B','KB','MB','GB'];const i=Math.floor(Math.log(b)/Math.log(1024));return(b/Math.pow(1024,i)).toFixed(i>0?1:0)+' '+u[i];}
function esc(s){const d=document.createElement('div');d.textContent=s;return d.innerHTML;}
function basename(p){return p.replace(/\\/g,'/').split('/').filter(Boolean).pop()||p;}

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
                    cm.innerHTML=`<div class="chat-empty-state"><div class="chat-empty-icon">💬</div><div class="chat-empty-title">Session Chat</div><div class="chat-empty-sub">Messages sent here are encrypted and shared live with all Web and CLI collaborators.</div></div>`;
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

// ===== TAB SYSTEM =====
// Tab IDs: "term-N" for terminals, "file:path" for editors
function activeTabId(){return currentTab;}
function switchToTab(id){
    if(webPreviewOpen){
        toggleWebPreview(false);
    }
    currentTab=id;
    document.querySelectorAll('.tab-btn-container').forEach(b=>b.classList.remove('active'));
    const el=document.getElementById('tab-'+CSS.escape(id));
    if(el)el.classList.add('active');
    // Hide all containers
    document.querySelectorAll('#terminal-wrapper > div').forEach(d=>d.style.display='none');
    const cont=document.getElementById('content-'+CSS.escape(id));
    if(cont)cont.style.display=cont.dataset.type==='editor'?'flex':'block';
    // Terminal-specific
    if(id.startsWith('term-')){
        const tid=parseInt(id.slice(5));
        if(terminals[tid]){setTimeout(()=>{terminals[tid].fitAddon.fit();},20);terminals[tid].term.focus();}
        sendJson({type:'control',action:'set_focus',viewer_id:myViewerId,viewer_name:myUsername,tab_id:tid});
        sendJson({type:'control',action:'req_sync',tab_id:tid});
    } else if(id.startsWith('file:')&&editorTabs[id]&&editorTabs[id].cm){
        setTimeout(()=>{try{editorTabs[id].cm.refresh();}catch(e){}},20);
    }
    setTimeout(refitActive,50);
}
function refitActive(){
    if(currentTab.startsWith('term-')){
        const tid=parseInt(currentTab.slice(5)),t=terminals[tid];
        if(!t)return;
        try{t.fitAddon.fit();sendJson({type:'control',action:'resize',tab_id:tid,cols:t.term.cols,rows:t.term.rows});}catch(e){}
    } else if(currentTab.startsWith('file:')&&editorTabs[currentTab]&&editorTabs[currentTab].cm){
        try{editorTabs[currentTab].cm.refresh();}catch(e){}
    }
}

// Terminal tabs
function initTerminal(tabId){
    if(terminals[tabId])return;
    const id='term-'+tabId;
    const cont=document.createElement('div');cont.id='content-'+CSS.escape(id);cont.dataset.type='terminal';
    cont.style.cssText='height:100%;width:100%;display:'+(currentTab===id?'block':'none');
    document.getElementById('terminal-wrapper').appendChild(cont);
    const t=new Terminal({cursorBlink:true,convertEol:true,theme:{background:'#0d1117',foreground:'#e6edf3',cursor:'#58a6ff'},fontFamily:"'Consolas','Courier New',monospace",fontSize:14});
    t.attachCustomKeyEventHandler(e => {
        if (e.type === 'keydown' && (e.ctrlKey || e.metaKey) && (e.key === 'c' || e.key === 'C')) {
            if (t.hasSelection()) {
                const sel = t.getSelection();
                if (navigator.clipboard && navigator.clipboard.writeText) {
                    navigator.clipboard.writeText(sel).catch(()=>{});
                }
                return false;
            } else {
                sendBin(tabId, new Uint8Array([3]));
                return false;
            }
        }
        return true;
    });
    const fa=new FitAddon.FitAddon();t.loadAddon(fa);
    terminals[tabId]={term:t,fitAddon:fa};
    t.open(cont);fa.fit();
    t.onData(d=>sendBin(tabId,new TextEncoder().encode(d)));
    addTermTabBtn(tabId);
    if(!document.querySelector('.tab-btn-container.active'))switchToTab(id);
}
let draggedTabEl=null;
function enableTabDrag(el){
    el.draggable=true;
    el.addEventListener('dragstart',e=>{
        draggedTabEl=el;
        el.classList.add('tab-dragging');
        e.dataTransfer.effectAllowed='move';
        e.dataTransfer.setData('text/plain',el.id);
    });
    el.addEventListener('dragend',()=>{
        el.classList.remove('tab-dragging');
        draggedTabEl=null;
    });
    el.addEventListener('dragover',e=>{
        e.preventDefault();
        e.dataTransfer.dropEffect='move';
        if(!draggedTabEl||draggedTabEl===el)return;
        const rect=el.getBoundingClientRect();
        const mid=rect.left+rect.width/2;
        const parent=el.parentNode;
        if(!parent)return;
        if(e.clientX<mid){
            parent.insertBefore(draggedTabEl,el);
        } else {
            parent.insertBefore(draggedTabEl,el.nextSibling);
        }
    });
}

function addTermTabBtn(tabId){
    const id='term-'+tabId;
    if(document.getElementById('tab-'+CSS.escape(id)))return;
    const c=document.createElement('div');c.id='tab-'+CSS.escape(id);c.className='tab-btn-container'+(currentTab===id?' active':'');
    c.onclick=()=>switchToTab(id);
    enableTabDrag(c);
    const icon=document.createElement('span');icon.className='tab-icon';icon.innerText='⬛';
    const content=document.createElement('div');content.className='tab-btn-content';
    const title=document.createElement('span');title.className='tab-title-text';title.innerText='Tab '+tabId;
    const sub=document.createElement('span');sub.id='tab-subtext-'+tabId;sub.className='tab-subtext';
    content.appendChild(title);content.appendChild(sub);
    const close=document.createElement('button');close.className='tab-close-btn';close.innerText='×';
    close.onclick=async e=>{e.stopPropagation();if(await uiConfirm('Delete Tab '+tabId+'?',{okText:'Delete',danger:true}))sendJson({type:'control',action:'delete_tab',tab_id:tabId});};
    c.appendChild(icon);c.appendChild(content);c.appendChild(close);
    document.getElementById('tabs').appendChild(c);
}
function removeTermTab(tabId){
    const id='term-'+tabId;
    const el=document.getElementById('tab-'+CSS.escape(id));if(el)el.remove();
    const cont=document.getElementById('content-'+CSS.escape(id));if(cont)cont.remove();
    if(terminals[tabId]){terminals[tabId].term.dispose();delete terminals[tabId];}
    if(currentTab===id){const k=Object.keys(terminals);if(k.length)switchToTab('term-'+k[0]);else{const et=Object.keys(editorTabs);if(et.length)switchToTab(et[0]);}}
}
function requestNewTab(){sendJson({type:'control',action:'request_new_tab'});}

// Editor tabs
function checkDirty(id){
    const et=editorTabs[id];if(!et)return;
    const val=et.cm?et.cm.getValue():(et.textarea?et.textarea.value:'');
    const isDirty=val!==et.original;
    const tabEl=document.getElementById('tab-'+CSS.escape(id));
    if(tabEl)tabEl.classList.toggle('dirty',isDirty);
}

function openEditorTab(path,text){
    const id='file:'+path;
    if(editorTabs[id]){
        if(editorTabs[id].cm){
            editorTabs[id].cm.setValue(text);
            setTimeout(()=>{try{editorTabs[id].cm.refresh();}catch(e){}},20);
        }
        else if(editorTabs[id].textarea)editorTabs[id].textarea.value=text;
        editorTabs[id].original=text;
        checkDirty(id);
        switchToTab(id);
        return;
    }
    const isText=isTextFile(path);
    // Container
    const cont=document.createElement('div');cont.id='content-'+CSS.escape(id);cont.dataset.type='editor';cont.className='editor-container';cont.style.display='none';
    // Bar
    const bar=document.createElement('div');bar.className='editor-bar';
    bar.innerHTML=`<span class="editor-path">${esc(path)}</span><span class="editor-lang">${getLang(path)}</span><span class="editor-status" id="estatus-${CSS.escape(id)}"></span>`;
    if(isText){
        const saveBtn=document.createElement('button');saveBtn.className='editor-save';saveBtn.innerText='💾 Save';
        saveBtn.onclick=()=>saveEditor(id,path);bar.appendChild(saveBtn);
    }
    cont.appendChild(bar);

    let cmInstance=null, taInstance=null;
    if(isText){
        if(typeof CodeMirror!=='undefined'){
            try {
                cmInstance=CodeMirror(cont,{
                    value:text,
                    mode:getCodeMirrorMode(path),
                    theme:'material-darker',
                    lineNumbers:true,
                    indentUnit:4,
                    tabSize:4,
                    indentWithTabs:true,
                    lineWrapping:false
                });
                cmInstance.on('change',()=>checkDirty(id));
                editorTabs[id]={path,cm:cmInstance,textarea:null,original:text};
            } catch(cmErr){
                console.warn('CodeMirror failed to initialize, falling back to textarea:', cmErr);
                cmInstance=null;
            }
        }
        if(!cmInstance){
            taInstance=document.createElement('textarea');taInstance.className='editor-textarea';taInstance.spellcheck=false;taInstance.value=text;
            taInstance.addEventListener('keydown',e=>{if(e.key==='Tab'){e.preventDefault();const s=taInstance.selectionStart;taInstance.value=taInstance.value.substring(0,s)+'\t'+taInstance.value.substring(taInstance.selectionEnd);taInstance.selectionStart=taInstance.selectionEnd=s+1;}});
            taInstance.addEventListener('input',()=>checkDirty(id));
            cont.appendChild(taInstance);
            editorTabs[id]={path,cm:null,textarea:taInstance,original:text};
        }
    } else {
        const bp=document.createElement('div');bp.className='binary-preview';
        bp.innerHTML=`<span class="bp-icon">${fileIcon(path)}</span><span class="bp-msg">Binary file — cannot preview</span><span class="bp-msg" style="font-size:11px;color:#484f58">${fmtSize(text.length)} · ${path}</span>`;
        cont.appendChild(bp);
        editorTabs[id]={path,cm:null,textarea:null,original:null};
    }
    document.getElementById('terminal-wrapper').appendChild(cont);

    // Tab button
    const c=document.createElement('div');c.id='tab-'+CSS.escape(id);c.className='tab-btn-container editor-tab';
    c.onclick=()=>switchToTab(id);
    enableTabDrag(c);
    const icon=document.createElement('span');icon.className='tab-icon';icon.innerText=fileIcon(path);
    const content=document.createElement('div');content.className='tab-btn-content';
    const title=document.createElement('span');title.className='tab-title-text';title.innerText=basename(path);
    content.appendChild(title);

    const dirtyDot=document.createElement('span');dirtyDot.className='tab-dirty-dot';dirtyDot.innerText='●';dirtyDot.title='Unsaved changes';
    const close=document.createElement('button');close.className='tab-close-btn';close.innerText='×';
    close.onclick=e=>{e.stopPropagation();closeEditorTab(id);};

    c.appendChild(icon);c.appendChild(content);c.appendChild(dirtyDot);c.appendChild(close);
    document.getElementById('tabs').appendChild(c);
    switchToTab(id);
    if(cmInstance){
        requestAnimationFrame(()=>{try{cmInstance.refresh();}catch(e){}});
        setTimeout(()=>{try{cmInstance.refresh();}catch(e){}},30);
        setTimeout(()=>{try{cmInstance.refresh();}catch(e){}},120);
    }
}

async function closeEditorTab(id){
    const et=editorTabs[id];
    if(et){
        const val=et.cm?et.cm.getValue():(et.textarea?et.textarea.value:'');
        if(val!==et.original&&!await uiConfirm('File has unsaved changes. Close anyway?',{title:'Unsaved Changes',okText:'Close',danger:true}))return;
    }
    const el=document.getElementById('tab-'+CSS.escape(id));if(el)el.remove();
    const cont=document.getElementById('content-'+CSS.escape(id));if(cont)cont.remove();
    delete editorTabs[id];
    if(currentTab===id){const k=Object.keys(terminals);if(k.length)switchToTab('term-'+k[0]);else{const et2=Object.keys(editorTabs);if(et2.length)switchToTab(et2[0]);}}
}

function saveEditor(id,path){
    const et=editorTabs[id];if(!et)return;
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
    document.getElementById('file-explorer').style.display=fileManagerOpen?'flex':'none';
    const resizer=document.getElementById('fe-resizer');
    if(resizer)resizer.style.display=fileManagerOpen?'block':'none';
    const b=document.getElementById('toggle-files-btn');
    if(b)b.classList.toggle('active',fileManagerOpen);
    if(fileManagerOpen)requestDir(currentFilePath);
    setTimeout(refitActive,100);
}
function requestDir(p){currentFilePath=p;sendJson({type:'control',action:'req_dir',path:p});}

function renderFileList(path,files){
    renderBreadcrumb(path);
    hideInlineInput();
    const filterInput=document.getElementById('fe-filter');
    if(filterInput)filterInput.value='';
    const list=document.getElementById('fe-list');list.innerHTML='';
    const parentPath = path.endsWith('/') ? (path + '..') : (path + '/..');
    list.appendChild(mkItem('📁','..','',parentPath,true,()=>requestDir(parentPath)));
    if(!files||!files.length){list.appendChild(Object.assign(document.createElement('div'),{className:'fe-empty',innerText:'Empty directory'}));return;}
    files.forEach(f=>{
        const fp=(path.endsWith('/')?path:path+'/')+f.name;
        if(f.is_dir){list.appendChild(mkItem('📁',f.name,'',fp,true,()=>requestDir(fp)));}
        else{list.appendChild(mkItem(fileIcon(f.name),f.name,fmtSize(f.size),fp,false,()=>{_log.info('Open file',{path:fp,isText:isTextFile(f.name)});if(!isTextFile(f.name)){downloadFile(fp,f.name);return;}openFileForEdit(fp);}));}
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

function openFileForEdit(path){
    if(!beginFileOp())return;
    currentTransferId=newTransferId();
    pendingDownload=false;
    pendingDownloadName=null;
    sendJson({type:'control',action:'req_read_file',path,transfer_id:currentTransferId});
}

function mkItem(icon,name,size,fullPath,isDir,onclick){
    const item=document.createElement('div');item.className='fe-item'+(isDir?' is-dir':'');
    const iconEl=document.createElement('span');iconEl.className='fe-icon';iconEl.innerText=icon;
    const nameEl=document.createElement('span');nameEl.className='fe-name';nameEl.innerText=name;
    const sizeEl=document.createElement('span');sizeEl.className='fe-size';sizeEl.innerText=size;
    const aDiv=document.createElement('div');aDiv.className='fe-item-actions';
    // Download action for files
    if(!isDir){
        const dlBtn=document.createElement('button');dlBtn.innerText='⬇';dlBtn.title='Download';dlBtn.className='fe-download';
        dlBtn.onclick=e=>{e.stopPropagation();downloadFile(fullPath,name,e);};
        aDiv.appendChild(dlBtn);
    }
    // Rename
    const renBtn=document.createElement('button');renBtn.innerText='✏️';renBtn.title='Rename';
    renBtn.onclick=e=>{e.stopPropagation();startInlineRename(item,nameEl,fullPath,name);};
    // Delete
    const delBtn=document.createElement('button');delBtn.innerText='🗑';delBtn.title='Delete';delBtn.className='fe-delete';
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
    const hint=document.createElement('span');hint.className='bc-hint';hint.innerText='Enter ↵';
    inp.onkeydown=e=>{if(e.key==='Enter'){e.preventDefault();const v=inp.value.trim();if(v)requestDir(v);else renderBreadcrumb(currentFilePath);}if(e.key==='Escape'){renderBreadcrumb(currentFilePath);}};
    inp.onblur=()=>{setTimeout(()=>renderBreadcrumb(currentFilePath),150);};
    bc.appendChild(inp);bc.appendChild(hint);inp.focus();inp.select();
}

// ===== INLINE INPUT (new file/folder) =====
function showInlineInput(type){
    const el=document.getElementById('fe-inline');el.style.display='flex';el.innerHTML='';
    const icon=document.createElement('span');icon.className='inline-icon';icon.innerText=type==='dir'?'📁':'📄';
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
        const old=btn.innerText;
        btn.innerText='✓ Copied!';
        btn.classList.add('copied');
        setTimeout(()=>{btn.innerText=old;btn.classList.remove('copied');},2000);
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
    Object.keys(tabs).forEach(tid=>{const s=document.getElementById('tab-subtext-'+tid);if(s&&tabs[tid]&&tabs[tid].length)s.innerText=tabs[tid].join(', ');});
    const list=document.getElementById('users-list');list.innerHTML='';
    Object.keys(tabs).forEach(tid=>(tabs[tid]||[]).forEach(name=>{const i=document.createElement('div');i.className='user-item';i.innerHTML=`<span class="dot"></span><span class="user-name">${esc(name)}</span><span class="user-tab-badge">Tab ${tid}</span>`;list.appendChild(i);}));
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
    }
    if(tabName === 'chat'){
        unreadChatCount = 0;
        const b = document.getElementById('chat-badge');
        if(b) b.style.display = 'none';
        const ci = document.getElementById('chat-input');
        if(ci) setTimeout(() => ci.focus(), 60);
    }
    setTimeout(refitActive, 50);
}

function toggleSidebar(preferredTab){
    const ws = document.getElementById('workspace');
    const isCollapsed = ws.classList.contains('sidebar-collapsed');
    if(!preferredTab) preferredTab = 'collab';

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
    setTimeout(refitActive, 200);
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

    if(currentSidebarTab !== 'activity'){
        unreadActivityCount++;
        const badge = document.getElementById('activity-badge');
        if(badge){
            badge.innerText = unreadActivityCount > 99 ? '99+' : unreadActivityCount;
            badge.style.display = 'inline-block';
        }
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

window.addEventListener('resize',refitActive);
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

    document.addEventListener('keydown',e=>{
        if((e.ctrlKey||e.metaKey)&&e.key==='s'){
            const id=currentTab;if(id.startsWith('file:')&&editorTabs[id]){e.preventDefault();saveEditor(id,editorTabs[id].path);}
        }
        if((e.ctrlKey||e.metaKey)&&e.key.toLowerCase()==='f'){
            const fi=document.getElementById('fe-filter');
            if(fi&&fileManagerOpen){
                e.preventDefault();
                fi.focus();
                fi.select();
            }
        }
        if(e.key==='Escape'){
            const jm=document.getElementById('join-modal');
            if(jm&&jm.style.display!=='none')closeJoinModal();
            const cf=document.getElementById('confirm-modal');
            if(cf&&cf.style.display!=='none')closeConfirm(false);
            const hm=document.getElementById('help-modal');
            if(hm&&hm.style.display!=='none')toggleHelpModal();
            const sm=document.getElementById('share-modal');
            if(sm&&sm.style.display!=='none')toggleShareModal(false);
        }
    });

    initFileExplorerResizer();
});

function initFileExplorerResizer() {
    const resizer = document.getElementById('fe-resizer');
    const fe = document.getElementById('file-explorer');
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
        refitActive();
    });

    window.addEventListener('mouseup', () => {
        if (isDragging) {
            isDragging = false;
            resizer.classList.remove('is-dragging');
            document.body.style.cursor = '';
            document.body.style.userSelect = '';
            try {
                localStorage.setItem('rmte_fe_width', parseInt(fe.style.width, 10));
            } catch(e) {}
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
        webPreviewOpen = !webPreviewOpen;
    }
    const panel = document.getElementById('preview-panel');
    const termWrap = document.getElementById('terminal-wrapper');
    const btn = document.getElementById('toggle-preview-btn');
    if (panel) panel.style.display = webPreviewOpen ? 'flex' : 'none';
    if (termWrap) termWrap.style.display = webPreviewOpen ? 'none' : 'block';
    if (btn) btn.classList.toggle('active', webPreviewOpen);

    if (webPreviewOpen) {
        document.querySelectorAll('.tab-btn-container').forEach(b => b.classList.remove('active'));
        const portInput = document.getElementById('preview-port-input');
        if (portInput && !portInput.value) {
            portInput.value = previewPort || 8080;
        }
        const frame = document.getElementById('preview-frame');
        if (frame && (!frame.src || frame.src === 'about:blank')) {
            previewNavigate();
        }
    } else {
        switchToTab(currentTab || 'term-0');
    }
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

