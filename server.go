package radchat

import (
	"crypto/hmac"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
)

//go:embed web/*
var frontend embed.FS

func (n *Node) APIToken() string { n.mu.Lock(); defer n.mu.Unlock(); return n.vault.APIToken }
func (n *Node) State() map[string]any {
	n.mu.Lock()
	name, email, kind := n.vault.Name, n.vault.Email, n.vault.Kind
	lastErr := n.lastNetworkError
	org := n.vault.Org
	out := map[string]any{"peer": n.Host.ID().String(), "name": name, "email": email, "kind": kind, "networkError": lastErr, "runtime": "native", "org": nil}
	if org != nil {
		var policy Policy
		json.Unmarshal(org.Policy.Payload, &policy)
		members := []Certificate{}
		for _, s := range org.Members {
			var c Certificate
			json.Unmarshal(s.Payload, &c)
			members = append(members, c)
		}
		out["org"] = map[string]any{"id": org.ID, "name": org.Name, "owner": len(org.RootPrivate) > 0, "policy": policy, "members": members}
		inactive := false
		if record, err := verifyAccess(org.Access); err == nil {
			inactive = !record.Active[n.Host.ID().String()]
		}
		out["deactivated"] = inactive
		out["authorized"] = n.activeLocked(n.Host.ID().String())
	}
	n.mu.Unlock()
	out["addresses"] = n.Addresses()
	peers := []map[string]any{}
	for _, p := range n.Host.Network().Peers() {
		relayed := false
		for _, c := range n.Host.Network().ConnsToPeer(p) {
			if strings.Contains(c.RemoteMultiaddr().String(), "/p2p-circuit") {
				relayed = true
			}
		}
		peers = append(peers, map[string]any{"id": p.String(), "relayed": relayed, "bootstrap": n.bootstrap != nil && p == n.bootstrap.ID})
	}
	out["runner"] = n.runnerNode().runner.Snapshot()
	out["peers"] = peers
	return out
}
func (n *Node) Handler() http.Handler {
	assets, _ := fs.Sub(frontend, "web")
	files := http.FileServer(http.FS(assets))
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			w.WriteHeader(405)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "radchat_device", Value: n.APIToken(), Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil})
		files.ServeHTTP(w, r)
	})
	api := http.NewServeMux()
	api.Handle("/api/economy", SolanaHandler())
	api.HandleFunc("/api/runner", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			respond(w, 405, map[string]string{"error": "POST required"})
			return
		}
		var p struct {
			Action string       `json:"action"`
			Config RunnerConfig `json:"config"`
			Name   string       `json:"name"`
		}
		if bodyJSON(w, r, &p) != nil {
			respond(w, 400, map[string]string{"error": "invalid runner request"})
			return
		}
		var err error
		switch p.Action {
		case "start":
			err = n.LaunchContributor(r.Context(), p.Name, p.Config)
		case "stop":
			n.runnerNode().runner.stop("paused")
		case "approve":
			err = n.runnerNode().ApproveRunner(r.Context())
		default:
			err = fmt.Errorf("unsupported runner action")
		}
		if err != nil {
			respond(w, 400, map[string]string{"error": err.Error()})
			return
		}
		respond(w, 200, n.runnerNode().runner.Snapshot())
	})
	api.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		respond(w, 200, n.State())
	})
	api.HandleFunc("/api/messages", func(w http.ResponseWriter, r *http.Request) {
		n.mu.Lock()
		inactive := false
		if n.vault.Org != nil {
			if record, err := verifyAccess(n.vault.Org.Access); err == nil {
				inactive = !record.Active[n.Host.ID().String()]
			}
		}
		n.mu.Unlock()
		if inactive {
			respond(w, 403, map[string]string{"error": "Deactivated Account: ask your organization owner to reactivate access"})
			return
		}
		if r.Method == "GET" {
			respond(w, 200, n.Messages())
			return
		}
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var p struct {
			Channel string `json:"channel"`
			Text    string `json:"text"`
			ReplyTo string `json:"replyTo"`
		}
		if bodyJSON(w, r, &p) != nil {
			respond(w, 400, map[string]string{"error": "invalid message"})
			return
		}
		var m ChatMessage
		var propagated bool
		var err error
		if strings.HasPrefix(p.Channel, "dm:") {
			m, propagated, err = n.SendDirect(r.Context(), strings.TrimPrefix(p.Channel, "dm:"), p.Text, p.ReplyTo)
		} else {
			m, propagated, err = n.Send(r.Context(), p.Channel, p.Text, p.ReplyTo)
		}
		if err != nil {
			respond(w, 400, map[string]string{"error": err.Error()})
			return
		}
		respond(w, 200, map[string]any{"message": m, "propagated": propagated})
	})
	api.HandleFunc("/api/auth/request", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var p struct {
			Email string `json:"email"`
		}
		if bodyJSON(w, r, &p) != nil {
			respond(w, 400, map[string]string{"error": "invalid email"})
			return
		}
		code, err := n.RequestCode(r.Context(), p.Email)
		if err != nil {
			respond(w, 400, map[string]string{"error": err.Error()})
			return
		}
		respond(w, 200, map[string]string{"devCode": code})
	})
	api.HandleFunc("/api/auth/verify", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var p struct {
			Email string `json:"email"`
			Code  string `json:"code"`
			Name  string `json:"name"`
		}
		if bodyJSON(w, r, &p) != nil {
			respond(w, 400, map[string]string{"error": "invalid verification"})
			return
		}
		err := n.VerifyCode(r.Context(), p.Email, p.Code, p.Name)
		if err != nil {
			respond(w, 400, map[string]string{"error": err.Error()})
			return
		}
		respond(w, 200, map[string]bool{"ok": true})
	})
	api.HandleFunc("/api/org/create", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var p struct {
			Name string `json:"name"`
		}
		if bodyJSON(w, r, &p) != nil {
			respond(w, 400, map[string]string{"error": "invalid organization"})
			return
		}
		err := n.CreateOrg(p.Name)
		if err != nil {
			respond(w, 400, map[string]string{"error": err.Error()})
			return
		}
		respond(w, 200, map[string]bool{"ok": true})
	})
	api.HandleFunc("/api/org/join", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var p struct {
			Invite string `json:"invite"`
		}
		if bodyJSON(w, r, &p) != nil {
			respond(w, 400, map[string]string{"error": "invalid invite"})
			return
		}
		err := n.Join(r.Context(), p.Invite)
		if err != nil {
			respond(w, 400, map[string]string{"error": err.Error()})
			return
		}
		respond(w, 200, map[string]bool{"ok": true})
	})
	api.HandleFunc("/api/invites", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var p struct {
			Email string `json:"email"`
			Kind  string `json:"kind"`
		}
		if bodyJSON(w, r, &p) != nil {
			respond(w, 400, map[string]string{"error": "invalid invite"})
			return
		}
		invite, err := n.Invite(p.Email, p.Kind)
		if err != nil {
			respond(w, 400, map[string]string{"error": err.Error()})
			return
		}
		respond(w, 200, map[string]string{"invite": invite})
	})
	api.HandleFunc("/api/org/policy", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var p struct {
			RetentionDays int       `json:"retentionDays"`
			Channels      []Channel `json:"channels"`
		}
		if bodyJSON(w, r, &p) != nil {
			respond(w, 400, map[string]string{"error": "invalid policy"})
			return
		}
		if err := n.SetPolicy(r.Context(), p.RetentionDays, p.Channels); err != nil {
			respond(w, 400, map[string]string{"error": err.Error()})
			return
		}
		respond(w, 200, map[string]bool{"ok": true})
	})
	api.HandleFunc("/api/recovery/export", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var p struct {
			Password string `json:"password"`
		}
		if bodyJSON(w, r, &p) != nil {
			respond(w, 400, map[string]string{"error": "invalid recovery passphrase"})
			return
		}
		data, err := n.Export(p.Password)
		if err != nil {
			respond(w, 400, map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", "attachment; filename=radchat.recovery")
		w.Write(data)
	})
	api.HandleFunc("/api/accounts", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var p struct {
			Peer   string `json:"peer"`
			Active bool   `json:"active"`
		}
		if bodyJSON(w, r, &p) != nil {
			respond(w, 400, map[string]string{"error": "invalid account update"})
			return
		}
		if err := n.SetAccountActive(r.Context(), p.Peer, p.Active); err != nil {
			respond(w, 400, map[string]string{"error": err.Error()})
			return
		}
		respond(w, 200, map[string]bool{"ok": true})
	})

	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("radchat_device")
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		tokenOK := strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") && hmac.Equal([]byte(bearer), []byte(n.APIToken()))
		if !tokenOK && (err != nil || !hmac.Equal([]byte(cookie.Value), []byte(n.APIToken()))) {
			respond(w, 401, map[string]string{"error": "device authentication required"})
			return
		}
		if r.Method != "GET" && !tokenOK {
			origin, err := url.Parse(r.Header.Get("Origin"))
			if err != nil || origin.Host != r.Host || (origin.Scheme != "http" && origin.Scheme != "https") {
				respond(w, 403, map[string]string{"error": "same-origin request required"})
				return
			}
		}
		api.ServeHTTP(w, r)
	}))
	mux.HandleFunc("/.well-known/agent-card.json", n.agentCard)
	mux.HandleFunc("/a2a", n.a2a)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hostname := r.Host
		if host, _, err := net.SplitHostPort(hostname); err == nil {
			hostname = host
		}
		ip := net.ParseIP(hostname)
		if hostname != "localhost" && (ip == nil || !ip.IsLoopback()) {
			respond(w, 403, map[string]string{"error": "local node requires a loopback Host"})
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		mux.ServeHTTP(w, r)
	})
}
func (n *Node) agentCard(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		w.WriteHeader(405)
		return
	}
	respond(w, 200, map[string]any{"name": "Rad Chat bridge", "description": "Invite-only organization chat bridge. A2A messages are signed by this agent device and shared with human members.", "url": "http://" + r.Host + "/a2a", "version": "0.1.0", "protocolVersion": "0.3.0", "preferredTransport": "JSONRPC", "capabilities": map[string]bool{"streaming": false, "pushNotifications": false}, "defaultInputModes": []string{"text/plain"}, "defaultOutputModes": []string{"text/plain"}, "securitySchemes": map[string]any{"deviceToken": map[string]string{"type": "http", "scheme": "bearer"}}, "security": []any{map[string]any{"deviceToken": []string{}}}, "skills": []any{map[string]any{"id": "organization-chat", "name": "Chat with humans", "description": "Send text to an organization channel; contextId is the channel name.", "tags": []string{"chat", "humans", "p2p"}}}})
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func (n *Node) a2a(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || !hmac.Equal([]byte(strings.TrimPrefix(auth, "Bearer ")), []byte(n.APIToken())) {
		respond(w, 401, map[string]string{"error": "agent device bearer token required"})
		return
	}
	n.mu.Lock()
	kind := n.vault.Kind
	n.mu.Unlock()
	if kind != "agent" {
		respond(w, 403, map[string]string{"error": "A2A sending requires an invited agent node"})
		return
	}
	var req rpcRequest
	if bodyJSON(w, r, &req) != nil {
		respond(w, 200, map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32700, "message": "Parse error"}})
		return
	}
	id := req.ID
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	result := func(v any) { respond(w, 200, map[string]any{"jsonrpc": "2.0", "id": id, "result": v}) }
	fail := func(code int, msg string) {
		respond(w, 200, map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
	}
	if req.JSONRPC != "2.0" {
		fail(-32600, "Invalid Request")
		return
	}
	switch req.Method {
	case "message/send":
		var p struct {
			Message struct {
				Kind      string `json:"kind"`
				Role      string `json:"role"`
				MessageID string `json:"messageId"`
				ContextID string `json:"contextId"`
				Parts     []struct {
					Kind string `json:"kind"`
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"message"`
		}
		if json.Unmarshal(req.Params, &p) != nil || p.Message.Kind != "message" || p.Message.Role != "user" || p.Message.MessageID == "" || len(p.Message.MessageID) > 128 || len(p.Message.Parts) == 0 {
			fail(-32602, "Invalid message")
			return
		}
		text := []string{}
		for _, part := range p.Message.Parts {
			if part.Kind != "text" {
				fail(-32005, "Only text parts are supported")
				return
			}
			text = append(text, part.Text)
		}
		channel := p.Message.ContextID
		if channel == "" {
			channel = "general"
		}
		var m ChatMessage
		var propagated bool
		var err error
		if strings.HasPrefix(channel, "dm:") {
			m, propagated, err = n.SendDirect(r.Context(), strings.TrimPrefix(channel, "dm:"), strings.Join(text, "\n"), "")
		} else {
			m, propagated, err = n.Send(r.Context(), channel, strings.Join(text, "\n"), "")
		}
		if err != nil {
			fail(-32602, err.Error())
			return
		}
		result(map[string]any{"kind": "message", "role": "agent", "messageId": m.ID, "contextId": channel, "parts": []any{map[string]string{"kind": "text", "text": "Message stored on this participant device."}}, "metadata": map[string]any{"radchat": map[string]any{"propagated": propagated, "chatMessageId": m.ID}}})
	case "radchat/history":
		var p struct {
			ContextID string `json:"contextId"`
		}
		if len(req.Params) > 0 && json.Unmarshal(req.Params, &p) != nil {
			fail(-32602, "Invalid params")
			return
		}
		messages := []ChatMessage{}
		for _, m := range n.Messages() {
			if p.ContextID == "" || m.Channel == p.ContextID {
				messages = append(messages, m)
			}
		}
		result(messages)
	default:
		fail(-32601, "Method not found; supported: message/send, radchat/history")
	}
}
func Frontend() fs.FS { assets, _ := fs.Sub(frontend, "web"); return assets }
