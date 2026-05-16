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
                // Request active tabs immediately
                const getTabsMsg = { type: "control", action: "get_tabs" };
                logDebug("OUT", "json", getTabsMsg);
                ws.send(JSON.stringify(getTabsMsg));
			} else if (msg.type === 'control' && msg.action === 'tab_created') {
				addTabButton(msg.tab_id);
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
                Object.keys(msg.tabs).forEach(tabId => {
                    const btn = document.getElementById(`tab-btn-${tabId}`);
                    if (btn) {
                        const users = msg.tabs[tabId];
                        if (users && users.length > 0) {
                            btn.innerText = `Tab ${tabId} (${users.join(', ')})`;
                        } else {
                            btn.innerText = `Tab ${tabId}`;
                        }
                    }
                });
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
    if (document.getElementById(`tab-btn-${tabId}`)) return;
    
    const btn = document.createElement('button');
    btn.id = `tab-btn-${tabId}`;
    btn.className = 'tab-btn' + (tabId === currentTab ? ' active' : '');
    btn.innerText = `Tab ${tabId}`;
    btn.onclick = () => switchTab(tabId);
    tabsDiv.appendChild(btn);
}

function switchTab(tabId) {
    currentTab = tabId;
    document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
    document.getElementById(`tab-btn-${tabId}`).classList.add('active');
    
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
