package radchat

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http/httptest"
	"regexp"
	"testing"
)

func TestPublicAppAssetURLsMatchServedBytes(t *testing.T) {
	server := httptest.NewServer(PublicHandler())
	defer server.Close()
	response, e := server.Client().Get(server.URL + "/app")
	if e != nil {
		t.Fatal(e)
	}
	html, _ := io.ReadAll(response.Body)
	response.Body.Close()
	refs := regexp.MustCompile(`(?:src|href)="([^"?]+\.(?:js|css|svg))\?v=([a-f0-9]{24})"`).FindAllSubmatch(html, -1)
	if len(refs) < 5 {
		t.Fatal("app still contains unversioned assets")
	}
	for _, ref := range refs {
		r, e := server.Client().Get(server.URL + string(ref[1]) + "?v=" + string(ref[2]))
		if e != nil {
			t.Fatal(e)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		digest := sha256.Sum256(b)
		if r.StatusCode != 200 || string(ref[2]) != fmt.Sprintf("%x", digest[:12]) {
			t.Fatalf("asset fingerprint mismatch: %s", ref[1])
		}
	}
}
