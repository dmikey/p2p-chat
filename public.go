package radchat

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed public/*
var publicFiles embed.FS

func PublicHandler() http.Handler {
	assets, _ := fs.Sub(publicFiles, "public")
	files := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			w.WriteHeader(405)
			return
		}
		if r.URL.Path == "/docs" || r.URL.Path == "/docs/" {
			r.URL.Path = "/docs.html"
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'")
		files.ServeHTTP(w, r)
	})
}
