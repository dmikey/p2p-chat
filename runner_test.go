package radchat

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeContributorIdentityAndKeyBoundary(t *testing.T) {
	_, auth := authorityTest(t)
	owner := testNode(t, auth.URL, "human", "")
	verifyTestEmail(t, owner, "contributor@example.com", "Contributor")
	if err := owner.CreateOrg("Studio"); err != nil {
		t.Fatal(err)
	}
	c := RunnerConfig{Provider: "openai", Model: "fixture-model", APIKey: "fixture-key-never-persist", Prompt: "Explain a plan", Channel: "general", Interval: 30, Limit: 1, Consent: true}
	if err := owner.LaunchContributor(context.Background(), "Native agent", c); err != nil {
		t.Fatal(err)
	}
	agent := owner.runnerNode()
	agent.runner.stop("paused")
	if agent == owner || agent.Host.ID() == owner.Host.ID() {
		t.Fatal("agent must have an independent identity")
	}
	var cert Certificate
	if json.Unmarshal(agent.snapshotOrg().Member.Payload, &cert) != nil || cert.Kind != "agent" {
		t.Fatal("agent must have owner-signed agent membership")
	}
	if len(agent.snapshotOrg().RootPrivate) != 0 {
		t.Fatal("agent must not inherit org owner private key")
	}
	data, err := os.ReadFile(filepath.Join(agent.cfg.Dir, "vault.enc"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), c.APIKey) {
		t.Fatal("key leaked into vault")
	}
	agent.mu.Lock()
	exported := string(pack(agent.vault))
	agent.mu.Unlock()
	if strings.Contains(exported, c.APIKey) {
		t.Fatal("provider key entered recoverable device state")
	}
}
func TestRunnerApprovalAndRevocation(t *testing.T) {
	_, auth := authorityTest(t)
	owner := testNode(t, auth.URL, "human", "")
	verifyTestEmail(t, owner, "owner@example.com", "Owner")
	if err := owner.CreateOrg("Team"); err != nil {
		t.Fatal(err)
	}
	agent := testNode(t, auth.URL, "agent", "")
	verifyTestEmail(t, agent, "agent@example.com", "Agent")
	inv, _ := owner.Invite("agent@example.com", "agent")
	if err := agent.Join(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
	r := &agent.runner
	ctx, cancel := context.WithCancel(agent.ctx)
	defer cancel()
	r.mu.Lock()
	r.state = RunnerState{Phase: "listening", Limit: 1}
	r.channel = "general"
	r.mu.Unlock()
	go agent.runModelLoop(ctx, 0, RunnerConfig{Interval: 30}, func(context.Context, RunnerConfig) (string, error) { return "Review before publishing", nil })
	eventually(t, func() bool { return r.Snapshot().Phase == "awaiting-approval" })
	if hasText(agent, "Review before publishing") {
		t.Fatal("loop published without approval")
	}
	if err := agent.ApproveRunner(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !hasText(agent, "Review before publishing") || r.Snapshot().Phase != "completed" {
		t.Fatal("approved output did not publish within call limit")
	}
	r.mu.Lock()
	r.state.Phase = "awaiting-approval"
	r.pending = "Must not publish"
	r.mu.Unlock()
	agent.mu.Lock()
	var policy Policy
	json.Unmarshal(agent.vault.Org.Policy.Payload, &policy)
	policy.Deactivated[agent.Host.ID().String()] = 1
	agent.vault.Org.Policy = sign(policy, owner.snapshotOrg().RootPrivate)
	agent.mu.Unlock()
	if err := agent.ApproveRunner(context.Background()); err == nil {
		t.Fatal("deactivated agent could publish")
	}
}
