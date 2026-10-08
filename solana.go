package radchat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

type SolanaStatus struct {
	Cluster           string `json:"cluster"`
	RPC               string `json:"rpc"`
	Connected         bool   `json:"connected"`
	Slot              uint64 `json:"slot"`
	Genesis           string `json:"genesis,omitempty"`
	Checked           int64  `json:"checked"`
	Error             string `json:"error,omitempty"`
	SettlementEnabled bool   `json:"settlementEnabled"`
	USDCMint          string `json:"usdcMint,omitempty"`
}

func solanaRPC(ctx context.Context, client *http.Client, endpoint, method string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(pack(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": []any{}})))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return errors.New("Solana RPC unavailable")
	}
	var r struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&r) != nil || len(r.Result) == 0 || string(r.Error) != "" && string(r.Error) != "null" {
		return errors.New("Solana RPC returned an error")
	}
	return json.Unmarshal(r.Result, out)
}
func SolanaHandler() http.Handler {
	var mu sync.Mutex
	var cached SolanaStatus
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if time.Since(time.UnixMilli(cached.Checked)) > 30*time.Second {
			cluster := os.Getenv("RADCHAT_SOLANA_CLUSTER")
			if cluster == "" {
				cluster = "testnet"
			}
			endpoint := "https://api.testnet.solana.com"
			expected := "4uhcVJyU9pJkvQyS88uRDiswHXSCkY3zQawwpjk2NsNY"
			mint := ""
			if cluster == "devnet" {
				endpoint = "https://api.devnet.solana.com"
				expected = "EtWTRABZaYq6iMfeYKouRu166VU2xqa1"
				mint = "4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU"
			}
			cached = SolanaStatus{Cluster: cluster, RPC: endpoint, Checked: time.Now().UnixMilli(), USDCMint: mint}
			if cluster != "testnet" && cluster != "devnet" {
				cached.Error = "Only test networks are enabled"
			} else {
				ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
				client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect rejected") }}
				err := solanaRPC(ctx, client, endpoint, "getGenesisHash", &cached.Genesis)
				if err == nil && cached.Genesis != expected {
					err = errors.New("Solana cluster identity mismatch")
				}
				if err == nil {
					err = solanaRPC(ctx, client, endpoint, "getSlot", &cached.Slot)
				}
				cancel()
				cached.Connected = err == nil
				if err != nil {
					cached.Error = "Could not verify the Solana test network"
				}
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		respond(w, 200, cached)
	})
}
