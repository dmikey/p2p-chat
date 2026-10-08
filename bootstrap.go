package radchat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	relay "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	ma "github.com/multiformats/go-multiaddr"
)

const discoveryProtocol protocol.ID = "/radchat/discovery/1.0.0"
const joinProtocol protocol.ID = "/radchat/join/1.0.0"
const historyProtocol protocol.ID = "/radchat/history/1.0.0"

func peerID(s string) (peer.ID, error) { return peer.Decode(s) }
func addresses(h host.Host) []string {
	out := []string{}
	for _, a := range h.Addrs() {
		out = append(out, a.String()+"/p2p/"+h.ID().String())
	}
	return out
}
func addrInfo(raw string) (*peer.AddrInfo, error) {
	a, err := ma.NewMultiaddr(raw)
	if err != nil {
		return nil, err
	}
	return peer.AddrInfoFromP2pAddr(a)
}
func readJSON(s network.Stream, v any) error {
	return json.NewDecoder(io.LimitReader(s, 4<<20)).Decode(v)
}
func writeJSON(s network.Stream, v any) error { return json.NewEncoder(s).Encode(v) }

type DiscoveryRequest struct {
	Tag       string   `json:"tag"`
	Addresses []string `json:"addresses"`
}
type discoveryEntry struct {
	ID        peer.ID
	Addresses []string
	Until     time.Time
}
type Bootstrap struct {
	Host    host.Host
	service *relay.Relay
	mu      sync.Mutex
	entries map[string]map[peer.ID]discoveryEntry
	limits  map[peer.ID]time.Time
}

func NewBootstrap(dir, listen string) (*Bootstrap, error) {
	v, key, err := loadVault(dir)
	if err != nil {
		return nil, err
	}
	if err = saveVault(dir, v, key); err != nil {
		return nil, err
	}
	priv, err := identityKey(v)
	if err != nil {
		return nil, err
	}
	listeners := []string{listen}
	if websocket := os.Getenv("RADCHAT_WS_P2P"); websocket != "" {
		listeners = append(listeners, websocket)
	}
	h, err := libp2p.New(libp2p.Identity(priv), libp2p.ListenAddrStrings(listeners...), libp2p.EnableNATService())
	if err != nil {
		return nil, err
	}
	resources := relay.DefaultResources()
	resources.MaxReservations = 512
	resources.MaxCircuits = 32
	// Limit every circuit; clients reconnect and synchronize from participant devices.
	registry, err := newAccessRegistry(dir)
	if err != nil {
		h.Close()
		return nil, err
	}
	service, err := relay.New(h, relay.WithResources(resources), relay.WithACL(registry))
	if err != nil {
		h.Close()
		return nil, err
	}
	b := &Bootstrap{Host: h, service: service, entries: map[string]map[peer.ID]discoveryEntry{}, limits: map[peer.ID]time.Time{}}
	h.SetStreamHandler(discoveryProtocol, b.discover)
	h.SetStreamHandler(accessProtocol, registry.handle)
	return b, nil
}
func (b *Bootstrap) Close() error { b.service.Close(); return b.Host.Close() }
func (b *Bootstrap) discover(s network.Stream) {
	defer s.Close()
	s.SetDeadline(time.Now().Add(8 * time.Second))
	var req DiscoveryRequest
	if readJSON(s, &req) != nil || len(req.Tag) != 64 || len(req.Addresses) > 12 {
		return
	}
	id := s.Conn().RemotePeer()
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	for tag, entries := range b.entries {
		for id, e := range entries {
			if now.After(e.Until) {
				delete(entries, id)
			}
		}
		if len(entries) == 0 {
			delete(b.entries, tag)
		}
	}
	for id, t := range b.limits {
		if now.Sub(t) > 5*time.Minute {
			delete(b.limits, id)
		}
	}
	if now.Sub(b.limits[id]) < time.Second || len(b.limits) >= 10000 {
		return
	}
	b.limits[id] = now
	entries := b.entries[req.Tag]
	if entries == nil {
		if len(b.entries) >= 2048 {
			return
		}
		entries = map[peer.ID]discoveryEntry{}
		b.entries[req.Tag] = entries
	}
	if len(entries) >= 256 {
		return
	}
	valid := []string{}
	for _, raw := range req.Addresses {
		info, err := addrInfo(raw)
		if err == nil && info.ID == id {
			valid = append(valid, raw)
		}
	}
	entries[id] = discoveryEntry{id, valid, now.Add(2 * time.Minute)}
	result := []string{}
	for other, e := range entries {
		if other != id {
			result = append(result, e.Addresses...)
		}
	}
	writeJSON(s, result)
}
func discoverPeers(ctx context.Context, h host.Host, bootstrap peer.ID, tag string, addrs []string) ([]string, error) {
	s, err := h.NewStream(ctx, bootstrap, discoveryProtocol)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	s.SetDeadline(time.Now().Add(8 * time.Second))
	if err = writeJSON(s, DiscoveryRequest{tag, addrs}); err != nil {
		return nil, err
	}
	var result []string
	err = readJSON(s, &result)
	if len(result) > 3072 {
		return nil, errors.New("oversized discovery response")
	}
	return result, err
}
