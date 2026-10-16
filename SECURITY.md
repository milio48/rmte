# Security Policy — RMTE

## Supported Versions

Only the latest minor release receives security updates. We strongly encourage all users to stay on the most recent release.

| Version | Supported          |
| ------- | ------------------ |
| 0.7.x   | :white_check_mark: |
| < 0.7.0 | :x:                |

---

## Reporting a Vulnerability

If you discover a security vulnerability within RMTE, please report it privately. **Do NOT report vulnerabilities through public GitHub issues.**

- **Email**: `security@rmte.biz.id` (or contact maintainers privately via GitHub Security Advisories)
- **Response Commitment**: We acknowledge receipt of reports within **48 hours**, provide an initial assessment within **5 business days**, and coordinate a fix and release before public disclosure.
- **Responsible Disclosure**: We kindly ask that you keep the vulnerability confidential until a patch is released.

---

## Threat Model & Security Posture

RMTE provides remote terminal and workspace sharing over zero-inbound WebSocket relay connections. Understanding the boundaries of the security model ensures safe deployment:

### 1. What Is and Is Not End-to-End Encrypted

> [!WARNING]
> **The relay is still partly trusted.** Terminal streams, file contents and all viewer↔host control messages are end-to-end encrypted and authenticated (protocol 0.7+). However, the relay serves the browser UI and sees Web Preview traffic. A malicious relay operator (including the operator of the shared public relay `my.rmte.biz.id`) can capture the password of anyone who opens the web UI from that relay, and with it act as a viewer. On a host started with `--allow-exec`, that is equivalent to shell access. Run your own relay for anything sensitive, or use CLI/MCP clients.

| Channel | E2EE? | What the relay can do |
| --- | --- | --- |
| Terminal I/O (binary tab frames) | ✅ AES-256-GCM | Cannot read or modify. The frame header (direction, tab id) is authenticated (protocol 0.6+), so frames cannot be redirected to another tab or reflected back. **Can replay** a captured frame on the same tab. |
| File contents (Tab 255) | ✅ AES-256-GCM | Cannot read or modify. Transfer id and direction are authenticated, so a download cannot be re-uploaded or re-wrapped. |
| Control messages (file ops, `ai_exec`, tabs, presence) | ✅ AES-256-GCM envelope (protocol 0.7+) | Sees only the routing id (`target_conn`), size and timing. Cannot read, forge, reorder or replay: each sender numbers its messages, and viewer→host messages are bound to the host's per-process epoch, so commands captured earlier (even from a previous host process with the same password) are rejected. The host acts on no plaintext viewer message. |
| Chat | ✅ (inside the control envelope) | Cannot read or forge. The host, not the relay, fans chat out to viewers. |
| Relay status notices (`host_status`, `session_closed`, `viewer_disconnected`, `pong`) | ❌ (relay-originated) | Can spoof them. They only affect status display and presence, never host actions. |
| Web Preview (`/p/...`) | ❌ | Browser↔relay is plain HTTP(S) and relay↔host is an unencrypted smux stream. The relay sees preview requests, responses and the preview cookie. The cookie is MAC'd by the host with its own subkey and expires after 30 minutes (renewed while in use), and the host only forwards ports opened explicitly (see below). |
| Web UI (`app.js`) | n/a | Served by the relay. A malicious relay, or a network attacker when using plain HTTP, can serve modified JavaScript that captures the password. CLI clients are not affected. |

Other notes:
- **No relay storage.** The relay stores no terminal buffers, files or chat history. Chat history is kept in memory by the host process and served to viewers on request (`get_chat_history`).
- **Key derivation (protocol 0.8+).** `master = PBKDF2-HMAC-SHA256(password, "rmte-kdf-v1:" + session id, 600000)` (session id lower-cased, as the relay does). HKDF-SHA256 then derives independent subkeys: `enc` (AES-256-GCM), `auth` (the token the relay checks) and `preview` (Web Preview ticket/cookie MAC). The salt is not secret; it makes each session's derivation different. The iteration count is fixed in the code, so a relay cannot lower it. A relay that learns the auth token still has to run PBKDF2 per password guess: the random default password (12 characters) is out of reach, short user-chosen `--pass` values may not be. Nothing is written to disk or kept on the relay for this, so a host restarted with the same `--id` and password derives the same keys and resumes its session.
- **Every password holder is fully trusted (by design).** RMTE has no viewer-only role: whoever has the session password is part of the host's team and can run commands, transfer files and also register as the host for that session (this is how a restarted host resumes). Share the password only with people you would give a shell to.
- **Envelope replay state** is kept in memory per host process for up to 4096 viewer streams (one per viewer connection); the least recently used stream is forgotten first. A relay could replay messages from a forgotten stream only if more than 4096 newer viewer connections were made in the same host process.
- **`--sandbox` is off by default.** Without it, forged file operations can reach any path the host user can access.

### 2. Credential Security & Ingestion Methods
RMTE supports multiple methods for supplying session passwords and relay secrets, with differing trade-offs:

1. **`--pass-file <path>` (Most Secure)**:
   - Reads the password from a local file.
   - On Unix systems, RMTE verifies file permissions and warns if permissions are looser than `0600` (readable by other users).
   - Recommended for automated services, systemd units, and production deployments.
2. **Environment Variables (`RMTE_PASS`, `RMTE_ADMIN_PASS`, etc.)**:
   - Avoids leaking credentials into system command lines (`ps aux`).
   - *Considerations*: On Linux systems, environment variables can still be inspected via `/proc/<pid>/environ` by processes running with the same UID, and they are inherited by child processes spawned in the session.
3. **CLI Arguments (`--pass="secret"`)**:
   - Convenient for quick interactive use, but exposed in process process table listings (`ps aux`, `tasklist`) to other users on multi-user systems.
4. **Host Anti-Footgun Banner**:
   - When a host session starts with `RMTE_PASS` set in the environment, RMTE prints a prominent terminal banner: `⚠️ [Auth] Using pre-set password from RMTE_PASS environment variable (NOT random).` This prevents unintentional long-term re-use of predictable credentials.

### 3. Filesystem Sandbox (`--sandbox` / `--dir-only`)
When `--sandbox` is enabled on the host:
- File browsing, read/write tools, and upload/download operations (via Web File Explorer and MCP tools) are strictly confined to the host workspace root directory (`hostWorkDir`).
- Path evaluation uses canonicalized paths (`filepath.EvalSymlinks`), strict traversal checks (`isSubPath`), Windows case-folding normalization, and ancestor resolution for newly created files to defeat symlink escapes and directory traversal.
- Cross-boundary renames (moving files into or out of the sandbox) are denied.

> [!WARNING]
> **Filesystem Confinement $\neq$ Shell Container**  
> The `--sandbox` flag confines **file system operations only** (Web Explorer, file transfer, and MCP file tools). It does **NOT** jail, chroot, or restrict commands executed in the shell (`rmte exec` or interactive terminal tabs). Shell commands run with the full OS privileges of the user running RMTE.  
> If you need complete shell isolation for untrusted viewers or AI agents, execute RMTE inside an isolated **Docker container** or dedicated virtual machine.

### 4. Release Integrity & Self-Update (`rmte update`)
- **Fail-Closed Policy**: Running `rmte update` downloads official release metadata and requires a matching `checksums.txt` file from GitHub Releases. If `checksums.txt` is missing or the target binary checksum is absent, the update is **aborted immediately**.
- **Constant-Time Verification**: Calculated SHA-256 hashes are verified against expected release hashes using `subtle.ConstantTimeCompare` to avoid timing side-channel leaks.
- *Bypass*: Verification can only be bypassed if explicitly requested via the `--skip-checksum-verify` flag.
- **Build Provenance**: Releases are built only after formatting, vet and tests pass on Linux and Windows, with every GitHub Action pinned to a commit SHA and least-privilege workflow permissions. Each binary and `checksums.txt` carries a signed SLSA build-provenance attestation (Sigstore), verifiable independently of the release page:
  `gh attestation verify rmte-linux-amd64 --repo milio48/rmte`

### 5. Web Client Hardening & Isolation (v0.7.15+)
- **Developer-Centric Web Preview Sandbox & Awareness**: The Web Preview iframe runs with `sandbox="allow-scripts allow-forms allow-modals allow-downloads allow-same-origin"`. This retains full, unrestricted compatibility with modern web development frameworks (allowing `localStorage`, `sessionStorage`, client-side cookies, Service Workers/PWAs, and full SPA client routing). Because the host running the previewed code is already a trusted peer holding terminal execution privileges, restricting origin capabilities is not an effective security boundary and would break real developer workflows. To ensure clear security awareness, the Web Preview panel includes an explicit, dismissible notice: *Shares session origin for framework storage. Untrusted scripts running in preview can read active session credentials.* For untrusted code, run the RMTE session inside an isolated container or virtual machine.
- **Subresource Integrity (SRI)**: All external CDN stylesheets and JavaScript dependencies (xterm.js, CodeMirror, asmCrypto) are pinned with SHA-384 SRI hashes and loaded with `crossorigin="anonymous"`.
- **HTTP Security Headers**: The built-in web server automatically enforces `X-Content-Type-Options: nosniff`, `X-Frame-Options: SAMEORIGIN` (IDE UI) / `DENY` (Admin Dashboard), `Referrer-Policy: strict-origin-when-cross-origin`, and a hardened `Content-Security-Policy` (`object-src 'none'; base-uri 'self'; form-action 'self'`).
- **Admin CSRF Protection & Reverse Proxy Deployment**: State-changing POST endpoints on the relay admin dashboard (`/login`, `/logout`, `/terminate`, `/ban`, `/unban`) enforce fail-closed CSRF validation requiring `Origin` or `Referer` to match `r.Host`. When running the relay behind a reverse proxy (e.g., Nginx, Caddy, Traefik), the proxy **must** pass through the original Host header (e.g., `proxy_set_header Host $host;`) and should set `X-Forwarded-Proto`.
- **Admin Login Throttling**: After 5 failed logins from one client IP, further attempts are refused with `429` and an exponential backoff (up to 15 minutes); a successful login clears it. A global cap of 30 failed logins per minute applies across all IPs, so guessing stays slow even if client IPs are forged. The admin cookie is `HttpOnly`, `SameSite=Lax` and `Secure` when the dashboard is reached over HTTPS (directly or via `X-Forwarded-Proto: https`).
- **Trusted Proxies for Client IPs**: Client IPs (used for bans, login throttling and logs) come from `CF-Connecting-IP`, `X-Forwarded-For` or `X-Real-IP` **only when the TCP peer is a trusted proxy** (`--trusted-proxies`, default `loopback`: nginx, Caddy or `cloudflared` on the same machine). Clients connecting directly are identified by their socket address, so they cannot forge another IP to escape a ban. `X-Forwarded-For` is read right to left past trusted hops, so entries a client prepends are ignored. If the relay sits behind a proxy on another machine, list it (`--trusted-proxies=10.0.0.5,loopback` or `private`); if Cloudflare connects to the relay directly, list Cloudflare's published IP ranges. `--trusted-proxies=none` ignores forwarding headers entirely.
- **WebSocket Origin Check**: Browsers may open relay WebSockets only from the relay's own origin (loopback aliases such as `localhost` / `127.0.0.1` on the same port count as one) or from origins listed in `--ws-origins` (e.g. a separately hosted UI). Non-browser clients send no `Origin` and authenticate with tokens.
- **Flexible Frame Navigation**: The CSP allows `frame-src 'self' about: http: https: blob: data:` so previewed applications can navigate to external OAuth providers, test APIs, and embed dev tools without parent policy interference.

### 6. Relay Resource Protection & DoS Mitigations (v0.7.15+)
- **Transport WebSocket Read Limits (`SetReadLimit`)**: Before authentication every connection is limited to 64 KB frames. After authentication the limit is `2 × max-buffer + 1` MB (a full buffer can be base64-encoded twice inside a sealed envelope). `--max-buffer=0` now means the default of **256 MB** instead of unlimited. Oversized frames are dropped before heap exhaustion, triggering a WebSocket `CloseMessageTooBig` (1009) closure.
- **Adaptive Buffer Clamping**: Cooperative `rmte` hosts negotiate their requested buffer during handshake. If a host requests more memory than `--max-buffer` (or 256 MB by default), the relay instructs the host to clamp its local allocation (`clamped_buffer`), preserving host and relay memory without rejecting the session.
- **Non-Blocking Relay Writes**: Every relay connection has its own bounded write queue (256 frames) drained by a dedicated goroutine with a 15-second write deadline. Broadcasts and routing only enqueue, so a viewer or host that stops reading can no longer stall the relay while it holds session locks; a peer whose queue overflows or whose write times out is disconnected.
- **Web Preview Port Allowlist**: The host forwards preview traffic only to ports opened explicitly. By default a viewer opens a port through a sealed `preview_open` control message, which the host logs (`PREVIEW_OPEN`); other loopback services (databases, admin panels) stay unreachable through the preview proxy. With `--preview-ports=3000,5173,8000-8010` the host enforces a fixed allowlist that viewers cannot extend.
- **Session Capacity Guard (`--max-sessions`)**: Restricts the maximum number of concurrent active sessions (default: `100`, `0` = unlimited). New session requests exceeding capacity are rejected fail-closed, while existing host reconnects during the 120-second grace period are always honored.
- **Viewer Concurrency Ceiling (`--max-viewers`)**: Tracks total active WebSocket connections per session across all viewer IDs and tabs (default: `50`, `0` = unlimited), preventing connection flooding by single or distributed viewer IDs.
- **Lean Smux Multiplexing**: Smux v2 multiplexing buffers for Web Preview proxying are tuned to 512 KB per stream and 4 MB aggregate per session, eliminating unbounded buffer growth and saving ~87.5% memory per session.

---

## Security Roadmap (v0.8.0)

The following architectural enhancements are scheduled for the next major milestone:
1. **Memory-Hard Key Derivation**: Evaluating Argon2id (needs a WASM build in the browser) on top of the PBKDF2 + HKDF scheme shipped in protocol 0.8.
2. **Binary Frame Sequence Numbers**: Extending the replay protection of the control envelope (done in protocol 0.7) to terminal and file-channel binary frames.
3. **Self-Update Provenance Check**: Signed build provenance is published with every release; `rmte update` still verifies only `checksums.txt` and should also verify the attestation (or a Minisign signature) before replacing the binary.
4. **Relay Rate Limiting**: Implementing IP-level and session-level throttling on public relay endpoints to mitigate resource abuse and denial of service.
