package radchat

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
)

// ContributionPolicy is frozen before an epoch starts. Credits have no monetary
// value; this calculator neither creates a token nor promises a token allocation.
type ContributionPolicy struct {
	Epoch        string                       `json:"epoch"`
	Starts       int64                        `json:"starts"`
	Ends         int64                        `json:"ends"`
	Rates        map[string]uint64            `json:"rates"`
	Budget       uint64                       `json:"budget"`
	PerMemberCap uint64                       `json:"perMemberCap"`
	Quorum       int                          `json:"quorum"`
	Verifiers    map[string]ed25519.PublicKey `json:"verifiers"`
}

func (p ContributionPolicy) Digest() string {
	h := sha256.Sum256(pack(p))
	return hex.EncodeToString(h[:])
}

// ContributionEvidence contains references only: never prompts, emails, API
// credentials, account tokens or provider payloads. Member is an opaque stable
// participant ID assigned by admission review, not a peer ID or email hash.
type ContributionEvidence struct {
	Domain   string `json:"domain"`
	Policy   string `json:"policy"`
	Member   string `json:"member"`
	Kind     string `json:"kind"`
	Resource string `json:"resource"`
	Units    uint64 `json:"units"`
	Observed int64  `json:"observed"`
}

type ContributionReceipt struct {
	Evidence   ContributionEvidence `json:"evidence"`
	Signatures map[string][]byte    `json:"signatures"`
}

// CalculateContributionCredits verifies a complete epoch batch. Independent
// trusted reviewers must attest eligibility and measurement before signing.
// priorJoinMembers MUST come from the accepted lifetime join registry. Callers
// must atomically persist the full epoch and registry; this pure calculator is
// not an issuance service. An invalid or over-budget batch produces no credits.
func CalculateContributionCredits(p ContributionPolicy, receipts []ContributionReceipt, priorJoinMembers map[string]bool) (map[string]uint64, error) {
	bad := errors.New("invalid contribution policy or evidence")
	if p.Epoch == "" || p.Starts <= 0 || p.Ends <= p.Starts || p.Budget == 0 || p.Budget > math.MaxInt64 || p.PerMemberCap == 0 || p.PerMemberCap > p.Budget || p.Quorum < 2 || p.Quorum > len(p.Verifiers) || len(receipts) > 100000 {
		return nil, bad
	}
	keys := map[string]bool{}
	for id, key := range p.Verifiers {
		if id == "" || len(key) != ed25519.PublicKeySize || keys[string(key)] {
			return nil, bad
		}
		keys[string(key)] = true
	}
	for _, kind := range []string{"join", "agent-work", "sandbox-compute", "byom"} {
		if p.Rates[kind] == 0 || p.Rates[kind] > 1000000 {
			return nil, bad
		}
	}
	if len(p.Rates) != 4 {
		return nil, bad
	}
	balances, resources, joined := map[string]uint64{}, map[string]bool{}, map[string]bool{}
	var total uint64
	for _, receipt := range receipts {
		e := receipt.Evidence
		rate := p.Rates[e.Kind]
		if e.Domain != "radchat-contribution-v1" || e.Policy != p.Digest() || len(e.Member) < 1 || len(e.Member) > 128 || len(e.Resource) < 1 || len(e.Resource) > 128 || rate == 0 || e.Units == 0 || e.Units > 1000000000 || e.Observed < p.Starts || e.Observed >= p.Ends {
			return nil, bad
		}
		votes := 0
		for id, sig := range receipt.Signatures {
			key, ok := p.Verifiers[id]
			if !ok || !ed25519.Verify(key, pack(e), sig) {
				return nil, bad
			}
			votes++
		}
		if votes < p.Quorum {
			return nil, errors.New("contribution verification quorum not met")
		}
		// Global per-kind resource uniqueness prevents paying different identities
		// for the same task, compute interval, or model usage receipt.
		resource := e.Kind + ":" + e.Resource
		if resources[resource] {
			return nil, errors.New("duplicate contribution resource")
		}
		resources[resource] = true
		if e.Kind == "join" {
			if e.Units != 1 || priorJoinMembers[e.Member] || joined[e.Member] {
				return nil, errors.New("joining credit already used or invalid")
			}
			joined[e.Member] = true
		}
		points := rate * e.Units // Bounds above keep multiplication below MaxInt64.
		if points > p.PerMemberCap-balances[e.Member] || points > p.Budget-total {
			return nil, errors.New("contribution cap or epoch budget exceeded")
		}
		balances[e.Member] += points
		total += points
	}
	return balances, nil
}
