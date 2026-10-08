package radchat

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	relayclient "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
)

func testNode(t *testing.T, url, kind, bootstrap string) *Node {
	t.Helper()
	n, err := NewNode(context.Background(), Config{Dir: t.TempDir(), Listen: "/ip4/127.0.0.1/tcp/0", AuthURL: url, Kind: kind, Bootstrap: bootstrap})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	return n
}
func verifyTestEmail(t *testing.T, n *Node, email, name string) {
	t.Helper()
	code, err := n.RequestCode(context.Background(), email)
	if err != nil {
		t.Fatal(err)
	}
	if code == "" {
		t.Fatal("expected isolated dev code")
	}
	if err = n.VerifyCode(context.Background(), email, code, name); err != nil {
		t.Fatal(err)
	}
}
func authorityTest(t *testing.T) (*Authority, *httptest.Server) {
	t.Helper()
	a, err := NewAuthority(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(a.Handler())
	t.Cleanup(s.Close)
	return a, s
}
func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	until := time.Now().Add(15 * time.Second)
	for time.Now().Before(until) {
		if condition() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("condition not reached within 15 seconds")
}
func hasText(n *Node, text string) bool {
	for _, m := range n.Messages() {
		if m.Text == text {
			return true
		}
	}
	return false
}

func TestOrganizationChatDMPolicyAndA2A(t *testing.T) {
	_, authority := authorityTest(t)
	owner := testNode(t, authority.URL, "human", "")
	verifyTestEmail(t, owner, "owner@example.com", "Owner")
	if err := owner.CreateOrg("Private team"); err != nil {
		t.Fatal(err)
	}
	human := testNode(t, authority.URL, "human", "")
	verifyTestEmail(t, human, "human@example.com", "Human")
	invite, err := owner.Invite("human@example.com", "human")
	if err != nil {
		t.Fatal(err)
	}
	if err = human.Join(context.Background(), invite); err != nil {
		t.Fatal(err)
	}
	agent := testNode(t, authority.URL, "agent", "")
	verifyTestEmail(t, agent, "agent@example.com", "Agent")
	invite, err = owner.Invite("agent@example.com", "agent")
	if err != nil {
		t.Fatal(err)
	}
	if err = agent.Join(context.Background(), invite); err != nil {
		t.Fatal(err)
	}
	// Certs converge even before a participant sends a channel message.
	owner.syncPeer(context.Background(), human.Host.ID())
	human.syncPeer(context.Background(), owner.Host.ID())
	agent.syncPeer(context.Background(), owner.Host.ID())
	if _, _, err = owner.Send(context.Background(), "general", "encrypted channel hello", ""); err != nil {
		t.Fatal(err)
	}
	human.syncPeer(context.Background(), owner.Host.ID())
	agent.syncPeer(context.Background(), owner.Host.ID())
	if !hasText(human, "encrypted channel hello") || !hasText(agent, "encrypted channel hello") {
		t.Fatal("channel sync failed")
	}
	if _, _, err = human.SendDirect(context.Background(), owner.Host.ID().String(), "pairwise secret", ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return hasText(owner, "pairwise secret") })
	if hasText(agent, "pairwise secret") {
		t.Fatal("DM leaked to third member")
	}
	human.mu.Lock()
	packet := human.directPackets[0]
	human.mu.Unlock()
	if _, err = agent.decodeDirect(packet); err == nil {
		t.Fatal("third member decoded DM")
	}
	var recipient Certificate
	json.Unmarshal(packet.To.Payload, &recipient)
	spyKey, err := pairKey(agent.vault.Identity, recipient.EncryptionKey, agent.vault.Org.ID, agent.Host.ID().String(), recipient.Peer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = open(spyKey, packet.Data, "radchat-dm:"+agent.vault.Org.ID); err == nil {
		t.Fatal("third member cryptographically decrypted DM")
	}
	if err = human.SetPolicy(context.Background(), 7, []Channel{{"general", ""}}); err == nil {
		t.Fatal("member changed owner's retention")
	}
	if err = owner.SetPolicy(context.Background(), 7, []Channel{{"general", ""}, {"engineering", "Build things"}}); err != nil {
		t.Fatal(err)
	}
	human.syncPeer(context.Background(), owner.Host.ID())
	agent.syncPeer(context.Background(), owner.Host.ID())
	if _, _, err = human.Send(context.Background(), "engineering", "channel created", ""); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "http://127.0.0.1/a2a", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"kind":"message","role":"user","messageId":"client-1","contextId":"engineering","parts":[{"kind":"text","text":"hello from A2A"}]}}}`))
	req.Header.Set("Authorization", "Bearer "+agent.APIToken())
	w := httptest.NewRecorder()
	agent.Handler().ServeHTTP(w, req)
	if w.Code != 200 || bytes.Contains(w.Body.Bytes(), []byte(`"error"`)) {
		t.Fatalf("A2A failed: %s", w.Body.String())
	}
	owner.syncPeer(context.Background(), agent.Host.ID())
	if !hasText(owner, "hello from A2A") {
		t.Fatal("A2A message did not reach human")
	}
	for _, m := range owner.Messages() {
		if m.Text == "hello from A2A" && m.Kind != "agent" {
			t.Fatal("A2A sender not marked agent")
		}
	}
	// Public history and vault contain only encrypted payloads; DMs never enter group history.
	for _, name := range []string{"vault.enc", "history.enc", "dm.enc"} {
		b, e := os.ReadFile(filepath.Join(owner.cfg.Dir, name))
		if e != nil {
			t.Fatal(e)
		}
		if bytes.Contains(b, []byte("encrypted channel hello")) || bytes.Contains(b, []byte("pairwise secret")) || bytes.Contains(b, []byte("owner@example.com")) {
			t.Fatalf("plaintext in %s", name)
		}
	}
	// Relay-free restart works using the pinned authority key, even when the authority is offline.
	dir := owner.cfg.Dir
	owner.Close()
	authority.Close()
	restored, err := NewNode(context.Background(), Config{Dir: dir, Listen: "/ip4/127.0.0.1/tcp/0", AuthURL: authority.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if !hasText(restored, "pairwise secret") || !hasText(restored, "encrypted channel hello") {
		t.Fatal("encrypted history did not survive restart")
	}
}

func TestInvitesAreBoundSingleUseAndProofsCannotBeForged(t *testing.T) {
	a, s := authorityTest(t)
	owner := testNode(t, s.URL, "human", "")
	verifyTestEmail(t, owner, "owner@example.com", "Owner")
	owner.CreateOrg("Org")
	invite, _ := owner.Invite("member@example.com", "human")
	wrong := testNode(t, s.URL, "human", "")
	verifyTestEmail(t, wrong, "wrong@example.com", "Wrong")
	if wrong.Join(context.Background(), invite) == nil {
		t.Fatal("wrong email accepted invite")
	}
	member := testNode(t, s.URL, "human", "")
	verifyTestEmail(t, member, "member@example.com", "Member")
	if err := member.Join(context.Background(), invite); err != nil {
		t.Fatal(err)
	}
	copyNode := testNode(t, s.URL, "human", "")
	verifyTestEmail(t, copyNode, "member@example.com", "Other device")
	if copyNode.Join(context.Background(), invite) == nil {
		t.Fatal("invite replay succeeded with another peer")
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	forged := sign(EmailProof{emailHash("member@example.com"), wrong.Host.ID().String(), time.Now().Add(time.Hour).Unix()}, priv)
	var proof EmailProof
	if forged.verify(a.key.Public().(ed25519.PublicKey), &proof) == nil {
		t.Fatal("forged proof accepted")
	}
	code, err := wrong.RequestCode(context.Background(), "guess@example.com")
	if err != nil {
		t.Fatal(err)
	}
	bad := "000000"
	if bad == code {
		bad = "000001"
	}
	for i := 0; i < 6; i++ {
		if wrong.VerifyCode(context.Background(), "guess@example.com", bad, "Guesser") == nil {
			t.Fatal("bad code accepted")
		}
	}
	if wrong.VerifyCode(context.Background(), "guess@example.com", code, "Guesser") == nil {
		t.Fatal("bruteforced challenge still usable")
	}
}

func TestRetentionAndRecovery(t *testing.T) {
	_, s := authorityTest(t)
	n := testNode(t, s.URL, "human", "")
	verifyTestEmail(t, n, "owner@example.com", "Owner")
	n.CreateOrg("Org")
	if err := n.SetPolicy(context.Background(), 0, []Channel{{"general", ""}}); err != nil {
		t.Fatal(err)
	}
	o := n.snapshotOrg()
	priv, _ := identityKey(n.vault)
	old := Message{n.Host.ID().String() + ":" + token(), "general", "old channel message", "", time.Now().Add(-48 * time.Hour).UnixMilli(), o.Member}
	sig, _ := priv.Sign(pack(old))
	packet, _ := seal(o.Secret, pack(signedMessage{old, sig}), o.ID)
	if err := n.accept(packet, true); err != nil {
		t.Fatal(err)
	}
	if !hasText(n, "old channel message") {
		t.Fatal("no-expiry policy discarded history")
	}
	if err := n.SetPolicy(context.Background(), 1, []Channel{{"general", ""}}); err != nil {
		t.Fatal(err)
	}
	if hasText(n, "old channel message") {
		t.Fatal("expired channel history not deleted")
	}
	n.accept(packet, true)
	if hasText(n, "old channel message") {
		t.Fatal("expired history resurrected")
	}
	exported, err := n.Export("a very long recovery passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = RestoreRecovery(exported, "wrong"); err == nil {
		t.Fatal("wrong recovery passphrase accepted")
	}
	v, err := RestoreRecovery(exported, "a very long recovery passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(v.Identity, n.vault.Identity) || !bytes.Equal(v.Org.Secret, n.vault.Org.Secret) {
		t.Fatal("recovery changed identity or organization")
	}
	if v.APIToken == n.APIToken() {
		t.Fatal("restoring device did not rotate API token")
	}
}

func TestCircuitRelayInviteAndDM(t *testing.T) {
	_, s := authorityTest(t)
	relay, err := NewBootstrap(t.TempDir(), "/ip4/127.0.0.1/tcp/0")
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	addr := addresses(relay.Host)[0]
	owner := testNode(t, s.URL, "human", addr)
	verifyTestEmail(t, owner, "owner@example.com", "Owner")
	owner.CreateOrg("Relayed org")
	member := testNode(t, s.URL, "human", addr)
	verifyTestEmail(t, member, "relay@example.com", "Relayed human")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	info, _ := addrInfo(addr)
	owner.Host.Connect(ctx, *info)
	if _, err = relayclient.Reserve(ctx, owner.Host, *info); err != nil {
		t.Fatal(err)
	}
	raw, _ := owner.Invite("relay@example.com", "human")
	_, inv, err := decodeInvite(raw)
	if err != nil {
		t.Fatal(err)
	}
	inv.Owner = []string{addr + "/p2p-circuit/p2p/" + owner.Host.ID().String()}
	raw = b64.EncodeToString(pack(sign(inv, owner.vault.Org.RootPrivate)))
	if err = member.Join(network.WithAllowLimitedConn(ctx, "test-relay"), raw); err != nil {
		t.Fatal(err)
	}
	relayed := false
	for _, conn := range member.Host.Network().ConnsToPeer(owner.Host.ID()) {
		if strings.Contains(conn.RemoteMultiaddr().String(), "/p2p-circuit") {
			relayed = true
		}
	}
	if !relayed {
		t.Fatal("test did not establish a circuit-relayed connection")
	}
	if _, delivered, err := member.SendDirect(network.WithAllowLimitedConn(ctx, "test-relay"), owner.Host.ID().String(), "relayed DM stays private", ""); err != nil || !delivered {
		t.Fatalf("relay DM failed: %v delivered=%v", err, delivered)
	}
	if !hasText(owner, "relayed DM stays private") {
		t.Fatal("relayed DM missing")
	}
	if _, _, err := owner.Send(ctx, "general", "relayed channel history", ""); err != nil {
		t.Fatal(err)
	}
	member.syncPeer(network.WithAllowLimitedConn(ctx, "test-relay"), owner.Host.ID())
	if !hasText(member, "relayed channel history") {
		t.Fatal("relay history sync failed")
	}
	if _, err = os.Stat(filepath.Join(relay.Host.ID().String(), "history.enc")); !os.IsNotExist(err) {
		t.Fatal("unexpected relay history")
	}
	var _ peer.ID = owner.Host.ID()
}

func TestLocalHTTPBoundaries(t *testing.T) {
	_, s := authorityTest(t)
	n := testNode(t, s.URL, "human", "")
	for _, tc := range []struct {
		url, method, origin string
		cookie              bool
		status              int
	}{{"http://127.0.0.1/api/state", "GET", "", false, 401}, {"http://evil.example/api/state", "GET", "", true, 403}, {"http://127.0.0.1/api/org/create", "POST", "https://evil.example", true, 403}} {
		r := httptest.NewRequest(tc.method, tc.url, strings.NewReader(`{"name":"Bad"}`))
		if tc.cookie {
			r.AddCookie(&http.Cookie{Name: "radchat_device", Value: n.APIToken()})
		}
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		n.Handler().ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s got %d want %d", tc.url, w.Code, tc.status)
		}
	}
}
