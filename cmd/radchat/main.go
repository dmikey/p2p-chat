package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	chat "github.com/radninja/radchat"
)

var version = "dev"

func env(key, fallback string) string {
	if s := os.Getenv(key); s != "" {
		return s
	}
	return fallback
}
func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	if len(os.Args) < 2 {
		fmt.Println("Rad Chat " + version + "\n\nUsage: radchat node | relay | restore | version\n\nnode: local human/agent device and browser UI\nrelay: self-hosted Circuit Relay v2, bootstrap and email authority\nrestore: recover a device before starting the node\n\nUse radchat <command> -help for options.")
		return nil
	}
	mode := os.Args[1]
	if mode == "healthcheck" {
		if len(os.Args) != 3 {
			return fmt.Errorf("healthcheck requires a URL")
		}
		client := &http.Client{Timeout: 3 * time.Second}
		res, err := client.Get(os.Args[2])
		if err != nil {
			return err
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			return fmt.Errorf("unhealthy: HTTP %d", res.StatusCode)
		}
		return nil
	}
	if mode == "version" {
		fmt.Println(version)
		return nil
	}
	if mode == "restore" {
		return restore(os.Args[2:])
	}
	if mode != "node" && mode != "relay" {
		return fmt.Errorf("unknown command %q", mode)
	}
	f := flag.NewFlagSet(mode, flag.ContinueOnError)
	defaultDir := filepath.Join(os.Getenv("HOME"), ".radchat", mode)
	dir := f.String("data", env("RADCHAT_DATA", defaultDir), "device or relay data directory (never put node data on Rad Ninja)")
	httpDefault := "127.0.0.1:8787"
	p2pDefault := "/ip4/0.0.0.0/tcp/0"
	if mode == "relay" {
		httpDefault = "127.0.0.1:8788"
		p2pDefault = "/ip4/0.0.0.0/tcp/4001"
	}
	httpAddr := f.String("http", env("RADCHAT_HTTP", httpDefault), "HTTP bind; node must use loopback; relay behind HTTPS reverse proxy")
	p2pAddr := f.String("p2p", env("RADCHAT_P2P", p2pDefault), "libp2p listening multiaddress")
	bootstrap := f.String("bootstrap", env("RADCHAT_BOOTSTRAP", ""), "relay multiaddress including /p2p/<peer-id>; defaults to discovery from auth URL")
	auth := f.String("auth", env("RADCHAT_AUTH", "https://chat.therad.ninja"), "email authority and bootstrap URL; change for a self-hosted relay")
	kind := f.String("kind", env("RADCHAT_KIND", "human"), "human or agent (must match invite)")
	dev := f.Bool("dev-auth", false, "relay only: return email codes in loopback HTTP responses; NEVER public")
	if err := f.Parse(os.Args[2:]); err != nil {
		return err
	}
	if mode == "node" || *dev {
		host, _, err := net.SplitHostPort(*httpAddr)
		if err != nil || !(host == "localhost" || net.ParseIP(host).IsLoopback()) {
			return fmt.Errorf("local nodes and dev auth must bind to loopback")
		}
	}
	if mode == "node" && *dev {
		return fmt.Errorf("--dev-auth is only for local relay testing")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var handler http.Handler
	var closeFn func() error
	if mode == "relay" {
		if !*dev && (os.Getenv("SENDGRID_API_KEY") == "" || (os.Getenv("SENDGRID_FROM_EMAIL") == "" && os.Getenv("EMAIL_FROM") == "")) {
			return fmt.Errorf("relay requires SENDGRID_API_KEY and SENDGRID_FROM_EMAIL (or --dev-auth for loopback tests)")
		}
		relay, err := chat.NewBootstrap(*dir, *p2pAddr)
		if err != nil {
			return err
		}
		closeFn = relay.Close
		authority, err := chat.NewAuthority(*dir, *dev)
		if err != nil {
			relay.Close()
			return err
		}
		mux := http.NewServeMux()
		mux.Handle("/api/auth/", authority.Handler())
		mux.HandleFunc("/api/bootstrap", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" {
				w.WriteHeader(405)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			json.NewEncoder(w).Encode(map[string]any{"peer": relay.Host.ID().String(), "addresses": chat.BootstrapAddresses(relay), "version": version})
		})
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
		mux.Handle("/", chat.PublicHandler())
		handler = mux
		log.Printf("Relay peer: %s", relay.Host.ID())
		for _, a := range chat.BootstrapAddresses(relay) {
			log.Printf("Relay address: %s", a)
		}
	} else {
		if *bootstrap == "" {
			found, err := chat.FetchBootstrap(*auth)
			if err != nil {
				log.Printf("Bootstrap unavailable: %v; existing devices can still use known peers", err)
			} else {
				*bootstrap = found
			}
		}
		node, err := chat.NewNode(ctx, chat.Config{Dir: *dir, Listen: *p2pAddr, Bootstrap: *bootstrap, AuthURL: *auth, Kind: *kind})
		if err != nil {
			return err
		}
		closeFn = node.Close
		handler = node.Handler()
		log.Printf("Device peer: %s", node.Host.ID())
		log.Printf("Open http://%s", *httpAddr)
		if *kind == "agent" {
			log.Printf("A2A bearer token is in %s (0600); read it locally", filepath.Join(*dir, "a2a.token"))
			if err := chat.WriteAgentToken(*dir, node.APIToken()); err != nil {
				node.Close()
				return err
			}
		}
	}
	defer closeFn()
	srv := &http.Server{Addr: *httpAddr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	listener, err := net.Listen("tcp", *httpAddr)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	case err := <-done:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}
func restore(args []string) error {
	f := flag.NewFlagSet("restore", flag.ContinueOnError)
	dir := f.String("data", filepath.Join(os.Getenv("HOME"), ".radchat", "node"), "new device directory; must not contain an existing vault")
	file := f.String("file", "", "recovery export")
	if err := f.Parse(args); err != nil {
		return err
	}
	password := os.Getenv("RADCHAT_RECOVERY_PASSPHRASE")
	if password == "" {
		return fmt.Errorf("set RADCHAT_RECOVERY_PASSPHRASE for restore (avoid command-line secrets)")
	}
	data, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	if len(data) > 4<<20 {
		return fmt.Errorf("recovery export too large")
	}
	if err = chat.RestoreDevice(*dir, data, password); err != nil {
		return err
	}
	fmt.Println("Device restored. Stop the old node before starting this identity on another device.")
	return nil
}
