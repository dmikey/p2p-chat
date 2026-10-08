package radchat

import (
	"bytes"
	"context"
	"encoding/json"
	relayclient "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDeactivatedAccountReactivationAndRelayPersistence(t *testing.T) {
	_, auth := authorityTest(t)
	dir := t.TempDir()
	relay, err := NewBootstrap(dir, "/ip4/127.0.0.1/tcp/0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { relay.Close() }()
	addr := addresses(relay.Host)[0]
	owner := testNode(t, auth.URL, "human", addr)
	verifyTestEmail(t, owner, "owner@example.com", "Owner")
	if err = owner.CreateOrg("Lifecycle team"); err != nil {
		t.Fatal(err)
	}
	member := testNode(t, auth.URL, "human", addr)
	verifyTestEmail(t, member, "member@example.com", "Member")
	invite, _ := owner.Invite("member@example.com", "human")
	if err = member.Join(context.Background(), invite); err != nil {
		t.Fatal(err)
	}
	if _, _, err = member.Send(context.Background(), "general", "before deactivation", ""); err != nil {
		t.Fatal(err)
	}
	owner.syncPeer(context.Background(), member.Host.ID())
	oldSecret := append([]byte{}, member.snapshotOrg().Secret...)
	oldAccess := owner.snapshotOrg().Access
	if err = member.SetAccountActive(context.Background(), owner.Host.ID().String(), false); err == nil {
		t.Fatal("ordinary member deactivated owner")
	}
	if err = owner.SetAccountActive(context.Background(), member.Host.ID().String(), false); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(oldSecret, owner.snapshotOrg().Secret) {
		t.Fatal("deactivation did not rotate group key")
	}
	member.Host.Connect(context.Background(), *member.bootstrap)
	if err = member.refreshAccess(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err = member.Send(context.Background(), "general", "should be blocked", ""); err == nil {
		t.Fatal("deactivated member posted to channel")
	}
	if _, _, err = owner.SendDirect(context.Background(), member.Host.ID().String(), "should be blocked", ""); err == nil {
		t.Fatal("DM to deactivated account accepted")
	}
	record, err := verifyAccess(member.snapshotOrg().Access)
	if err != nil {
		t.Fatal(err)
	}
	if record.Active[member.Host.ID().String()] || len(record.Grants[member.Host.ID().String()]) != 0 {
		t.Fatal("deactivated member received key grant")
	}
	if _, err = owner.accessExchange(context.Background(), &oldAccess); err == nil {
		t.Fatal("relay accepted old signed access revision")
	}
	if _, _, err = owner.Send(context.Background(), "general", "after deactivation", ""); err != nil {
		t.Fatal(err)
	}
	owner.mu.Lock()
	lastPacket := append([]byte{}, owner.packets[len(owner.packets)-1]...)
	owner.mu.Unlock()
	if _, err = open(oldSecret, lastPacket, owner.snapshotOrg().ID); err == nil {
		t.Fatal("deactivated key decrypted future channel traffic")
	}
	// Restart the actual relay on the same listening address; status/grants survive.
	relayPeer := relay.Host.ID()
	listen := relay.Host.Addrs()[0].String()
	relay.Close()
	relay, err = NewBootstrap(dir, listen)
	if err != nil {
		t.Fatal(err)
	}
	if relay.Host.ID() != relayPeer {
		t.Fatal("relay identity changed after restart")
	}
	owner.Host.Connect(context.Background(), *owner.bootstrap)
	current, err := owner.accessExchange(context.Background(), nil)
	for attempt := 0; err != nil && attempt < 20; attempt++ {
		time.Sleep(50 * time.Millisecond)
		owner.Host.Connect(context.Background(), *owner.bootstrap)
		current, err = owner.accessExchange(context.Background(), nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	persisted, _ := verifyAccess(current)
	if persisted.Active[member.Host.ID().String()] {
		t.Fatal("relay forgot deactivated account")
	}
	b, err := os.ReadFile(filepath.Join(dir, "access.enc"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, owner.snapshotOrg().Secret) || bytes.Contains(b, []byte("Lifecycle team")) || bytes.Contains(b, []byte("before deactivation")) {
		t.Fatal("relay access file exposed plaintext application data")
	}
	if err = owner.SetAccountActive(context.Background(), member.Host.ID().String(), true); err != nil {
		t.Fatal(err)
	}
	member.Host.Connect(context.Background(), *member.bootstrap)
	if err = member.refreshAccess(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(member.snapshotOrg().Secret, owner.snapshotOrg().Secret) {
		t.Fatal("reactivated account did not recover current group key")
	}
	if !hasText(member, "before deactivation") {
		t.Fatal("reactivation lost earlier retained history")
	}
	if _, _, err = member.Send(context.Background(), "general", "reactivated successfully", ""); err != nil {
		t.Fatal(err)
	}
	// The grant is relay-held ciphertext; owner need not be online to recover it.
	owner.Close()
	if err = member.refreshAccess(context.Background()); err != nil {
		t.Fatal(err)
	}
	member.mu.Lock()
	member.accessFresh = time.Now().Add(-accessLease - time.Second)
	member.mu.Unlock()
	if _, _, err = member.Send(context.Background(), "general", "expired lease", ""); err == nil {
		t.Fatal("expired relay authorization allowed sending")
	}
	// A revoked account's old certificate still authenticates historical authorship.
	var cert Certificate
	if err = json.Unmarshal(member.snapshotOrg().Member.Payload, &cert); err != nil {
		t.Fatal(err)
	}
	if cert.Name != "Member" {
		t.Fatal("account identity was deleted instead of deactivated")
	}
}

// An accepted update may lose its reply. Rebuilding at the same version with a
// fresh encryption nonce conflicts, so the owner must read the current version.
func TestOwnerRecoversAccessAfterLostResponse(t *testing.T) {
	_, auth := authorityTest(t)
	relay, err := NewBootstrap(t.TempDir(), "/ip4/127.0.0.1/tcp/0")
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	owner := testNode(t, auth.URL, "human", addresses(relay.Host)[0])
	verifyTestEmail(t, owner, "lost-response@example.com", "Owner")
	if err = owner.CreateOrg("Lost response"); err != nil {
		t.Fatal(err)
	}
	owner.accessUpdate.Lock()
	record, err := owner.buildAccess()
	if err == nil {
		_, err = owner.accessExchange(context.Background(), &record)
	}
	owner.accessUpdate.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately do not apply the reply: local access is behind the relay.
	if err = owner.publishAccess(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := verifyAccess(owner.snapshotOrg().Access)
	previous, _ := verifyAccess(record)
	if err != nil || got.Version <= previous.Version {
		t.Fatalf("version not recovered: %v", err)
	}
}

func TestOrgOwnerCannotBanUnrelatedPeerFromRelay(t *testing.T) {
	_, auth := authorityTest(t)
	relay, err := NewBootstrap(t.TempDir(), "/ip4/127.0.0.1/tcp/0")
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	owner := testNode(t, auth.URL, "human", addresses(relay.Host)[0])
	verifyTestEmail(t, owner, "scope@example.com", "Owner")
	if err = owner.CreateOrg("Untrusted org"); err != nil {
		t.Fatal(err)
	}
	victim := testNode(t, auth.URL, "agent", "")
	// A malicious root can name someone who never joined its organization.
	owner.mu.Lock()
	o := owner.vault.Org
	o.Members = append(o.Members, sign(Certificate{Org: o.ID, Peer: victim.Host.ID().String(), Name: "Unrelated", Kind: "agent", EncryptionKey: random(32)}, o.RootPrivate))
	p, _ := verifyPolicy(o, o.Policy)
	p.Revision++
	p.Deactivated[victim.Host.ID().String()] = time.Now().UnixMilli()
	o.Policy = sign(p, o.RootPrivate)
	owner.mu.Unlock()
	if err = owner.publishAccess(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Exercise the actual relay ACL, rather than a client-side policy check.
	info, _ := addrInfo(addresses(relay.Host)[0])
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = victim.Host.Connect(ctx, *info); err != nil {
		t.Fatal(err)
	}
	if _, err = relayclient.Reserve(ctx, victim.Host, *info); err != nil {
		t.Fatalf("unrelated organization denied victim transport: %v", err)
	}
	record, _ := verifyAccess(owner.snapshotOrg().Access)
	if record.Active[victim.Host.ID().String()] {
		t.Fatal("org deactivation status was lost")
	}
}
