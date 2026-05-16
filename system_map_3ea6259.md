# System Map - RMTE (Remote Terminal Relay)
**Commit:** `3ea6259`  
**Description:** Dokumen peta arsitektur dan fungsionalitas berkas kode RMTE (`rmte/rmte` dan `ui/`).

> [!NOTE]
> Proyek **RMTE** dirancang dengan prinsip enkripsi ujung-ke-ujung (E2EE) menggunakan AES-GCM 256. Relay server murni bertindak sebagai *dumb pipeline* yang menyalurkan data biner terenkripsi tanpa dapat membacanya.

---

## 📁 Direktori: `rmte/` (Core Go Application)

Berkas utama aplikasi terminal multiplexer remote ditulis menggunakan bahasa pemrograman Go.

### 1. 📄 [main.go](file:///d:/fz/project/rmte/rmte/main.go)
*   **Peran:** Entry point utama aplikasi CLI.
*   **Garis Besar Fungsi:**
    *   Menerima dan mem-parsing sub-perintah utama: `serve`, `share`, dan `join`.
    *   Mengekstrak flag parameter pendukung seperti `--port`, `--server`, `--pass`, dan `--id`.
    *   Mengalihkan eksekusi ke modul server, host, atau viewer yang sesuai.

### 2. 📄 [server.go](file:///d:/fz/project/rmte/rmte/server.go)
*   **Peran:** Mesin utama WebSocket Relay Server.
*   **Garis Besar Fungsi:**
    *   Mengelola siklus hidup koneksi WebSocket antara Host dan Viewer.
    *   Menerapkan skema pemetaan berlapis: `sessions[session_id] -> Viewers[viewer_id][conn_id]`.
    *   **JSON Routing:** Melakukan inspeksi terhadap JSON teks kontrol. Jika melampirkan target khusus (`target_conn`), server akan merutekan pesan hanya ke pipa koneksi tersebut.
    *   **Binary Proxying:** Meneruskan frame biner terenkripsi secara efisien dari Host ke Viewer dan sebaliknya tanpa membaca isi payload.

### 3. 📄 [host.go](file:///d:/fz/project/rmte/rmte/host.go)
*   **Peran:** Sisi pengirim/berbagi terminal (Host).
*   **Garis Besar Fungsi:**
    *   Menginisialisasi PTY shell (`bash` atau `cmd.exe`) untuk multi-tab.
    *   **Fallback Windows:** Jika PTY tidak didukung (Windows), inisialisasi menggunakan *anonymous pipes* dengan parameter `/q` (Quiet) untuk mematikan duplikasi *echo* dari shell, serta melakukan penanganan *manual echo* dan emulasi *line buffer* secara presisi.
    *   Merekam riwayat output terminal ke dalam *Ring Buffer* lokal per tab.
    *   **Sync Terarah:** Menangani pesan kontrol `req_sync` dengan mengirimkan data riwayat buffer secara terarah ke Viewer pemohon menggunakan Base64 JSON `sync_data`.
    *   Menangani aksi `set_focus` untuk menyebarkan pembaruan status `presence` kolaboratif.

### 4. 📄 [viewer.go](file:///d:/fz/project/rmte/rmte/viewer.go)
*   **Peran:** Aplikasi CLI pemantau terminal (Viewer).
*   **Garis Besar Fungsi:**
    *   Menampilkan menu TUI interaktif untuk memilih, membuat, dan menggabungkan diri ke tab sesi.
    *   Mengatur mode terminal ke **Raw Mode** agar input keyboard ditangkap mentah-mentah (termasuk kontrol seperti `Ctrl+C`).
    *   Mendekripsi data biner yang diterima, menangani konversi newline (`\r\n`), dan menuliskannya ke Stdout secara rapi.
    *   Menerima pemberitahuan asinkron `tab_created` dan memicu notifikasi instan agar TUI tetap termonitor secara real-time meskipun diblokir input scanner.

### 5. 📄 [crypto.go](file:///d:/fz/project/rmte/rmte/crypto.go)
*   **Peran:** Jantung kriptografi E2EE.
*   **Garis Besar Fungsi:**
    *   `setupCrypto`: Menurunkan kunci enkripsi 32-byte (AES-256) dari password menggunakan SHA-256.
    *   `encryptBinary`: Membungkus data dengan format biner `[1 Byte TabID] + [12 Byte Nonce] + [Ciphertext]`.
    *   `decryptBinary`: Memvalidasi dan mendekripsi frame biner beralamat TabID tertentu.

### 6. 📄 [identity.go](file:///d:/fz/project/rmte/rmte/identity.go)
*   **Peran:** Generator identitas penonton unik.
*   **Garis Besar Fungsi:**
    *   Menghasilkan string `viewer_id` deterministik dengan melakukan hashing terhadap gabungan `hostname | username | MachineGUID`.

### 7. 📄 [web.go](file:///d:/fz/project/rmte/rmte/web.go)
*   **Peran:** Embed asset server untuk Web UI.
*   **Garis Besar Fungsi:**
    *   Menggunakan fitur `//go:embed` untuk mengompilasi seluruh folder `ui/` ke dalam file binary eksekusi `rmte.exe`.
    *   Menyediakan HTTP handler untuk melayani file static di rute root `/`.

---

## 📁 Direktori: `ui/` (Web-based Viewer)

Folder aset antarmuka berbasis web yang memungkinkan viewer memantau melalui peramban (browser).

### 1. 📄 [ui/index.html](file:///d:/fz/project/rmte/rmte/ui/index.html)
*   **Peran:** Halaman tata letak (HTML5 structure).
*   **Garis Besar Fungsi:**
    *   Menyediakan form input autentikasi (`server`, `sessionId`, `password`).
    *   Menyusun struktur bar navigasi tab (`tabs-bar`) dan kontainer utama terminal.
    *   Memuat library eksternal Xterm.js dan berkas logika klien `xterm.js`.

### 2. 📄 [ui/xterm.css](file:///d:/fz/project/rmte/rmte/ui/xterm.css)
*   **Peran:** Lembar gaya desain visual (premium aesthetics).
*   **Garis Besar Fungsi:**
    *   Menerapkan desain bertema gelap (*dark mode*) yang memikat dengan warna solid, transisi halus, dan tata letak fleksibel.
    *   Memberikan visualisasi interaktif pada tombol tab yang aktif dan efek melayang (*hover*).

### 3. 📄 [ui/xterm.js](file:///d:/fz/project/rmte/rmte/ui/xterm.js)
*   **Peran:** Logika sisi klien Web (Web Client Engine).
*   **Garis Besar Fungsi:**
    *   Menyederhanakan SHA-256 key derivation menggunakan Web Crypto API untuk menyamakan kunci AES-GCM dengan backend Go.
    *   Mengelola koneksi websocket, menangani konversi Base64 dari data JSON `"sync_data"`, dan mendekripsinya sebelum menulisnya ke modul instance terminal Xterm.js.
    *   Mengirimkan perintah kontrol interaktif (`resize`, `request_new_tab`, `req_sync`).
    *   Mengirimkan `set_focus` saat pergantian tab, menangani data biner `"presence"`, dan memperbarui teks tombol tab secara dinamis untuk menampilkan daftar pengguna aktif.
