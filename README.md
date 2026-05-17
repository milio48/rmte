# RMTE - Remote Terminal Relay

> "I love sshx, but my endless curiosity to build it from scratch got the best of me 🥲"

RMTE is a secure, real-time, multi-user remote terminal sharing system. It’s built entirely in Go with a centralized WebSocket relay architecture, securing all terminal traffic with **AES-GCM 256-bit End-to-End Encryption (E2EE)**.

Think of it as a lightweight, zero-trust alternative for terminal collaboration. It allows hosts to share terminal sessions, manage multiple tabs concurrently, and collaborate with peers via a slick Web UI or an interactive TUI-based CLI client—all packed into a single binary.

---

## ✨ The Cool Stuff (Key Features)

* **Absolute Privacy (AES-GCM 256-bit):** Keys and terminal I/O are encrypted locally. The central relay server is literally a "dumb pipe" that only routes encrypted binary frames. It never sees your plaintext data or your password.
* **Multi-Tab Collaboration:** Spawn, switch, navigate, and terminate multiple concurrent shell sessions on the host seamlessly.
* **Premium Web UI:** A beautiful, responsive `xterm.js` interface packed with collaborator presence indicators, a glowing user sidebar, and custom dark-mode scrollbars. (No bloated frameworks, just pure Vanilla JS magic).
* **State Persistence & Auto-Reconnect:** Web client credentials live safely in `sessionStorage`. Accidentally refreshed the page? It snaps back and reconnects in milliseconds.
* **TUI CLI Client:** Prefer the terminal? Join via CLI and enjoy interactive tab navigation, presence synchronization, and a built-in chat room.
* **Integrated Chat Room:** A memory-cached chat bridge connecting Web and CLI clients in real-time, preserving the last 50 messages for newcomers.

---

## 📦 Installation

### The Easy Way (Pre-built Binaries)
Grab the ready-to-use binary for your OS and CPU architecture from the [Releases](https://github.com/your-username/rmte/releases) page.

### The Hacker Way (Build from Source)
Got Go installed (v1.21+)? Let's build it:
```bash
git clone [https://github.com/your-username/rmte.git](https://github.com/your-username/rmte.git)
cd rmte/rmte
go build -o rmte

```

---

## 🚀 Quick Start Guide

You only need one binary to rule them all. Here is how to orchestrate a session:

### 1. Spin Up the Relay Server

The relay server acts as the central broker. It handles WebSocket routing and serves the embedded Web UI.

```bash
./rmte serve --port=8080

```

### 2. Share Your Terminal (The Host)

Run this on the machine you want to expose.
*(Note: Uses PTY on Linux/macOS, and a custom Pipes fallback on Windows).*

```bash
./rmte share --server="ws://localhost:8080/ws" --pass="supersecret123"

```

It will spit out a unique Session ID:

```text
Session ID: a1b2c3d4
Share this ID with viewers to join.

```

### 3. Join the Party (CLI Viewer)

On a different machine, jump into the session via your terminal:

```bash
./rmte join --server="ws://localhost:8080/ws" --id="a1b2c3d4" --pass="supersecret123"

```

You'll be greeted by an interactive TUI menu where you can:

* `[j]` **Join Tab:** Dive into the active shell (Press `Ctrl + ]` to escape back to the menu).
* `[n]` **New Tab:** Spawn a fresh concurrent shell on the host.
* `[s]` **Switch Tab:** Hop between active tabs.
* `[c]` **Chat:** Enter the real-time chat room (Type `/exit` to leave).
* `[q]` **Quit:** Disconnect gracefully.

### 4. Join the Party (Web Viewer)

Not a terminal junkie? Open your browser and go to:

```text
http://localhost:8080/

```

1. Enter the Server URL, **Session ID** (`a1b2c3d4`), E2EE **Password** (`supersecret123`), and your Nickname.
2. Hit **Connect**.
3. Enjoy the premium UI: see who's typing on which tab, toggle the collaborator sidebar (`👥`), chat with the team, and collaborate in real-time.

---

## 🔒 Under the Hood: The Security Model

I built this with a "Trust No One" philosophy. Here is how the E2EE pipeline works:

1. Your `--pass` is hashed into a raw 256-bit key using **SHA-256**, strictly on the client side.
2. The Host encrypts all terminal `stdout` bytes using this key, appending a dynamically generated random IV (Nonce) to every single binary frame.
3. The Viewers receive these encrypted bytes, extract the IV, decrypt the payload locally using WebCrypto API (or Go's crypto package), and feed the plaintext into the renderer.
4. **The Relay Server is blind.** It only forwards the encrypted binary chunks. It literally cannot read your commands or output.
