package radchat

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed public/*
var publicFiles embed.FS

func PublicHandler() http.Handler {
	assets, _ := fs.Sub(publicFiles, "public")
	solana := SolanaHandler()
	files := http.FileServer(http.FS(assets))
	appFiles := http.StripPrefix("/app-assets/", http.FileServer(http.FS(Frontend())))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			w.WriteHeader(405)
			return
		}
		if r.URL.Path == "/api/economy" {
			solana.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/docs" || r.URL.Path == "/docs/" {
			r.URL.Path = "/docs.html"
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; connect-src 'self' wss: ws://127.0.0.1:* ws://localhost:*; style-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'")
		if r.URL.Path == "/app" || r.URL.Path == "/app/" {
			data, _ := fs.ReadFile(Frontend(), "index.html")
			html := strings.ReplaceAll(string(data), "href=\"/style.css\"", "href=\"/app-assets/style.css\"")
			html = strings.ReplaceAll(html, "src=\"/theme.js\"", "src=\"/app-assets/theme.js\"")
			html = strings.ReplaceAll(html, "<script defer src=\"/app.js\"></script>", "<script defer src=\"/browser.js\"></script><script defer src=\"/app-assets/app.js\"></script>")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			if r.Method != "HEAD" {
				w.Write([]byte(html))
			}
			return
		}
		if strings.HasPrefix(r.URL.Path, "/app-assets/") {
			appFiles.ServeHTTP(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
}
