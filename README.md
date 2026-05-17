# RMTE - Remote Terminal Relay

RMTE is a secure, real-time, multi-user remote terminal sharing system. It is built in Go with a centralized WebSocket relay architecture and secures all terminal traffic with **AES-GCM 256-bit End-to-End Encryption (E2EE)**.

It allows hosts to share terminal sessions, manage multiple terminal tabs concurrently, and collaborate with viewers via a premium Web UI or an interactive TUI-based CLI client.

---

## Key Features

*   **End-to-End Encryption (AES-GCM 256-bit):** Keys and terminal inputs/outputs are encrypted locally before being transmitted. The central relay server only routes encrypted binary frames and never accesses plaintext data or your password.
*   **Multi-Tab Collaboration:** Create, switch, navigate, and close multiple concurrent terminal sessions or tabs on the shared host.
*   **Premium Web UI:** Includes a responsive xterm.js terminal, collaborator presence indicators per tab, glowing collaborator sidebar list, and real-time custom scrollbars.
*   **State Persistence & Auto-Reconnect:** Web client connection details are stored securely in sessionStorage. Reloading or refreshing the page reconnects the session in milliseconds.
*   **TUI CLI Client:** Interactive terminal joining with automated presence synchronization, tab navigation menu, and chat room access.
*   **Real-time Integrated Chat:** Connects Web and CLI clients with a memory-cached chat room showing up to the last 50 messages.

---

## Installation

### From Pre-built Binaries
Download the binary for your OS and CPU architecture from the [Releases](https://github.com/your-username/rmte/releases) page.

### Building from Source
Ensure you have Go installed (v1.18 or higher):
```bash
git clone https://github.com/your-username/rmte.git
cd rmte/rmte
go build -o rmte
```

---

## CLI Reference

### 1. Start the Relay Server
The relay server acts as a central broker forwarding encrypted traffic between the host and viewers, and hosts the web interface.
```bash
rmte serve [options]
```
**Options:**
*   `--port`: The port to listen on (default: `8080`).

---

### 2. Share a Terminal Session (Host)
Run this command on the machine you want to share. This initiates a session and displays a unique **Session ID**.
```bash
rmte share --server=<relay-websocket-url> --pass=<encryption-password>
```
**Options:**
*   `--server`: The relay server WebSocket endpoint (e.g., `ws://localhost:8080/ws`). (Required)
*   `--pass`: The E2EE password. This key is processed locally using SHA-256 and **never** leaves your computer. (Required)

---

### 3. Join a Session via CLI (Viewer)
Run this command on the viewer's machine to connect to an active session interactively.
```bash
rmte join --server=<relay-websocket-url> --id=<session-id> --pass=<encryption-password>
```
**Options:**
*   `--server`: The relay server WebSocket endpoint (e.g., `ws://localhost:8080/ws`). (Required)
*   `--id`: The active Session ID shared by the host. (Required)
*   `--pass`: The decryption password used by the host. (Required)

---

## Quick Start Guide

### Step 1: Run the Server
```bash
rmte serve --port=8080
```

### Step 2: Share your Host Terminal
Run this on the host machine:
```bash
rmte share --server="ws://localhost:8080/ws" --pass="mypassword123"
```
The client fallback on Windows uses pipes, while Linux/macOS uses PTY. 
Upon successful connection, it will output:
```text
Session ID: a1b2c3d4
Share this ID with viewers to join.
```

### Step 3: Connect as a CLI Viewer
On a viewer machine:
```bash
rmte join --server="ws://localhost:8080/ws" --id="a1b2c3d4" --pass="mypassword123"
```
1.  Enter your display name when prompted:
    ```text
    Enter your display name: Alex
    ```
2.  Navigate the interactive Text User Interface (TUI):
    *   `[j] Join Tab` – Join the shared active terminal shell (Press `Ctrl + ]` to exit back to the menu).
    *   `[n] New Tab` – Spawn a new concurrent terminal session/tab on the host.
    *   `[s] Switch Tab` – Switch to another active tab.
    *   `[d] Delete Tab` – Terminate and delete the current tab.
    *   `[c] Chat` – Enter the chat room (Type `/exit` or send an empty message to return to the menu).
    *   `[q] Quit` – Safely disconnect from the session.

### Step 4: Connect as a Web Viewer
Open a web browser and navigate to the relay server:
```text
http://localhost:8080/
```
1.  Enter the Server URL (`ws://localhost:8080/ws`), **Session ID** (`a1b2c3d4`), E2EE **Password** (`mypassword123`), and your display name.
2.  Click **Connect**. The button status will update to `Connecting...` and seamlessly load the terminal layout.
3.  **Features on the Web UI:**
    *   **Tab Presence Subtext:** View active collaborators under each tab title.
    *   **Collaborators Sidebar:** Click the user icon (`👥`) to toggle a sidebar showing a glowing green presence list of active users.
    *   **Premium Custom Scrollbar:** Fully customized styling for scrollable elements that aligns with the premium dark theme.
    *   **Real-time Chat Pane:** Send messages instantly by typing in the chat container and clicking the new send icon `➤` or pressing Enter.
    *   **Page Persistence:** Feel free to refresh the browser page. The state will automatically persist and reconnect in milliseconds.

---

## Security Model

The system ensures strict privacy using standard **AES-256-GCM** encryption:
1.  The password is encoded into a raw 256-bit key using **SHA-256** entirely client-side.
2.  The host encrypts all binary terminal outputs using this key along with a dynamically generated random IV for each frame.
3.  The viewers receive these encrypted byte arrays, read the IV, decrypt the payload locally using the shared key, and feed it into the renderer.
4.  The relay server only acts as a fast WebSocket conduit for the encrypted data, ensuring **true Zero-Knowledge End-to-End Encryption**.
