package radchat

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestEncryptedServiceTaskLifecycle(t *testing.T) {
	a, e := NewAuthority(t.TempDir(), true)
	if e != nil {
		t.Fatal(e)
	}
	var worker *ServiceWorker
	mux := http.NewServeMux()
	mux.Handle("/", a.Handler())
	mux.HandleFunc("/api/services", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, map[string]any{"services": worker.Catalog()})
	})
	auth := httptest.NewServer(mux)
	defer auth.Close()
	agent := testNode(t, auth.URL, "agent", "")
	buyer := testNode(t, auth.URL, "human", "")
	other := testNode(t, auth.URL, "human", "")
	verifyTestEmail(t, buyer, "buyer@example.com", "Buyer")
	verifyTestEmail(t, other, "other@example.com", "Other")
	var calls atomic.Int32
	worker, e = agent.StartServiceWorker(ServiceOptions{Offers: []ServiceOffer{{ID: "plan", Name: "Planner", Skill: "plan", Version: "1"}}, Slots: 1, DailyPerBuyer: 2, DailyTotal: 4, Execute: func(ctx context.Context, o ServiceOffer, p string) (string, error) {
		calls.Add(1)
		return "A useful plan for " + p, nil
	}})
	if e != nil {
		t.Fatal(e)
	}
	p := ServiceClientRequest{Service: "plan", Action: "message/send", MessageID: "a-request-id-at-least-16", Prompt: "private banana mission", Consent: true}
	receipt, e := buyer.ServiceCall(context.Background(), p)
	if e != nil {
		t.Fatal(e)
	}
	var view TaskView
	json.Unmarshal(receipt.Payload, &view)
	p.Action = "tasks/get"
	p.TaskID = view.ID
	for i := 0; i < 40; i++ {
		receipt, e = buyer.ServiceCall(context.Background(), p)
		if e != nil {
			t.Fatal(e)
		}
		json.Unmarshal(receipt.Payload, &view)
		if view.State == "completed" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if view.State != "completed" || view.Result != "A useful plan for private banana mission" {
		t.Fatalf("undelivered task: %+v", view)
	}
	if _, e = other.ServiceCall(context.Background(), p); e == nil {
		t.Fatal("another buyer read private result")
	}
	replay := p
	replay.Action = "message/send"
	if _, e = buyer.ServiceCall(context.Background(), replay); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 1 {
		t.Fatal("retry executed a duplicate task")
	}
	replay.Prompt = "changed prompt"
	if _, e = buyer.ServiceCall(context.Background(), replay); e == nil {
		t.Fatal("conflicting idempotency request accepted")
	}
	p.Action = "tasks/feedback"
	p.Accepted = true
	if _, e = buyer.ServiceCall(context.Background(), p); e != nil {
		t.Fatal(e)
	}
	p.Accepted = false
	if _, e = buyer.ServiceCall(context.Background(), p); e == nil {
		t.Fatal("feedback was overwritten")
	}
	worker.mu.Lock()
	rep := worker.reputation("plan")
	worker.mu.Unlock()
	if rep.Buyers != 1 || rep.Accepted != 1 || rep.Score < 66 || rep.Score > 67 {
		t.Fatalf("bad reputation: %+v", rep)
	}
	b, e := os.ReadFile(filepath.Join(agent.cfg.Dir, "work.enc"))
	if e != nil || bytes.Contains(b, []byte("banana")) {
		t.Fatal("task data not encrypted at rest")
	}
	worker.mu.Lock()
	task := worker.tasks[view.ID]
	var att TaskFeedback
	key, _ := identityKey(buyer.vault)
	raw, _ := key.GetPublic().Raw()
	if task.Attestation.verify(raw, &att) != nil || !att.Accepted {
		t.Error("buyer signature missing")
	}
	worker.mu.Unlock()
}
func TestServiceRejectsForgedProofAndFeedback(t *testing.T) {
	_, auth := authorityTest(t)
	agent := testNode(t, auth.URL, "agent", "")
	buyer := testNode(t, auth.URL, "human", "")
	verifyTestEmail(t, buyer, "proof@example.com", "Buyer")
	worker, e := agent.StartServiceWorker(ServiceOptions{Offers: []ServiceOffer{{ID: "test", Name: "Test", Version: "1", Skill: "plan"}}, Slots: 1, DailyPerBuyer: 1, DailyTotal: 1, Execute: func(context.Context, ServiceOffer, string) (string, error) { return "ok", nil }})
	if e != nil {
		t.Fatal(e)
	}
	r := ServiceRequest{JSONRPC: "2.0", ID: "one", Method: "tasks/get", Proof: buyer.proof}
	if _, e = worker.request(agent.Host.ID(), r); e == nil {
		t.Fatal("email proof reused from different Noise peer")
	}
	r.Proof.Signature = make([]byte, ed25519.SignatureSize)
	if _, e = worker.request(buyer.Host.ID(), r); e == nil {
		t.Fatal("forged email proof")
	}
}
