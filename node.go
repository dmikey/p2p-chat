package radchat

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pb "github.com/libp2p/go-libp2p-pubsub/pb"
	lc "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	relayclient "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
)

type Config struct{ Dir, Listen, Bootstrap, AuthURL, Kind string }
type Message struct {
	ID      string `json:"id"`
	Channel string `json:"channel"`
	Text    string `json:"text"`
	ReplyTo string `json:"replyTo,omitempty"`
	Created int64  `json:"created"`
	Member  Signed `json:"member"`
}
type signedMessage struct {
	Message   Message `json:"message"`
	Signature []byte  `json:"signature"`
}
type ChatMessage struct {
	ID      string `json:"id"`
	Channel string `json:"channel"`
	Text    string `json:"text"`
	ReplyTo string `json:"replyTo,omitempty"`
	Created int64  `json:"created"`
	Peer    string `json:"peer"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
}
type Node struct {
	contributorMu    sync.Mutex
	contributed      *Node
	runner           Runner
	Host             host.Host
	Auth             *AuthClient
	cfg              Config
	mu               sync.Mutex
	vault            *Vault
	key              []byte
	proof            Signed
	messages         []ChatMessage
	packets          [][]byte
	seen             map[string]bool
	ps               *pubsub.PubSub
	topic            *pubsub.Topic
	sub              *pubsub.Subscription
	ctx              context.Context
	cancel           context.CancelFunc
	bootstrap        *peer.AddrInfo
	lastNetworkError string
	reservationUntil time.Time
	directPackets    []DirectPacket
	directMessages   []ChatMessage
	setup            sync.Mutex
	mesh             sync.Mutex
	accessFresh      time.Time
	accessUpdate     sync.Mutex
	history          sync.Mutex
	admin            sync.Mutex
}

func identityKey(v *Vault) (lc.PrivKey, error) { return lc.UnmarshalPrivateKey(v.Identity) }
func NewNode(ctx context.Context, cfg Config) (*Node, error) {
	if cfg.Kind == "" {
		cfg.Kind = "human"
	}
	if cfg.Kind != "human" && cfg.Kind != "agent" {
		return nil, errors.New("kind must be human or agent")
	}
	v, key, err := loadVault(cfg.Dir)
	if err != nil {
		return nil, err
	}
	if v.Org != nil {
		if v.AuthURL != "" && v.AuthURL != cfg.AuthURL {
			return nil, errors.New("enrolled device authority cannot change")
		}
		if cfg.Bootstrap == "" {
			cfg.Bootstrap = v.Bootstrap
		}
		if v.Bootstrap != "" && cfg.Bootstrap != v.Bootstrap {
			return nil, errors.New("enrolled device relay cannot change")
		}
	}
	if v.Email == "" {
		v.Kind = cfg.Kind
	}
	priv, err := identityKey(v)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	var boot *peer.AddrInfo
	opts := []libp2p.Option{libp2p.Identity(priv), libp2p.ListenAddrStrings(cfg.Listen), libp2p.NATPortMap(), libp2p.EnableHolePunching()}
	if cfg.Bootstrap != "" {
		boot, err = addrInfo(cfg.Bootstrap)
		if err != nil {
			cancel()
			return nil, err
		}
		opts = append(opts, libp2p.EnableAutoRelayWithStaticRelays([]peer.AddrInfo{*boot}))
	}
	h, err := libp2p.New(opts...)
	if err != nil {
		cancel()
		return nil, err
	}
	pinPath := filepath.Join(cfg.Dir, "authority.pin")
	pinned, err := os.ReadFile(pinPath)
	if err != nil && !os.IsNotExist(err) {
		h.Close()
		cancel()
		return nil, err
	}
	if len(pinned) == 0 {
		pinned = v.Authority
	}
	auth, err := NewAuthClient(cfg.AuthURL, pinned)
	if err != nil {
		h.Close()
		cancel()
		return nil, err
	}
	if len(pinned) == 0 {
		if err = atomicWrite(pinPath, auth.Key); err != nil {
			h.Close()
			cancel()
			return nil, err
		}
	}
	v.Bootstrap, v.AuthURL, v.Authority = cfg.Bootstrap, cfg.AuthURL, auth.Key
	n := &Node{Host: h, Auth: auth, cfg: cfg, vault: v, key: key, seen: map[string]bool{}, ctx: ctx, cancel: cancel, bootstrap: boot}
	ps, err := pubsub.NewGossipSub(network.WithAllowLimitedConn(ctx, "radchat"), h, pubsub.WithMaxMessageSize(64*1024), pubsub.WithMessageSignaturePolicy(pubsub.StrictNoSign), pubsub.WithMessageIdFn(func(message *pb.Message) string { h := sha256.Sum256(message.Data); return string(h[:]) }))
	if err != nil {
		n.Close()
		return nil, err
	}
	n.ps = ps
	h.SetStreamHandler(joinProtocol, n.handleJoin)
	h.SetStreamHandler(historyProtocol, n.handleHistory)
	h.SetStreamHandler(directProtocol, n.handleDirect)
	if err = saveVault(cfg.Dir, v, key); err != nil {
		n.Close()
		return nil, err
	}
	if v.Org != nil {
		if err = n.startOrg(); err != nil {
			n.Close()
			return nil, err
		}
	}
	go n.networkLoop()
	return n, nil
}
func (n *Node) Close() error {
	n.runner.stop("stopped")
	n.contributorMu.Lock()
	if n.contributed != nil {
		n.contributed.Close()
	}
	n.contributorMu.Unlock()
	n.cancel()
	n.stopTopic()
	return n.Host.Close()
}
func (n *Node) snapshotOrg() *Org {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.vault.Org == nil {
		return nil
	}
	var o Org
	json.Unmarshal(pack(n.vault.Org), &o)
	return &o
}
func (n *Node) saveLocked() error { return saveVault(n.cfg.Dir, n.vault, n.key) }
func (n *Node) RequestCode(ctx context.Context, email string) (string, error) {
	var result struct {
		DevCode string `json:"devCode"`
		OK      bool   `json:"ok"`
	}
	err := n.Auth.call(ctx, "POST", "/api/auth/request", map[string]string{"email": email, "peer": n.Host.ID().String()}, &result)
	return result.DevCode, err
}
func (n *Node) VerifyCode(ctx context.Context, email, code, name string) error {
	e, err := normalizeEmail(email)
	if err != nil {
		return err
	}
	if n.bootstrap == nil && !n.Auth.Dev {
		return errors.New("a relay is required for organization access management")
	}
	name = strings.TrimSpace(name)
	if len(name) < 1 || len(name) > 80 {
		return errors.New("name must contain 1–80 characters")
	}
	var proof Signed
	if err = n.Auth.call(ctx, "POST", "/api/auth/verify", map[string]string{"email": e, "code": code, "peer": n.Host.ID().String()}, &proof); err != nil {
		return err
	}
	var p EmailProof
	if err = proof.verify(n.Auth.Key, &p); err != nil {
		return err
	}
	if p.Peer != n.Host.ID().String() || p.EmailHash != emailHash(e) || p.Expires < time.Now().Unix() {
		return errors.New("invalid email proof")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.vault.Email != "" && n.vault.Email != e {
		return errors.New("this device belongs to another email; use a separate data directory")
	}
	n.proof = proof
	n.vault.Email = e
	n.vault.Name = name
	return n.saveLocked()
}
func (n *Node) verifiedLocked() bool {
	var p EmailProof
	return n.proof.verify(n.Auth.Key, &p) == nil && p.Peer == n.Host.ID().String() && p.EmailHash == emailHash(n.vault.Email) && p.Expires >= time.Now().Unix()
}
func (n *Node) CreateOrg(name string) error {
	if n.bootstrap == nil && !n.Auth.Dev {
		return errors.New("connect to your configured relay before creating an organization")
	}
	n.setup.Lock()
	defer n.setup.Unlock()
	name = strings.TrimSpace(name)
	if len(name) < 1 || len(name) > 80 {
		return errors.New("organization name must contain 1–80 characters")
	}
	n.mu.Lock()
	if n.vault.Org != nil {
		n.mu.Unlock()
		return errors.New("this device already has an organization")
	}
	if !n.verifiedLocked() {
		n.mu.Unlock()
		return errors.New("verify your email first")
	}
	root, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		n.mu.Unlock()
		return err
	}
	o := &Org{ID: orgID(root), Name: name, Root: root, RootPrivate: priv, Secret: random(32), Used: map[string]string{}}
	encryption, err := encryptionIdentity(n.vault.Identity)
	if err != nil {
		n.mu.Unlock()
		return err
	}
	o.Member = sign(Certificate{o.ID, n.Host.ID().String(), n.vault.Name, n.vault.Kind, encryption.PublicKey().Bytes()}, priv)
	o.Members = []Signed{o.Member}
	o.Policy = sign(Policy{Org: o.ID, Revision: 1, RetentionDays: 30, Channels: []Channel{{"general", "A shared space for your people and agents."}}, Epoch: 1, KeyHash: keyHash(o.Secret), Deactivated: map[string]int64{}}, priv)
	n.vault.Org = o
	err = n.saveLocked()
	n.mu.Unlock()
	if err != nil {
		return err
	}
	if err = n.startOrg(); err != nil {
		return err
	}
	return n.publishAccess(n.ctx)
}
func (n *Node) Invite(email, kind string) (string, error) {
	e, err := normalizeEmail(email)
	if err != nil {
		return "", err
	}
	if kind != "human" && kind != "agent" {
		return "", errors.New("invalid member kind")
	}
	o := n.snapshotOrg()
	if o == nil || len(o.RootPrivate) != 64 {
		return "", errors.New("only the organization owner can invite")
	}
	inv := Invitation{o.ID, o.Name, o.Root, emailHash(e), kind, token(), time.Now().Add(24 * time.Hour).Unix(), n.Addresses()}
	return b64.EncodeToString(pack(sign(inv, o.RootPrivate))), nil
}

type joinRequest struct {
	Invite        Signed `json:"invite"`
	Proof         Signed `json:"proof"`
	Name          string `json:"name"`
	EncryptionKey []byte `json:"encryptionKey"`
}
type joinResponse struct {
	Org   *Org   `json:"org,omitempty"`
	Error string `json:"error,omitempty"`
}

func (n *Node) handleJoin(s network.Stream) {
	defer s.Close()
	s.SetDeadline(time.Now().Add(10 * time.Second))
	var req joinRequest
	if readJSON(s, &req) != nil {
		return
	}
	n.mu.Lock()
	locked := true
	defer func() {
		if locked {
			n.mu.Unlock()
		}
	}()
	o := n.vault.Org
	fail := func(msg string) { writeJSON(s, joinResponse{Error: msg}) }
	if o == nil || len(o.RootPrivate) != 64 {
		fail("owner unavailable")
		return
	}
	var inv Invitation
	if req.Invite.verify(o.Root, &inv) != nil || inv.Org != o.ID || inv.Expires < time.Now().Unix() || inv.Expires > time.Now().Add(8*24*time.Hour).Unix() || (o.Used[inv.ID] != "" && o.Used[inv.ID] != s.Conn().RemotePeer().String()) || len(req.Name) < 1 || len(req.Name) > 80 || len(req.EncryptionKey) != 32 || (inv.Kind != "human" && inv.Kind != "agent") {
		fail("invite expired, invalid, or already used")
		return
	}
	var p EmailProof
	if req.Proof.verify(n.Auth.Key, &p) != nil || p.EmailHash != inv.EmailHash || p.Peer != s.Conn().RemotePeer().String() || p.Expires < time.Now().Unix() {
		fail("email verification does not match invite")
		return
	}
	member := sign(Certificate{o.ID, p.Peer, req.Name, inv.Kind, req.EncryptionKey}, o.RootPrivate)
	if o.Used == nil {
		o.Used = map[string]string{}
	}
	o.Used[inv.ID] = p.Peer
	existing := false
	for _, m := range o.Members {
		var c Certificate
		json.Unmarshal(m.Payload, &c)
		if c.Peer == p.Peer {
			existing = true
			member = m
			break
		}
	}
	if !existing {
		o.Members = append(o.Members, member)
	}
	if err := n.saveLocked(); err != nil {
		fail("could not persist membership")
		return
	}
	response := &Org{ID: o.ID, Name: o.Name, Root: o.Root, Secret: o.Secret, Member: member, Members: o.Members, Policy: o.Policy}
	n.mu.Unlock()
	locked = false
	if err := n.publishAccess(n.ctx); err != nil {
		fail("relay could not persist key access; retry this invite on the same device")
		return
	}
	response.Access = n.snapshotOrg().Access
	writeJSON(s, joinResponse{Org: response})
}
func (n *Node) Join(ctx context.Context, raw string) error {
	n.setup.Lock()
	defer n.setup.Unlock()
	if n.bootstrap == nil && !n.Auth.Dev {
		return errors.New("a relay is required for organization access management")
	}
	invSigned, inv, err := decodeInvite(raw)
	if err != nil {
		return err
	}
	n.mu.Lock()
	if n.vault.Org != nil {
		n.mu.Unlock()
		return errors.New("this device already has an organization")
	}
	if !n.verifiedLocked() {
		n.mu.Unlock()
		return errors.New("verify email first")
	}
	if inv.EmailHash != emailHash(n.vault.Email) {
		n.mu.Unlock()
		return errors.New("invite belongs to another email")
	}
	if inv.Kind != n.vault.Kind {
		n.mu.Unlock()
		return errors.New("invite kind must match node kind")
	}
	enc, e := encryptionIdentity(n.vault.Identity)
	if e != nil {
		n.mu.Unlock()
		return e
	}
	req := joinRequest{invSigned, n.proof, n.vault.Name, enc.PublicKey().Bytes()}
	n.mu.Unlock()
	var result joinResponse
	for _, raw := range inv.Owner {
		info, e := addrInfo(raw)
		if e != nil {
			continue
		}
		dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = n.Host.Connect(dialCtx, *info)
		cancel()
		if err != nil {
			continue
		}
		s, e := n.Host.NewStream(network.WithAllowLimitedConn(ctx, "radchat"), info.ID, joinProtocol)
		if e != nil {
			err = e
			continue
		}
		s.SetDeadline(time.Now().Add(10 * time.Second))
		err = writeJSON(s, req)
		if err == nil {
			err = readJSON(s, &result)
		}
		s.Close()
		if err == nil {
			break
		}
	}
	if err != nil || result.Org == nil {
		if result.Error != "" {
			return errors.New(result.Error)
		}
		return errors.New("invite owner is unreachable; keep their node online and make a fresh invite")
	}
	o := result.Org
	if o.ID != inv.Org || orgID(o.Root) != inv.Org || len(o.Secret) != 32 || len(o.RootPrivate) != 0 {
		return errors.New("invalid organization response")
	}
	if _, err = verifyMember(o, o.Member, n.Host.ID()); err != nil {
		return err
	}
	n.mu.Lock()
	n.vault.Org = o
	err = n.saveLocked()
	n.mu.Unlock()
	if err != nil {
		return err
	}
	if err = n.startOrg(); err != nil {
		return err
	}
	return n.refreshAccess(ctx)
}
func topicName(o *Org) string {
	h := sha256.Sum256(append([]byte("radchat-topic-v1:"), o.Secret...))
	return hex.EncodeToString(h[:])
}
func (n *Node) startOrg() error {
	o := n.snapshotOrg()
	if _, err := verifyMember(o, o.Member, n.Host.ID()); err != nil {
		return err
	}
	if _, err := verifyPolicy(o, o.Policy); err != nil {
		return err
	}
	if err := n.ps.RegisterTopicValidator(topicName(o), func(ctx context.Context, id peer.ID, msg *pubsub.Message) bool { return n.validPacket(msg.Data) }); err != nil {
		return err
	}
	topic, err := n.ps.Join(topicName(o))
	if err != nil {
		return err
	}
	sub, err := topic.Subscribe()
	if err != nil {
		topic.Close()
		return err
	}
	n.mu.Lock()
	n.topic = topic
	n.sub = sub
	n.mu.Unlock()
	data, err := os.ReadFile(filepath.Join(n.cfg.Dir, "history.enc"))
	if err == nil {
		var packets [][]byte
		if err = json.Unmarshal(data, &packets); err != nil {
			return err
		}
		for _, p := range packets {
			p, err = n.normalizeStoredPacket(p)
			if err != nil {
				return err
			}
			if err = n.accept(p, false); err != nil {
				return err
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err = n.loadDirect(); err != nil {
		return err
	}
	go func() {
		for {
			msg, err := sub.Next(n.ctx)
			if err != nil {
				return
			}
			n.accept(msg.Data, true)
		}
	}()
	return nil
}
func validChannel(s string) bool {
	if len(s) < 1 || len(s) > 40 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}
func (n *Node) verifyPacket(packet []byte) (ChatMessage, error) {
	o := n.snapshotOrg()
	if o == nil || len(packet) > 64*1024 {
		return ChatMessage{}, errors.New("invalid packet")
	}
	plain, err := open(o.Secret, packet, o.ID)
	if err != nil {
		return ChatMessage{}, err
	}
	var sm signedMessage
	if err = json.Unmarshal(plain, &sm); err != nil {
		return ChatMessage{}, err
	}
	var c Certificate
	if err = json.Unmarshal(sm.Message.Member.Payload, &c); err != nil {
		return ChatMessage{}, err
	}
	id, err := peer.Decode(c.Peer)
	if err != nil {
		return ChatMessage{}, err
	}
	c, err = verifyMember(o, sm.Message.Member, id)
	if err != nil {
		return ChatMessage{}, err
	}
	pub, err := id.ExtractPublicKey()
	if err != nil {
		return ChatMessage{}, err
	}
	ok, err := pub.Verify(pack(sm.Message), sm.Signature)
	if err != nil || !ok {
		return ChatMessage{}, errors.New("invalid message signature")
	}
	m := sm.Message
	var policy Policy
	json.Unmarshal(o.Policy.Payload, &policy)
	if at := policy.Deactivated[c.Peer]; at != 0 && m.Created >= at {
		return ChatMessage{}, errors.New("deactivated sender")
	}
	if !validChannel(m.Channel) || len(m.Text) < 1 || len(m.Text) > 4000 || len(m.ID) > 128 || !strings.HasPrefix(m.ID, c.Peer+":") || m.Created <= 0 || m.Created > time.Now().Add(5*time.Minute).UnixMilli() || len(m.ReplyTo) > 128 {
		return ChatMessage{}, errors.New("invalid message")
	}
	return ChatMessage{m.ID, m.Channel, m.Text, m.ReplyTo, m.Created, c.Peer, c.Name, c.Kind}, nil
}
func (n *Node) accept(packet []byte, persist bool) error {
	if policy, ok := n.packetPolicy(packet); ok {
		return n.applyPolicy(policy)
	}
	n.history.Lock()
	defer n.history.Unlock()
	m, err := n.verifyPacket(packet)
	if err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.seen[m.ID] {
		return nil
	}
	var policy Policy
	json.Unmarshal(n.vault.Org.Policy.Payload, &policy)
	if policy.RetentionDays > 0 && m.Created < time.Now().Add(-time.Duration(policy.RetentionDays)*24*time.Hour).UnixMilli() {
		return nil
	}
	nextPackets := append(append([][]byte{}, n.packets...), packet)
	nextMessages := append(append([]ChatMessage{}, n.messages...), m)
	if persist {
		if err = atomicWrite(filepath.Join(n.cfg.Dir, "history.enc"), pack(nextPackets)); err != nil {
			return err
		}
	}
	n.packets = nextPackets
	n.messages = nextMessages
	n.seen = map[string]bool{}
	for _, m := range n.messages {
		n.seen[m.ID] = true
	}
	return nil
}
func (n *Node) Send(ctx context.Context, channel, text, reply string) (ChatMessage, bool, error) {
	if !validChannel(channel) || len(strings.TrimSpace(text)) < 1 || len(text) > 4000 || len(reply) > 128 {
		return ChatMessage{}, false, errors.New("invalid channel or message; maximum 4000 bytes")
	}
	n.mu.Lock()
	o := n.vault.Org
	topic := n.topic
	if o == nil || topic == nil || !n.activeLocked(n.Host.ID().String()) {
		n.mu.Unlock()
		return ChatMessage{}, false, errors.New("join an organization first")
	}
	var policy Policy
	json.Unmarshal(o.Policy.Payload, &policy)
	exists := false
	for _, c := range policy.Channels {
		if c.Name == channel {
			exists = true
		}
	}
	if !exists {
		n.mu.Unlock()
		return ChatMessage{}, false, errors.New("channel does not exist; ask the owner to create it")
	}
	member := o.Member
	secret := append([]byte{}, o.Secret...)
	org := o.ID
	priv, err := identityKey(n.vault)
	n.mu.Unlock()
	if err != nil {
		return ChatMessage{}, false, err
	}
	m := Message{n.Host.ID().String() + ":" + token(), channel, text, reply, time.Now().UnixMilli(), member}
	sig, err := priv.Sign(pack(m))
	if err != nil {
		return ChatMessage{}, false, err
	}
	packet, err := seal(secret, pack(signedMessage{m, sig}), org)
	if err != nil {
		return ChatMessage{}, false, err
	}
	if err = n.accept(packet, true); err != nil {
		return ChatMessage{}, false, err
	}
	cm, err := n.verifyPacket(packet)
	if err != nil {
		return ChatMessage{}, false, err
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	err = topic.Publish(ctx, packet)
	return cm, err == nil && len(topic.ListPeers()) > 0, nil
}
func (n *Node) Messages() []ChatMessage {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := append(append([]ChatMessage{}, n.messages...), n.directMessages...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Created == out[j].Created {
			return out[i].ID < out[j].ID
		}
		return out[i].Created < out[j].Created
	})
	if out == nil {
		out = []ChatMessage{}
	}
	return out
}

type historyRequest struct {
	Member Signed `json:"member"`
	Offset int    `json:"offset"`
}
type historyResponse struct {
	Packets [][]byte `json:"packets"`
	Members []Signed `json:"members"`
	Policy  Signed   `json:"policy"`
	Next    int      `json:"next"`
	More    bool     `json:"more"`
}

func (n *Node) handleHistory(s network.Stream) {
	defer s.Close()
	s.SetDeadline(time.Now().Add(15 * time.Second))
	var req historyRequest
	if readJSON(s, &req) != nil || req.Offset < 0 {
		return
	}
	o := n.snapshotOrg()
	if o == nil {
		return
	}
	if _, err := n.activeMember(o, req.Member, s.Conn().RemotePeer()); err != nil {
		return
	}
	n.mergeMembers([]Signed{req.Member})
	n.mu.Lock()
	end := req.Offset + 32
	if end > len(n.packets) {
		end = len(n.packets)
	}
	start := req.Offset
	if start > end {
		start = end
	}
	packets := append([][]byte{}, n.packets[start:end]...)
	more := end < len(n.packets)
	n.mu.Unlock()
	writeJSON(s, historyResponse{packets, o.Members, o.Policy, end, more})
}
func (n *Node) syncPeer(ctx context.Context, id peer.ID) {
	o := n.snapshotOrg()
	if o == nil {
		return
	}
	n.mu.Lock()
	allowed := n.activeLocked(id.String()) && n.activeLocked(n.Host.ID().String())
	n.mu.Unlock()
	if !allowed {
		return
	}
	offset := 0
	for pages := 0; pages < 100; pages++ {
		s, err := n.Host.NewStream(network.WithAllowLimitedConn(ctx, "radchat"), id, historyProtocol)
		if err != nil {
			return
		}
		s.SetDeadline(time.Now().Add(15 * time.Second))
		if writeJSON(s, historyRequest{o.Member, offset}) != nil {
			s.Close()
			return
		}
		var result historyResponse
		err = readJSON(s, &result)
		s.Close()
		if err != nil || len(result.Packets) > 32 {
			return
		}
		if _, err = verifyPolicy(o, result.Policy); err != nil {
			return
		}
		n.applyPolicy(result.Policy)
		n.mergeMembers(result.Members)
		for _, packet := range result.Packets {
			n.accept(packet, true)
		}
		if !result.More || result.Next <= offset {
			break
		}
		offset = result.Next
	}
	n.retryDirect(ctx, id)
}
func (n *Node) networkLoop() {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		n.networkStep()
		select {
		case <-n.ctx.Done():
			return
		case <-tick.C:
		}
	}
}
func (n *Node) networkStep() {
	ctx, cancel := context.WithTimeout(n.ctx, 20*time.Second)
	defer cancel()
	o := n.snapshotOrg()
	if n.bootstrap != nil {
		err := n.Host.Connect(ctx, *n.bootstrap)
		if err == nil && o != nil {
			if n.needsAccessUpdate() {
				err = n.publishAccess(ctx)
			} else {
				err = n.refreshAccess(ctx)
			}
			o = n.snapshotOrg()
		}
		n.mu.Lock()
		needReservation := time.Now().Add(time.Minute).After(n.reservationUntil)
		n.mu.Unlock()
		if err == nil && needReservation {
			if reservation, e := relayclient.Reserve(ctx, n.Host, *n.bootstrap); e == nil {
				n.mu.Lock()
				n.reservationUntil = reservation.Expiration
				n.mu.Unlock()
			}
		}
		if err == nil && o != nil {
			var found []string
			found, err = discoverPeers(ctx, n.Host, n.bootstrap.ID, topicName(o), n.Addresses())
			if err == nil {
				for _, raw := range found {
					info, e := addrInfo(raw)
					if e == nil && info.ID != n.Host.ID() {
						dialCtx, done := context.WithTimeout(ctx, 2*time.Second)
						n.Host.Connect(dialCtx, *info)
						done()
					}
				}
			}
		}
		n.mu.Lock()
		if err != nil {
			n.lastNetworkError = "bootstrap unreachable"
		} else {
			n.lastNetworkError = ""
		}
		n.mu.Unlock()
	}
	if o != nil {
		for _, id := range n.Host.Network().Peers() {
			if n.bootstrap == nil || id != n.bootstrap.ID {
				n.syncPeer(ctx, id)
			}
		}
	}
	n.pruneHistory()
}
func (n *Node) Connect(ctx context.Context, raw string) error {
	info, err := addrInfo(raw)
	if err != nil {
		return err
	}
	return n.Host.Connect(ctx, *info)
}
func (n *Node) Export(password string) ([]byte, error) {
	n.mu.Lock()
	var v Vault
	json.Unmarshal(pack(n.vault), &v)
	n.mu.Unlock()
	return ExportRecovery(&v, password)
}
func (n *Node) Addresses() []string {
	out := addresses(n.Host)
	n.mu.Lock()
	reserved := time.Now().Before(n.reservationUntil)
	n.mu.Unlock()
	if n.bootstrap != nil && reserved {
		for _, a := range n.bootstrap.Addrs {
			out = append(out, a.String()+"/p2p/"+n.bootstrap.ID.String()+"/p2p-circuit/p2p/"+n.Host.ID().String())
		}
	}
	return out
}

// The same membership checks apply to direct and circuit-relayed streams.
var _ protocol.ID = historyProtocol
