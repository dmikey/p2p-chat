package radchat

import (
	"context"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

const directProtocol protocol.ID = "/radchat/direct/1.0.0"

type DirectPacket struct {
	From Signed `json:"from"`
	To   Signed `json:"to"`
	Data []byte `json:"data"`
}
type directContent struct {
	Message   Message `json:"message"`
	To        string  `json:"to"`
	Signature []byte  `json:"signature"`
}

func encryptionIdentity(identity []byte) (*ecdh.PrivateKey, error) {
	seed := sha256.Sum256(append([]byte("radchat-x25519-v1:"), identity...))
	return ecdh.X25519().NewPrivateKey(seed[:])
}
func pairKey(identity, other []byte, org, a, b string) ([]byte, error) {
	priv, err := encryptionIdentity(identity)
	if err != nil {
		return nil, err
	}
	pub, err := ecdh.X25519().NewPublicKey(other)
	if err != nil {
		return nil, err
	}
	shared, err := priv.ECDH(pub)
	if err != nil {
		return nil, err
	}
	ids := []string{a, b}
	sort.Strings(ids)
	// HKDF-SHA256 extract and expand, with protocol, organization and pair separation.
	extract := hmac.New(sha256.New, []byte(org))
	extract.Write(shared)
	expand := hmac.New(sha256.New, extract.Sum(nil))
	expand.Write([]byte("radchat-dm-v1:" + ids[0] + ":" + ids[1]))
	expand.Write([]byte{1})
	return expand.Sum(nil), nil
}
func (n *Node) directMember(target string) (Signed, Certificate, error) {
	o := n.snapshotOrg()
	if o == nil {
		return Signed{}, Certificate{}, errors.New("join an organization first")
	}
	for _, s := range o.Members {
		var c Certificate
		json.Unmarshal(s.Payload, &c)
		if c.Peer == target {
			id, err := peer.Decode(target)
			if err != nil {
				return s, c, err
			}
			c, err = verifyMember(o, s, id)
			return s, c, err
		}
	}
	return Signed{}, Certificate{}, errors.New("recipient is not in the signed organization roster yet")
}
func (n *Node) decodeDirect(p DirectPacket) (ChatMessage, error) {
	o := n.snapshotOrg()
	if o == nil || len(p.Data) > 16*1024 {
		return ChatMessage{}, errors.New("invalid direct packet")
	}
	var from, to Certificate
	if json.Unmarshal(p.From.Payload, &from) != nil || json.Unmarshal(p.To.Payload, &to) != nil {
		return ChatMessage{}, errors.New("invalid DM participants")
	}
	fromID, err := peer.Decode(from.Peer)
	if err != nil {
		return ChatMessage{}, err
	}
	toID, err := peer.Decode(to.Peer)
	if err != nil {
		return ChatMessage{}, err
	}
	if from, err = verifyMember(o, p.From, fromID); err != nil {
		return ChatMessage{}, err
	}
	if to, err = verifyMember(o, p.To, toID); err != nil {
		return ChatMessage{}, err
	}
	self := n.Host.ID().String()
	other := from
	if self == from.Peer {
		other = to
	} else if self != to.Peer {
		return ChatMessage{}, errors.New("DM belongs to other participants")
	}
	n.mu.Lock()
	identity := append([]byte{}, n.vault.Identity...)
	n.mu.Unlock()
	key, err := pairKey(identity, other.EncryptionKey, o.ID, self, other.Peer)
	if err != nil {
		return ChatMessage{}, err
	}
	plain, err := open(key, p.Data, "radchat-dm:"+o.ID)
	if err != nil {
		return ChatMessage{}, err
	}
	var content directContent
	if json.Unmarshal(plain, &content) != nil || content.To != to.Peer {
		return ChatMessage{}, errors.New("invalid DM content")
	}
	m := content.Message
	unsigned := struct {
		Message Message `json:"message"`
		To      string  `json:"to"`
	}{m, content.To}
	pub, err := fromID.ExtractPublicKey()
	if err != nil {
		return ChatMessage{}, err
	}
	ok, err := pub.Verify(pack(unsigned), content.Signature)
	if err != nil || !ok {
		return ChatMessage{}, errors.New("invalid DM signature")
	}
	if !strings.HasPrefix(m.ID, from.Peer+":") || len(m.ID) > 128 || len(m.Text) < 1 || len(m.Text) > 4000 || len(m.ReplyTo) > 128 || m.Channel != "dm:"+to.Peer || m.Created <= 0 || m.Created > time.Now().Add(5*time.Minute).UnixMilli() {
		return ChatMessage{}, errors.New("invalid direct message")
	}
	return ChatMessage{m.ID, "dm:" + other.Peer, m.Text, m.ReplyTo, m.Created, from.Peer, from.Name, from.Kind}, nil
}
func (n *Node) acceptDirect(p DirectPacket, persist bool) error {
	m, err := n.decodeDirect(p)
	if err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, old := range n.directMessages {
		if old.ID == m.ID {
			return nil
		}
	}
	packets := append(append([]DirectPacket{}, n.directPackets...), p)
	if persist {
		encrypted, err := seal(n.key, pack(packets), "radchat-dm-storage-v1")
		if err != nil {
			return err
		}
		if err = atomicWrite(filepath.Join(n.cfg.Dir, "dm.enc"), encrypted); err != nil {
			return err
		}
	}
	n.directPackets = packets
	n.directMessages = append(n.directMessages, m)
	return nil
}
func (n *Node) loadDirect() error {
	b, err := os.ReadFile(filepath.Join(n.cfg.Dir, "dm.enc"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	plain, err := open(n.key, b, "radchat-dm-storage-v1")
	if err != nil {
		return err
	}
	var packets []DirectPacket
	if err = json.Unmarshal(plain, &packets); err != nil {
		return err
	}
	for _, p := range packets {
		if err = n.acceptDirect(p, false); err != nil {
			return err
		}
	}
	return nil
}
func (n *Node) SendDirect(ctx context.Context, target, text, reply string) (ChatMessage, bool, error) {
	if target == n.Host.ID().String() || len(strings.TrimSpace(text)) < 1 || len(text) > 4000 || len(reply) > 128 {
		return ChatMessage{}, false, errors.New("invalid direct message")
	}
	to, toCert, err := n.directMember(target)
	if err != nil {
		return ChatMessage{}, false, err
	}
	o := n.snapshotOrg()
	if _, err = n.activeMember(o, to, mustPeer(target)); err != nil {
		return ChatMessage{}, false, err
	}
	n.mu.Lock()
	identity := append([]byte{}, n.vault.Identity...)
	priv, err := identityKey(n.vault)
	n.mu.Unlock()
	if err != nil {
		return ChatMessage{}, false, err
	}
	key, err := pairKey(identity, toCert.EncryptionKey, o.ID, n.Host.ID().String(), target)
	if err != nil {
		return ChatMessage{}, false, err
	}
	m := Message{n.Host.ID().String() + ":" + token(), "dm:" + target, text, reply, time.Now().UnixMilli(), o.Member}
	unsigned := struct {
		Message Message `json:"message"`
		To      string  `json:"to"`
	}{m, target}
	sig, err := priv.Sign(pack(unsigned))
	if err != nil {
		return ChatMessage{}, false, err
	}
	data, err := seal(key, pack(directContent{m, target, sig}), "radchat-dm:"+o.ID)
	if err != nil {
		return ChatMessage{}, false, err
	}
	p := DirectPacket{o.Member, to, data}
	if err = n.acceptDirect(p, true); err != nil {
		return ChatMessage{}, false, err
	}
	cm, err := n.decodeDirect(p)
	if err != nil {
		return ChatMessage{}, false, err
	}
	id, _ := peer.Decode(target)
	return cm, n.deliverDirect(ctx, id, p), nil
}
func (n *Node) handleDirect(s network.Stream) {
	defer s.Close()
	s.SetDeadline(time.Now().Add(10 * time.Second))
	var p DirectPacket
	if readJSON(s, &p) != nil {
		return
	}
	var from, to Certificate
	if json.Unmarshal(p.From.Payload, &from) != nil || json.Unmarshal(p.To.Payload, &to) != nil || from.Peer != s.Conn().RemotePeer().String() || to.Peer != n.Host.ID().String() {
		return
	}
	o := n.snapshotOrg()
	if o == nil {
		return
	}
	if _, err := n.activeMember(o, p.From, s.Conn().RemotePeer()); err != nil {
		return
	}
	if n.acceptDirect(p, true) == nil {
		writeJSON(s, map[string]bool{"ok": true})
	}
}
func (n *Node) deliverDirect(ctx context.Context, id peer.ID, p DirectPacket) bool {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	s, err := n.Host.NewStream(network.WithAllowLimitedConn(ctx, "radchat"), id, directProtocol)
	if err != nil {
		return false
	}
	defer s.Close()
	s.SetDeadline(time.Now().Add(4 * time.Second))
	if writeJSON(s, p) != nil {
		return false
	}
	var ack struct {
		OK bool `json:"ok"`
	}
	return readJSON(s, &ack) == nil && ack.OK
}
func (n *Node) retryDirect(ctx context.Context, id peer.ID) {
	n.mu.Lock()
	packets := append([]DirectPacket{}, n.directPackets...)
	n.mu.Unlock()
	for _, p := range packets {
		var to, from Certificate
		json.Unmarshal(p.To.Payload, &to)
		json.Unmarshal(p.From.Payload, &from)
		if to.Peer == id.String() && from.Peer == n.Host.ID().String() {
			if !n.deliverDirect(ctx, id, p) {
				return
			}
		}
	}
}
