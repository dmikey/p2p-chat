package radchat

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"net/http"
	"sort"
	"strings"
	"time"
)

type ServiceClientRequest struct {
	Service   string `json:"service"`
	Action    string `json:"action"`
	MessageID string `json:"messageId"`
	TaskID    string `json:"taskId"`
	Prompt    string `json:"prompt"`
	Consent   bool   `json:"consent"`
	Accepted  bool   `json:"accepted"`
}

func VerifyServiceCard(s Signed) (ServiceCard, error) {
	var c ServiceCard
	if json.Unmarshal(s.Payload, &c) != nil {
		return c, errors.New("invalid service card")
	}
	id, e := peer.Decode(c.Peer)
	if e != nil {
		return c, e
	}
	key, e := id.ExtractPublicKey()
	if e != nil {
		return c, e
	}
	raw, e := key.Raw()
	if e != nil {
		return c, e
	}
	if s.verify(raw, &c) != nil || c.Expires < time.Now().Unix() || c.Expires > time.Now().Add(2*time.Minute).Unix() || c.Protocol != string(ServiceProtocol) || c.ID == "" || len(c.Addresses) > 16 {
		return c, errors.New("expired or unverified service")
	}
	return c, nil
}
func (n *Node) ServiceCatalog(ctx context.Context) ([]Signed, error) {
	var out struct {
		Services []Signed `json:"services"`
	}
	if e := n.Auth.call(ctx, "GET", "/api/services", nil, &out); e != nil {
		return nil, e
	}
	if len(out.Services) > 100 {
		return nil, errors.New("too many services")
	}
	for _, s := range out.Services {
		if _, e := VerifyServiceCard(s); e != nil {
			return nil, e
		}
	}
	return out.Services, nil
}
func (n *Node) ServiceCall(ctx context.Context, p ServiceClientRequest) (Signed, error) {
	cards, e := n.ServiceCatalog(ctx)
	if e != nil {
		return Signed{}, e
	}
	var card ServiceCard
	for _, s := range cards {
		c, _ := VerifyServiceCard(s)
		if c.ID == p.Service {
			card = c
			break
		}
	}
	if card.ID == "" {
		return Signed{}, errors.New("service unavailable")
	}
	n.mu.Lock()
	proof := n.proof
	n.mu.Unlock()
	var email EmailProof
	if proof.verify(n.Auth.Key, &email) != nil || email.Expires < time.Now().Unix() {
		return Signed{}, errors.New("verify your email again before using an agent")
	}
	req := ServiceRequest{JSONRPC: "2.0", ID: token(), Method: p.Action, Proof: proof}
	req.Params.Service = p.Service
	req.Params.TaskID = p.TaskID
	req.Params.Consent = p.Consent
	req.Params.Accepted = p.Accepted
	if p.Action == "tasks/feedback" {
		n.mu.Lock()
		key, _ := identityKey(n.vault)
		raw, _ := key.Raw()
		n.mu.Unlock()
		req.Feedback = sign(TaskFeedback{p.TaskID, card.Peer, p.Accepted}, ed25519.PrivateKey(raw))
	}
	req.Params.Message.MessageID = p.MessageID
	req.Params.Message.Role = "user"
	req.Params.Message.Parts = append(req.Params.Message.Parts, struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}{"text", p.Prompt})
	id, _ := peer.Decode(card.Peer)
	var stream network.Stream
	if len(n.Host.Network().ConnsToPeer(id)) > 0 {
		stream, _ = n.Host.NewStream(network.WithAllowLimitedConn(ctx, "service"), id, ServiceProtocol)
	}
	// Contributor containers commonly advertise private interfaces before their
	// relay route. Try the circuit first and bound each alternative dial.
	sort.SliceStable(card.Addresses, func(i, j int) bool {
		return strings.Contains(card.Addresses[i], "/p2p-circuit") && !strings.Contains(card.Addresses[j], "/p2p-circuit")
	})
	for _, addr := range card.Addresses {
		if stream != nil {
			break
		}
		info, err := addrInfo(addr)
		if err != nil || info.ID != id {
			continue
		}
		dialCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		if n.Host.Connect(dialCtx, *info) == nil {
			stream, e = n.Host.NewStream(network.WithAllowLimitedConn(ctx, "service"), id, ServiceProtocol)
		}
		cancel()
	}
	if stream == nil {
		return Signed{}, errors.New("agent could not be reached")
	}
	defer stream.Close()
	stream.SetDeadline(time.Now().Add(10 * time.Second))
	if e = writeJSON(stream, req); e != nil {
		return Signed{}, e
	}
	var res struct {
		Result struct {
			Metadata map[string]Signed `json:"metadata"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if e = readJSON(stream, &res); e != nil {
		return Signed{}, e
	}
	if res.Error != nil {
		return Signed{}, errors.New(res.Error.Message)
	}
	key, _ := id.ExtractPublicKey()
	raw, _ := key.Raw()
	receipt := res.Result.Metadata["radchat/receipt"]
	var view TaskView
	if receipt.verify(raw, &view) != nil || view.Worker != card.Peer || view.Buyer != n.Host.ID().String() || view.Service != card.ID || (p.TaskID != "" && p.TaskID != view.ID) {
		return Signed{}, errors.New("invalid task receipt")
	}
	if p.Action == "tasks/feedback" && p.Accepted && strings.HasPrefix(card.ID, "agent-") {
		if _, e := n.MarketCall(ctx, MarketRequest{Action: "tasks/attest", Completion: view.Completion, Feedback: req.Feedback}); e != nil {
			return Signed{}, e
		}
	}
	return receipt, nil
}
func (n *Node) serviceHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		cards, e := n.ServiceCatalog(r.Context())
		if e != nil {
			respond(w, 503, map[string]string{"error": e.Error()})
			return
		}
		respond(w, 200, map[string]any{"services": cards, "preview": true})
		return
	}
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	var p ServiceClientRequest
	if bodyJSON(w, r, &p) != nil {
		respond(w, 400, map[string]string{"error": "invalid service request"})
		return
	}
	result, e := n.ServiceCall(r.Context(), p)
	if e != nil {
		respond(w, 400, map[string]string{"error": e.Error()})
		return
	}
	respond(w, 200, result)
}
