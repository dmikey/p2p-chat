package radchat

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// ServiceProtocol carries an A2A text-task subset over authenticated Noise streams.
// This is independent of organization membership and never grants channel access.
const ServiceProtocol protocol.ID = "/radchat/service/1.0.0"

type ServiceOffer struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Skill       string   `json:"skill"`
	Version     string   `json:"version"`
	Permissions []string `json:"permissions"`
}
type ServiceCard struct {
	ServiceOffer
	Peer       string     `json:"peer"`
	Addresses  []string   `json:"addresses"`
	Protocol   string     `json:"protocol"`
	Expires    int64      `json:"expires"`
	Available  int        `json:"available"`
	Reputation Reputation `json:"reputation"`
}
type Reputation struct {
	Score     float64 `json:"score"`
	Buyers    int     `json:"buyers"`
	Accepted  int     `json:"accepted"`
	Rejected  int     `json:"rejected"`
	Completed int     `json:"completed"`
	Basis     string  `json:"basis"`
}
type WorkTask struct {
	ID          string `json:"id"`
	Service     string `json:"service"`
	Version     string `json:"version"`
	Buyer       string `json:"buyer"`
	EmailHash   string `json:"emailHash"`
	Prompt      string `json:"prompt"`
	Digest      string `json:"digest"`
	State       string `json:"state"`
	Created     int64  `json:"created"`
	Updated     int64  `json:"updated"`
	Lease       string `json:"lease"`
	LeaseUntil  int64  `json:"leaseUntil"`
	Result      string `json:"result,omitempty"`
	Error       string `json:"error,omitempty"`
	Feedback    *bool  `json:"feedback,omitempty"`
	Attestation Signed `json:"attestation"`
}
type TaskView struct {
	ID       string `json:"id"`
	Service  string `json:"service"`
	Version  string `json:"version"`
	Buyer    string `json:"buyer"`
	Worker   string `json:"worker"`
	Digest   string `json:"digest"`
	State    string `json:"state"`
	Created  int64  `json:"created"`
	Updated  int64  `json:"updated"`
	Result   string `json:"result,omitempty"`
	Error    string `json:"error,omitempty"`
	Feedback *bool  `json:"feedback,omitempty"`
}
type TaskFeedback struct {
	TaskID   string `json:"taskId"`
	Worker   string `json:"worker"`
	Accepted bool   `json:"accepted"`
}
type ServiceRequest struct {
	JSONRPC  string `json:"jsonrpc"`
	ID       string `json:"id"`
	Method   string `json:"method"`
	Proof    Signed `json:"proof"`
	Feedback Signed `json:"feedback"`
	Params   struct {
		Service string `json:"service"`
		TaskID  string `json:"id"`
		Message struct {
			MessageID string `json:"messageId"`
			Role      string `json:"role"`
			Parts     []struct {
				Kind string `json:"kind"`
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"message"`
		Consent  bool `json:"consent"`
		Accepted bool `json:"accepted"`
	} `json:"params"`
}
type ServiceOptions struct {
	Offers        []ServiceOffer
	Slots         int
	DailyPerBuyer int
	DailyTotal    int
	Execute       func(context.Context, ServiceOffer, string) (string, error)
}
type ServiceWorker struct {
	n       *Node
	opts    ServiceOptions
	mu      sync.Mutex
	tasks   map[string]*WorkTask
	running int
	wake    chan struct{}
}

// StartServiceWorker exposes reusable execution plumbing, not operator pricing or keys.
// The encrypted journal lives on this explicit task participant, never the relay.
func (n *Node) StartServiceWorker(opts ServiceOptions) (*ServiceWorker, error) {
	if n.cfg.Kind != "agent" || n.snapshotOrg() != nil {
		return nil, errors.New("services require an independent agent device")
	}
	if opts.Slots < 1 || opts.Slots > 8 || opts.DailyPerBuyer < 1 || opts.DailyTotal < 1 || opts.DailyTotal > 1000 || opts.Execute == nil || len(opts.Offers) < 1 || len(opts.Offers) > 32 {
		return nil, errors.New("bounded service configuration required")
	}
	seen := map[string]bool{}
	for _, o := range opts.Offers {
		if o.ID == "" || len(o.ID) > 80 || seen[o.ID] || o.Version == "" || o.Skill == "" || o.Name == "" {
			return nil, errors.New("invalid offer")
		}
		seen[o.ID] = true
	}
	w := &ServiceWorker{n: n, opts: opts, tasks: map[string]*WorkTask{}, wake: make(chan struct{}, 1)}
	b, e := os.ReadFile(filepath.Join(n.cfg.Dir, "work.enc"))
	if e == nil {
		b, e = open(n.key, b, "radchat-work-v1")
		if e == nil {
			e = json.Unmarshal(b, &w.tasks)
		}
	}
	if e != nil && !os.IsNotExist(e) {
		return nil, e
	}
	// Restart does not silently repeat provider spend or external side effects.
	for _, t := range w.tasks {
		if t.State == "working" || t.State == "submitted" {
			t.State = "failed"
			t.Error = "Runner restarted. This task was not automatically repeated."
			t.Updated = time.Now().UnixMilli()
		}
	}
	if e = w.saveLocked(); e != nil {
		return nil, e
	}
	n.Host.SetStreamHandler(ServiceProtocol, n.streamHandler(w.handle))
	n.background(w.dispatch)
	return w, nil
}
func (w *ServiceWorker) saveLocked() error {
	b, e := seal(w.n.key, pack(w.tasks), "radchat-work-v1")
	if e != nil {
		return e
	}
	return atomicWrite(filepath.Join(w.n.cfg.Dir, "work.enc"), b)
}
func (w *ServiceWorker) offer(id string) (ServiceOffer, bool) {
	for _, o := range w.opts.Offers {
		if o.ID == id {
			return o, true
		}
	}
	return ServiceOffer{}, false
}
func (w *ServiceWorker) identity() ed25519.PrivateKey {
	w.n.mu.Lock()
	defer w.n.mu.Unlock()
	k, _ := identityKey(w.n.vault)
	raw, _ := k.Raw()
	return ed25519.PrivateKey(raw)
}
func (w *ServiceWorker) view(t *WorkTask) Signed {
	return sign(TaskView{t.ID, t.Service, t.Version, t.Buyer, w.n.Host.ID().String(), t.Digest, t.State, t.Created, t.Updated, t.Result, t.Error, t.Feedback}, w.identity())
}
func (w *ServiceWorker) handle(s network.Stream) {
	defer s.Close()
	s.SetDeadline(time.Now().Add(10 * time.Second))
	var req ServiceRequest
	if json.NewDecoder(io.LimitReader(s, 16384)).Decode(&req) != nil {
		return
	}
	result, e := w.request(s.Conn().RemotePeer(), req)
	if e != nil {
		writeJSON(s, map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32000, "message": e.Error()}})
		return
	}
	writeJSON(s, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": a2aServiceTask(result)})
}

func a2aServiceTask(receipt Signed) map[string]any {
	var t TaskView
	json.Unmarshal(receipt.Payload, &t)
	out := map[string]any{"id": t.ID, "contextId": t.ID, "status": map[string]any{"state": t.State, "timestamp": time.UnixMilli(t.Updated).UTC().Format(time.RFC3339Nano)}, "metadata": map[string]any{"radchat/receipt": receipt}}
	if t.Result != "" {
		out["artifacts"] = []any{map[string]any{"artifactId": t.ID + ":result", "name": "Result", "parts": []any{map[string]string{"kind": "text", "text": t.Result}}}}
	}
	return out
}

func (w *ServiceWorker) request(remote peer.ID, r ServiceRequest) (Signed, error) {
	if r.JSONRPC != "2.0" || r.ID == "" || len(r.ID) > 128 {
		return Signed{}, errors.New("invalid A2A envelope")
	}
	var proof EmailProof
	if r.Proof.verify(w.n.Auth.Key, &proof) != nil || proof.Peer != remote.String() || proof.Expires < time.Now().Unix() || proof.Expires > time.Now().Add(16*time.Minute).Unix() || len(proof.EmailHash) != 64 {
		return Signed{}, errors.New("verify your email again before using an agent")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if r.Method == "message/send" {
		p := r.Params
		if !p.Consent || p.Message.Role != "user" || len(p.Message.Parts) != 1 || p.Message.Parts[0].Kind != "text" || len(p.Message.MessageID) < 16 || len(p.Message.MessageID) > 128 {
			return Signed{}, errors.New("approve a single text task")
		}
		prompt := strings.TrimSpace(p.Message.Parts[0].Text)
		if prompt == "" || len(prompt) > 4000 {
			return Signed{}, errors.New("task must be 1–4000 bytes")
		}
		offer, ok := w.offer(p.Service)
		if !ok {
			return Signed{}, errors.New("service unavailable")
		}
		hash := sha256.Sum256(pack([]string{remote.String(), p.Message.MessageID}))
		id := hex.EncodeToString(hash[:])
		digest := sha256.Sum256(pack([]string{p.Service, offer.Version, prompt}))
		d := hex.EncodeToString(digest[:])
		if t := w.tasks[id]; t != nil {
			if t.Digest != d {
				return Signed{}, errors.New("request ID already used for another task")
			}
			return w.view(t), nil
		}
		now := time.Now()
		mine, total, queued := 0, 0, 0
		for id, t := range w.tasks {
			age := now.Sub(time.UnixMilli(t.Created))
			if age > 30*24*time.Hour {
				delete(w.tasks, id)
				continue
			}
			if age > 24*time.Hour {
				t.Prompt = ""
				t.Result = ""
			}
			if time.UnixMilli(t.Created).UTC().Format("2006-01-02") == now.UTC().Format("2006-01-02") {
				total++
				if t.EmailHash == proof.EmailHash {
					mine++
				}
			}
			if t.State == "submitted" || t.State == "working" {
				queued++
			}
		}
		if mine >= w.opts.DailyPerBuyer || total >= w.opts.DailyTotal || queued >= 16 {
			return Signed{}, errors.New("preview capacity reached; please return later")
		}
		t := &WorkTask{ID: id, Service: offer.ID, Version: offer.Version, Buyer: remote.String(), EmailHash: proof.EmailHash, Prompt: prompt, Digest: d, State: "submitted", Created: now.UnixMilli(), Updated: now.UnixMilli()}
		w.tasks[id] = t
		if e := w.saveLocked(); e != nil {
			delete(w.tasks, id)
			return Signed{}, errors.New("task could not be stored")
		}
		select {
		case w.wake <- struct{}{}:
		default:
		}
		return w.view(t), nil
	}
	t := w.tasks[r.Params.TaskID]
	if t == nil || t.Buyer != remote.String() || t.EmailHash != proof.EmailHash {
		return Signed{}, errors.New("task unavailable to this device")
	}
	if time.Since(time.UnixMilli(t.Created)) > 24*time.Hour {
		return Signed{}, errors.New("private task retention expired")
	}
	if r.Method == "tasks/feedback" {
		key, err := remote.ExtractPublicKey()
		if err != nil {
			return Signed{}, errors.New("buyer signing key unavailable")
		}
		raw, _ := key.Raw()
		var feedback TaskFeedback
		if r.Feedback.verify(raw, &feedback) != nil || feedback.TaskID != t.ID || feedback.Worker != w.n.Host.ID().String() || feedback.Accepted != r.Params.Accepted {
			return Signed{}, errors.New("invalid buyer feedback signature")
		}
		if t.State != "completed" {
			return Signed{}, errors.New("feedback requires a completed task")
		}
		if t.Feedback != nil && *t.Feedback != r.Params.Accepted {
			return Signed{}, errors.New("feedback already recorded")
		}
		accepted := r.Params.Accepted
		t.Feedback = &accepted
		t.Attestation = r.Feedback
		if e := w.saveLocked(); e != nil {
			return Signed{}, e
		}
	} else if r.Method != "tasks/get" {
		return Signed{}, errors.New("unsupported service method")
	}
	if time.Since(time.UnixMilli(t.Created)) > 24*time.Hour {
		return Signed{}, errors.New("private task retention expired")
	}
	return w.view(t), nil
}
func (w *ServiceWorker) dispatch() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-w.n.ctx.Done():
			return
		case <-tick.C:
		case <-w.wake:
		}
		w.mu.Lock()
		changed := false
		now := time.Now()
		for id, t := range w.tasks {
			age := now.Sub(time.UnixMilli(t.Created))
			if age > 30*24*time.Hour {
				delete(w.tasks, id)
				changed = true
				continue
			}
			if age > 24*time.Hour && (t.Prompt != "" || t.Result != "") {
				t.Prompt = ""
				t.Result = ""
				changed = true
			}
			if t.State == "working" && now.UnixMilli() > t.LeaseUntil {
				t.State = "failed"
				t.Error = "Execution lease expired. The task was not repeated."
				t.Updated = now.UnixMilli()
				t.Lease = ""
				changed = true
			}
		}
		if changed {
			w.saveLocked()
		}
		for w.running < w.opts.Slots {
			var next *WorkTask
			for _, t := range w.tasks {
				if t.State == "submitted" && (next == nil || t.Created < next.Created || t.Created == next.Created && t.ID < next.ID) {
					next = t
				}
			}
			if next == nil {
				break
			}
			lease := token()
			next.Lease = lease
			next.LeaseUntil = time.Now().Add(75 * time.Second).UnixMilli()
			next.State = "working"
			next.Updated = time.Now().UnixMilli()
			if w.saveLocked() != nil {
				next.State = "submitted"
				break
			}
			w.running++
			task := *next
			w.n.background(func() { w.execute(task, lease) })
		}
		w.mu.Unlock()
	}
}
func (w *ServiceWorker) execute(task WorkTask, lease string) {
	ctx, cancel := context.WithTimeout(w.n.ctx, 60*time.Second)
	defer cancel()
	offer, _ := w.offer(task.Service)
	text, e := w.opts.Execute(ctx, offer, task.Prompt)
	if e == nil && (strings.TrimSpace(text) == "" || len(text) > 12000) {
		e = errors.New("invalid agent result")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.running--
	t := w.tasks[task.ID]
	if t == nil || t.Lease != lease || time.Now().UnixMilli() > t.LeaseUntil {
		return
	}
	t.Updated = time.Now().UnixMilli()
	t.Prompt = ""
	if e != nil {
		t.State = "failed"
		t.Error = "The agent could not finish this task. No payment was taken."
	} else {
		t.State = "completed"
		t.Result = text
	}
	if w.saveLocked() != nil {
		t.State = "failed"
		t.Result = ""
		t.Error = "Result persistence failed"
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Reputation uses at most one recent outcome per verified email per service.
// Bayesian smoothing prevents one lucky result dominating; email is not Sybil resistance.
func (w *ServiceWorker) reputation(id string) Reputation {
	r := Reputation{Basis: "30-day verified-email trial feedback · not paid orders"}
	buyers := map[string]*WorkTask{}
	for _, t := range w.tasks {
		if t.Service != id || time.Since(time.UnixMilli(t.Created)) > 30*24*time.Hour {
			continue
		}
		if t.State == "completed" {
			r.Completed++
		}
		if t.Feedback != nil {
			prev := buyers[t.EmailHash]
			if prev == nil || (t.Created > prev.Created || t.Created == prev.Created && t.ID > prev.ID) {
				buyers[t.EmailHash] = t
			}
		}
	}
	for _, t := range buyers {
		if *t.Feedback {
			r.Accepted++
		} else {
			r.Rejected++
		}
	}
	r.Buyers = len(buyers)
	r.Score = 100 * float64(r.Accepted+3) / float64(r.Buyers+5)
	return r
}
func (w *ServiceWorker) Catalog() []Signed {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := []Signed{}
	for _, o := range w.opts.Offers {
		out = append(out, sign(ServiceCard{o, w.n.Host.ID().String(), w.n.Addresses(), string(ServiceProtocol), time.Now().Add(90 * time.Second).Unix(), w.opts.Slots - w.running, w.reputation(o.ID)}, w.identity()))
	}
	sort.Slice(out, func(i, j int) bool {
		var a, b ServiceCard
		json.Unmarshal(out[i].Payload, &a)
		json.Unmarshal(out[j].Payload, &b)
		if a.Reputation.Score == b.Reputation.Score {
			return a.ID < b.ID
		}
		return a.Reputation.Score > b.Reputation.Score
	})
	return out
}
func (w *ServiceWorker) Handler() http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			rw.WriteHeader(405)
			return
		}
		if r.URL.Path == "/healthz" {
			respond(rw, 200, map[string]bool{"ok": true})
			return
		}
		if r.URL.Path != "/api/services" {
			rw.WriteHeader(404)
			return
		}
		respond(rw, 200, map[string]any{"services": w.Catalog(), "preview": true})
	})
}
