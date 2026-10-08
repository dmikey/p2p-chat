package radchat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

type Channel struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}
type Policy struct {
	Org           string           `json:"org"`
	Revision      uint64           `json:"revision"`
	RetentionDays int              `json:"retentionDays"`
	Channels      []Channel        `json:"channels"`
	Epoch         uint64           `json:"epoch"`
	KeyHash       []byte           `json:"keyHash"`
	Deactivated   map[string]int64 `json:"deactivated"`
}

func verifyPolicy(o *Org, s Signed) (Policy, error) {
	var p Policy
	if err := s.verify(o.Root, &p); err != nil {
		return p, err
	}
	if p.Epoch < 1 || len(p.KeyHash) != 32 || len(p.Deactivated) > 256 || p.Org != o.ID || p.Revision < 1 || p.RetentionDays < 0 || p.RetentionDays > 36500 || len(p.Channels) < 1 || len(p.Channels) > 100 {
		return p, errors.New("invalid organization policy")
	}
	names := map[string]bool{}
	for _, c := range p.Channels {
		if !validChannel(c.Name) || names[c.Name] || len(c.Description) > 200 {
			return p, errors.New("invalid channel")
		}
		names[c.Name] = true
	}
	return p, nil
}
func (n *Node) applyPolicy(s Signed) error {
	o := n.snapshotOrg()
	if o == nil {
		return errors.New("no organization")
	}
	p, err := verifyPolicy(o, s)
	if err != nil {
		return err
	}
	n.mu.Lock()
	var old Policy
	json.Unmarshal(n.vault.Org.Policy.Payload, &old)
	if p.Epoch != old.Epoch || !bytes.Equal(p.KeyHash, keyHash(n.vault.Org.Secret)) {
		n.mu.Unlock()
		return errors.New("refresh encrypted key access from relay first")
	}
	if p.Revision <= old.Revision {
		n.mu.Unlock()
		return nil
	}
	n.vault.Org.Policy = s
	err = n.saveLocked()
	n.mu.Unlock()
	if err != nil {
		return err
	}
	return n.pruneHistory()
}
func (n *Node) SetPolicy(ctx context.Context, retention int, channels []Channel) error {
	n.admin.Lock()
	defer n.admin.Unlock()
	o := n.snapshotOrg()
	if o == nil || len(o.RootPrivate) != 64 {
		return errors.New("only the organization owner can change channels and retention")
	}
	current, err := verifyPolicy(o, o.Policy)
	if err != nil {
		return err
	}
	current.Revision++
	current.RetentionDays = retention
	current.Channels = channels
	next := sign(current, o.RootPrivate)
	if _, err = verifyPolicy(o, next); err != nil {
		return err
	}
	if len(pack(next)) > 10*1024 {
		return errors.New("policy too large")
	}
	if err = n.applyPolicy(next); err != nil {
		return err
	}
	packet, err := seal(o.Secret, pack(map[string]any{"policy": next}), o.ID)
	if err != nil {
		return err
	}
	n.mu.Lock()
	topic := n.topic
	n.mu.Unlock()
	if topic != nil {
		topic.Publish(ctx, packet)
	}
	return n.publishAccess(ctx)
}
func (n *Node) packetPolicy(packet []byte) (Signed, bool) {
	o := n.snapshotOrg()
	if o == nil || len(packet) > 64*1024 {
		return Signed{}, false
	}
	plain, err := open(o.Secret, packet, o.ID)
	if err != nil {
		return Signed{}, false
	}
	var p struct {
		Policy *Signed `json:"policy"`
	}
	if json.Unmarshal(plain, &p) != nil || p.Policy == nil {
		return Signed{}, false
	}
	return *p.Policy, true
}
func (n *Node) validPacket(packet []byte) bool {
	n.mu.Lock()
	active := n.activeLocked(n.Host.ID().String())
	n.mu.Unlock()
	if !active {
		return false
	}
	if s, ok := n.packetPolicy(packet); ok {
		_, err := verifyPolicy(n.snapshotOrg(), s)
		return err == nil
	}
	_, err := n.verifyPacket(packet)
	return err == nil
}
func (n *Node) pruneHistory() error {
	n.history.Lock()
	defer n.history.Unlock()
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.vault.Org == nil {
		return nil
	}
	var p Policy
	json.Unmarshal(n.vault.Org.Policy.Payload, &p)
	if p.RetentionDays == 0 {
		return nil
	}
	cutoff := time.Now().Add(-time.Duration(p.RetentionDays) * 24 * time.Hour).UnixMilli()
	packets := [][]byte{}
	messages := []ChatMessage{}
	for i, m := range n.messages {
		if m.Created >= cutoff {
			packets = append(packets, n.packets[i])
			messages = append(messages, m)
		}
	}
	if len(packets) == len(n.packets) {
		return nil
	}
	if err := atomicWrite(filepath.Join(n.cfg.Dir, "history.enc"), pack(packets)); err != nil {
		return err
	}
	n.packets = packets
	n.messages = messages
	n.seen = map[string]bool{}
	for _, m := range messages {
		n.seen[m.ID] = true
	}
	return nil
}
func (n *Node) mergeMembers(members []Signed) error {
	if len(members) > 256 {
		return errors.New("member roster too large")
	}
	o := n.snapshotOrg()
	if o == nil {
		return errors.New("no organization")
	}
	valid := map[string]Signed{}
	for _, s := range members {
		var c Certificate
		if json.Unmarshal(s.Payload, &c) != nil {
			continue
		}
		id, err := peer.Decode(c.Peer)
		if err != nil {
			continue
		}
		if _, err = verifyMember(o, s, id); err == nil {
			valid[c.Peer] = s
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, s := range n.vault.Org.Members {
		var c Certificate
		json.Unmarshal(s.Payload, &c)
		delete(valid, c.Peer)
	}
	if len(valid) == 0 {
		return nil
	}
	if len(n.vault.Org.Members)+len(valid) > 256 {
		return errors.New("organization member limit reached")
	}
	for _, s := range valid {
		n.vault.Org.Members = append(n.vault.Org.Members, s)
	}
	return n.saveLocked()
}
