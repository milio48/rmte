let ws;
let aesKey;
let currentTab = 0;
let terminals = {}; // tabID -> { term, container, fitAddon }
let myUsername = "";
const myViewerId = "v-web-" + Math.random().toString(16).slice(2, 10);
let debugLogs = [];

function logDebug(direction, type, payload) {
    const entry = { time: new Date().toISOString(), direction, type, payload };
    debugLogs.push(entry);
    console.debug("[RMTE_DEBUG]", JSON.stringify(entry));
}

window.dumpDebug = () => {
    console.log(JSON.stringify(debugLogs, null, 2));
}

async function connect() {
	const server = document.getElementById('server').value;
	const sessionId = document.getElementById('sessionId').value;
	const password = document.getElementById('password').value;
	const usernameInput = document.getElementById('username').value.trim();

	if (!sessionId || !password) return alert("Session ID and Password required");

	myUsername = usernameInput || ("Web-" + Math.random().toString(36).slice(2, 6).toUpperCase());

	// Derive Key using SHA-256 to match Go's sha256.Sum256
	const enc = new TextEncoder();
	const msgUint8 = enc.encode(password);
	const hashBuffer = await crypto.subtle.digest('SHA-256', msgUint8);
	aesKey = await crypto.subtle.importKey(
		"raw",
		hashBuffer,
		{ name: "AES-GCM" },
		false,
		["encrypt", "decrypt"]
	);

	ws = new WebSocket(server);
	ws.binaryType = 'arraybuffer';

	ws.onopen = () => {
        const authMsg = {
			type: "auth",
			role: "viewer",
			session_id: sessionId,
			viewer_id: myViewerId,
			viewer_name: myUsername
		};
        logDebug("OUT", "auth", authMsg);
		ws.send(JSON.stringify(authMsg));
	};

	ws.onmessage = async (event) => {
		if (typeof event.data === 'string') {
			const msg = JSON.parse(event.data);
            logDebug("IN", "json", msg);
			if (msg.type === 'auth_success') {
				document.getElementById('setup').style.display = 'none';
				document.getElementById('terminal-container').style.display = 'flex';
                
                // Persist session for autoconnect upon reload using sessionStorage
                sessionStorage.setItem('rmte_autoconnect', 'true');
                sessionStorage.setItem('rmte_server', server);
                sessionStorage.setItem('rmte_sessionId', sessionId);
                sessionStorage.setItem('rmte_password', password);
                sessionStorage.setItem('rmte_username', myUsername);

                // Request active tabs immediately
                const getTabsMsg = { type: "control", action: "get_tabs" };
                logDebug("OUT", "json", getTabsMsg);
                ws.send(JSON.stringify(getTabsMsg));
			} else if (msg.type === 'control' && msg.action === 'chat_history') {
				const msgsDiv = document.getElementById('chat-messages');
				if (msgsDiv) {
					msgsDiv.innerHTML = '';
				}
				if (msg.history) {
					msg.history.forEach(chatMsg => {
						appendChatMessage(chatMsg.sender, chatMsg.message, chatMsg.time);
					});
				}
			} else if (msg.type === 'control' && msg.action === 'tab_created') {
				addTabButton(msg.tab_id);
			} else if (msg.type === 'control' && msg.action === 'tab_deleted') {
				const container = document.getElementById(`tab-container-${msg.tab_id}`);
				if (container) {
					container.remove();
				}
				
				if (terminals[msg.tab_id]) {
					terminals[msg.tab_id].term.dispose();
					const wrapper = document.getElementById(`term-container-${msg.tab_id}`);
					if (wrapper) wrapper.remove();
					delete terminals[msg.tab_id];
				}
				
				if (currentTab === msg.tab_id) {
					const remainingTabIds = Object.keys(terminals);
					if (remainingTabIds.length > 0) {
						switchTab(parseInt(remainingTabIds[0]));
					} else {
						currentTab = 0;
					}
				}
			} else if (msg.type === 'control' && msg.action === 'tabs_list') {
                msg.tabs.forEach(tabId => {
                    addTabButton(tabId);
                });
                if (msg.tabs.length > 0 && !terminals[currentTab]) {
                    if (msg.tabs.includes(0)) {
                        initTerminal(0);
                    } else {
                        currentTab = msg.tabs[0];
                        initTerminal(currentTab);
                    }
                }
            } else if (msg.type === 'control' && msg.action === 'sync_data') {
                const binaryString = window.atob(msg.data);
                const bytes = new Uint8Array(binaryString.length);
                for (let i = 0; i < binaryString.length; i++) {
                    bytes[i] = binaryString.charCodeAt(i);
                }
                await handleBinaryBytes(bytes);
            } else if (msg.type === 'control' && msg.action === 'presence') {
                // Clear all tab subtexts
                document.querySelectorAll('.tab-subtext').forEach(el => {
                    el.innerText = '';
                });

                // Update subtexts for tabs with users
                Object.keys(msg.tabs).forEach(tabId => {
                    const sub = document.getElementById(`tab-subtext-${tabId}`);
                    if (sub) {
                        const users = msg.tabs[tabId];
                        if (users && users.length > 0) {
                            sub.innerText = users.join(', ');
                        }
                    }
                });

                // Update Sidebar
                const usersListDiv = document.getElementById('users-list');
                if (usersListDiv) {
                    usersListDiv.innerHTML = '';
                    
                    // Collect all active users across tabs
                    Object.keys(msg.tabs).forEach(tabId => {
                        const users = msg.tabs[tabId];
                        if (users && users.length > 0) {
                            users.forEach(username => {
                                const item = document.createElement('div');
                                item.className = 'user-item';
                                
                                const dot = document.createElement('span');
                                dot.className = 'dot';
                                
                                const nameSpan = document.createElement('span');
                                nameSpan.className = 'user-name';
                                nameSpan.innerText = username;
                                
                                const badge = document.createElement('span');
                                badge.className = 'user-tab-badge';
                                badge.innerText = `Tab ${tabId}`;
                                
                                item.appendChild(dot);
                                item.appendChild(nameSpan);
                                item.appendChild(badge);
                                usersListDiv.appendChild(item);
                            });
                        }
                    });
                }
            } else if (msg.type === 'control' && msg.action === 'chat') {
                appendChatMessage(msg.sender, msg.message, msg.time);
            }
		} else {
			// Binary Frame
			const rawBytes = new Uint8Array(event.data);
            await handleBinaryBytes(rawBytes);
		}
	};
}

async function handleBinaryBytes(rawBytes) {
    const tabId = rawBytes[0];
    const iv = rawBytes.slice(1, 13);
    const ciphertext = rawBytes.slice(13);

    try {
        const decryptedBuffer = await crypto.subtle.decrypt(
            { name: "AES-GCM", iv: iv },
            aesKey,
            ciphertext
        );
        const decodedStr = new TextDecoder().decode(decryptedBuffer);
        logDebug("IN", "binary_text", { tabId, text: decodedStr });
        if (!terminals[tabId]) initTerminal(tabId);
        terminals[tabId].term.write(new Uint8Array(decryptedBuffer));
    } catch (e) {
        console.error("Decryption failed", e);
    }
}

function requestNewTab() {
    if (ws && ws.readyState === WebSocket.OPEN) {
        const msg = { type: "control", action: "request_new_tab" };
        logDebug("OUT", "json", msg);
        ws.send(JSON.stringify(msg));
    }
}

function initTerminal(tabId) {
	if (terminals[tabId]) return;

    const termContainer = document.createElement('div');
    termContainer.id = `term-container-${tabId}`;
    termContainer.style.display = tabId === currentTab ? 'block' : 'none';
    termContainer.style.height = '100%';
    termContainer.style.width = '100%';
    document.getElementById('terminal-wrapper').appendChild(termContainer);

	const t = new Terminal({
		cursorBlink: true,
		convertEol: true,
		theme: { background: '#000', foreground: '#cccccc' },
        fontFamily: "'Consolas', 'Courier New', monospace",
        fontSize: 14
	});
	
	const fitAddon = new FitAddon.FitAddon();
	t.loadAddon(fitAddon);
	
	terminals[tabId] = { term: t, container: termContainer, fitAddon: fitAddon };
	
    t.open(termContainer);
	fitAddon.fit();
    t.onData(data => sendInput(tabId, data));

	addTabButton(tabId);
    
    if (tabId === currentTab) {
        const focusMsg = {
            type: "control",
            action: "set_focus",
            viewer_id: myViewerId,
            viewer_name: myUsername,
            tab_id: tabId
        };
        logDebug("OUT", "json", focusMsg);
        ws.send(JSON.stringify(focusMsg));

        const msg = { type: "control", action: "req_sync", tab_id: tabId };
        logDebug("OUT", "json", msg);
        ws.send(JSON.stringify(msg));
    }
}

function addTabButton(tabId) {
    const tabsDiv = document.getElementById('tabs');
    if (document.getElementById(`tab-container-${tabId}`)) return;
    
    const btnContainer = document.createElement('div');
    btnContainer.id = `tab-container-${tabId}`;
    btnContainer.className = 'tab-btn-container' + (tabId === currentTab ? ' active' : '');
    
    const btnContent = document.createElement('div');
    btnContent.className = 'tab-btn-content';
    btnContent.onclick = () => switchTab(tabId);
    
    const titleText = document.createElement('span');
    titleText.id = `tab-title-${tabId}`;
    titleText.className = 'tab-title-text';
    titleText.innerText = `Tab ${tabId}`;
    
    const subtext = document.createElement('span');
    subtext.id = `tab-subtext-${tabId}`;
    subtext.className = 'tab-subtext';
    subtext.innerText = '';
    
    btnContent.appendChild(titleText);
    btnContent.appendChild(subtext);
    
    const closeBtn = document.createElement('button');
    closeBtn.className = 'tab-close-btn';
    closeBtn.innerText = '×';
    closeBtn.title = "Delete Tab";
    closeBtn.onclick = (e) => {
        e.stopPropagation();
        if (confirm(`Are you sure you want to delete Tab ${tabId}?`)) {
            deleteTab(tabId);
        }
    };
    
    btnContainer.appendChild(btnContent);
    btnContainer.appendChild(closeBtn);
    tabsDiv.appendChild(btnContainer);
}

// ... rest of the file
function deleteTab(tabId) {
    if (ws && ws.readyState === WebSocket.OPEN) {
        const msg = { type: "control", action: "delete_tab", tab_id: tabId };
        logDebug("OUT", "json", msg);
        ws.send(JSON.stringify(msg));
    }
}

function switchTab(tabId) {
    currentTab = tabId;
    document.querySelectorAll('.tab-btn-container').forEach(b => b.classList.remove('active'));
    const activeContainer = document.getElementById(`tab-container-${tabId}`);
    if (activeContainer) activeContainer.classList.add('active');
    
    Object.keys(terminals).forEach(id => {
        terminals[id].container.style.display = (id == tabId) ? 'block' : 'none';
    });
    
    if (terminals[tabId] && terminals[tabId].fitAddon) {
        terminals[tabId].fitAddon.fit();
    }

    // Send set_focus
    const focusMsg = {
        type: "control",
        action: "set_focus",
        viewer_id: myViewerId,
        viewer_name: myUsername,
        tab_id: tabId
    };
    logDebug("OUT", "json", focusMsg);
    ws.send(JSON.stringify(focusMsg));

    // Request sync
    const msg = { type: "control", action: "req_sync", tab_id: tabId };
    logDebug("OUT", "json", msg);
    ws.send(JSON.stringify(msg));
}

async function sendInput(tabId, data) {
    logDebug("OUT", "binary_text_input", { tabId, text: data });
    const enc = new TextEncoder();
    const plaintext = enc.encode(data);
    const iv = crypto.getRandomValues(new Uint8Array(12));
    const ciphertext = await crypto.subtle.encrypt(
        { name: "AES-GCM", iv: iv },
        aesKey,
        plaintext
    );

    const payload = new Uint8Array(1 + 12 + ciphertext.byteLength);
    payload[0] = tabId;
    payload.set(iv, 1);
    payload.set(new Uint8Array(ciphertext), 13);

    ws.send(payload);
}

window.addEventListener('resize', () => {
    if (terminals[currentTab]) {
        const active = terminals[currentTab];
        if (active.fitAddon) {
            active.fitAddon.fit();
        }
        if (ws && ws.readyState === WebSocket.OPEN) {
            const resizeMsg = {
                type: "control",
                action: "resize",
                tab_id: currentTab,
                cols: active.term.cols,
                rows: active.term.rows
            };
            logDebug("OUT", "json", resizeMsg);
            ws.send(JSON.stringify(resizeMsg));
        }
    }
});

function toggleSidebar() {
    const wsEl = document.getElementById('workspace');
    if (wsEl) {
        wsEl.classList.toggle('sidebar-collapsed');
        // Fit term canvas and send resize command after animation completes
        setTimeout(() => {
            if (terminals[currentTab]) {
                const active = terminals[currentTab];
                if (active.fitAddon) {
                    active.fitAddon.fit();
                }
                if (ws && ws.readyState === WebSocket.OPEN) {
                    const resizeMsg = {
                        type: "control",
                        action: "resize",
                        tab_id: currentTab,
                        cols: active.term.cols,
                        rows: active.term.rows
                    };
                    logDebug("OUT", "json", resizeMsg);
                    ws.send(JSON.stringify(resizeMsg));
                }
            }
        }, 250);
    }
}

function disconnectSession() {
    sessionStorage.setItem('rmte_autoconnect', 'false');
    window.location.reload();
}

function handleChatKey(event) {
    if (event.key === 'Enter') {
        sendChatMessage();
    }
}

function sendChatMessage() {
    const input = document.getElementById('chat-input');
    if (!input) return;
    const text = input.value.trim();
    if (!text) return;
    
    if (ws && ws.readyState === WebSocket.OPEN) {
        const msg = {
            type: "control",
            action: "chat",
            sender: myUsername,
            message: text,
            time: new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
        };
        logDebug("OUT", "json", msg);
        ws.send(JSON.stringify(msg));
        input.value = '';
    }
}

function appendChatMessage(sender, message, timeStr) {
    const msgsDiv = document.getElementById('chat-messages');
    if (msgsDiv) {
        const msgEl = document.createElement('div');
        msgEl.className = 'chat-msg';
        
        const header = document.createElement('div');
        header.className = 'chat-msg-header';
        
        const senderSpan = document.createElement('span');
        senderSpan.className = 'chat-msg-sender';
        senderSpan.innerText = sender;
        if (sender === myUsername) {
            senderSpan.classList.add('self');
        }
        
        const timeSpan = document.createElement('span');
        timeSpan.className = 'chat-msg-time';
        timeSpan.innerText = timeStr || '';
        
        header.appendChild(senderSpan);
        header.appendChild(timeSpan);
        
        const body = document.createElement('div');
        body.className = 'chat-msg-body';
        body.innerText = message;
        
        msgEl.appendChild(header);
        msgEl.appendChild(body);
        msgsDiv.appendChild(msgEl);
        msgsDiv.scrollTop = msgsDiv.scrollHeight;
    }
}

window.addEventListener('DOMContentLoaded', () => {
    const autoconnect = sessionStorage.getItem('rmte_autoconnect');
    if (sessionStorage.getItem('rmte_server')) {
        document.getElementById('server').value = sessionStorage.getItem('rmte_server');
    }
    if (sessionStorage.getItem('rmte_sessionId')) {
        document.getElementById('sessionId').value = sessionStorage.getItem('rmte_sessionId');
    }
    if (sessionStorage.getItem('rmte_password')) {
        document.getElementById('password').value = sessionStorage.getItem('rmte_password');
    }
    if (sessionStorage.getItem('rmte_username')) {
        document.getElementById('username').value = sessionStorage.getItem('rmte_username');
    }
    
    if (autoconnect === 'true') {
        connect();
    }
});
