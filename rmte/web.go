package main

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed ui/*
var uiAssets embed.FS

func setupWebHandler() {
	public, err := fs.Sub(uiAssets, "ui")
	if err != nil {
		panic(err)
	}
	http.Handle("/", http.FileServer(http.FS(public)))
}
