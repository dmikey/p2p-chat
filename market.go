package radchat

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

const MarketProtocol protocol.ID = "/radchat/market/1.0.0"
const TestCreditBudget uint64 = 1000000

type CompletionEvidence struct {
	Domain    string `json:"domain"`
	ID        string `json:"id"`
	Service   string `json:"service"`
	Version   string `json:"version"`
	Worker    string `json:"worker"`
	Buyer     string `json:"buyer"`
	Digest    string `json:"digest"`
	Completed int64  `json:"completed"`
}
type AgentDefinition struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Instructions string `json:"instructions"`
	Skill        string `json:"skill"`
	Runtime      string `json:"runtime"`
	Active       bool   `json:"active"`
	Created      int64  `json:"created"`
}
type ServiceBinding struct {
	Domain  string `json:"domain"`
	Service string `json:"service"`
	Worker  string `json:"worker"`
	Owner   string `json:"owner"`
	Expires int64  `json:"expires"`
}
type MarketRequest struct {
	Action        string          `json:"action"`
	Proof         Signed          `json:"proof"`
	Agent         AgentDefinition `json:"agent"`
	Card          Signed          `json:"card"`
	Binding       Signed          `json:"binding"`
	WorkerBinding Signed          `json:"workerBinding"`
	Completion    Signed          `json:"completion"`
	Feedback      Signed          `json:"feedback"`
}
type MarketRegistration struct {
	Owner  string `json:"owner"`
	Email  string `json:"email"`
	Peer   string `json:"peer"`
	Card   Signed `json:"card"`
	Paused bool   `json:"paused"`
}
type CreditEvent struct {
	ID         string `json:"id"`
	Member     string `json:"member"`
	Kind       string `json:"kind"`
	Points     uint64 `json:"points"`
	Created    int64  `json:"created"`
	Evidence   Signed `json:"evidence"`
	Acceptance Signed `json:"acceptance"`
}
type marketData struct {
	Agents        map[string]AgentDefinition    `json:"agents"`
	Registrations map[string]MarketRegistration `json:"registrations"`
	Events        map[string]CreditEvent        `json:"events"`
}
type Market struct {
	mu     sync.Mutex
	worker *ServiceWorker
	data   marketData
}

func NewMarket(w *ServiceWorker) (*Market, error) {
	m := &Market{worker: w, data: marketData{Agents: map[string]AgentDefinition{}, Registrations: map[string]MarketRegistration{}, Events: map[string]CreditEvent{}}}
	b, e := os.ReadFile(filepath.Join(w.n.cfg.Dir, "market.enc"))
	if e == nil {
		b, e = open(w.n.key, b, "radchat-market-v1")
		if e == nil {
			e = json.Unmarshal(b, &m.data)
		}
	}
	if e != nil && !os.IsNotExist(e) {
		return nil, e
	}
	if m.data.Agents == nil || m.data.Registrations == nil || m.data.Events == nil {
		return nil, errors.New("invalid market journal")
	}
	w.mu.Lock()
	for _, a := range m.data.Agents {
		w.opts.Offers = append(w.opts.Offers, m.offer(a))
		w.disabled[a.ID] = !a.Active
	}
	w.mu.Unlock()
	w.n.Host.SetStreamHandler(MarketProtocol, m.handle)
	return m, nil
}
func (m *Market) offer(a AgentDefinition) ServiceOffer {
	return ServiceOffer{ID: a.ID, Name: a.Name, Description: a.Description, Skill: a.Skill, Version: "1.0.0", Instructions: a.Instructions, Permissions: []string{"Selected task only", "OpenAI processing on RadOps", "No files, shell or accounts"}}
}
func (m *Market) member(email string) string {
	h := hmac.New(sha256.New, m.worker.n.key)
	h.Write([]byte("test-credit-member:" + email))
	return hex.EncodeToString(h.Sum(nil))
}
func (m *Market) save() error {
	b, e := seal(m.worker.n.key, pack(m.data), "radchat-market-v1")
	if e != nil {
		return e
	}
	return atomicWrite(filepath.Join(m.worker.n.cfg.Dir, "market.enc"), b)
}
func publicKey(id string) ([]byte, error) {
	p, e := peer.Decode(id)
	if e != nil {
		return nil, e
	}
	k, e := p.ExtractPublicKey()
	if e != nil {
		return nil, e
	}
	return k.Raw()
}
func (m *Market) handle(s network.Stream) {
	defer s.Close()
	s.SetDeadline(time.Now().Add(15 * time.Second))
	var r MarketRequest
	if readJSON(s, &r) != nil {
		return
	}
	v, e := m.Request(s.Conn().RemotePeer(), r)
	if e != nil {
		writeJSON(s, map[string]any{"error": e.Error()})
		return
	}
	writeJSON(s, sign(v, m.worker.identity()))
}
func (m *Market) Request(remote peer.ID, r MarketRequest) (map[string]any, error) {
	var proof EmailProof
	if r.Proof.verify(m.worker.n.Auth.Key, &proof) != nil || proof.Peer != remote.String() || len(proof.EmailHash) != 64 || proof.Expires < time.Now().Unix() || proof.Expires > time.Now().Add(16*time.Minute).Unix() {
		return nil, errors.New("verify your email before contributing")
	}
	// All mutation lock order is worker then market, including catalog reconciliation.
	m.worker.mu.Lock()
	defer m.worker.mu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	member := m.member(proof.EmailHash)
	before := pack(m.data)
	rollback := func(e error) (map[string]any, error) { json.Unmarshal(before, &m.data); return nil, e }
	switch r.Action {
	case "profile":
	case "services/pause":
		reg, ok := m.data.Registrations[r.Agent.ID]
		if !ok || reg.Owner != member {
			return nil, errors.New("service belongs to another contributor")
		}
		reg.Paused = true
		m.data.Registrations[r.Agent.ID] = reg
		if e := m.save(); e != nil {
			return rollback(e)
		}
	case "agents/create":
		a := r.Agent
		a.Name = strings.TrimSpace(a.Name)
		a.Description = strings.TrimSpace(a.Description)
		if len(a.Name) < 1 || len(a.Name) > 80 || len(a.Description) < 1 || len(a.Description) > 300 || len(a.Instructions) < 1 || len(a.Instructions) > 2000 || (a.Skill != "plan" && a.Skill != "write" && a.Skill != "numbers") {
			return nil, errors.New("choose a name, description, instructions and supported skill")
		}
		count := 0
		for _, reg := range m.data.Registrations {
			if reg.Email == proof.EmailHash {
				count++
			}
		}
		if count >= 3 || len(m.data.Registrations) >= 80 {
			return nil, errors.New("testnet publishing limit reached")
		}
		a.ID = "agent-" + token()
		a.Created = time.Now().UnixMilli()
		a.Active = true
		a.Runtime = "radops-hosted"
		m.data.Agents[a.ID] = a
		m.data.Registrations[a.ID] = MarketRegistration{Owner: member, Email: proof.EmailHash, Peer: m.worker.n.Host.ID().String()}
		m.join(member)
		if e := m.save(); e != nil {
			return rollback(e)
		}
		m.worker.opts.Offers = append(m.worker.opts.Offers, m.offer(a))
	case "agents/toggle":
		reg, ok := m.data.Registrations[r.Agent.ID]
		if !ok || reg.Owner != member {
			return nil, errors.New("agent belongs to another contributor")
		}
		a, ok := m.data.Agents[r.Agent.ID]
		if !ok {
			return nil, errors.New("pause a desktop agent from its own device")
		}
		a.Active = r.Agent.Active
		m.data.Agents[a.ID] = a
		if e := m.save(); e != nil {
			return rollback(e)
		}
		m.worker.disabled[a.ID] = !a.Active
	case "services/register":
		card, e := VerifyServiceCard(r.Card)
		if e != nil {
			return nil, e
		}
		key, e := publicKey(remote.String())
		if e != nil {
			return nil, e
		}
		var binding ServiceBinding
		if r.Binding.verify(key, &binding) != nil || binding.Domain != "radchat-service-owner-v1" || binding.Worker != card.Peer || binding.Service != card.ID || binding.Owner != remote.String() || binding.Expires < time.Now().Unix() || binding.Expires > time.Now().Add(2*time.Minute).Unix() {
			return nil, errors.New("invalid service ownership binding")
		}
		workerKey, e := publicKey(card.Peer)
		if e != nil {
			return nil, e
		}
		var workerBinding ServiceBinding
		if r.WorkerBinding.verify(workerKey, &workerBinding) != nil || !bytes.Equal(r.Binding.Payload, r.WorkerBinding.Payload) {
			return nil, errors.New("worker must co-sign its service owner")
		}
		if !strings.HasPrefix(card.ID, "agent-") || len(card.ID) > 80 || len(card.Name) > 80 || len(card.Description) > 300 || card.Peer == m.worker.n.Host.ID().String() {
			return nil, errors.New("invalid contributor service")
		}
		old, exists := m.data.Registrations[card.ID]
		if exists && (old.Owner != member || old.Peer != card.Peer) {
			return nil, errors.New("service identity already bound")
		}
		if !exists {
			count := 0
			for _, reg := range m.data.Registrations {
				if reg.Email == proof.EmailHash {
					count++
				}
			}
			if count >= 3 || len(m.data.Registrations) >= 80 {
				return nil, errors.New("publishing limit reached")
			}
		}
		m.data.Registrations[card.ID] = MarketRegistration{Owner: member, Email: proof.EmailHash, Peer: card.Peer, Card: r.Card}
		m.join(member)
		if e = m.save(); e != nil {
			return rollback(e)
		}
	case "tasks/attest":
		var c CompletionEvidence
		if json.Unmarshal(r.Completion.Payload, &c) != nil {
			return nil, errors.New("invalid completion")
		}
		key, e := publicKey(c.Worker)
		if e != nil {
			return nil, e
		}
		if r.Completion.verify(key, &c) != nil || c.Domain != "radchat-test-completion-v1" || c.Buyer != remote.String() || c.Completed < time.Now().Add(-24*time.Hour).UnixMilli() || c.Completed > time.Now().Add(time.Minute).UnixMilli() || len(c.ID) != 64 || len(c.Digest) != 64 {
			return nil, errors.New("unverified completion")
		}
		buyerKey, e := publicKey(remote.String())
		if e != nil {
			return nil, e
		}
		var f TaskFeedback
		if r.Feedback.verify(buyerKey, &f) != nil || !f.Accepted || f.TaskID != c.ID || f.Worker != c.Worker {
			return nil, errors.New("buyer acceptance required")
		}
		reg, ok := m.data.Registrations[c.Service]
		if !ok || reg.Peer != c.Worker {
			return nil, errors.New("service is not a registered contribution")
		}
		if reg.Email == proof.EmailHash {
			return nil, errors.New("self-review does not earn contribution credits")
		}
		m.join(member)
		m.join(reg.Owner)
		day := time.UnixMilli(c.Completed).UTC().Format("2006-01-02")
		h := sha256.Sum256(pack([]string{proof.EmailHash, reg.Owner, c.Service, day}))
		id := "work:" + hex.EncodeToString(h[:])
		if _, exists := m.data.Events[id]; !exists {
			var daily, total uint64
			for _, ev := range m.data.Events {
				total += ev.Points
				if ev.Member == reg.Owner && ev.Kind == "agent-work" && time.UnixMilli(ev.Created).UTC().Format("2006-01-02") == day {
					daily += ev.Points
				}
			}
			if daily+10 <= 100 && total+10 <= TestCreditBudget {
				m.data.Events[id] = CreditEvent{ID: id, Member: reg.Owner, Kind: "agent-work", Points: 10, Created: c.Completed, Evidence: r.Completion, Acceptance: r.Feedback}
			}
		}
		if e = m.save(); e != nil {
			return rollback(e)
		}
	default:
		return nil, errors.New("unsupported marketplace action")
	}
	return m.profile(member), nil
}
func (m *Market) join(member string) {
	id := "join:" + member
	if _, ok := m.data.Events[id]; ok {
		return
	}
	var total uint64
	for _, e := range m.data.Events {
		total += e.Points
	}
	if total < TestCreditBudget {
		m.data.Events[id] = CreditEvent{ID: id, Member: member, Kind: "join", Points: 1, Created: time.Now().UnixMilli()}
	}
}
func (m *Market) profile(member string) map[string]any {
	agents := []AgentDefinition{}
	for id, r := range m.data.Registrations {
		if r.Owner != member {
			continue
		}
		if a, ok := m.data.Agents[id]; ok {
			agents = append(agents, a)
		} else {
			var c ServiceCard
			json.Unmarshal(r.Card.Payload, &c)
			agents = append(agents, AgentDefinition{ID: id, Name: c.Name, Description: c.Description, Skill: c.Skill, Runtime: "desktop-byom", Active: c.Expires > time.Now().Unix() && !r.Paused})
		}
	}
	events := []CreditEvent{}
	var balance uint64
	for _, e := range m.data.Events {
		if e.Member == member {
			balance += e.Points
			events = append(events, e)
		}
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Created > events[j].Created })
	sort.Slice(agents, func(i, j int) bool { return agents[i].ID < agents[j].ID })
	return map[string]any{"member": member, "balance": balance, "events": events, "agents": agents, "program": "radchat-testnet-1", "testnet": true, "transferable": false, "joinRate": 1, "workRate": 10, "dailyWorkCap": 100, "epochBudget": TestCreditBudget, "verification": "operator-verified; email-scoped; no proof of unique humans", "computeCreditsEnabled": false, "byomCreditsEnabled": false}
}
func (m *Market) Catalog() []Signed {
	out := m.worker.Catalog()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.data.Registrations {
		if len(r.Card.Payload) > 0 && !r.Paused {
			if _, e := VerifyServiceCard(r.Card); e == nil {
				out = append(out, r.Card)
			}
		}
	}
	return out
}
func (m *Market) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		switch r.URL.Path {
		case "/api/services":
			respond(w, 200, map[string]any{"services": m.Catalog(), "preview": true})
		case "/api/market":
			respond(w, 200, map[string]any{"peer": m.worker.n.Host.ID().String(), "addresses": m.worker.n.Addresses(), "protocol": string(MarketProtocol), "program": "radchat-testnet-1"})
		default:
			m.worker.Handler().ServeHTTP(w, r)
		}
	})
}

func (n *Node) MarketCall(ctx context.Context, r MarketRequest) (Signed, error) {
	var endpoint struct {
		Peer      string   `json:"peer"`
		Addresses []string `json:"addresses"`
		Protocol  string   `json:"protocol"`
	}
	if e := n.Auth.call(ctx, "GET", "/api/market", nil, &endpoint); e != nil {
		return Signed{}, e
	}
	if endpoint.Protocol != string(MarketProtocol) || len(endpoint.Addresses) > 16 {
		return Signed{}, errors.New("invalid marketplace endpoint")
	}
	n.mu.Lock()
	r.Proof = n.proof
	n.mu.Unlock()
	id, e := peer.Decode(endpoint.Peer)
	if e != nil {
		return Signed{}, e
	}
	var stream network.Stream
	if len(n.Host.Network().ConnsToPeer(id)) > 0 {
		stream, _ = n.Host.NewStream(network.WithAllowLimitedConn(ctx, "market"), id, MarketProtocol)
	}
	sort.SliceStable(endpoint.Addresses, func(i, j int) bool {
		return strings.Contains(endpoint.Addresses[i], "/p2p-circuit") && !strings.Contains(endpoint.Addresses[j], "/p2p-circuit")
	})
	for _, addr := range endpoint.Addresses {
		if stream != nil {
			break
		}
		info, e := addrInfo(addr)
		if e != nil || info.ID != id {
			continue
		}
		dial, cancel := context.WithTimeout(ctx, 4*time.Second)
		e = n.Host.Connect(dial, *info)
		if e == nil {
			stream, e = n.Host.NewStream(network.WithAllowLimitedConn(dial, "market"), id, MarketProtocol)
		}
		cancel()
		if stream != nil {
			break
		}
	}
	if stream == nil {
		return Signed{}, errors.New("market coordinator unavailable")
	}
	defer stream.Close()
	stream.SetDeadline(time.Now().Add(15 * time.Second))
	if e = writeJSON(stream, r); e != nil {
		return Signed{}, e
	}
	var out struct {
		Signed
		Error string `json:"error"`
	}
	if e = readJSON(stream, &out); e != nil {
		return Signed{}, e
	}
	if out.Error != "" {
		return Signed{}, errors.New(out.Error)
	}
	key, e := publicKey(endpoint.Peer)
	if e != nil {
		return Signed{}, e
	}
	var value map[string]any
	if e = out.Signed.verify(key, &value); e != nil {
		return Signed{}, e
	}
	return out.Signed, nil
}
