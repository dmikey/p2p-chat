package radchat

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSMTPRefusesPlaintextDowngrade(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	done := make(chan string, 1)
	go func() {
		c, e := l.Accept()
		if e != nil {
			done <- ""
			return
		}
		defer c.Close()
		io.WriteString(c, "220 fixture SMTP\r\n")
		reader := bufio.NewReader(c)
		line, _ := reader.ReadString('\n')
		io.WriteString(c, "250-fixture\r\n250 AUTH PLAIN\r\n")
		more, _ := io.ReadAll(c)
		done <- line + string(more)
	}()
	host, port, _ := net.SplitHostPort(l.Addr().String())
	cfg := SMTPConfig{Host: host, Port: port, Username: "fixture", Password: "fixture-secret", From: "sender@example.com", TLSMode: "starttls"}
	if cfg.SendCode(context.Background(), "recipient@example.com", "123456") == nil {
		t.Fatal("plaintext SMTP accepted")
	}
	commands := <-done
	if strings.Contains(commands, "AUTH ") || strings.Contains(commands, "MAIL ") {
		t.Fatal("credentials or email sent before TLS")
	}
}
func TestProviderAdapterDoesNotFollowRedirects(t *testing.T) {
	reached := false
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true; w.WriteHeader(204) }))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer source.Close()
	p := ProviderAdapter{URL: source.URL, Token: "fixture-token", Client: source.Client()}
	if p.SendCode(context.Background(), "recipient@example.com", "123456") == nil || reached {
		t.Fatal("provider redirect must reject without forwarding bearer token")
	}
}
func TestEmailProviderSelection(t *testing.T) {
	t.Setenv("RADCHAT_EMAIL_PROVIDER", "smtp")
	t.Setenv("SMTP_HOST", "smtp.example.com")
	t.Setenv("SMTP_FROM_EMAIL", "sender@example.com")
	t.Setenv("SMTP_USERNAME", "")
	t.Setenv("SMTP_PASSWORD", "")
	sender, err := ConfiguredEmailSender()
	if err != nil {
		t.Fatal(err)
	}
	cfg := sender.(SMTPConfig)
	if cfg.TLSMode != "starttls" {
		t.Fatal("secure default required")
	}
	t.Setenv("SMTP_TLS", "none")
	if _, err = ConfiguredEmailSender(); err == nil {
		t.Fatal("insecure SMTP allowed")
	}
	t.Setenv("RADCHAT_EMAIL_PROVIDER", "adapter")
	t.Setenv("RADCHAT_EMAIL_ADAPTER_URL", "http://adapter.example.com")
	t.Setenv("RADCHAT_EMAIL_ADAPTER_TOKEN", "fixture")
	if _, err = ConfiguredEmailSender(); err == nil {
		t.Fatal("plaintext adapter allowed")
	}
}
