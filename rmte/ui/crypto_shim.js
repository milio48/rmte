// RMTE Crypto Abstraction — native Web Crypto API with asmcrypto.js fallback
// Provides AES-GCM 256 + SHA-256 on both HTTPS and plain HTTP origins.

const rmteCrypto = (function() {
    const hasSubtle = !!(window.crypto && window.crypto.subtle);

    // ─── Native (secure context) ───
    const native = {
        async sha256(data) {
            return new Uint8Array(await crypto.subtle.digest('SHA-256', data));
        },
        async importKey(rawKey) {
            return await crypto.subtle.importKey('raw', rawKey, {name:'AES-GCM'}, false, ['encrypt','decrypt']);
        },
        async encrypt(iv, key, plaintext) {
            const ct = await crypto.subtle.encrypt({name:'AES-GCM', iv}, key, plaintext);
            return new Uint8Array(ct);
        },
        async decrypt(iv, key, ciphertext) {
            const pt = await crypto.subtle.decrypt({name:'AES-GCM', iv}, key, ciphertext);
            return new Uint8Array(pt);
        },
        async hmacSha256(keyBytes, dataBytes) {
            const hmacKey = await crypto.subtle.importKey('raw', keyBytes, {name: 'HMAC', hash: 'SHA-256'}, false, ['sign']);
            const sig = await crypto.subtle.sign('HMAC', hmacKey, dataBytes);
            return new Uint8Array(sig);
        },
        randomBytes(n) {
            return crypto.getRandomValues(new Uint8Array(n));
        }
    };

    // ─── Fallback (asmcrypto.js — loaded via CDN) ───
    const fallback = {
        async sha256(data) {
            const hash = new asmCrypto.Sha256();
            hash.process(data);
            hash.finish();
            return new Uint8Array(hash.result);
        },
        async importKey(rawKey) {
            // Just store the raw key bytes — asmcrypto uses them directly
            return new Uint8Array(rawKey);
        },
        async encrypt(iv, key, plaintext) {
            // asmCrypto.AES_GCM.encrypt(data, key, nonce, adata, tagSize)
            return new Uint8Array(asmCrypto.AES_GCM.encrypt(plaintext, key, iv, undefined, 16));
        },
        async decrypt(iv, key, ciphertext) {
            // asmCrypto.AES_GCM.decrypt(data, key, nonce, adata, tagSize)
            return new Uint8Array(asmCrypto.AES_GCM.decrypt(ciphertext, key, iv, undefined, 16));
        },
        async hmacSha256(keyBytes, dataBytes) {
            let key = keyBytes;
            if (key.length > 64) {
                key = await fallback.sha256(key);
            }
            const paddedKey = new Uint8Array(64);
            paddedKey.set(key);
            const ipad = new Uint8Array(64);
            const opad = new Uint8Array(64);
            for (let i = 0; i < 64; i++) {
                ipad[i] = paddedKey[i] ^ 0x36;
                opad[i] = paddedKey[i] ^ 0x5c;
            }
            const inner = new Uint8Array(64 + dataBytes.length);
            inner.set(ipad);
            inner.set(dataBytes, 64);
            const innerHash = await fallback.sha256(inner);

            const outer = new Uint8Array(64 + innerHash.length);
            outer.set(opad);
            outer.set(innerHash, 64);
            return await fallback.sha256(outer);
        },
        randomBytes(n) {
            // crypto.getRandomValues works even in insecure contexts
            if (window.crypto && window.crypto.getRandomValues) {
                return crypto.getRandomValues(new Uint8Array(n));
            }
            // Ultimate fallback (very rare — only ancient browsers)
            const arr = new Uint8Array(n);
            for (let i = 0; i < n; i++) arr[i] = Math.floor(Math.random() * 256);
            return arr;
        }
    };

    const impl = hasSubtle ? native : fallback;

    if (!hasSubtle) {
        console.warn('[RMTE] crypto.subtle unavailable (HTTP non-localhost). Using asmcrypto.js fallback.');
    } else {
        console.info('[RMTE] Using native Web Crypto API.');
    }

    return {
        isNative: hasSubtle,
        sha256: impl.sha256,
        importKey: impl.importKey,
        encrypt: impl.encrypt,
        decrypt: impl.decrypt,
        hmacSha256: impl.hmacSha256,
        randomBytes: impl.randomBytes,
    };
})();
