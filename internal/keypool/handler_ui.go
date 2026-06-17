package keypool

import (
	"embed"
	"net/http"
)

//go:embed ui/*
var uiEmbed embed.FS

func (h *Handler) UIRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/ui/channels", http.StatusFound)
}

func (h *Handler) UIKeys(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/ui/channels", http.StatusFound)
}

func (h *Handler) UIChannels(w http.ResponseWriter, r *http.Request) {
	h.serveUIPage(w, "ui/channels.html")
}

func (h *Handler) UIMappings(w http.ResponseWriter, r *http.Request) {
	h.serveUIPage(w, "ui/mappings.html")
}

func (h *Handler) UILogs(w http.ResponseWriter, r *http.Request) {
	h.serveUIPage(w, "ui/logs.html")
}

func (h *Handler) UISettings(w http.ResponseWriter, r *http.Request) {
	h.serveUIPage(w, "ui/settings.html")
}

func (h *Handler) UIStaticCSS(w http.ResponseWriter, r *http.Request) {
	data, err := uiEmbed.ReadFile("ui/style.css")
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Write(data)
}

func (h *Handler) UIStaticJS(w http.ResponseWriter, r *http.Request) {
	data, err := uiEmbed.ReadFile("ui/common.js")
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Write(data)
}

func (h *Handler) serveUIPage(w http.ResponseWriter, path string) {
	data, err := uiEmbed.ReadFile(path)
	if err != nil {
		http.Error(w, "Failed to load page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}
