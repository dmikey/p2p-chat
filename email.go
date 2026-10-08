package radchat

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"os"
	"strings"
	"time"
)

// EmailSender delivers an OTP. It never receives organization or device keys.
// Applications may inject any provider implementation with NewAuthorityWithSender.
type EmailSender interface {
	SendCode(context.Context, string, string) error
}
type EmailSenderFunc func(context.Context, string, string) error

func (f EmailSenderFunc) SendCode(ctx context.Context, email, code string) error {
	return f(ctx, email, code)
}

func ConfiguredEmailSender() (EmailSender, error) {
	switch os.Getenv("RADCHAT_EMAIL_PROVIDER") {
	case "", "sendgrid":
		if os.Getenv("SENDGRID_API_KEY") == "" || (os.Getenv("SENDGRID_FROM_EMAIL") == "" && os.Getenv("EMAIL_FROM") == "") {
			return nil, errors.New("SendGrid requires API key and verified sender")
		}
		return EmailSenderFunc(SendGridOTP), nil
	case "smtp":
		cfg := SMTPConfig{Host: os.Getenv("SMTP_HOST"), Port: os.Getenv("SMTP_PORT"), Username: os.Getenv("SMTP_USERNAME"), Password: os.Getenv("SMTP_PASSWORD"), From: os.Getenv("SMTP_FROM_EMAIL"), TLSMode: os.Getenv("SMTP_TLS")}
		if cfg.Port == "" {
			cfg.Port = "587"
		}
		if cfg.TLSMode == "" {
			cfg.TLSMode = "starttls"
		}
		if cfg.Host == "" || strings.ContainsAny(cfg.Host, "\r\n") || (cfg.TLSMode != "starttls" && cfg.TLSMode != "implicit") {
			return nil, errors.New("SMTP requires host and TLS mode starttls or implicit")
		}
		if _, err := normalizeEmail(cfg.From); err != nil {
			return nil, errors.New("SMTP requires valid SMTP_FROM_EMAIL")
		}
		if (cfg.Username == "") != (cfg.Password == "") {
			return nil, errors.New("SMTP username and password must be set together")
		}
		return cfg, nil
	case "adapter":
		u, err := url.Parse(os.Getenv("RADCHAT_EMAIL_ADAPTER_URL"))
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
			return nil, errors.New("provider adapter requires HTTPS URL")
		}
		if os.Getenv("RADCHAT_EMAIL_ADAPTER_TOKEN") == "" {
			return nil, errors.New("provider adapter requires bearer token")
		}
		return ProviderAdapter{URL: u.String(), Token: os.Getenv("RADCHAT_EMAIL_ADAPTER_TOKEN")}, nil
	default:
		return nil, errors.New("unknown RADCHAT_EMAIL_PROVIDER; use sendgrid, smtp, or adapter")
	}
}

type SMTPConfig struct {
	Host, Port, Username, Password, From, TLSMode string
	TLSConfig                                     *tls.Config
}

func (s SMTPConfig) SendCode(ctx context.Context, email, code string) error {
	from, err := normalizeEmail(s.From)
	if err != nil {
		return err
	}
	recipient, err := normalizeEmail(email)
	if err != nil {
		return err
	}
	if len(code) != 6 || strings.Trim(code, "0123456789") != "" {
		return errors.New("invalid OTP")
	}
	timeout := time.Now().Add(15 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(timeout) {
		timeout = d
	}
	dialer := net.Dialer{Deadline: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(s.Host, s.Port))
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(timeout)
	done := context.AfterFunc(ctx, func() { conn.Close() })
	defer done()
	config := &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}
	if s.TLSConfig != nil {
		config = s.TLSConfig.Clone()
		config.ServerName = s.Host
		config.MinVersion = tls.VersionTLS12
	}
	if config.InsecureSkipVerify {
		return errors.New("SMTP certificate verification cannot be disabled")
	}
	if s.TLSMode == "implicit" {
		secure := tls.Client(conn, config)
		if err = secure.HandshakeContext(ctx); err != nil {
			return err
		}
		conn = secure
	} else if s.TLSMode != "starttls" {
		return errors.New("SMTP requires TLS")
	}
	client, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return err
	}
	defer client.Close()
	if s.TLSMode == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("SMTP server must support STARTTLS")
		}
		if err = client.StartTLS(config); err != nil {
			return err
		}
	}
	if s.Username != "" {
		if err = client.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			return err
		}
	}
	if err = client.Mail(from); err != nil {
		return err
	}
	if err = client.Rcpt(recipient); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(writer, "From: Rad Chat <%s>\r\nTo: %s\r\nSubject: Your Rad Chat sign-in code\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\nYour Rad Chat code is %s. It expires in 10 minutes. If you did not request this, ignore this email.\r\n", from, recipient, code)
	if err != nil {
		writer.Close()
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}

// ProviderAdapter calls a self-hosted adapter that returns 200, 202 or 204 after
// accepting delivery. Provider credentials stay in the adapter, never the client.
type ProviderAdapter struct {
	URL, Token string
	Client     *http.Client
}

func (p ProviderAdapter) SendCode(ctx context.Context, email, code string) error {
	req, err := http.NewRequestWithContext(ctx, "POST", p.URL, bytes.NewReader(pack(map[string]any{"email": email, "code": code, "expiresIn": 600, "purpose": "radchat-sign-in"})))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.Token)
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if p.Client != nil {
		clone := *p.Client
		clone.CheckRedirect = client.CheckRedirect
		client = &clone
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 && res.StatusCode != 202 && res.StatusCode != 204 {
		return errors.New("provider adapter did not accept delivery")
	}
	return nil
}
