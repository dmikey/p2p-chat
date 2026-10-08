package radchat

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPublishedAgentDispatchCreditsPrivacyAndRestart(t *testing.T) {
	a, e := NewAuthority(t.TempDir(), true)
	if e != nil {
		t.Fatal(e)
	}
	var market *Market
	mux := http.NewServeMux()
	mux.Handle("/api/auth/", a.Handler())
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { market.Handler().ServeHTTP(w, r) }))
	server := httptest.NewServer(mux)
	defer server.Close()
	agent := testNode(t, server.URL, "agent", "")
	owner := testNode(t, server.URL, "human", "")
	buyer := testNode(t, server.URL, "human", "")
	intruder := testNode(t, server.URL, "human", "")
	verifyTestEmail(t, owner, "owner@example.com", "Owner")
	verifyTestEmail(t, buyer, "buyer@example.com", "Buyer")
	verifyTestEmail(t, intruder, "intruder@example.com", "Other")
	worker, e := agent.StartServiceWorker(ServiceOptions{Offers: []ServiceOffer{{ID: "default", Name: "Default", Skill: "plan", Version: "1"}}, Slots: 1, DailyPerBuyer: 5, DailyTotal: 20, Execute: func(_ context.Context, o ServiceOffer, p string) (string, error) {
		return "Delivered " + p + " using " + o.Instructions, nil
	}})
	if e != nil {
		t.Fatal(e)
	}
	market, e = NewMarket(worker)
	if e != nil {
		t.Fatal(e)
	}
	created, e := owner.MarketCall(context.Background(), MarketRequest{Action: "agents/create", Agent: AgentDefinition{Name: "Launch coach", Description: "Useful planning", Instructions: "private instructions watermelon", Skill: "plan"}})
	if e != nil {
		t.Fatal(e)
	}
	var profile struct {
		Balance uint64            `json:"balance"`
		Agents  []AgentDefinition `json:"agents"`
		Events  []CreditEvent     `json:"events"`
	}
	json.Unmarshal(created.Payload, &profile)
	if profile.Balance != 1 || len(profile.Agents) != 1 {
		t.Fatalf("publisher missing join credit: %s", created.Payload)
	}
	service := profile.Agents[0].ID
	if _, e = intruder.MarketCall(context.Background(), MarketRequest{Action: "agents/toggle", Agent: AgentDefinition{ID: service, Active: false}}); e == nil {
		t.Fatal("another participant paused an agent")
	}
	catalog, e := buyer.ServiceCatalog(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(pack(catalog), []byte("watermelon")) {
		t.Fatal("private instructions in public catalog")
	}
	p := ServiceClientRequest{Service: service, Action: "message/send", MessageID: "unique-test-dispatch-123456", Prompt: "private buyer task apricot", Consent: true}
	receipt, e := buyer.ServiceCall(context.Background(), p)
	if e != nil {
		t.Fatal(e)
	}
	var view TaskView
	json.Unmarshal(receipt.Payload, &view)
	p.TaskID = view.ID
	p.Action = "tasks/get"
	for i := 0; i < 80; i++ {
		receipt, e = buyer.ServiceCall(context.Background(), p)
		if e != nil {
			t.Fatal(e)
		}
		json.Unmarshal(receipt.Payload, &view)
		if view.State == "completed" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if view.State != "completed" || !bytes.Contains([]byte(view.Result), []byte("watermelon")) {
		t.Fatalf("custom definition not executed: %+v", view)
	}
	p.Action = "tasks/feedback"
	p.Accepted = true
	for i := 0; i < 2; i++ {
		if _, e = buyer.ServiceCall(context.Background(), p); e != nil {
			t.Fatal(e)
		}
	}
	receipt, e = owner.MarketCall(context.Background(), MarketRequest{Action: "profile"})
	if e != nil {
		t.Fatal(e)
	}
	json.Unmarshal(receipt.Payload, &profile)
	if profile.Balance != 11 || len(profile.Events) != 2 {
		t.Fatalf("credits duplicate or absent: %s", receipt.Payload)
	}
	data, e := os.ReadFile(filepath.Join(agent.cfg.Dir, "market.enc"))
	if e != nil || bytes.Contains(data, []byte("watermelon")) || bytes.Contains(data, []byte("apricot")) || bytes.Contains(data, []byte("owner@example.com")) {
		t.Fatal("unencrypted journal")
	}
	// A second Market over the same encrypted journal simulates process restoration.
	worker.mu.Lock()
	worker.opts.Offers = worker.opts.Offers[:1]
	worker.mu.Unlock()
	recovered, e := NewMarket(worker)
	if e != nil {
		t.Fatal(e)
	}
	owner.mu.Lock()
	proof := owner.proof
	owner.mu.Unlock()
	out, e := recovered.Request(owner.Host.ID(), MarketRequest{Action: "profile", Proof: proof})
	if e != nil || out["balance"].(uint64) != 11 {
		t.Fatal("restart lost credits", e)
	}
	if bytes.Contains(pack(recovered.data.Events), []byte("apricot")) {
		t.Fatal("task text entered credit evidence")
	}
	buyer.mu.Lock()
	buyerProof := buyer.proof
	buyer.mu.Unlock()
	view.Completion.Signature[0] ^= 1
	if _, e = recovered.Request(buyer.Host.ID(), MarketRequest{Action: "tasks/attest", Proof: buyerProof, Completion: view.Completion}); e == nil {
		t.Fatal("forged completion earned credits")
	}
}

func TestDesktopServiceRequiresOwnerAndWorkerCoSignatures(t *testing.T) {
	a, e := NewAuthority(t.TempDir(), true)
	if e != nil {
		t.Fatal(e)
	}
	var market *Market
	mux := http.NewServeMux()
	mux.Handle("/api/auth/", a.Handler())
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { market.Handler().ServeHTTP(w, r) }))
	server := httptest.NewServer(mux)
	defer server.Close()
	hub := testNode(t, server.URL, "agent", "")
	device := testNode(t, server.URL, "agent", "")
	owner := testNode(t, server.URL, "human", "")
	buyer := testNode(t, server.URL, "human", "")
	verifyTestEmail(t, owner, "native-owner@example.com", "Contributor")
	verifyTestEmail(t, buyer, "native-buyer@example.com", "Buyer")
	opts := ServiceOptions{Offers: []ServiceOffer{{ID: "default", Name: "Default", Skill: "plan", Version: "1"}}, Slots: 1, DailyPerBuyer: 3, DailyTotal: 10, Execute: func(context.Context, ServiceOffer, string) (string, error) { return "native result", nil }}
	coordinator, e := hub.StartServiceWorker(opts)
	if e != nil {
		t.Fatal(e)
	}
	market, e = NewMarket(coordinator)
	if e != nil {
		t.Fatal(e)
	}
	opts.Offers = []ServiceOffer{{ID: "agent-native-test", Name: "Native", Description: "Local work", Skill: "plan", Version: "1"}}
	worker, e := device.StartServiceWorker(opts)
	if e != nil {
		t.Fatal(e)
	}
	owner.mu.Lock()
	priv, _ := identityKey(owner.vault)
	raw, _ := priv.Raw()
	owner.mu.Unlock()
	binding := ServiceBinding{Domain: "radchat-service-owner-v1", Service: "agent-native-test", Worker: device.Host.ID().String(), Owner: owner.Host.ID().String(), Expires: time.Now().Add(time.Minute).Unix()}
	request := MarketRequest{Action: "services/register", Card: worker.Catalog()[0], Binding: sign(binding, raw)}
	if _, e = owner.MarketCall(context.Background(), request); e == nil {
		t.Fatal("claimed public card without worker approval")
	}
	request.WorkerBinding = sign(binding, worker.identity())
	if _, e = owner.MarketCall(context.Background(), request); e != nil {
		t.Fatal(e)
	}
	p := ServiceClientRequest{Service: "agent-native-test", Action: "message/send", MessageID: "desktop-dispatch-12345", Prompt: "test", Consent: true}
	out, e := buyer.ServiceCall(context.Background(), p)
	if e != nil {
		t.Fatal(e)
	}
	var v TaskView
	json.Unmarshal(out.Payload, &v)
	p.TaskID = v.ID
	p.Action = "tasks/get"
	for i := 0; i < 60; i++ {
		out, e = buyer.ServiceCall(context.Background(), p)
		if e != nil {
			t.Fatal(e)
		}
		json.Unmarshal(out.Payload, &v)
		if v.State == "completed" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	p.Action = "tasks/feedback"
	p.Accepted = true
	if _, e = buyer.ServiceCall(context.Background(), p); e != nil {
		t.Fatal(e)
	}
	out, e = owner.MarketCall(context.Background(), MarketRequest{Action: "profile"})
	if e != nil {
		t.Fatal(e)
	}
	var profile struct {
		Balance uint64 `json:"balance"`
	}
	json.Unmarshal(out.Payload, &profile)
	if profile.Balance != 11 {
		t.Fatal("native work credit missing")
	}
	if _, e = owner.MarketCall(context.Background(), MarketRequest{Action: "services/pause", Agent: AgentDefinition{ID: "agent-native-test"}}); e != nil {
		t.Fatal(e)
	}
	for _, s := range market.Catalog() {
		var card ServiceCard
		json.Unmarshal(s.Payload, &card)
		if card.ID == "agent-native-test" {
			t.Fatal("paused desktop service still discoverable")
		}
	}
}
