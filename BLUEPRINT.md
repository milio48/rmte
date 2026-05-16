## Cetak Biru Arsitektur `rmte` - Remote Terminal Relay

### 1. Filosofi & Arsitektur Jaringan Utama

Aplikasi ini beroperasi dengan tiga entitas yang saling terhubung via WebSocket, di mana titik tengahnya (Server) dirancang benar-benar "bodoh" dan tidak bisa mengintip isi lalu lintas.

1. **Host (VPS):** Menjalankan `pty.Start()`, menyimpan *Ring Buffer* di RAM lokal, dan mengenkripsi *stdout* terminal menggunakan algoritma AES-256-GCM sebelum dikirim.
2. **Relay Server:** Bertindak murni sebagai *router*. Menyimpan status sesi (di RAM), memetakan koneksi, dan menjaga koneksi fisik tetap hidup (*Ping/Pong*).
3. **Viewer (CLI & Web):** Mengubah terminal lokal menjadi *Raw Mode* (atau merender Vanilla JS via `xterm.js`), mendekripsi aliran layar dari VPS, dan mengenkripsi input *keystroke* pengguna.

### 2. Antarmuka Command Line (CLI)

Satu *binary* mengeksekusi tiga mode berbeda secara prosedural berdasarkan argumen:

* **Relay Mode:** `./rmte serve --port=8080` (Berjalan di *foreground*, memegang Map sesi di RAM).
* **Host Mode:** `./rmte share --server="ws://localhost:8080/ws" --pass="rahasia123"` (Mengaitkan PTY dan mengunci enkripsi).
* **Viewer Mode:** `./rmte join --server="ws://localhost:8080/ws" --id="fneoql" --pass="rahasia123"` (Menampilkan TUI dan menghubungkan *input* ke PTY target).

### 3. Protokol Multiplexing Data

Pengiriman dipisah menjadi dua jenis *frame* WebSocket untuk optimasi performa tertinggi tanpa *overhead* Base64:

#### A. Kontrol Sesi (Text Frame - JSON)

Plaintext yang dibaca oleh Relay Server untuk mengurus rute dan antarmuka.

* **Auth:** `{"type": "auth", "role": "viewer", "session_id": "fneoql", "viewer_id": "v-8a1f9c2d", "name": "Muflihun"}`
* **Presence (Broadcast ke Viewer):** `{"type": "control", "action": "presence", "tabs": {"1": ["Muflihun"]}}`
* **Resize:** `{"type": "control", "action": "resize", "tab_id": 1, "cols": 120, "rows": 40}`
* **Request Sync (Sinkronisasi Layar):** `{"type": "control", "action": "req_sync", "tab_id": 1, "target_conn": "c-992"}`
* **Request New Tab (Viewer ke Host):** `{"type": "control", "action": "request_new_tab"}`
* **Tab Created (Host ke Viewer):** `{"type": "control", "action": "tab_created", "tab_id": 2}`

#### B. Lalu Lintas Terminal (Binary Frame - Terenkripsi E2EE)

Aliran *raw byte* yang tidak bisa dibaca oleh Relay Server. Total *overhead* mutlak konstan sebesar 28 byte, seberapapun besar log yang dimuntahkan.

* `[Byte 0]`: **Tab ID** (1 byte). Rentang 0-255 untuk mengidentifikasi PTY spesifik di VPS.
* `[Byte 1...12]`: **Nonce / IV** (12 byte). Angka acak anti-replay attack untuk AES-GCM.
* `[Byte 13...N]`: **Ciphertext + Auth Tag**. Teks terminal murni yang telah dienkripsi.

---

### 4. Manajemen State (Relay Server)

Untuk menghindari insiden tarik-tambang koneksi (satu identitas membuka banyak terminal paralel), Relay Server membedakan identitas *user* dan koneksi fisiknya.

* `session_id` (Cakupan Publik): *String random* (misal: `fneoql`) yang di-*generate* server saat Host terhubung.
* `viewer_id` (Cakupan Internal): Identitas per mesin klien.
* `conn_id` (Cakupan Fisik): ID koneksi WebSocket unik yang diterbitkan server (misal: `c-992`).

**Hierarki Memori Server:**
`Map[session_id] -> Map[viewer_id] -> Map[conn_id] = *websocket.Conn`

---

### 5. Struktur Direktori Single-Binary

Direktori disusun rata (*flat*) agar kompilasi murni prosedural dan *embed file* berjalan mulus.

```text
rmte/
├── main.go          # Routing argumen CLI
├── identity.go      # Logika fingerprinting lokal tanpa file konfigurasi
├── crypto.go        # Key derivation & AES-GCM (Golang)
├── host.go          # PTY spawn, Adaptive Ring Buffer
├── server.go        # State router & Ping/Pong heartbeat
├── viewer.go        # TUI menu, Raw mode transition (Golang)
├── ui/              
│   ├── index.html   # HTML UI (Embed)
│   ├── xterm.css    # Style murni (Embed)
│   └── xterm.js     # Web Crypto API & WebSocket handler (Embed)
└── web.go           # http.FS //go:embed ui/*

```

---

### 6. Implementasi Pseudo-Code & Logika Inti

Berikut adalah fungsi-fungsi prosedural kritis yang menyusun sistem ini.

#### A. Identitas Klien Lokal (`identity.go`)

Menciptakan *fingerprint* unik per mesin untuk validasi otomatis yang anti-tabrakan, tanpa menulis data apapun ke OS lokal pengguna.

```go
func generateViewerID() string {
	hostname, _ := os.Hostname()
	
	currentUser, err := user.Current()
	username := "unknown"
	if err == nil { username = currentUser.Username }

	machineID := ""
	switch runtime.GOOS {
	case "linux":
		b, _ := os.ReadFile("/etc/machine-id")
		machineID = strings.TrimSpace(string(b))
	case "windows":
		out, _ := exec.Command("cmd", "/C", `reg query HKEY_LOCAL_MACHINE\SOFTWARE\Microsoft\Cryptography /v MachineGuid`).Output()
		machineID = string(out)
	}

	rawString := fmt.Sprintf("%s|%s|%s|rmte-app", hostname, username, machineID)
	hash := sha256.Sum256([]byte(rawString))
	return fmt.Sprintf("v-%x", hash[:4]) // Output contoh: v-8a1f9c2d
}

```

#### B. Keamanan E2EE & Pengiriman Binary (`crypto.go`)

Membangun kunci simetris murni dari flag `--pass`.

```go
var aesGCM cipher.AEAD

func setupCrypto(password string) {
	key := sha256.Sum256([]byte(password))
	block, _ := aes.NewCipher(key[:])
	aesGCM, _ = cipher.NewGCM(block)
}

func sendEncryptedBinary(conn *websocket.Conn, tabID byte, plaintext []byte) {
	nonce := make([]byte, aesGCM.NonceSize())
	io.ReadFull(rand.Reader, nonce) // 12 Byte acak

	ciphertext := aesGCM.Seal(nil, nonce, plaintext, nil)

	payload := make([]byte, 1+len(nonce)+len(ciphertext))
	payload[0] = tabID
	copy(payload[1:], nonce)
	copy(payload[1+len(nonce):], ciphertext)

	conn.WriteMessage(websocket.BinaryMessage, payload)
}

```

#### C. Web Crypto API & Dekripsi JS (`ui/xterm.js`)

Menggunakan API kriptografi bawaan *browser* modern tanpa modul eksternal.

```javascript
async function deriveKey(password) {
    const enc = new TextEncoder();
    const keyMaterial = await crypto.subtle.importKey(
        "raw", enc.encode(password), {name: "PBKDF2"}, false, ["deriveKey"]
    );
    return await crypto.subtle.deriveKey(
        { name: "PBKDF2", salt: enc.encode("rmte-salt"), iterations: 100000, hash: "SHA-256" },
        keyMaterial, { name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]
    );
}

ws.onmessage = async (event) => {
    if (event.data instanceof ArrayBuffer) {
        const rawBytes = new Uint8Array(event.data);
        const tabId = rawBytes[0];
        const iv = rawBytes.slice(1, 13);
        const ciphertext = rawBytes.slice(13);

        try {
            const decryptedBuffer = await crypto.subtle.decrypt(
                { name: "AES-GCM", iv: iv }, aesKey, ciphertext
            );
            terminals[tabId].write(new Uint8Array(decryptedBuffer));
        } catch (e) {
            console.error("Dekripsi E2EE Gagal");
        }
    }
};

```

#### D. Adaptive Read & Ring Buffer VPS (`host.go`)

Membaca keluaran PTY dengan cerdas: instan saat mengetik, *bulk* (borongan) hingga 4KB saat server memuntahkan log besar, lalu menyimpannya di memori maksimal 100KB per tab.

```go
const maxBufferSize = 100 * 1024

func readPTYAndStream(tabID byte, tab *TabSession, wsConn *websocket.Conn) {
    readBuffer := make([]byte, 4096) // Adaptive 4KB blocker

    for {
        n, err := tab.PTY.Read(readBuffer)
        if err != nil { break }
        
        validData := readBuffer[:n]

        tab.Mutex.Lock()
        tab.Buffer = append(tab.Buffer, validData...)
        if len(tab.Buffer) > maxBufferSize {
            tab.Buffer = tab.Buffer[len(tab.Buffer)-maxBufferSize:]
        }
        tab.Mutex.Unlock()

        sendEncryptedBinary(wsConn, tabID, validData)
    }
}

```

#### E. Transisi TUI ke Raw Terminal (`viewer.go`)

Memungkinkan pengguna masuk untuk berkolaborasi dan keluar kembali ke menu TUI tanpa memutuskan *binary session*.

```go
func enterRawTerminal(wsConn *websocket.Conn, tabID byte) {
	oldState, _ := term.MakeRaw(int(os.Stdin.Fd()))
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	buf := make([]byte, 1)
	for {
		os.Stdin.Read(buf)
		
		// ESCAPE KEY: Tekan Ctrl+] (ASCII 29) untuk keluar dari PTY kembali ke Menu
		if buf[0] == 29 { 
			return 
		}
		sendEncryptedBinary(wsConn, tabID, buf)
	}
}

```

#### F. Ping/Pong Heartbeat Anti-Zombie (`server.go`)

Injeksi *keep-alive* paksa ke tingkat TCP untuk membodohi *firewall* dan *router* yang sering membunuh koneksi diam.

```go
func pumpHeartbeat(conn *websocket.Conn, connID string) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	conn.SetReadDeadline(time.Now().Add(35 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(35 * time.Second))
		return nil
	})

	for {
		<-ticker.C
		err := conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(10*time.Second))
		if err != nil {
			cleanUpConnection(connID)
			break
		}
	}
}

```