package radchat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Runner is a bounded model-only loop. It cannot execute code, read files, or use accounts.
// The provider key lives only in memory and never enters org messages or recovery exports.
type Runner struct {
	mu         sync.Mutex
	state      RunnerState
	cancel     context.CancelFunc
	generation uint64
	pending    string
	channel    string
}
type RunnerState struct {
	Phase    string `json:"phase"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Channel  string `json:"channel"`
	Calls    int    `json:"calls"`
	Limit    int    `json:"limit"`
	Pending  string `json:"pending,omitempty"`
	Error    string `json:"error,omitempty"`
	Updated  int64  `json:"updated"`
}
type RunnerConfig struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	APIKey   string `json:"apiKey"`
	Prompt   string `json:"prompt"`
	Channel  string `json:"channel"`
	Interval int    `json:"interval"`
	Limit    int    `json:"limit"`
	Consent  bool   `json:"consent"`
}

func (r *Runner) Snapshot() RunnerState {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.state
	if s.Phase == "" {
		s.Phase = "not-configured"
	}
	return s
}
func (r *Runner) stop(phase string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	r.generation++
	r.pending = ""
	r.state.Pending = ""
	r.state.Phase = phase
	r.state.Updated = time.Now().UnixMilli()
}
func (n *Node) StartRunner(c RunnerConfig) error {
	n.mu.Lock()
	agent := n.vault.Kind == "agent" && n.vault.Org != nil && n.activeLocked(n.Host.ID().String())
	n.mu.Unlock()
	if !agent {
		return errors.New("join an organization with an agent invitation before launching a runner")
	}
	if !c.Consent {
		return errors.New("approve sending this prompt to your selected model provider")
	}
	if c.Provider != "openai" && c.Provider != "anthropic" {
		return errors.New("choose OpenAI or Anthropic")
	}
	if strings.TrimSpace(c.APIKey) == "" || len(c.APIKey) > 512 || strings.ContainsAny(c.APIKey, "\r\n") || strings.TrimSpace(c.Model) == "" || len(c.Model) > 100 || len(c.Prompt) < 1 || len(c.Prompt) > 4000 || c.Interval < 30 || c.Interval > 86400 || c.Limit < 1 || c.Limit > 100 {
		return errors.New("invalid configuration: interval 30–86400 seconds, 1–100 calls, and a prompt are required")
	}
	o := n.snapshotOrg()
	p, err := verifyPolicy(o, o.Policy)
	if err != nil {
		return err
	}
	found := false
	for _, ch := range p.Channels {
		found = found || ch.Name == c.Channel
	}
	if !found {
		return errors.New("choose an existing channel")
	}
	r := &n.runner
	r.stop("setting-up")
	r.mu.Lock()
	ctx, cancel := context.WithCancel(n.ctx)
	r.cancel = cancel
	gen := r.generation
	r.channel = c.Channel
	r.state = RunnerState{Phase: "listening", Provider: c.Provider, Model: c.Model, Channel: c.Channel, Limit: c.Limit, Updated: time.Now().UnixMilli()}
	r.mu.Unlock()
	n.background(func() { n.runModelLoop(ctx, gen, c, modelCompletion) })
	return nil
}

type completionFunc func(context.Context, RunnerConfig) (string, error)

func (n *Node) runModelLoop(ctx context.Context, gen uint64, c RunnerConfig, complete completionFunc) {
	r := &n.runner
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	next := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		r.mu.Lock()
		if r.generation != gen {
			r.mu.Unlock()
			return
		}
		ready := r.state.Phase == "listening" && time.Now().After(next)
		if ready {
			r.state.Phase = "working"
			r.state.Calls++
			r.state.Updated = time.Now().UnixMilli()
		}
		r.mu.Unlock()
		if !ready {
			continue
		}
		n.mu.Lock()
		active := n.activeLocked(n.Host.ID().String())
		n.mu.Unlock()
		if !active {
			r.mu.Lock()
			if r.generation == gen {
				r.state.Phase = "failed"
				r.state.Error = "Organization authorization expired or access was deactivated"
			}
			r.mu.Unlock()
			return
		}
		requestCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		text, err := complete(requestCtx, c)
		cancel()
		r.mu.Lock()
		if r.generation != gen {
			r.mu.Unlock()
			return
		}
		r.state.Updated = time.Now().UnixMilli()
		if err != nil {
			r.state.Phase = "failed"
			r.state.Error = err.Error()
			r.mu.Unlock()
			return
		}
		r.pending = text
		r.state.Pending = text
		r.state.Phase = "awaiting-approval"
		r.mu.Unlock()
		next = time.Now().Add(time.Duration(c.Interval) * time.Second)
	}
}
func (n *Node) ApproveRunner(ctx context.Context) error {
	r := &n.runner
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state.Phase != "awaiting-approval" || r.pending == "" {
		return errors.New("no output is awaiting approval")
	}
	if _, _, err := n.Send(ctx, r.channel, r.pending, ""); err != nil {
		return err
	}
	r.pending = ""
	r.state.Pending = ""
	r.state.Updated = time.Now().UnixMilli()
	if r.state.Calls >= r.state.Limit {
		r.state.Phase = "completed"
		if r.cancel != nil {
			r.cancel()
			r.cancel = nil
		}
	} else {
		r.state.Phase = "listening"
	}
	return nil
}
func modelCompletion(ctx context.Context, c RunnerConfig) (string, error) {
	endpoint := "https://api.openai.com/v1/chat/completions"
	payload := map[string]any{"model": c.Model, "max_completion_tokens": 512, "messages": []map[string]string{{"role": "user", "content": c.Prompt}}}
	if c.Provider == "anthropic" {
		endpoint = "https://api.anthropic.com/v1/messages"
		delete(payload, "max_completion_tokens")
		payload["max_tokens"] = 512
	}
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(pack(payload)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Provider == "anthropic" {
		req.Header.Set("x-api-key", c.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	client := &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("provider redirects are disabled") }}
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("model provider could not be reached")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("model provider returned HTTP %d; check your model, credential and quota", resp.StatusCode)
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result) != nil {
		return "", errors.New("invalid model response")
	}
	text := ""
	if len(result.Choices) > 0 {
		text = result.Choices[0].Message.Content
	} else {
		for _, part := range result.Content {
			if part.Type == "text" {
				text += part.Text
			}
		}
	}
	if strings.TrimSpace(text) == "" || len(text) > 4000 {
		return "", errors.New("model output was empty or exceeded the 4000-byte chat limit")
	}
	return text, nil
}
