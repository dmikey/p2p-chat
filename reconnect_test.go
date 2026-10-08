package radchat

import (
	"context"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
	"strings"
	"testing"
	"time"
)

func TestRelayRestartRestoresCircuitWithoutRestartingAgent(t *testing.T) {
	_, auth := authorityTest(t)
	dir := t.TempDir()
	relay, e := NewBootstrap(dir, "/ip4/127.0.0.1/tcp/0")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { relay.Close() }()
	address := addresses(relay.Host)[0]
	listen := relay.Host.Addrs()[0].String()
	agent := testNode(t, auth.URL, "agent", address)
	agent.Host.SetStreamHandler(protocol.ID("/test/reconnect"), func(s network.Stream) { defer s.Close(); writeJSON(s, map[string]bool{"ok": true}) })
	sender, e := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if e != nil {
		t.Fatal(e)
	}
	defer sender.Close()
	info, e := addrInfo(address + "/p2p-circuit/p2p/" + agent.Host.ID().String())
	if e != nil {
		t.Fatal(e)
	}
	dial := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		sender.Network().ClosePeer(info.ID)
		sender.Peerstore().ClearAddrs(info.ID)
		if e := sender.Connect(network.WithAllowLimitedConn(ctx, "test"), *info); e != nil {
			return e
		}
		s, e := sender.NewStream(network.WithAllowLimitedConn(ctx, "test"), info.ID, protocol.ID("/test/reconnect"))
		if e != nil {
			return e
		}
		defer s.Close()
		if !strings.Contains(s.Conn().RemoteMultiaddr().String(), "/p2p-circuit") {
			t.Error("test did not use a relay circuit")
		}
		var result struct {
			OK bool `json:"ok"`
		}
		return readJSON(s, &result)
	}
	for attempt := 0; attempt < 30; attempt++ {
		agent.networkStep()
		e = dial()
		if e == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if e != nil {
		t.Fatal(e)
	}
	relay.Close()
	relay, e = NewBootstrap(dir, listen)
	if e != nil {
		t.Fatal(e)
	}
	sender.Network().ClosePeer(agent.Host.ID())
	for attempt := 0; attempt < 30; attempt++ {
		agent.networkStep()
		e = dial()
		if e == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if e != nil {
		t.Fatalf("agent failed to reacquire relay reservation: %v", e)
	}
}
