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

### 1. Data Plane vs. Control Plane
- **Data Plane (E2EE)**: Terminal PTY streams (keystrokes, terminal output) and Web Preview reverse-proxy streams are encrypted end-to-end between the host and viewers using **AES-256-GCM** (via native Go crypto on the CLI and WebCrypto API in the browser). The relay server acts strictly as a dumb blind pipe and cannot decrypt session terminal traffic.
- **Control Plane**: Session orchestration messages (connection handshakes, tab presence, room ID, viewer list) traverse the relay unencrypted for routing purposes. Sensitive authentication tokens are exchanged over TLS (WSS/HTTPS) to the relay.

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

### 5. Web Client Hardening & Isolation (v0.7.15+)
- **Developer-Centric Web Preview Sandbox**: The Web Preview iframe runs with `sandbox="allow-scripts allow-forms allow-modals allow-downloads allow-same-origin"`. This retains full, unrestricted compatibility with modern web development frameworks (allowing `localStorage`, `sessionStorage`, client-side cookies, Service Workers/PWAs, and full SPA client routing). Because the host running the previewed code is already a trusted peer holding terminal execution privileges, restricting origin capabilities is not an effective security boundary and would break real developer workflows. For previewing completely untrusted external web pages, users should use the "Open in New Tab" feature or run RMTE inside container sandboxes.
- **Subresource Integrity (SRI)**: All external CDN stylesheets and JavaScript dependencies (xterm.js, CodeMirror, asmCrypto) are pinned with SHA-384 SRI hashes and loaded with `crossorigin="anonymous"`.
- **HTTP Security Headers**: The built-in web server automatically enforces `X-Content-Type-Options: nosniff`, `X-Frame-Options: SAMEORIGIN` (IDE UI) / `DENY` (Admin Dashboard), `Referrer-Policy: strict-origin-when-cross-origin`, and a hardened `Content-Security-Policy` (`object-src 'none'; base-uri 'self'; form-action 'self'`).
- **Admin CSRF Protection & Reverse Proxy Deployment**: State-changing POST endpoints on the relay admin dashboard (`/login`, `/logout`, `/terminate`, `/ban`, `/unban`) enforce fail-closed CSRF validation requiring `Origin` or `Referer` to match `r.Host`. When running the relay behind a reverse proxy (e.g., Nginx, Caddy, Traefik), the proxy **must** pass through the original Host header (e.g., `proxy_set_header Host $host;`).
- **Flexible Frame Navigation**: The CSP allows `frame-src 'self' about: http: https: blob: data:` so previewed applications can navigate to external OAuth providers, test APIs, and embed dev tools without parent policy interference.

---

## Security Roadmap (v0.8.0)

The following architectural enhancements are scheduled for the next major milestone:
1. **Session Password Key Derivation (PBKDF2-HMAC-SHA256)**: Transitioning from raw SHA-256 key hashing to standard library `crypto/pbkdf2` (available in Go 1.25 stdlib) and native browser WebCrypto PBKDF2 with public session salt, resisting offline precomputation attacks while maintaining zero external WASM/library bloat.
2. **Control Channel Encryption**: Encrypting metadata control channel messages end-to-end to eliminate session intelligence exposure on unverified relay nodes.
3. **Cryptographic Release Signing**: Adding Minisign / Cosign digital signatures alongside SHA-256 checksums for verified binary distribution.
4. **Relay Rate Limiting**: Implementing IP-level and session-level throttling on public relay endpoints to mitigate resource abuse and denial of service.
