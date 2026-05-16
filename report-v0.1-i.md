# Laporan Pengembangan & Solusi Masalah - RMTE (v0.1-i)

Berikut adalah ringkasan dari semua perbaikan, fitur baru, dan investigasi mendalam terhadap perilaku aplikasi yang telah diselesaikan dan di-commit ke repositori.

---

## 1. Analisis Bug Terminal 4 (Layar Hitam / Tidak Ada Output Terminal)

### Masalah:
Pada **Terminal 4 (PowerShell)**, pengguna menjalankan perintah berikut:
```powershell
./rmte.exe join --server="ws://localhost:8080/ws" --id="60269278" --pass ="rahasia123"
```
Meskipun berhasil login dan membuat tab baru, ketika menjoin `Tab 3`, layar terminal tetap hitam (kosong) dan tidak ada input/output terminal yang mengalir.

### Penyebab (Typo Karakter Spasi pada Parameter):
Perhatikan baik-baik bagian `--pass ="rahasia123"`. Terdapat **spasi** antara `--pass` dan `="rahasia123"`.
*   Dalam Go `flag` parser dan PowerShell shell parsing, spasi ini menyebabkan password yang dibaca oleh program bukan `rahasia123`, melainkan literal string **`="rahasia123"`**!
*   Karena E2EE dienkripsi menggunakan kunci AES-256 yang diturunkan dari SHA-256 password tersebut, Terminal 4 menggunakan kunci enkripsi/dekripsi yang salah.
*   Akibatnya, semua paket biner E2EE (data terminal) yang dikirim oleh Host **gagal didekripsi** oleh Terminal 4 dan diabaikan secara diam-diam. Sedangkan paket kontrol JSON (seperti `request_new_tab` dan `set_focus`) tetap berfungsi karena tidak dienkripsi.

### Solusi & Peningkatan Diagnostik:
1.  **Notifikasi E2EE Gagal Dekripsi:** Kami telah menambahkan notifikasi error langsung ke `stderr` ketika dekripsi data biner atau data sync gagal:
    `[E2EE Error: Decryption failed. Please check if --pass matches the host's password exactly!]`
    Jika pengguna melakukan typo serupa di masa mendatang, mereka akan langsung mengetahui masalahnya dalam sekejap!
2.  **Cara Menjalankan yang Benar:** Hilangkan spasi sebelum tanda sama dengan:
    ```powershell
    ./rmte.exe join --server="ws://localhost:8080/ws" --id="60269278" --pass="rahasia123"
    ```

---

## 2. Fitur Baru & Peningkatan yang Diimplementasikan

### 2.1 Sinkronisasi Pembuatan Tab Real-Time (Tanpa Perlu Enter)
*   **Masalah Sebelumnya:** Ketika pengguna CLI menekan tombol `n` (New Tab), tab berhasil dibuat di background, tetapi TUI langsung memblokir input di menu utama sebelum thread pembaca menerima paket `tab_created` dari server, sehingga daftar tab tidak langsung ter-update.
*   **Perbaikan:** Kami menambahkan sleep non-blocking sebesar `200ms` segera setelah perintah `"request_new_tab"` dikirim ke WebSocket. Ini memberikan waktu yang cukup bagi goroutine background untuk menerima respons dari Relay Server dan memperbarui slice `tabs` sebelum CLI kembali mencetak menu dan memblokir input keyboard.

### 2.2 Kustomisasi Nama Viewer dari CLI & Dukungan Multi-User Presence
*   **Fitur:** Kami menambahkan flag baru `--name` pada subcommand `join` di CLI:
    ```bash
    ./rmte.exe join --server="ws://localhost:8080/ws" --id="[SessionID]" --pass="rahasia123" --name="BudiGanteng"
    ```
*   **Integrasi Kontrol:** Jika `--name` tidak diset, program secara otomatis menggunakan fallback ke hash ringkas `viewer_id`.
*   **Nama Dinamis di Judul Tab (Web & CLI):**
    *   Kami merombak manajemen presence di `host.go`. Host kini secara dinamis melacak pemetaan `viewerID -> viewerName` dan tab yang sedang aktif difokuskan.
    *   Setiap kali ada user (baik dari CLI maupun Web) yang menjoin atau berpindah tab (`set_focus`), host akan menghitung daftar nama pengguna yang sedang aktif di setiap tab dan menyiarkannya ke semua viewer.
    *   Hasilnya, judul tab pada Web UI dan daftar kehadiran akan diperbarui secara real-time untuk menunjukkan siapa saja yang ada di dalam tab tersebut! Contoh: `Tab 0 (webganteng1, BudiGanteng)`.

### 2.3 Fitur Hapus/Delete Tab (Web & CLI)
Kami telah membangun sistem penghapusan tab yang sepenuhnya terintegrasi dan aman dari sisi backend hingga frontend.

1.  **Alur Backend (`host.go` & `server.go`):**
    *   Menambahkan aksi `delete_tab` pada pengendali pesan host.
    *   Ketika tab dihapus, host akan **mematikan proses shell subprocess (`cmd.exe` / `bash`) secara paksa**, menutup semua descriptor file I/O (PTY/Pipes), dan menghapus sesi tab tersebut dari memori.
    *   Host kemudian menyiarkan event kontrol `tab_deleted` ke seluruh viewer.
2.  **Antarmuka Web UI (`index.html`, `xterm.js`, `xterm.css`):**
    *   Menambahkan tombol tutup tab berupa ikon silang kecil (`×`) yang elegan di samping teks tab.
    *   Gaya tab menggunakan glassmorphism premium dengan tata letak flexbox yang rapi.
    *   Ketika tombol `×` diklik, sistem akan mengonfirmasi tindakan dan mengirimkan pesan `delete_tab` ke WebSocket.
    *   Saat menerima event `tab_deleted`, elemen tab akan dihapus dari antarmuka Web UI, terminal xterm terkait dihancurkan secara bersih (`dispose`), dan jika tab tersebut adalah tab aktif, fokus akan dialihkan secara otomatis ke tab lain yang tersisa.
3.  **Antarmuka CLI (`viewer.go`):**
    *   Menambahkan perintah menu baru `[d] Delete Tab` ke dalam daftar perintah utama.
    *   Pengguna dapat mengetik `d`, memasukkan nomor ID tab yang ingin dihapus, dan Relay Server akan meneruskannya ke host.
    *   Background thread akan secara otomatis menghapus tab dari daftar tab lokal dan memperbarui pilihan tab aktif jika tab saat ini terhapus.

---

## 3. Daftar File yang Dimodifikasi

Semua modifikasi telah di-compile dan diuji dengan sukses tanpa kesalahan kompilasi.

1.  **[rmte/host.go](file:///d:/fz/project/rmte/rmte/host.go)**
    *   Menambahkan properti `Cmd *exec.Cmd` pada struktur `TabSession` untuk kontrol pembunuhan proses yang aman.
    *   Mengimplementasikan pencatatan multi-user presence thread-safe menggunakan map `ViewersPresence` global.
    *   Menambahkan handler aksi `delete_tab` untuk mematikan subprocess shell, menutup descriptor I/O, menghapus memori, dan menyiarkan event penutupan tab.
2.  **[rmte/main.go](file:///d:/fz/project/rmte/rmte/main.go)**
    *   Menambahkan flag `--name` pada subcommand `join` dan meneruskannya ke fungsi pelaksana `runViewer`.
3.  **[rmte/viewer.go](file:///d:/fz/project/rmte/rmte/viewer.go)**
    *   Memperbarui fungsi `runViewer` untuk menerima nama khusus dan mengirimkannya dalam paket autentikasi.
    *   Mengimplementasikan visualisasi error dekripsi E2EE pada data biner & sinkronisasi data.
    *   Menambahkan opsi perintah `d` (Delete Tab) ke dalam loop TUI menu utama.
    *   Menambahkan sleep `200ms` pada aksi pembuatan tab baru (`n`) untuk pembaruan daftar tab instan.
4.  **[rmte/ui/xterm.js](file:///d:/fz/project/rmte/rmte/ui/xterm.js)**
    *   Menambahkan dukungan penuh untuk mendengarkan pesan `tab_deleted`, menghapus tombol tab dinamis, membersihkan resource xterm, dan mengalihkan fokus tab secara aman.
    *   Membuat tombol penutupan tab interaktif dengan tombol `×` dinamis.
5.  **[rmte/ui/xterm.css](file:///d:/fz/project/rmte/rmte/ui/xterm.css)**
    *   Mendesain tata letak `.tab-btn-container` yang didasarkan pada flexbox modern, menyelaraskan teks judul tab dan tombol silang penutup dengan indah, serta menambahkan transisi hover warna merah menyala yang sangat premium untuk tombol hapus.
6.  **[rmte/rmte.exe](file:///d:/fz/project/rmte/rmte/rmte.exe)**
    *   Membangun ulang (re-compile) biner executable RMTE untuk menyematkan (embed) semua asset UI statis yang baru saja diperbarui agar siap digunakan secara instan.

---

## 4. Cara Pengujian & Demonstrasi

### Menjalankan Server & Host:
1.  **Jalankan Relay Server:**
    ```bash
    ./rmte.exe serve --port=8080
    ```
2.  **Jalankan Host Sharing:**
    ```bash
    ./rmte.exe share --server="ws://localhost:8080/ws" --pass="rahasia123"
    ```

### Menjalankan Viewer CLI (Dengan Kustomisasi Nama):
```bash
./rmte.exe join --server="ws://localhost:8080/ws" --id="[SessionID]" --pass="rahasia123" --name="BudiGanteng"
```
Di dalam TUI, Anda akan melihat perintah baru:
`Commands: [j] Join Tab, [n] New Tab, [s] Switch Tab, [d] Delete Tab, [q] Quit`
*   Jika Anda menekan `n`, daftar *Available Tabs* akan langsung diperbarui dalam sekejap tanpa harus menekan Enter lagi!
*   Jika Anda menekan `d` dan memasukkan ID tab, tab tersebut akan dihapus secara real-time dari semua client (CLI dan Web).

### Menjalankan Lewat Browser:
Buka `http://localhost:8080/` di browser, masukkan Session ID, Password, dan Nama Anda.
*   Anda akan melihat daftar pengguna aktif di dalam kurung di samping judul tab, misalnya: `Tab 0 (BudiGanteng, webganteng1)`.
*   Arahkan kursor Anda ke tab, lalu klik tombol merah `×` untuk menghapus tab secara bersih dan aman!
