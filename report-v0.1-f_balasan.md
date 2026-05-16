Analisis yang sangat jeli! Insting arsitektur jaringanmu benar-benar tajam.

Di draf spesifikasi `v0.1-f` ini, ada bagian yang memang **kurang dan kritis (di JSON Kontrol)**, tetapi ada juga bagian yang **sudah benar untuk dikosongkan (di Binary Stream)**.

Mari kita bedah secara prosedural mengapa penempatan `viewer_id` (dan pasangannya, `conn_id`) harus diletakkan dengan tepat agar sistem multi-tab dan multi-koneksi kita tidak berantakan.

---

### 1. Pada JSON Kontrol: Ya, Ini Kelalaian Fatal! (Wajib Ditambahkan)

Di bagian `Control Messages (JSON)`, draf `v0.1-f` benar-benar melupakan identitas pengirim. Jika ini dibiarkan, Relay Server dan Host akan mengalami kebutaan rute (*routing blindness*).

* **Kasus `req_sync` (Sinkronisasi Layar):** Jika kamu membuka dua terminal CLI secara paralel (Terminal A dan Terminal B), lalu Terminal B mengirim `req_sync` untuk Tab 2, Host harus tahu ke mana ia harus mengirimkan data *history* Ring Buffer-nya. Jika tidak ada `target_conn`, Host akan mem-broadcast history tersebut ke *semua* terminal, membuat tampilan Terminal A milikmu tiba-tiba kebanjiran log dari Tab 2.
* **Kasus Fitur Kolaborasi (Presence):** Kita belum memasukkan event `set_focus` dan `presence_update` yang kemarin kita sepakati untuk melihat siapa saja yang sedang aktif di Tab 1 atau Tab 2.

### 2. Pada Terminal Data Stream (Binary Frame): Jangan Dimasukkan! (Sudah Benar)

Untuk paket biner, keputusan Antigravity tidak memasukkan `viewer_id` di dalam struktur *byte* sudah **100% benar**. Mengapa?

* **Identitas Implisit (Connection State):** Sesuai rancangan Map bersarang kita di RAM Server (`Map[session_id] -> Map[viewer_id] -> Map[conn_id]`), Relay Server secara prosedural sudah tahu paket biner tersebut datang dari siapa. Ketika sebuah WebSocket menerima *byte* mentah, memori server langsung mengenali: *"Oh, biner ini masuk dari pipa `conn_id: c-992` milik `viewer_id: v-8a1f`"*.
* **Menghemat Bandwidth:** Jika kita memaksa memasukkan string `v-web-25104aec` ke dalam setiap *Binary Frame*, kita akan menambah *overhead* sia-sia di setiap ketikan tombol (*keystroke*), yang mana merusak tujuan awal kita bermigrasi dari Base64 ke biner murni.

---

### Revisi Spesifikasi Protokol v0.1-f (Tambalan untuk Antigravity)

Agar protokol ini kedap air dan bisa dijadikan acuan tanpa merusak performa biner, berikut adalah bagian spesifikasi yang harus diperbaiki dan dimasukkan ke dalam `MASTER_PROMPT.md` atau `BLUEPRINT.md`:

#### Perbaikan Section 1 (Auth Success)

Server harus memberikan `conn_id` yang unik untuk sesi koneksi fisik tersebut, selain mengonfirmasi `viewer_id`.

```json
{
  "type": "auth_success",
  "viewer_id": "v-web-25104aec",
  "conn_id": "c-992" // Ditambahkan agar klien tahu ID pipa fisiknya
}

```

#### Perbaikan Section 2.C (Request Sync)

Viewer harus menyertakan `conn_id` miliknya agar Host tahu ke mana rute balik data Ring Buffer-nya.

```json
{
  "type": "control",
  "action": "req_sync",
  "tab_id": 0,
  "target_conn": "c-992" // Diisi otomatis oleh klien/server untuk rute balik
}

```

#### Penambahan Section 2.D & 2.E (Presence & Focus yang Terlewat)

**D. Set Focus (Viewer -> Server -> Host)**
Dikirim setiap kali user berpindah tab di Web UI atau CLI TUI.

```json
{
  "type": "control",
  "action": "set_focus",
  "viewer_id": "v-web-25104aec",
  "viewer_name": "Ganteng",
  "tab_id": 1
}

```

**E. Presence Update (Host -> Server -> Broadcast All Viewers)**
Host mem-broadcast status terbaru siapa berada di mana ke seluruh penonton setiap kali ada perubahan fokus atau user baru yang masuk.

```json
{
  "type": "control",
  "action": "presence",
  "tabs": {
    "0": ["CLI-Client"],
    "1": ["Ganteng", "Guest-Web"]
  }
}

```