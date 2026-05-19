// RMTE v0.2 — Web Viewer with Editor Tabs + File Manager
let ws, aesKey, currentTab = 'term-0', myUsername = '';
const myViewerId = 'v-web-' + Math.random().toString(16).slice(2,10);
let terminals = {}, editorTabs = {};
let fileManagerOpen = false, currentFilePath = './';
let waitingForFileData = false, pendingFileBytes = null, pendingEditorPath = null;
const DATA_CH = 255;

const TEXT_EXT = new Set('go,js,ts,jsx,tsx,py,rb,rs,c,cpp,h,hpp,java,kt,cs,php,html,css,scss,less,json,yaml,yml,toml,xml,sql,md,txt,log,csv,ini,cfg,conf,env,sh,bash,bat,ps1,cmd,mod,sum,lock,editorconfig,gitignore,makefile,dockerfile'.split(','));
const LANG_MAP = {go:'Go',js:'JavaScript',ts:'TypeScript',py:'Python',rs:'Rust',html:'HTML',css:'CSS',json:'JSON',md:'Markdown',yaml:'YAML',yml:'YAML',sh:'Shell',sql:'SQL',c:'C',cpp:'C++',java:'Java',rb:'Ruby',php:'PHP',xml:'XML',toml:'TOML'};

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

// ===== CONNECTION =====
async function connect() {
    const server=document.getElementById('server').value, sessionId=document.getElementById('sessionId').value;
    const password=document.getElementById('password').value, uname=document.getElementById('username').value.trim();
    hideError();
    if(!sessionId||!password){showError('Session ID and Password required');return;}
    const btn=document.getElementById('connect-btn'); btn.innerText='Connecting...'; btn.disabled=true;
    myUsername=uname||('Web-'+Math.random().toString(36).slice(2,6).toUpperCase());
    if(ws){try{ws.close();}catch(e){}}
    try {
        const enc=new TextEncoder();
        aesKey=await crypto.subtle.importKey('raw',await crypto.subtle.digest('SHA-256',enc.encode(password)),{name:'AES-GCM'},false,['encrypt','decrypt']);
        const authToken=Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256',enc.encode('rmte-auth:'+password)))).map(b=>b.toString(16).padStart(2,'0')).join('');
        ws=new WebSocket(server); ws.binaryType='arraybuffer';
        ws.onopen=()=>{_log.info('WS connected');sendRaw(JSON.stringify({type:'auth',role:'viewer',session_id:sessionId,viewer_id:myViewerId,viewer_name:myUsername,auth_token:authToken,protocol_version:'0.2'}));};
        ws.onclose=e=>{_log.warn('WS closed',{code:e.code});const s=document.getElementById('sb-connection');if(s){s.innerText='● Disconnected';s.style.color='#f85149';}};
        ws.onerror=()=>{_log.err('WS error');showError('Connection failed');btn.innerText='Connect';btn.disabled=false;};
        ws.onmessage=async e=>{try{typeof e.data==='string'?await onJson(JSON.parse(e.data)):await onBinary(new Uint8Array(e.data));}catch(err){_log.err('msg handler',err);}};
    } catch(e){_log.err('connect',e);showError(e.message);btn.innerText='Connect';btn.disabled=false;}
}
function sendRaw(d){if(ws&&ws.readyState===1)ws.send(d);else _log.err('WS not open');}
function sendJson(m){_log.out('json:'+m.action,m);sendRaw(JSON.stringify(m));}
async function sendBin(tabId,plain){const iv=crypto.getRandomValues(new Uint8Array(12));const ct=await crypto.subtle.encrypt({name:'AES-GCM',iv},aesKey,plain);const p=new Uint8Array(1+12+ct.byteLength);p[0]=tabId;p.set(iv,1);p.set(new Uint8Array(ct),13);sendRaw(p);}

// ===== MESSAGE HANDLERS =====
async function onJson(msg) {
    _log.in(msg.action||msg.type,msg);
    if(msg.type==='auth_success'){
        document.getElementById('setup').style.display='none';
        document.getElementById('terminal-container').style.display='flex';
        document.getElementById('sb-session').innerText='Session: '+(document.getElementById('sessionId').value);
        document.getElementById('sb-user').innerText=myUsername;
        ['server','sessionId','password','username'].forEach(k=>sessionStorage.setItem('rmte_'+k,document.getElementById(k).value));
        sessionStorage.setItem('rmte_autoconnect','true');sessionStorage.setItem('rmte_username',myUsername);
        const s=document.getElementById('sb-connection');if(s){s.innerText='● Connected';s.style.color='#fff';}
        sendJson({type:'control',action:'get_tabs'});return;
    }
    if(msg.type==='error'){showError(msg.message);document.getElementById('connect-btn').innerText='Connect';document.getElementById('connect-btn').disabled=false;return;}
    if(msg.type!=='control')return;
    switch(msg.action){
        case'tabs_list':{const t=msg.tabs||[];t.forEach(id=>addTermTabBtn(id));if(t.length>0&&!terminals[t[0]]){initTerminal(t.includes(0)?0:t[0]);}break;}
        case'tab_created':addTermTabBtn(msg.tab_id);break;
        case'tab_deleted':removeTermTab(msg.tab_id);break;
        case'sync_data':{const b=Uint8Array.from(atob(msg.data),c=>c.charCodeAt(0));await onBinary(b);}break;
        case'presence':updatePresence(msg.tabs);break;
        case'chat_history':document.getElementById('chat-messages').innerHTML='';(msg.history||[]).forEach(m=>appendChat(m.sender,m.message,m.time));break;
        case'chat':appendChat(msg.sender,msg.message,msg.time);break;
        // File Manager
        case'dir_data':renderFileList(msg.path,msg.files);break;
        case'read_file_start':_log.info('Waiting Tab255',{path:msg.path});waitingForFileData=true;pendingEditorPath=msg.path;break;
        case'ready_for_data':_log.info('Host ready, sending data');await sendPendingFile();break;
        case'file_saved':_log.info('Saved',msg);setEditorStatus(pendingEditorPath||msg.path,'Saved ✓');requestDir(currentFilePath);break;
        case'file_created':case'dir_created':requestDir(currentFilePath);break;
        case'file_renamed':case'file_deleted':requestDir(currentFilePath);break;
        case'fm_error':_log.err('FM: '+msg.message);showToast(msg.message);break;
        default:_log.warn('Unhandled: '+msg.action);
    }
}
async function onBinary(raw) {
    const tabId=raw[0],iv=raw.slice(1,13),ct=raw.slice(13);
    if(ct.length===0){_log.warn('Empty ct',{tabId});return;}
    try {
        const dec=await crypto.subtle.decrypt({name:'AES-GCM',iv},aesKey,ct);
        if(tabId===DATA_CH){
            if(waitingForFileData){waitingForFileData=false;const txt=new TextDecoder().decode(dec);_log.info('File received',{bytes:dec.byteLength});openEditorTab(pendingEditorPath,txt);}
            else _log.warn('Tab255 data but not waiting');
            return;
        }
        if(!terminals[tabId])initTerminal(tabId);
        terminals[tabId].term.write(new Uint8Array(dec));
    }catch(e){_log.err('Decrypt fail',{tabId,e:e.message});}
}

// ===== TAB SYSTEM =====
// Tab IDs: "term-N" for terminals, "file:path" for editors
function activeTabId(){return currentTab;}
function switchToTab(id){
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
    }
    setTimeout(refitActive,50);
}
function refitActive(){
    if(!currentTab.startsWith('term-'))return;
    const tid=parseInt(currentTab.slice(5)),t=terminals[tid];
    if(!t)return;
    try{t.fitAddon.fit();sendJson({type:'control',action:'resize',tab_id:tid,cols:t.term.cols,rows:t.term.rows});}catch(e){}
}

// Terminal tabs
function initTerminal(tabId){
    if(terminals[tabId])return;
    const id='term-'+tabId;
    const cont=document.createElement('div');cont.id='content-'+CSS.escape(id);cont.dataset.type='terminal';
    cont.style.cssText='height:100%;width:100%;display:'+(currentTab===id?'block':'none');
    document.getElementById('terminal-wrapper').appendChild(cont);
    const t=new Terminal({cursorBlink:true,convertEol:true,theme:{background:'#0d1117',foreground:'#e6edf3',cursor:'#58a6ff'},fontFamily:"'Consolas','Courier New',monospace",fontSize:14});
    const fa=new FitAddon.FitAddon();t.loadAddon(fa);
    terminals[tabId]={term:t,fitAddon:fa};
    t.open(cont);fa.fit();
    t.onData(d=>sendBin(tabId,new TextEncoder().encode(d)));
    addTermTabBtn(tabId);
    if(!document.querySelector('.tab-btn-container.active'))switchToTab(id);
}
function addTermTabBtn(tabId){
    const id='term-'+tabId;
    if(document.getElementById('tab-'+CSS.escape(id)))return;
    const c=document.createElement('div');c.id='tab-'+CSS.escape(id);c.className='tab-btn-container'+(currentTab===id?' active':'');
    const icon=document.createElement('span');icon.className='tab-icon';icon.innerText='⬛';
    const content=document.createElement('div');content.className='tab-btn-content';content.onclick=()=>switchToTab(id);
    const title=document.createElement('span');title.className='tab-title-text';title.innerText='Tab '+tabId;
    const sub=document.createElement('span');sub.id='tab-subtext-'+tabId;sub.className='tab-subtext';
    content.appendChild(title);content.appendChild(sub);
    const close=document.createElement('button');close.className='tab-close-btn';close.innerText='×';
    close.onclick=e=>{e.stopPropagation();if(confirm('Delete Tab '+tabId+'?'))sendJson({type:'control',action:'delete_tab',tab_id:tabId});};
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
function openEditorTab(path,text){
    const id='file:'+path;
    if(editorTabs[id]){// Already open - update content and switch
        editorTabs[id].textarea.value=text;editorTabs[id].original=text;switchToTab(id);return;
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
    if(isText){
        const ta=document.createElement('textarea');ta.className='editor-textarea';ta.spellcheck=false;ta.value=text;
        ta.addEventListener('keydown',e=>{if(e.key==='Tab'){e.preventDefault();const s=ta.selectionStart;ta.value=ta.value.substring(0,s)+'\t'+ta.value.substring(ta.selectionEnd);ta.selectionStart=ta.selectionEnd=s+1;}});
        cont.appendChild(ta);
        editorTabs[id]={path,textarea:ta,original:text};
    } else {
        const bp=document.createElement('div');bp.className='binary-preview';
        bp.innerHTML=`<span class="bp-icon">${fileIcon(path)}</span><span class="bp-msg">Binary file — cannot preview</span><span class="bp-msg" style="font-size:11px;color:#484f58">${fmtSize(text.length)} · ${path}</span>`;
        cont.appendChild(bp);
        editorTabs[id]={path,textarea:null,original:null};
    }
    document.getElementById('terminal-wrapper').appendChild(cont);
    // Tab button
    const c=document.createElement('div');c.id='tab-'+CSS.escape(id);c.className='tab-btn-container editor-tab';
    const icon=document.createElement('span');icon.className='tab-icon';icon.innerText=fileIcon(path);
    const content=document.createElement('div');content.className='tab-btn-content';content.onclick=()=>switchToTab(id);
    const title=document.createElement('span');title.className='tab-title-text';title.innerText=basename(path);
    content.appendChild(title);
    const close=document.createElement('button');close.className='tab-close-btn';close.innerText='×';
    close.onclick=e=>{e.stopPropagation();closeEditorTab(id);};
    c.appendChild(icon);c.appendChild(content);c.appendChild(close);
    document.getElementById('tabs').appendChild(c);
    switchToTab(id);
}
function closeEditorTab(id){
    const et=editorTabs[id];
    if(et&&et.textarea&&et.textarea.value!==et.original&&!confirm('Unsaved changes. Close anyway?'))return;
    const el=document.getElementById('tab-'+CSS.escape(id));if(el)el.remove();
    const cont=document.getElementById('content-'+CSS.escape(id));if(cont)cont.remove();
    delete editorTabs[id];
    if(currentTab===id){const k=Object.keys(terminals);if(k.length)switchToTab('term-'+k[0]);else{const et2=Object.keys(editorTabs);if(et2.length)switchToTab(et2[0]);}}
}
function saveEditor(id,path){
    const et=editorTabs[id];if(!et||!et.textarea)return;
    pendingFileBytes=new TextEncoder().encode(et.textarea.value);
    pendingEditorPath=path;
    setEditorStatus(path,'Saving...');
    sendJson({type:'control',action:'prepare_save',path});
}
function setEditorStatus(path,text){
    const id='file:'+path;
    const el=document.getElementById('estatus-'+CSS.escape(id));
    if(el){el.innerText=text;if(text)setTimeout(()=>{if(el.innerText===text)el.innerText='';},3000);}
    // Update original on save success
    if(text==='Saved ✓'&&editorTabs[id]&&editorTabs[id].textarea)editorTabs[id].original=editorTabs[id].textarea.value;
}
async function sendPendingFile(){if(!pendingFileBytes){_log.err('No pending data');return;}const d=pendingFileBytes;pendingFileBytes=null;await sendBin(DATA_CH,d);}

// ===== FILE MANAGER =====
function toggleFileManager(){
    fileManagerOpen=!fileManagerOpen;
    document.getElementById('file-explorer').style.display=fileManagerOpen?'flex':'none';
    document.getElementById('toggle-files-btn').classList.toggle('active',fileManagerOpen);
    if(fileManagerOpen)requestDir(currentFilePath);
    setTimeout(refitActive,200);
}
function requestDir(p){currentFilePath=p;sendJson({type:'control',action:'req_dir',path:p});}
function renderFileList(path,files){
    renderBreadcrumb(path);
    hideInlineInput();
    const list=document.getElementById('fe-list');list.innerHTML='';
    const parentPath = path.endsWith('/') ? (path + '..') : (path + '/..');
    list.appendChild(mkItem('📁','..','',parentPath,true,()=>requestDir(parentPath)));
    if(!files||!files.length){list.appendChild(Object.assign(document.createElement('div'),{className:'fe-empty',innerText:'Empty directory'}));return;}
    files.forEach(f=>{
        const fp=(path.endsWith('/')?path:path+'/')+f.name;
        if(f.is_dir){list.appendChild(mkItem('📁',f.name,'',fp,true,()=>requestDir(fp)));}
        else{list.appendChild(mkItem(fileIcon(f.name),f.name,fmtSize(f.size),fp,false,()=>{_log.info('Open file',{path:fp,isText:isTextFile(f.name)});if(!isTextFile(f.name)){openEditorTab(fp,'');return;}sendJson({type:'control',action:'req_read_file',path:fp});}));}
    });
}
function mkItem(icon,name,size,fullPath,isDir,onclick){
    const item=document.createElement('div');item.className='fe-item'+(isDir?' is-dir':'');
    const iconEl=document.createElement('span');iconEl.className='fe-icon';iconEl.innerText=icon;
    const nameEl=document.createElement('span');nameEl.className='fe-name';nameEl.innerText=name;
    const sizeEl=document.createElement('span');sizeEl.className='fe-size';sizeEl.innerText=size;
    const aDiv=document.createElement('div');aDiv.className='fe-item-actions';
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

// ===== BREADCRUMB (editable) =====
function renderBreadcrumb(path){
    const bc=document.getElementById('fe-breadcrumb');bc.innerHTML='';
    bc.dataset.editing='false';
    const span=document.createElement('span');
    span.style.cssText='font-family:var(--font-mono);font-size:11px;color:var(--text-secondary);overflow:hidden;text-overflow:ellipsis;white-space:nowrap;width:100%;';
    span.innerText=path;
    span.title='Double click to edit path';
    bc.appendChild(span);
}
function editBreadcrumb(){
    const bc=document.getElementById('fe-breadcrumb');
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
    el.innerHTML=`<span>⚠ ${esc(msg)}</span>`;
    const btn=document.createElement('button');btn.className='toast-close';btn.innerText='×';btn.onclick=()=>{el.style.display='none';};
    el.appendChild(btn);
    setTimeout(()=>{el.style.display='none';},5000);
}

async function handleFileUpload(e){
    const f=e.target.files[0];if(!f)return;
    const r=new FileReader();r.onload=()=>{pendingFileBytes=new Uint8Array(r.result);const p=(currentFilePath.endsWith('/')?currentFilePath:currentFilePath+'/')+f.name;sendJson({type:'control',action:'prepare_upload',path:p});};
    r.readAsArrayBuffer(f);e.target.value='';
}

// ===== PRESENCE + CHAT =====
function updatePresence(tabs){
    document.querySelectorAll('.tab-subtext').forEach(el=>el.innerText='');
    Object.keys(tabs).forEach(tid=>{const s=document.getElementById('tab-subtext-'+tid);if(s&&tabs[tid]&&tabs[tid].length)s.innerText=tabs[tid].join(', ');});
    const list=document.getElementById('users-list');list.innerHTML='';
    Object.keys(tabs).forEach(tid=>(tabs[tid]||[]).forEach(name=>{const i=document.createElement('div');i.className='user-item';i.innerHTML=`<span class="dot"></span><span class="user-name">${esc(name)}</span><span class="user-tab-badge">Tab ${tid}</span>`;list.appendChild(i);}));
}
function handleChatKey(e){if(e.key==='Enter')sendChatMessage();}
function sendChatMessage(){const i=document.getElementById('chat-input'),t=(i.value||'').trim();if(!t)return;sendJson({type:'control',action:'chat',sender:myUsername,message:t,time:new Date().toLocaleTimeString([],{hour:'2-digit',minute:'2-digit'})});i.value='';}
function appendChat(sender,msg,time){const c=document.getElementById('chat-messages');const el=document.createElement('div');el.className='chat-msg';el.innerHTML=`<div class="chat-msg-header"><span class="chat-msg-sender${sender===myUsername?' self':''}">${esc(sender)}</span><span>${time||''}</span></div><div class="chat-msg-body">${esc(msg)}</div>`;c.appendChild(el);c.scrollTop=c.scrollHeight;}

// ===== UI =====
function toggleSidebar(){document.getElementById('workspace').classList.toggle('sidebar-collapsed');setTimeout(refitActive,200);}
function disconnectSession(){sessionStorage.setItem('rmte_autoconnect','false');if(ws)ws.close();location.reload();}
function showError(m){const e=document.getElementById('setup-error');e.style.display='block';e.innerText=m;}
function hideError(){document.getElementById('setup-error').style.display='none';}

window.addEventListener('resize',refitActive);
window.addEventListener('DOMContentLoaded',()=>{
    ['server','sessionId','password','username'].forEach(k=>{const v=sessionStorage.getItem('rmte_'+k);if(v)document.getElementById(k).value=v;});
    if(sessionStorage.getItem('rmte_autoconnect')==='true')connect();
    document.addEventListener('keydown',e=>{
        if((e.ctrlKey||e.metaKey)&&e.key==='s'){
            const id=currentTab;if(id.startsWith('file:')&&editorTabs[id]&&editorTabs[id].textarea){e.preventDefault();saveEditor(id,editorTabs[id].path);}
        }
    });
});
