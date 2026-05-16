# RMTE Protocol & API Specification (v0.1.x)

Dokumen ini menjelaskan spesifikasi protokol komunikasi antara Viewer (Web/CLI), Server (Relay), dan Host dalam ekosistem RMTE. Seluruh komunikasi dilakukan melalui protokol **WebSocket** menggunakan dua format *frame*: **JSON** untuk *control/signaling* dan **Binary** untuk *stream* data terminal.

## 1. Handshake & Authentication (JSON)

Semua *client* (baik Host maupun Viewer) harus melakukan autentikasi sebagai pesan pertama setelah koneksi WebSocket terbuka.

### A. Host / Viewer Connect
**Direction:** Client -> Server
```json
{
  "type": "auth",
  "role": "host", // atau "viewer"
  "session_id": "4f3d986c",
  "viewer_id": "v-web-25104aec" // Opsional, hanya dikirim oleh viewer
}
```

### B. Auth Success
**Direction:** Server -> Client
```json
{
  "type": "auth_success",
  "viewer_id": "v-web-25104aec",
  "conn_id": "c-992" // Ditambahkan agar klien tahu ID pipa fisiknya
}
```

---

## 2. Control Messages (JSON)

Pesan kontrol digunakan untuk manajemen *state* seperti pembuatan, sinkronisasi, dan pengambilan daftar Tab. Seluruh pesan kontrol ini dibungkus dengan `"type": "control"`.

### A. Get Tabs List
Mengambil daftar ID tab yang saat ini aktif dari Host. Biasanya dipanggil langsung oleh Viewer setelah menerima `auth_success`.
**Direction:** Viewer -> Host
```json
{
  "type": "control",
  "action": "get_tabs"
}
```
**Response:** Host -> Viewer (Broadcast)
```json
{
  "type": "control",
  "action": "tabs_list",
  "tabs": [0, 1, 2, 3, 4]
}
```

### B. Request New Tab
Meminta Host untuk membuat sesi terminal/tab baru.
**Direction:** Viewer -> Host
```json
{
  "type": "control",
  "action": "request_new_tab"
}
```
**Response:** Host -> Viewer (Broadcast)
```json
{
  "type": "control",
  "action": "tab_created",
  "tab_id": 1
}
```

### C. Request Sync (Force Output)
Digunakan saat Viewer berpindah ke suatu tab dan ingin Host memaksa Terminal untuk mencetak ulang tampilan (atau dalam kasus PTY, meminta refresh/repaint).
**Direction:** Viewer -> Host
```json
{
  "type": "control",
  "action": "req_sync",
  "tab_id": 0,
  "target_conn": "c-992" // Diisi otomatis oleh klien/server untuk rute balik
}
```
*(Tidak ada balasan JSON khusus, Host akan langsung mem-broadcast state/output terbaru dari tab tersebut melalui Binary Frame khusus ke `target_conn` jika ada)*

### D. Set Focus
Mengirim informasi tab mana yang sedang dilihat oleh Viewer.
**Direction:** Viewer -> Host
```json
{
  "type": "control",
  "action": "set_focus",
  "viewer_id": "v-web-25104aec",
  "viewer_name": "Muflihun",
  "tab_id": 1
}
```

### E. Presence Update
Host mem-broadcast status terbaru siapa berada di mana ke seluruh penonton setiap kali ada perubahan fokus atau user baru yang masuk.
**Direction:** Host -> Viewer (Broadcast)
```json
{
  "type": "control",
  "action": "presence",
  "tabs": {
    "0": ["CLI-Client"],
    "1": ["Muflihun", "Guest-Web"]
  }
}
```

---

## 3. Terminal Data Stream (Binary Frame)

Untuk alasan keamanan dan performa, aliran data ketikan (Input) dan layar terminal (Output) tidak dikirim menggunakan JSON, melainkan **Binary Frame** yang dienkripsi secara *End-to-End* menggunakan algoritma **AES-GCM 256-bit**. Server Relay tidak dapat membaca isi pesan ini.

### Struktur Paket Biner
Setiap pesan biner yang dikirim/diterima memiliki struktur *byte* sebagai berikut:

| Byte Index | Length (Bytes) | Deskripsi |
| :--- | :--- | :--- |
| `0` | 1 | **Tab ID** (uint8), mengidentifikasi tab mana stream ini berasal/dituju. |
| `1 - 12` | 12 | **IV (Initialization Vector)**, angka acak unik untuk dekripsi AES-GCM paket ini. |
| `13+` | Variable | **Ciphertext**, data UTF-8 string terminal yang telah dienkripsi beserta *Authentication Tag* (16 bytes) di ujungnya. |

### Alur Enkripsi / Dekripsi
1. **Input (Viewer -> Host):** Viewer mengetik `ls`. Viewer men-enkripsi string `ls` menggunakan *Shared Secret/Password*. Paket biner dibuat dan dikirim ke Server.
2. **Relay (Server):** Server membaca `byte[0]` untuk mengetahui *Tab ID*, lalu mem-forward paket biner ini ke Host (dan melakukan *broadcast* ke Viewer lain jika diperlukan).
3. **Output (Host -> Viewer):** Host menerima paket, mendekripsi *Ciphertext* menggunakan IV yang menempel, lalu memasukkan string `ls` ke dalam proses `cmd.exe` / `bash`. Output dari shell kemudian dienkripsi ulang dengan IV baru dan di-broadcast kembali menggunakan struktur biner yang sama.
