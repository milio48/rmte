let ws;
let aesKey;
let term;
let currentTab = 0;
let terminals = {}; // tabID -> { term, buffer }

async function connect() {
	const server = document.getElementById('server').value;
	const sessionId = document.getElementById('sessionId').value;
	const password = document.getElementById('password').value;

	if (!sessionId || !password) return alert("Session ID and Password required");

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
		ws.send(JSON.stringify({
			type: "auth",
			role: "viewer",
			session_id: sessionId,
			viewer_id: "v-web-" + Math.random().toString(16).slice(2, 10)
		}));
	};

	ws.onmessage = async (event) => {
		if (typeof event.data === 'string') {
			const msg = JSON.parse(event.data);
			if (msg.type === 'auth_success') {
				document.getElementById('setup').style.display = 'none';
				document.getElementById('terminal-container').style.display = 'block';
				initTerminal(0);
			} else if (msg.type === 'control' && msg.action === 'tab_created') {
				addTabButton(msg.tab_id);
			}
		} else {
			// Binary Frame
			const rawBytes = new Uint8Array(event.data);
			const tabId = rawBytes[0];
			const iv = rawBytes.slice(1, 13);
			const ciphertext = rawBytes.slice(13);

			try {
				const decryptedBuffer = await crypto.subtle.decrypt(
					{ name: "AES-GCM", iv: iv },
					aesKey,
					ciphertext
				);
				if (!terminals[tabId]) initTerminal(tabId);
				terminals[tabId].term.write(new Uint8Array(decryptedBuffer));
			} catch (e) {
				console.error("Decryption failed", e);
			}
		}
	};
}

function initTerminal(tabId) {
	if (terminals[tabId]) return;

	const t = new Terminal({
		cursorBlink: true,
		theme: { background: '#000' }
	});
	
	terminals[tabId] = { term: t };
	
	if (tabId === currentTab) {
		t.open(document.getElementById('terminal'));
		t.onData(data => sendInput(tabId, data));
		// Request sync
		ws.send(JSON.stringify({
			type: "control",
			action: "req_sync",
			tab_id: tabId
		}));
	}
	
	addTabButton(tabId);
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
    
    const termDiv = document.getElementById('terminal');
    termDiv.innerHTML = '';
    terminals[tabId].term.open(termDiv);
    
    // Request sync
    ws.send(JSON.stringify({
        type: "control",
        action: "req_sync",
        tab_id: tabId
    }));
}

async function sendInput(tabId, data) {
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
