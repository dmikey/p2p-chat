package radchat

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type EmailProof struct {
	EmailHash string `json:"emailHash"`
	Peer      string `json:"peer"`
	Expires   int64  `json:"expires"`
}
type challenge struct {
	Digest   []byte
	Expires  time.Time
	Attempts int
	Peer     string
}
type bucket struct {
	Start time.Time
	Count int
}
type Authority struct {
	mu      sync.Mutex
	key     ed25519.PrivateKey
	pending map[string]challenge
	limits  map[string]bucket
	Send    func(context.Context, string, string) error
	Dev     bool
}

func NewAuthority(dir string, dev bool) (*Authority, error) {
	p := filepath.Join(dir, "email-authority.key")
	key, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		_, k, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return nil, e
		}
		key = k
		err = atomicWrite(p, key)
	}
	if err != nil {
		return nil, err
	}
	if len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid authority key")
	}
	a := &Authority{key: key, pending: map[string]challenge{}, limits: map[string]bucket{}, Dev: dev}
	a.Send = SendGridOTP
	return a, nil
}
func normalizeEmail(e string) (string, error) {
	e = strings.ToLower(strings.TrimSpace(e))
	parsed, err := mail.ParseAddress(e)
	if err != nil || parsed.Address != e || len(e) > 254 || !strings.Contains(e, ".") {
		return "", errors.New("enter a valid email address")
	}
	return e, nil
}
func (a *Authority) limitLocked(key string, max int) bool {
	now := time.Now()
	for k, b := range a.limits {
		if now.Sub(b.Start) > 15*time.Minute {
			delete(a.limits, k)
		}
	}
	for k, p := range a.pending {
		if now.After(p.Expires) {
			delete(a.pending, k)
		}
	}
	b := a.limits[key]
	if b.Start.IsZero() {
		b.Start = now
	}
	b.Count++
	a.limits[key] = b
	return b.Count <= max && len(a.limits) < 10000 && len(a.pending) < 5000
}
func (a *Authority) digest(id, code string) []byte {
	m := hmac.New(sha256.New, a.key.Seed())
	m.Write([]byte(id + ":" + code))
	return m.Sum(nil)
}
func (a *Authority) request(ctx context.Context, email, id, ip string) (string, error) {
	e, err := normalizeEmail(email)
	if err != nil {
		return "", err
	}
	if _, err := peerID(id); err != nil {
		return "", errors.New("invalid peer identity")
	}
	key := emailHash(e) + ":" + id
	a.mu.Lock()
	if !a.limitLocked("ip:"+ip, 20) || !a.limitLocked("email:"+emailHash(e), 5) {
		a.mu.Unlock()
		return "", errors.New("too many attempts; try in 15 minutes")
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		a.mu.Unlock()
		return "", err
	}
	code := fmt.Sprintf("%06d", n)
	a.pending[key] = challenge{a.digest(key, code), time.Now().Add(10 * time.Minute), 0, id}
	a.mu.Unlock()
	if !a.Dev {
		if err = a.Send(ctx, e, code); err != nil {
			a.mu.Lock()
			if p, ok := a.pending[key]; ok && hmac.Equal(p.Digest, a.digest(key, code)) {
				delete(a.pending, key)
			}
			a.mu.Unlock()
			return "", errors.New("email delivery unavailable")
		}
		return "", nil
	}
	return code, nil
}
func (a *Authority) verify(email, code, id, ip string) (Signed, error) {
	e, err := normalizeEmail(email)
	if err != nil {
		return Signed{}, err
	}
	key := emailHash(e) + ":" + id
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.limitLocked("verify:"+ip, 30) {
		return Signed{}, errors.New("too many attempts")
	}
	p, ok := a.pending[key]
	if !ok || time.Now().After(p.Expires) {
		return Signed{}, errors.New("code expired; request another")
	}
	p.Attempts++
	a.pending[key] = p
	if p.Attempts > 5 {
		delete(a.pending, key)
		return Signed{}, errors.New("too many incorrect codes")
	}
	if len(code) != 6 || !hmac.Equal(a.digest(key, code), p.Digest) {
		return Signed{}, errors.New("code did not match")
	}
	delete(a.pending, key)
	return sign(EmailProof{emailHash(e), id, time.Now().Add(15 * time.Minute).Unix()}, a.key), nil
}
func SendGridOTP(ctx context.Context, email, code string) error {
	key := os.Getenv("SENDGRID_API_KEY")
	from := os.Getenv("SENDGRID_FROM_EMAIL")
	if from == "" {
		from = os.Getenv("EMAIL_FROM")
	}
	if key == "" || from == "" {
		return errors.New("SendGrid is not configured")
	}
	body := map[string]any{"personalizations": []any{map[string]any{"to": []any{map[string]string{"email": email}}}}, "from": map[string]string{"email": from, "name": "Rad Chat"}, "subject": "Your Rad Chat sign-in code", "content": []any{map[string]string{"type": "text/plain", "value": "Your Rad Chat code is " + code + ". It expires in 10 minutes. If you did not request this, ignore this email."}}, "tracking_settings": map[string]any{"click_tracking": map[string]bool{"enable": false, "enable_text": false}, "open_tracking": map[string]bool{"enable": false}}}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.sendgrid.com/v3/mail/send", bytes.NewReader(pack(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != 202 {
		return errors.New("SendGrid did not accept delivery")
	}
	return nil
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func bodyJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var tail any
	if d.Decode(&tail) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
func remoteIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}
func (a *Authority) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/key", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		respond(w, 200, map[string]any{"publicKey": a.key.Public(), "dev": a.Dev})
	})
	mux.HandleFunc("/api/auth/request", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var p struct {
			Email string `json:"email"`
			Peer  string `json:"peer"`
		}
		if bodyJSON(w, r, &p) != nil {
			respond(w, 400, map[string]string{"error": "invalid request"})
			return
		}
		code, err := a.request(r.Context(), p.Email, p.Peer, remoteIP(r))
		if err != nil {
			respond(w, 400, map[string]string{"error": err.Error()})
			return
		}
		respond(w, 200, map[string]any{"ok": true, "devCode": code})
	})
	mux.HandleFunc("/api/auth/verify", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var p struct {
			Email string `json:"email"`
			Code  string `json:"code"`
			Peer  string `json:"peer"`
		}
		if bodyJSON(w, r, &p) != nil {
			respond(w, 400, map[string]string{"error": "invalid request"})
			return
		}
		proof, err := a.verify(p.Email, p.Code, p.Peer, remoteIP(r))
		if err != nil {
			respond(w, 400, map[string]string{"error": err.Error()})
			return
		}
		respond(w, 200, proof)
	})
	return mux
}

type AuthClient struct {
	Dev    bool
	URL    string
	Key    []byte
	client *http.Client
}

func NewAuthClient(endpoint string, pinned []byte) (*AuthClient, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid auth URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback())) {
		return nil, errors.New("auth requires HTTPS except on loopback")
	}
	a := &AuthClient{URL: strings.TrimRight(endpoint, "/"), client: &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}}
	var p struct {
		PublicKey []byte `json:"publicKey"`
		Dev       bool   `json:"dev"`
	}
	if err = a.call(context.Background(), "GET", "/api/auth/key", nil, &p); err != nil {
		if len(pinned) == 32 {
			a.Key = pinned
			return a, nil
		}
		return nil, err
	}
	if len(p.PublicKey) != 32 {
		return nil, errors.New("invalid authority public key")
	}
	if len(pinned) > 0 && !bytes.Equal(pinned, p.PublicKey) {
		return nil, errors.New("authority public key changed")
	}
	a.Dev = p.Dev
	a.Key = p.PublicKey
	return a, nil
}
func (a *AuthClient) call(ctx context.Context, method, path string, p, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, a.URL+path, bytes.NewReader(pack(p)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	if res.StatusCode != 200 {
		var e struct{ Error string }
		json.Unmarshal(b, &e)
		if e.Error == "" {
			e.Error = "email authority unavailable"
		}
		return errors.New(e.Error)
	}
	return json.Unmarshal(b, out)
}
