package radchat

import (
	"crypto/ed25519"
	"crypto/rand"
	"reflect"
	"testing"
)

func TestContributionCreditsRequireReviewedEvidence(t *testing.T) {
	pubA, keyA, _ := ed25519.GenerateKey(rand.Reader)
	pubB, keyB, _ := ed25519.GenerateKey(rand.Reader)
	p := ContributionPolicy{Epoch: "trial-1", Starts: 100, Ends: 200, Rates: map[string]uint64{"join": 1, "agent-work": 10, "sandbox-compute": 2, "byom": 3}, Budget: 1000, PerMemberCap: 100, Quorum: 2, Verifiers: map[string]ed25519.PublicKey{"a": pubA, "b": pubB}}
	receipt := func(kind, resource string, units uint64) ContributionReceipt {
		e := ContributionEvidence{Domain: "radchat-contribution-v1", Policy: p.Digest(), Member: "member-1", Kind: kind, Resource: resource, Units: units, Observed: 150}
		return ContributionReceipt{Evidence: e, Signatures: map[string][]byte{"a": ed25519.Sign(keyA, pack(e)), "b": ed25519.Sign(keyB, pack(e))}}
	}
	batch := []ContributionReceipt{receipt("join", "admission-1", 1), receipt("agent-work", "task-1", 1), receipt("sandbox-compute", "usage-1", 4), receipt("byom", "provider-1", 2)}
	balances, err := CalculateContributionCredits(p, batch, nil)
	if err != nil || balances["member-1"] != 25 {
		t.Fatalf("valid reviewed work: %v %v", balances, err)
	}
	reversed := []ContributionReceipt{batch[3], batch[2], batch[1], batch[0]}
	b, err := CalculateContributionCredits(p, reversed, nil)
	if err != nil || !reflect.DeepEqual(balances, b) {
		t.Fatal("batch order changed allocation")
	}
	for _, name := range []string{"forged-units", "one-reviewer", "duplicate-task", "repeat-join", "prior-join", "budget", "cap", "wrong-policy", "duplicate-verifier-key", "wrong-domain", "expired", "unknown-kind"} {
		t.Run(name, func(t *testing.T) {
			policy := p
			r := receipt("agent-work", "task-2", 1)
			receipts := []ContributionReceipt{r}
			var prior map[string]bool
			switch name {
			case "forged-units":
				receipts[0].Evidence.Units++
			case "one-reviewer":
				delete(r.Signatures, "b")
			case "duplicate-task":
				other := receipt("agent-work", "task-2", 1)
				other.Evidence.Member = "member-2"
				other.Signatures = map[string][]byte{"a": ed25519.Sign(keyA, pack(other.Evidence)), "b": ed25519.Sign(keyB, pack(other.Evidence))}
				receipts = append(receipts, other)
			case "repeat-join":
				receipts = []ContributionReceipt{receipt("join", "a", 1), receipt("join", "b", 1)}
			case "prior-join":
				receipts = []ContributionReceipt{receipt("join", "a", 1)}
				prior = map[string]bool{"member-1": true}
			case "budget":
				policy.Budget = 100
				x, y := receipt("agent-work", "task-2", 6), receipt("agent-work", "task-3", 6)
				x.Evidence.Policy, y.Evidence.Policy = policy.Digest(), policy.Digest()
				y.Evidence.Member = "member-2"
				for _, v := range []*ContributionReceipt{&x, &y} {
					v.Signatures = map[string][]byte{"a": ed25519.Sign(keyA, pack(v.Evidence)), "b": ed25519.Sign(keyB, pack(v.Evidence))}
				}
				receipts = []ContributionReceipt{x, y}
			case "cap":
				receipts = []ContributionReceipt{receipt("agent-work", "task-2", 11)}
			case "wrong-policy":
				policy.Epoch = "trial-2"
			case "duplicate-verifier-key":
				policy.Verifiers = map[string]ed25519.PublicKey{"a": pubA, "b": pubA}
			case "wrong-domain":
				receipts[0].Evidence.Domain = "task-result"
			case "expired":
				receipts[0].Evidence.Observed = 200
			case "unknown-kind":
				receipts = []ContributionReceipt{receipt("idle-uptime", "host-1", 1)}
			}
			if name == "expired" || name == "wrong-domain" {
				e := receipts[0].Evidence
				receipts[0].Signatures = map[string][]byte{"a": ed25519.Sign(keyA, pack(e)), "b": ed25519.Sign(keyB, pack(e))}
			}
			if result, err := CalculateContributionCredits(policy, receipts, prior); err == nil || result != nil {
				t.Fatalf("invalid batch awarded credits: %v %v", result, err)
			}
		})
	}
}
