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

	fileServer := http.FileServer(http.FS(public))
	if webPath == "/" {
		mux.Handle("/", fileServer)
		return
	}

	// "/web/" serves the UI; "/web" redirects to "/web/" (ServeMux does this automatically
	// for subtree patterns, but we keep it explicit for clarity).
	mux.Handle(webPath, http.StripPrefix(strings.TrimSuffix(webPath, "/"), fileServer))
	mux.HandleFunc(strings.TrimSuffix(webPath, "/"), func(w http.ResponseWriter, r *http.Request) {
		target := webPath
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusFound)
	})
}
