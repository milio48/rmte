package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed ui/*
var uiAssets embed.FS

// setupWebHandler mounts the embedded Web UI under webPath (e.g. "/" or "/web/")
// and exposes {webPath}config.json so the UI can derive the WebSocket URL.
func setupWebHandler(mux *http.ServeMux, webPath, wsPath string) {
	public, err := fs.Sub(uiAssets, "ui")
	if err != nil {
		panic(err)
	}

	mux.HandleFunc(webPath+"config.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(map[string]string{
			"ws_path":       wsPath,
			"ws_proxy_path": wsPath + "-proxy",
			"version":       appVersion,
		})
	})

	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		data, err := uiAssets.ReadFile("ui/rmte.svg")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(data)
	})

	rawIndex, err := uiAssets.ReadFile("ui/index.html")
	if err != nil {
		panic(err)
	}
	processedIndex := strings.ReplaceAll(string(rawIndex), "__RMTE_VERSION__", appVersion)

	fileServer := http.FileServer(http.FS(public))

	serveAsset := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cleanPath := strings.TrimPrefix(r.URL.Path, strings.TrimSuffix(webPath, "/"))
		cleanPath = strings.TrimPrefix(cleanPath, "/")

		if cleanPath == "" || cleanPath == "index.html" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			w.Header().Set("Pragma", "no-cache")
			w.Header().Set("Expires", "0")
			_, _ = w.Write([]byte(processedIndex))
			return
		}

		// Static assets: release-versioned queries (?v=...) can be cached long-term (immutable).
		// Non-versioned requests (or dev builds) require revalidation so reverse proxies don't serve stale files.
		if v := r.URL.Query().Get("v"); v != "" && v != "dev" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		if webPath != "/" {
			http.StripPrefix(strings.TrimSuffix(webPath, "/"), fileServer).ServeHTTP(w, r)
		} else {
			fileServer.ServeHTTP(w, r)
		}
	})

	if webPath == "/" {
		mux.Handle("/", serveAsset)
		return
	}

	// "/web/" serves the UI; "/web" redirects to "/web/" (ServeMux does this automatically
	// for subtree patterns, but we keep it explicit for clarity).
	mux.Handle(webPath, serveAsset)
	mux.HandleFunc(strings.TrimSuffix(webPath, "/"), func(w http.ResponseWriter, r *http.Request) {
		target := webPath
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusFound)
	})
}
