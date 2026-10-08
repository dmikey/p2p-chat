package radchat

import (
	"fmt"
	"strings"
	"testing"
)

func TestContributorAddressLimitPreservesRelay(t *testing.T) {
	var addresses []string
	for i := 0; i < 40; i++ {
		addresses = append(addresses, fmt.Sprintf("/ip4/172.18.%d.1/tcp/3333/p2p/worker", i))
	}
	relay := "/dns4/chat.therad.ninja/tcp/443/wss/p2p/bootstrap/p2p-circuit/p2p/worker"
	addresses = append(addresses, relay)
	bounded := boundedPeerAddresses(addresses)
	if len(bounded) != 16 || bounded[0] != relay {
		t.Fatalf("advertisement loses reachable relay or exceeds verification limit: %v", bounded)
	}
	if strings.Contains(addresses[0], "p2p-circuit") {
		t.Fatal("address selection mutated the caller's list")
	}
	if got := boundedPeerAddresses([]string{relay}); len(got) != 1 || got[0] != relay {
		t.Fatal("single-route contributors must retain their route")
	}
}
