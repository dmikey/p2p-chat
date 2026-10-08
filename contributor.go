package radchat

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
)

// LaunchContributor enrolls a contributor-owned native agent with the org owner's signature.
// Other contributors must first obtain an agent invite from their org owner.
func (n *Node) LaunchContributor(ctx context.Context, name string, c RunnerConfig) error {
	n.contributorMu.Lock()
	defer n.contributorMu.Unlock()
	n.mu.Lock()
	kind := n.vault.Kind
	n.mu.Unlock()
	if kind == "agent" {
		return n.StartRunner(c)
	}
	name = strings.TrimSpace(name)
	if len(name) < 1 || len(name) > 80 {
		return errors.New("choose an agent name of 1–80 bytes")
	}
	o := n.snapshotOrg()
	if o == nil || len(o.RootPrivate) != 64 {
		return errors.New("ask your organization owner for an agent invitation, then launch an agent node")
	}
	if n.contributed != nil {
		return n.contributed.StartRunner(c)
	}
	child, err := NewNode(n.ctx, Config{Dir: filepath.Join(n.cfg.Dir, "contributor-agent"), Listen: "/ip4/127.0.0.1/tcp/0", Bootstrap: n.cfg.Bootstrap, AuthURL: n.cfg.AuthURL, Kind: "agent"})
	if err != nil {
		return err
	}
	failed := true
	defer func() {
		if failed {
			child.Close()
		}
	}()
	child.mu.Lock()
	existing := child.vault.Org
	child.mu.Unlock()
	if existing == nil {
		child.mu.Lock()
		enc, err := encryptionIdentity(child.vault.Identity)
		child.mu.Unlock()
		if err != nil {
			return err
		}
		member := sign(Certificate{o.ID, child.Host.ID().String(), name, "agent", enc.PublicKey().Bytes()}, o.RootPrivate)
		n.mu.Lock()
		if len(n.vault.Org.Members) >= 256 {
			n.mu.Unlock()
			return errors.New("organization member capacity reached")
		}
		present := false
		for _, s := range n.vault.Org.Members {
			var cert Certificate
			json.Unmarshal(s.Payload, &cert)
			present = present || cert.Peer == child.Host.ID().String()
		}
		if !present {
			n.vault.Org.Members = append(n.vault.Org.Members, member)
		}
		err = n.saveLocked()
		n.mu.Unlock()
		if err != nil {
			return err
		}
		if err = n.publishAccess(ctx); err != nil {
			return err
		}
		o = n.snapshotOrg()
		child.mu.Lock()
		child.vault.Name = name
		child.vault.Kind = "agent"
		child.vault.Org = &Org{ID: o.ID, Name: o.Name, Root: o.Root, Secret: o.Secret, Member: member, Members: o.Members, Policy: o.Policy, Access: o.Access}
		err = child.saveLocked()
		child.mu.Unlock()
		if err != nil {
			return err
		}
		if err = child.startOrg(); err != nil {
			return err
		}
	}
	if child.bootstrap != nil {
		if err = child.refreshAccess(ctx); err != nil {
			return err
		}
	}
	if err = child.StartRunner(c); err != nil {
		return err
	}
	n.contributed = child
	failed = false
	return nil
}
func (n *Node) runnerNode() *Node {
	n.contributorMu.Lock()
	defer n.contributorMu.Unlock()
	if n.contributed != nil {
		return n.contributed
	}
	return n
}
