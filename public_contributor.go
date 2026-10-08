package radchat

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync/atomic"
	"time"
)

type PublicAgentConfig struct {
	Agent    AgentDefinition `json:"agent"`
	Provider string          `json:"provider"`
	Model    string          `json:"model"`
	APIKey   string          `json:"apiKey"`
	Calls    int             `json:"calls"`
	Consent  bool            `json:"consent"`
}

func (n *Node) PublishPublicAgent(ctx context.Context, c PublicAgentConfig) error {
	if !c.Consent || c.APIKey == "" || len(c.APIKey) > 512 || c.Model == "" || len(c.Model) > 100 || c.Calls < 1 || c.Calls > 100 || (c.Provider != "openai" && c.Provider != "anthropic" && c.Provider != "harness") || len(c.Agent.Name) < 1 || len(c.Agent.Name) > 80 || len(c.Agent.Description) < 1 || len(c.Agent.Description) > 300 || len(c.Agent.Instructions) < 1 || len(c.Agent.Instructions) > 2000 {
		return errors.New("approve bounded model access and configure your agent")
	}
	n.contributorMu.Lock()
	defer n.contributorMu.Unlock()
	n.mu.Lock()
	var proof EmailProof
	valid := n.proof.verify(n.Auth.Key, &proof) == nil && proof.Expires > time.Now().Unix()
	n.mu.Unlock()
	if !valid {
		return errors.New("verify email before publishing")
	}
	if n.publicNode != nil {
		return errors.New("pause your current public agent before starting another")
	}
	child, e := NewNode(n.ctx, Config{Dir: filepath.Join(n.cfg.Dir, "public-agent"), Listen: "/ip4/0.0.0.0/tcp/0", Bootstrap: n.cfg.Bootstrap, AuthURL: n.cfg.AuthURL, Kind: "agent"})
	if e != nil {
		return e
	}
	id := "agent-" + child.Host.ID().String()
	offer := ServiceOffer{ID: id, Name: c.Agent.Name, Description: c.Agent.Description, Skill: c.Agent.Skill, Version: "1.0.0", Permissions: []string{"Selected task only", c.Provider + " model processing on contributor device", "No files or accounts"}}
	var calls atomic.Int32
	worker, e := child.StartServiceWorker(ServiceOptions{Offers: []ServiceOffer{offer}, Slots: 1, DailyPerBuyer: 3, DailyTotal: c.Calls, Execute: func(ctx context.Context, o ServiceOffer, prompt string) (string, error) {
		if calls.Add(1) > int32(c.Calls) {
			return "", errors.New("model call limit reached")
		}
		return modelCompletion(ctx, RunnerConfig{Provider: c.Provider, Model: c.Model, APIKey: c.APIKey, Prompt: c.Agent.Instructions + "\n\nTask:\n" + prompt})
	}})
	if e != nil {
		child.Close()
		return e
	}
	// The worker proves its service identity; the email-verified owner binds it.
	register := func(ctx context.Context) error {
		cards := worker.Catalog()
		if len(cards) == 0 {
			return errors.New("worker unavailable")
		}
		n.mu.Lock()
		key, _ := identityKey(n.vault)
		raw, _ := key.Raw()
		n.mu.Unlock()
		binding := sign(ServiceBinding{Domain: "radchat-service-owner-v1", Service: id, Worker: child.Host.ID().String(), Owner: n.Host.ID().String(), Expires: time.Now().Add(90 * time.Second).Unix()}, ed25519.PrivateKey(raw))
		var details ServiceBinding
		json.Unmarshal(binding.Payload, &details)
		_, e := n.MarketCall(ctx, MarketRequest{Action: "services/register", Card: cards[0], Binding: binding, WorkerBinding: sign(details, worker.identity())})
		return e
	}
	if e = register(ctx); e != nil {
		child.Close()
		return e
	}
	n.publicNode = child
	n.publicWorker = worker
	child.background(func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-child.ctx.Done():
				return
			case <-tick.C:
				register(child.ctx)
			}
		}
	})
	return nil
}
func (n *Node) StopPublicAgent() {
	n.contributorMu.Lock()
	defer n.contributorMu.Unlock()
	if n.publicNode != nil {
		id := "agent-" + n.publicNode.Host.ID().String()
		n.publicNode.Close()
		n.publicNode = nil
		n.publicWorker = nil
		n.MarketCall(n.ctx, MarketRequest{Action: "services/pause", Agent: AgentDefinition{ID: id}})
	}
}
func (n *Node) publicProfile() map[string]any {
	n.contributorMu.Lock()
	defer n.contributorMu.Unlock()
	if n.publicNode == nil {
		return map[string]any{"active": false}
	}
	return map[string]any{"active": true, "peer": n.publicNode.Host.ID().String()}
}
