# RMTE — Remote Terminal Relay & Cloud IDE (v0.2.2)

> "I love sshx, but my endless curiosity to build it from scratch got the best of me 🥲"

RMTE is a secure, real-time, multi-user remote terminal sharing system and lightweight Cloud IDE. It is built entirely in Go with a centralized WebSocket relay architecture, securing all traffic with **AES-GCM 256-bit End-to-End Encryption (E2EE)**.

It allows hosts to share terminal sessions, navigate directories using a clean absolute-path File Explorer, and edit files in real-time via a multi-tab Web UI or an interactive TUI-based CLI client—all packed into a single binary.

---

## ✨ Key Features

* **Absolute Privacy (AES-GCM 256-bit):** Encryption keys and terminal/file I/O payloads are processed locally. The central relay server acts as a "dumb pipe" that only routes encrypted binary frames. It never sees your plaintext data, your files, or your password.
* **Split-Workspace Cloud IDE:** Toggle the folder icon `📁` in the browser tab bar to open a split-view workspace:
  * **Left Panel**: In-place File Explorer.
  * **Right Panel**: Tabbed text editor supporting file opening, modification warnings, and direct saving (`Ctrl + S`).
  * **Bottom Panel**: Interactive, multi-tab terminal shells.
* **Inline File Operations (Zero Modals):** Browser dialogs (`prompt()`, `confirm()`, `alert()`) are completely replaced with clean, non-blocking inline DOM inputs for creating, renaming, and deleting files or directories.
* **Parent Directory Navigation:** Always-present `..` navigation list item allows traversing up the directory tree across host drives.
* **Dynamic Max Buffer Limits:** Set customizable memory limits via CLI (e.g. `--buffer=5` for 5MB limits) to configure both the terminal ring buffer and the maximum allowed file sizes.
* **Zero-copy Binary Data Channel (Tab ID `255`):** Avoids heavy Base64 parsing overhead. Files are sent as pure, encrypted binary frames over a reserved channel.
* **Integrated Chat Room:** A memory-cached chat bridge connecting Web and CLI clients in real-time, preserving the last 50 messages.
* **State Persistence & Auto-Reconnect:** Connection credentials live safely in `sessionStorage` for immediate recovery upon page refresh.

---

## 🏗️ Architecture & Security Model
```
┌───────────────┐                  ┌──────────────┐                  ┌───────────────┐
│               │  E2EE Control    │              │  E2EE Control    │               │
│               ├─────────────────►│              │◄─────────────────┤               │
│   Host Go     │                  │  Relay Go    │                  │  Viewer JS    │
│  Workspace    │  E2EE Tab 255    │  (WebSockets)│  E2EE Tab 255    │   Browser     │
│               │◄─────────────────┤              ├─────────────────►│               │
└───────────────┘  (Raw Binary)    └──────────────┘  (Raw Binary)    └───────────────┘
```

1. **E2EE Key Derivation**: A 256-bit key is derived locally from the shared password using SHA-256.
2. **AES-GCM Payload Envelope**: Control messages (JSON) and binary streams (terminal I/O & file operations) are encrypted using AES-GCM with a unique 12-byte initialization vector (IV) prepended to the ciphertext.
3. **Zero-Knowledge Relay**: The server only proxies binary envelopes and target routing IDs. It cannot read your commands, terminal outputs, or files.

---

## 📦 Build from Source
Got Go installed (v1.21+)? Let's build the binary:
```bash
git clone https://github.com/your-username/rmte.git
cd rmte/rmte
go build -ldflags "-s -w" -o rmte
```

---

## 🚀 Quick Start Guide

### 1. Spin Up the Relay Server
The relay server acts as the central broker, routing WebSocket connections and serving the embedded Web UI.
```bash
./rmte serve --port=8080
```

### 2. Share Your Workspace (Host)
Run this on the machine you want to expose. It starts shell terminals and exposes directory operations.
```bash
./rmte share --server="ws://localhost:8080/ws" --pass="supersecret123" --buffer=5
```
* `--buffer` (Optional): Maximum buffer size in MB (applies to terminal ring buffer and max file sizes). Default is `1` MB.

The host will output a unique Session ID:
```text
Session ID: a1b2c3d4
Share this ID with viewers to join.
```

### 3. Join via CLI Client (Viewer)
Join from another terminal:
```bash
./rmte join --server="ws://localhost:8080/ws" --id="a1b2c3d4" --pass="supersecret123"
```
You'll enter an interactive TUI menu:
* `[j]` **Join Tab:** Dive into the active terminal shell (Press `Ctrl + ]` to escape).
* `[n]` **New Tab:** Spawn a concurrent shell on the host.
* `[s]` **Switch Tab:** Hop between active terminal tabs.
* `[c]` **Chat:** Enter the real-time chat room.
* `[q]` **Quit:** Disconnect gracefully.

### 4. Join via Web Client (Viewer)
Open your browser and navigate to:
```text
http://localhost:8080/
```
1. Enter the Server URL, E2EE **Password** (`supersecret123`), **Session ID** (`a1b2c3d4`), and your Nickname.
2. Click **Connect**.
3. Toggle the folder icon `📁` in the tab bar to access the workspace editor. Double-click the breadcrumb to input any absolute path directly.
