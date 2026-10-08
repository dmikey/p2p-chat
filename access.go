package radchat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	ma "github.com/multiformats/go-multiaddr"
)

const accessProtocol protocol.ID = "/radchat/access/1.0.0"
const accessLease = 30 * time.Second

type AccessRecord struct {
	Org     string            `json:"org"`
	Root    []byte            `json:"root"`
	Version uint64            `json:"version"`
	Epoch   uint64            `json:"epoch"`
	KeyHash []byte            `json:"keyHash"`
	Owner   Signed            `json:"owner"`
	Active  map[string]bool   `json:"active"`
	Grants  map[string][]byte `json:"grants"`
	Control []byte            `json:"control"`
}
type accessRequest struct {
	Org    string  `json:"org"`
	Update *Signed `json:"update,omitempty"`
}
type accessResponse struct {
	Record Signed `json:"record"`
	Error  string `json:"error,omitempty"`
}
type AccessRegistry struct {
	mu      sync.Mutex
	records map[string]Signed
	dir     string
	key     []byte
}

func keyHash(key []byte) []byte { h := sha256.Sum256(key); return h[:] }
func verifyAccess(s Signed) (AccessRecord, error) {
	var r AccessRecord
	if err := json.Unmarshal(s.Payload, &r); err != nil {
		return r, err
	}
	if err := s.verify(r.Root, &r); err != nil {
		return r, err
	}
	if r.Org != orgID(r.Root) || r.Version < 1 || r.Epoch < 1 || len(r.KeyHash) != 32 || len(r.Active) > 256 || len(r.Grants) > 256 || len(r.Control) > 1<<20 {
		return r, errors.New("invalid access record")
	}
	var owner Certificate
	if err := json.Unmarshal(r.Owner.Payload, &owner); err != nil {
		return r, err
	}
	id, err := peer.Decode(owner.Peer)
	if err != nil {
		return r, err
	}
	if _, err = verifyMember(&Org{ID: r.Org, Root: r.Root}, r.Owner, id); err != nil {
		return r, err
	}
	if !r.Active[owner.Peer] {
		return r, errors.New("owner must remain active")
	}
	for id, active := range r.Active {
		if _, err := peer.Decode(id); err != nil {
			return r, err
		}
		if active && len(r.Grants[id]) != 60 {
			return r, errors.New("active member requires an encrypted key grant")
		}
	}
	return r, nil
}
func newAccessRegistry(dir string) (*AccessRegistry, error) {
	_, key, err := loadVault(dir)
	if err != nil {
		return nil, err
	}
	r := &AccessRegistry{records: map[string]Signed{}, dir: dir, key: key}
	data, err := os.ReadFile(filepath.Join(dir, "access.enc"))
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	plain, err := open(key, data, "radchat-relay-access-v1")
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(plain, &r.records); err != nil {
		return nil, err
	}
	for _, s := range r.records {
		if _, err = verifyAccess(s); err != nil {
			return nil, err
		}
	}
	return r, nil
}
func (r *AccessRegistry) handle(s network.Stream) {
	defer s.Close()
	s.SetDeadline(time.Now().Add(10 * time.Second))
	var req accessRequest
	if readJSON(s, &req) != nil || len(req.Org) != 64 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	fail := func(msg string) { writeJSON(s, accessResponse{Error: msg}) }
	if req.Update != nil {
		record, err := verifyAccess(*req.Update)
		if err != nil || record.Org != req.Org {
			fail("invalid signed access update")
			return
		}
		var owner Certificate
		json.Unmarshal(record.Owner.Payload, &owner)
		if owner.Peer != s.Conn().RemotePeer().String() {
			fail("only the signed organization owner may update access")
			return
		}
		if old, ok := r.records[req.Org]; ok {
			oldRecord, _ := verifyAccess(old)
			if record.Version < oldRecord.Version {
				fail("access update is stale")
				return
			}
			if record.Version == oldRecord.Version && !bytes.Equal(pack(*req.Update), pack(old)) {
				fail("conflicting access revision")
				return
			}
		} else if len(r.records) >= 2048 {
			fail("relay organization capacity reached")
			return
		}
		previous, existed := r.records[req.Org]
		r.records[req.Org] = *req.Update
		encrypted, err := seal(r.key, pack(r.records), "radchat-relay-access-v1")
		if err == nil {
			err = atomicWrite(filepath.Join(r.dir, "access.enc"), encrypted)
		}
		if err != nil {
			if existed {
				r.records[req.Org] = previous
			} else {
				delete(r.records, req.Org)
			}
			fail("access registry could not persist update")
			return
		}
	}
	record, ok := r.records[req.Org]
	if !ok {
		fail("organization not registered on this relay")
		return
	}
	parsed, _ := verifyAccess(record)
	if _, known := parsed.Active[s.Conn().RemotePeer().String()]; !known {
		fail("invited membership required")
		return
	}
	writeJSON(s, accessResponse{Record: record})
}
func (r *AccessRegistry) allowed(id peer.ID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	known, active := false, false
	for _, s := range r.records {
		record, _ := verifyAccess(s)
		if state, ok := record.Active[id.String()]; ok {
			known = true
			active = active || state
		}
	}
	return !known || active
}
func (r *AccessRegistry) AllowReserve(id peer.ID, a ma.Multiaddr) bool { return r.allowed(id) }
func (r *AccessRegistry) AllowConnect(src peer.ID, a ma.Multiaddr, dest peer.ID) bool {
	return r.allowed(src) && r.allowed(dest)
}
func (n *Node) accessExchange(ctx context.Context, update *Signed) (Signed, error) {
	o := n.snapshotOrg()
	if o == nil || n.bootstrap == nil {
		return Signed{}, errors.New("organization relay is not configured")
	}
	s, err := n.Host.NewStream(ctx, n.bootstrap.ID, accessProtocol)
	if err != nil {
		return Signed{}, err
	}
	defer s.Close()
	s.SetDeadline(time.Now().Add(10 * time.Second))
	if err = writeJSON(s, accessRequest{o.ID, update}); err != nil {
		return Signed{}, err
	}
	var res accessResponse
	if err = readJSON(s, &res); err != nil {
		return Signed{}, err
	}
	if res.Error != "" {
		return Signed{}, errors.New(res.Error)
	}
	return res.Record, nil
}

type orgControl struct {
	Policy  Signed   `json:"policy"`
	Members []Signed `json:"members"`
}

func (n *Node) buildAccess() (Signed, error) {
	o := n.snapshotOrg()
	if o == nil || len(o.RootPrivate) != 64 {
		return Signed{}, errors.New("organization owner required")
	}
	p, err := verifyPolicy(o, o.Policy)
	if err != nil {
		return Signed{}, err
	}
	version := uint64(1)
	if old, err := verifyAccess(o.Access); err == nil {
		version = old.Version + 1
	}
	control, err := seal(o.Secret, pack(orgControl{o.Policy, o.Members}), "radchat-control:"+o.ID)
	if err != nil {
		return Signed{}, err
	}
	r := AccessRecord{o.ID, o.Root, version, p.Epoch, keyHash(o.Secret), o.Member, map[string]bool{}, map[string][]byte{}, control}
	n.mu.Lock()
	identity := append([]byte{}, n.vault.Identity...)
	n.mu.Unlock()
	for _, s := range o.Members {
		var c Certificate
		if json.Unmarshal(s.Payload, &c) != nil {
			return Signed{}, errors.New("invalid member")
		}
		active := p.Deactivated[c.Peer] == 0
		r.Active[c.Peer] = active
		if !active {
			continue
		}
		key, err := pairKey(identity, c.EncryptionKey, o.ID, n.Host.ID().String(), c.Peer)
		if err != nil {
			return Signed{}, err
		}
		grant, err := seal(key, o.Secret, "radchat-keygrant:"+o.ID+":"+string(pack(p.Epoch)))
		if err != nil {
			return Signed{}, err
		}
		r.Grants[c.Peer] = grant
	}
	record := sign(r, o.RootPrivate)
	if _, err = verifyAccess(record); err != nil {
		return Signed{}, err
	}
	return record, nil
}
func (n *Node) publishAccess(ctx context.Context) error {
	if n.bootstrap == nil {
		return nil
	}
	n.accessUpdate.Lock()
	defer n.accessUpdate.Unlock()
	if err := n.Host.Connect(ctx, *n.bootstrap); err != nil {
		return err
	}
	// Recover an update the relay accepted before its response reached this owner.
	// Keep newer local changes, but use the relay's signed version for the next write.
	o := n.snapshotOrg()
	latest, readErr := n.accessExchange(ctx, nil)
	if readErr == nil {
		r, err := verifyAccess(latest)
		if err != nil || r.Org != o.ID || !bytes.Equal(r.Root, o.Root) {
			return errors.New("invalid current access record")
		}
		var owner Certificate
		json.Unmarshal(r.Owner.Payload, &owner)
		if owner.Peer != n.Host.ID().String() {
			return errors.New("access owner mismatch")
		}
		old, _ := verifyAccess(o.Access)
		if old.Version > r.Version {
			return errors.New("relay attempted an access rollback")
		}
		policy, _ := verifyPolicy(o, o.Policy)
		newer := r.Epoch > policy.Epoch
		if r.Epoch == policy.Epoch {
			if !bytes.Equal(r.KeyHash, keyHash(o.Secret)) {
				return errors.New("epoch key conflict")
			}
			plain, e := open(o.Secret, r.Control, "radchat-control:"+o.ID)
			if e != nil {
				return e
			}
			var control orgControl
			if json.Unmarshal(plain, &control) != nil {
				return errors.New("invalid access control")
			}
			remote, e := verifyPolicy(o, control.Policy)
			if e != nil {
				return e
			}
			newer = remote.Revision > policy.Revision
		}
		if newer {
			if err = n.applyAccess(latest); err != nil {
				return err
			}
		} else {
			n.mu.Lock()
			n.vault.Org.Access = latest
			n.mu.Unlock()
		}
	} else {
		// A zero Signed value becomes payload:null after a vault JSON round trip.
		// Only a verified record proves this organization was already registered.
		_, existingErr := verifyAccess(o.Access)
		if existingErr == nil || readErr.Error() != "organization not registered on this relay" {
			return readErr
		}
	}
	record, err := n.buildAccess()
	if err != nil {
		return err
	}
	confirmed, err := n.accessExchange(ctx, &record)
	if err != nil {
		return err
	}
	return n.applyAccess(confirmed)
}
func (n *Node) refreshAccess(ctx context.Context) error {
	if n.bootstrap == nil {
		return nil
	}
	s, err := n.accessExchange(ctx, nil)
	if err != nil {
		return err
	}
	return n.applyAccess(s)
}
func (n *Node) applyAccess(s Signed) error {
	record, err := verifyAccess(s)
	if err != nil {
		return err
	}
	o := n.snapshotOrg()
	if o == nil || record.Org != o.ID || !bytes.Equal(record.Root, o.Root) {
		return errors.New("access record belongs to another organization")
	}
	if old, e := verifyAccess(o.Access); e == nil && old.Version > record.Version {
		return errors.New("relay attempted an access rollback")
	}
	if !record.Active[n.Host.ID().String()] {
		n.mu.Lock()
		n.vault.Org.Access = s
		n.accessFresh = time.Now()
		err = n.saveLocked()
		n.mu.Unlock()
		n.stopTopic()
		return err
	}
	var owner Certificate
	json.Unmarshal(record.Owner.Payload, &owner)
	n.mu.Lock()
	identity := append([]byte{}, n.vault.Identity...)
	n.mu.Unlock()
	key, err := pairKey(identity, owner.EncryptionKey, o.ID, n.Host.ID().String(), owner.Peer)
	if err != nil {
		return err
	}
	secret, err := open(key, record.Grants[n.Host.ID().String()], "radchat-keygrant:"+o.ID+":"+string(pack(record.Epoch)))
	if err != nil || !bytes.Equal(keyHash(secret), record.KeyHash) {
		return errors.New("invalid encrypted key grant")
	}
	plain, err := open(secret, record.Control, "radchat-control:"+o.ID)
	if err != nil {
		return err
	}
	var control orgControl
	if err = json.Unmarshal(plain, &control); err != nil {
		return err
	}
	policy, err := verifyPolicy(o, control.Policy)
	if err != nil || policy.Epoch != record.Epoch || !bytes.Equal(policy.KeyHash, record.KeyHash) {
		return errors.New("invalid grant control policy")
	}
	if len(control.Members) > 256 {
		return errors.New("member limit exceeded")
	}
	for _, member := range control.Members {
		var c Certificate
		if json.Unmarshal(member.Payload, &c) != nil {
			return errors.New("invalid member roster")
		}
		id, e := peer.Decode(c.Peer)
		if e != nil {
			return e
		}
		if _, e = verifyMember(o, member, id); e != nil {
			return e
		}
	}
	current, _ := verifyPolicy(o, o.Policy)
	if policy.Revision < current.Revision || policy.Epoch < current.Epoch {
		return errors.New("access control policy is stale")
	}
	changed := !bytes.Equal(o.Secret, secret)
	n.mu.Lock()
	if changed {
		n.vault.Org.PreviousSecrets = append(n.vault.Org.PreviousSecrets, n.vault.Org.Secret)
	}
	n.vault.Org.Secret = secret
	n.vault.Org.Policy = control.Policy
	n.vault.Org.Members = control.Members
	n.vault.Org.Access = s
	n.accessFresh = time.Now()
	err = n.saveLocked()
	n.mu.Unlock()
	if err != nil {
		return err
	}
	if changed {
		if err = n.reencryptHistory(); err != nil {
			return err
		}
	}
	n.mu.Lock()
	topicMissing := n.topic == nil
	n.mu.Unlock()
	if changed || topicMissing {
		if err = n.switchTopic(); err != nil {
			return err
		}
	}
	return n.pruneHistory()
}
func (n *Node) activeLocked(id string) bool {
	if n.vault.Org == nil {
		return false
	}
	var p Policy
	json.Unmarshal(n.vault.Org.Policy.Payload, &p)
	if p.Deactivated[id] != 0 {
		return false
	}
	if n.bootstrap == nil {
		return n.Auth.Dev
	}
	r, err := verifyAccess(n.vault.Org.Access)
	return err == nil && r.Active[id] && time.Since(n.accessFresh) <= accessLease
}
func (n *Node) activeMember(o *Org, s Signed, id peer.ID) (Certificate, error) {
	c, err := verifyMember(o, s, id)
	if err != nil {
		return c, err
	}
	n.mu.Lock()
	active := n.activeLocked(id.String()) && n.activeLocked(n.Host.ID().String())
	n.mu.Unlock()
	if !active {
		return c, errors.New("account deactivated or relay authorization expired")
	}
	return c, nil
}
func (n *Node) SetAccountActive(ctx context.Context, target string, active bool) error {
	n.admin.Lock()
	defer n.admin.Unlock()
	o := n.snapshotOrg()
	if o == nil || len(o.RootPrivate) != 64 || target == n.Host.ID().String() {
		return errors.New("owner required; owner cannot deactivate itself")
	}
	if _, _, err := n.directMember(target); err != nil {
		return err
	}
	policy, err := verifyPolicy(o, o.Policy)
	if err != nil {
		return err
	}
	if (policy.Deactivated[target] == 0) == active {
		return nil
	}
	if policy.Deactivated == nil {
		policy.Deactivated = map[string]int64{}
	}
	if active {
		delete(policy.Deactivated, target)
	} else {
		policy.Deactivated[target] = time.Now().UnixMilli()
	}
	policy.Revision++
	policy.Epoch++
	secret := random(32)
	policy.KeyHash = keyHash(secret)
	next := sign(policy, o.RootPrivate)
	n.mu.Lock()
	n.vault.Org.PreviousSecrets = append(n.vault.Org.PreviousSecrets, n.vault.Org.Secret)
	n.vault.Org.Secret = secret
	n.vault.Org.Policy = next
	err = n.saveLocked()
	n.mu.Unlock()
	if err != nil {
		return err
	}
	if err = n.reencryptHistory(); err != nil {
		return err
	}
	if err = n.switchTopic(); err != nil {
		return err
	}
	if err = n.publishAccess(ctx); err != nil {
		return err
	}
	if !active {
		if id, err := peer.Decode(target); err == nil {
			n.Host.Network().ClosePeer(id)
		}
	}
	return nil
}
func (n *Node) normalizeStoredPacket(packet []byte) ([]byte, error) {
	o := n.snapshotOrg()
	if _, err := open(o.Secret, packet, o.ID); err == nil {
		return packet, nil
	}
	for _, old := range o.PreviousSecrets {
		plain, err := open(old, packet, o.ID)
		if err == nil {
			return seal(o.Secret, plain, o.ID)
		}
	}
	return nil, errors.New("stored history could not be decrypted")
}
func (n *Node) reencryptHistory() error {
	n.history.Lock()
	defer n.history.Unlock()
	n.mu.Lock()
	packets := append([][]byte{}, n.packets...)
	n.mu.Unlock()
	for i, p := range packets {
		next, err := n.normalizeStoredPacket(p)
		if err != nil {
			return err
		}
		packets[i] = next
	}
	if err := atomicWrite(filepath.Join(n.cfg.Dir, "history.enc"), pack(packets)); err != nil {
		return err
	}
	n.mu.Lock()
	n.packets = packets
	n.mu.Unlock()
	return nil
}
func (n *Node) stopTopic() { n.mesh.Lock(); defer n.mesh.Unlock(); n.stopTopicLocked() }
func (n *Node) stopTopicLocked() {
	n.mu.Lock()
	sub, topic := n.sub, n.topic
	n.sub = nil
	n.topic = nil
	n.mu.Unlock()
	if sub != nil {
		sub.Cancel()
	}
	if topic != nil {
		topic.Close()
	}
}
func (n *Node) switchTopic() error {
	n.mesh.Lock()
	defer n.mesh.Unlock()
	n.stopTopicLocked()
	o := n.snapshotOrg()
	if o == nil {
		return nil
	}
	name := topicName(o)
	n.ps.UnregisterTopicValidator(name)
	if err := n.ps.RegisterTopicValidator(name, func(ctx context.Context, id peer.ID, msg *pubsub.Message) bool { return n.validPacket(msg.Data) }); err != nil {
		return err
	}
	topic, err := n.ps.Join(name)
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
	n.background(func() {
		for {
			msg, err := sub.Next(n.ctx)
			if err != nil {
				return
			}
			n.accept(msg.Data, true)
		}
	})
	return nil
}

func mustPeer(raw string) peer.ID { id, _ := peer.Decode(raw); return id }
func (n *Node) needsAccessUpdate() bool {
	o := n.snapshotOrg()
	if o == nil || len(o.RootPrivate) != 64 {
		return false
	}
	r, err := verifyAccess(o.Access)
	if err != nil || !bytes.Equal(r.KeyHash, keyHash(o.Secret)) {
		return true
	}
	plain, err := open(o.Secret, r.Control, "radchat-control:"+o.ID)
	if err != nil {
		return true
	}
	var c orgControl
	if json.Unmarshal(plain, &c) != nil {
		return true
	}
	p, _ := verifyPolicy(o, o.Policy)
	old, _ := verifyPolicy(o, c.Policy)
	return old.Revision != p.Revision || len(c.Members) != len(o.Members)
}
