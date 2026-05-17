# RMTE - Remote Terminal Relay (v0.1)

RMTE adalah sistem remote terminal sharing multi-user yang tangguh, aman, dan real-time. Didesain dengan arsitektur relay terpusat berbasis WebSocket dan sistem enkripsi ujung-ke-ujung (E2EE) berbasis AES-GCM 256-bit untuk menjamin kerahasiaan sesi terminal Anda.

Sistem ini mendukung kolaborasi multi-user secara simultan, lengkap dengan kesadaran kehadiran tab (Tab Presence Awareness), daftar kolaborator, dan ruang obrolan (Chat Room) terintegrasi pada antarmuka Web UI.

---

## 🛠️ Persyaratan Sistem

*   **Sistem Operasi:** Windows, Linux, atau macOS (Khusus Windows menggunakan Pipes fallback).
*   **Jaringan:** Akses ke server relay RMTE (lokal maupun publik).
*   **Web Browser:** Chrome, Edge, Firefox, Safari (versi terbaru) untuk akses Web UI.

---

## 🚀 Panduan Cepat Cara Penggunaan & Pengujian

Berikut adalah skenario pengujian lengkap untuk mensimulasikan sesi kolaborasi dengan 1 Relay Server, 1 Host Sharing, dan beberapa Viewer (CLI & Web UI).

### Langkah 1: Jalankan Relay Server
Server Relay berfungsi sebagai perantara lalu lintas data antara Host dan Viewer.
```bash
./rmte/rmte.exe serve --port=8080
```
> [!NOTE]
> Server akan mulai mendengarkan koneksi masuk pada port `8080`.

---

### Langkah 2: Jalankan Host Sharing
Host Sharing adalah mesin yang terminalnya akan dibagikan ke Viewer. Jalankan perintah ini dari terminal Host:
```bash
./rmte/rmte.exe share --server="ws://localhost:8080/ws" --pass="rahasia123"
```
> [!IMPORTANT]
> Opsi `--pass` wajib diisi dan digunakan sebagai kunci enkripsi AES-GCM 256-bit. Kunci ini **tidak pernah dikirim ke server relay** (E2EE sejati).
>
> Setelah tersambung, Host akan menampilkan **Session ID** unik (misalnya: `75537488`). Bagikan ID ini kepada kolaborator Anda.

---

### Langkah 3: Menghubungkan Viewer via CLI (Interaktif)
 Viewer CLI dapat bergabung ke sesi aktif dan berinteraksi langsung melalui terminal. Jalankan perintah ini di mesin Viewer:
```bash
./rmte/rmte.exe join --server="ws://localhost:8080/ws" --id="[SessionID]" --pass="rahasia123"
```

#### Fitur & Verifikasi Uji CLI:
1.  **Prompt Nama Interaktif:** Tepat sebelum terhubung, CLI akan menanyakan nama Anda secara interaktif:
    ```bash
    Enter your display name: BudiGanteng
    ```
    *(Ketik nama Anda lalu tekan Enter untuk bergabung dengan identitas Anda sendiri).*
2.  **Sinkronisasi Tab Awal yang Akurat:** TUI menu utama RMTE akan memblokir dan memastikan seluruh daftar tab aktif dari server relay telah tersinkronisasi sebelum mencetak menu pertama kali (mencegah *race condition*).
3.  **Navigasi TUI Menu:**
    *   `[j] Join Tab` — Masuk ke sesi terminal interaktif pada tab saat ini (Keluar kembali ke menu dengan menekan `Ctrl + ]`).
    *   `[n] New Tab` — Membuat tab terminal baru di sisi Host.
    *   `[s] Switch Tab` — Berpindah ke tab terminal lain.
    *   `[d] Delete Tab` — Menghapus tab terminal aktif.
    *   `[c] Chat` — Masuk ke ruang obrolan real-time (Chat Room) untuk mengobrol dengan kolaborator di Web UI maupun CLI lain. (Ketik `/exit` atau kosongkan pesan untuk kembali ke menu utama).
    *   `[q] Quit` — Keluar dan memutuskan koneksi.

> [!TIP]
> **Riwayat Percakapan (Chat History):** Seluruh pesan chat (maksimal 50 pesan terakhir) disimpan dengan aman di memori sesi Server Relay. Siapa saja yang baru bergabung (baik via browser web baru maupun CLI baru) akan otomatis menerima riwayat pesan lengkap secara instan!

---

### Langkah 4: Menghubungkan Viewer via Web UI (Premium Browser Experience)
Buka browser Anda dan navigasikan ke alamat server relay:
```
http://localhost:8080/
```

#### Fitur & Verifikasi Uji Web UI:
1.  **Form Login & Auto-Reconnect Pintar:**
    *   Masukkan isian Server URL, Session ID, Password (E2EE), dan Nama Anda pada halaman login, lalu klik **Connect**.
    *   **Uji Persistensi:** Lakukan reload halaman (`F5` atau `Ctrl + R`). Sistem akan secara otomatis mengisi seluruh isian dan menyambungkan kembali koneksi Anda dalam hitungan milidetik secara instan!
    *   **Disconnect:** Klik tombol keluar (`🚪`) di bar tab sebelah kanan untuk keluar secara bersih dan menghapus riwayat auto-connect.
2.  **Kesadaran Presensi Tab (Tab Presence):**
    *   Di tombol bar tab, Anda akan melihat subtext kecil di bawah judul tab utama (misalnya di bawah `Tab 0` tertulis: `BudiGanteng, webganteng1`).
    *   Subtext ini menunjukkan kolaborator mana saja yang sedang aktif melihat tab tersebut secara real-time.
3.  **Show/Hide Sidebar Kolaborator:**
    *   Klik tombol ikon pengguna (`👥`) di sebelah kanan bar tab untuk menyembunyikan atau memunculkan panel daftar kolaborator sebelah kanan.
4.  **Daftar Kolaborator Aktif (Collaborators Sidebar):**
    *   Menampilkan seluruh daftar kolaborator dengan dot hijau menyala dinamis yang berdenyut (glowing pulse).
    *   Menampilkan badge penanda tab aktif masing-masing kolaborator (misal: `[Tab 0]`).
5.  **Fitur Chat Room Terintegrasi:**
    *   Di bagian bawah sidebar, ketik pesan Anda pada kolom chat input dan tekan `Enter`.
    *   Seluruh kolaborator (baik di Web browser lain maupun CLI yang tersambung) akan menerima pesan obrolan secara real-time.
    *   Pesan Anda sendiri akan diwarnai dengan ungu premium (`#a78bfa`) dan pesan kolaborator lain berwarna biru cerah (`#38bdf8`).

---

## 🔒 Keamanan & Enkripsi (AES-GCM E2EE)

Seluruh input keyboard dan output terminal dienkripsi menggunakan algoritma **AES-256-GCM** di sisi klien sebelum dikirimkan melalui server relay.
*   **Host** mengenkripsi output terminal sebelum dikirim ke server relay.
*   **Viewer** menerima data terenkripsi, mendekripsinya secara lokal menggunakan kata sandi sesi, lalu merendernya di layar.
*   **Server Relay** hanya bertindak sebagai broker paket biner terenkripsi dan **tidak pernah memiliki akses ke plaintext data terminal maupun kata sandi Anda**.
