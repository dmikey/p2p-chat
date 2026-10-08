package radchat

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed public/*
var publicFiles embed.FS

// Version URLs by their embedded bytes so intermediary caches cannot retain an
// older script or stylesheet when a relay is upgraded.
func assetStamper(assets fs.FS, prefix string) func(string) string {
	entries, _ := fs.ReadDir(assets, ".")
	replacements := []string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !(strings.HasSuffix(name, ".js") || strings.HasSuffix(name, ".css") || strings.HasSuffix(name, ".svg")) {
			continue
		}
		data, _ := fs.ReadFile(assets, name)
		digest := sha256.Sum256(data)
		path := prefix + name
		replacements = append(replacements, "\""+path+"\"", fmt.Sprintf("\"%s?v=%x\"", path, digest[:12]))
	}
	replacer := strings.NewReplacer(replacements...)
	return replacer.Replace
}

func PublicHandler() http.Handler {
	assets, _ := fs.Sub(publicFiles, "public")
	stampPublic := assetStamper(assets, "/")
	stampApp := assetStamper(Frontend(), "/app-assets/")
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
		if r.URL.Path == "/explorer" || r.URL.Path == "/explorer/" {
			r.URL.Path = "/explorer.html"
		}
		if r.URL.Path == "/docs" || r.URL.Path == "/docs/" {
			r.URL.Path = "/docs.html"
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; connect-src 'self' wss: ws://127.0.0.1:* ws://localhost:*; style-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'")
		if r.URL.Path == "/app" || r.URL.Path == "/app/" {
			data, _ := fs.ReadFile(Frontend(), "index.html")
			html := strings.ReplaceAll(string(data), "href=\"/style.css\"", "href=\"/app-assets/style.css\"")
			html = strings.ReplaceAll(html, "src=\"/theme.js\"", "src=\"/app-assets/theme.js\"")
			html = strings.ReplaceAll(html, "<script defer src=\"/app.js\"></script>", "<script defer src=\"/browser.js\"></script><script defer src=\"/app-assets/app.js\"></script>")
			html = stampPublic(stampApp(html))
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
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if strings.HasSuffix(name, ".html") {
			if data, err := fs.ReadFile(assets, name); err == nil {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				if r.Method != "HEAD" {
					w.Write([]byte(stampPublic(string(data))))
				}
				return
			}
		}
		files.ServeHTTP(w, r)
	})
}
