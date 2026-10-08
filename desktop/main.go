package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"

	chat "github.com/radninja/radchat"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type Desktop struct {
	mu           sync.Mutex
	ctx          context.Context
	node         *chat.Node
	dir          string
	auth         string
	bootstrap    string
	startupError string
}

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		panic(err)
	}
	dir := os.Getenv("RADCHAT_DATA")
	if dir == "" {
		dir = filepath.Join(home, ".radchat", "desktop")
	}
	d := &Desktop{dir: dir, auth: "https://chat.therad.ninja"}
	settings, err := chat.LoadDeviceSettings(dir)
	if err != nil {
		d.startupError = err.Error()
	} else if settings.Auth != "" {
		d.auth = settings.Auth
		d.bootstrap = settings.Bootstrap
	}
	if a := os.Getenv("RADCHAT_AUTH"); a != "" {
		d.auth = a
	}
	if b := os.Getenv("RADCHAT_BOOTSTRAP"); b != "" {
		d.bootstrap = b
	}
	err = wails.Run(&options.App{Title: "Rad Chat", Width: 1380, Height: 880, MinWidth: 390, MinHeight: 640, BackgroundColour: &options.RGBA{R: 245, G: 247, B: 245, A: 255}, AssetServer: &assetserver.Options{Assets: chat.Frontend()}, OnStartup: func(ctx context.Context) {
		d.ctx = ctx
		go func() { d.mu.Lock(); defer d.mu.Unlock(); d.connectLocked() }()
	}, OnShutdown: func(ctx context.Context) {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.node != nil {
			d.node.Close()
		}
	}, Bind: []interface{}{d}})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func (d *Desktop) connectLocked() {
	bootstrap := d.bootstrap
	if bootstrap == "" {
		if found, err := chat.FetchBootstrap(d.auth); err == nil {
			bootstrap = found
		}
	}
	node, err := chat.NewNode(d.ctx, chat.Config{Dir: d.dir, Listen: "/ip4/0.0.0.0/tcp/0", Bootstrap: bootstrap, AuthURL: d.auth, Kind: "human"})
	if err != nil {
		d.startupError = "Connect to an available relay to get started: " + err.Error()
		return
	}
	d.node = node
	d.startupError = ""
}
func (d *Desktop) Configure(auth, bootstrap string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.node != nil && d.node.State()["org"] != nil {
		return errors.New("use a new device directory to change this organization's email authority")
	}
	if err := chat.SaveDeviceSettings(d.dir, chat.DeviceSettings{Auth: auth, Bootstrap: bootstrap}); err != nil {
		return err
	}
	if d.node != nil {
		d.node.Close()
		d.node = nil
	}
	d.auth = auth
	d.bootstrap = bootstrap
	d.connectLocked()
	if d.node == nil {
		return errors.New(d.startupError)
	}
	return nil
}
func (d *Desktop) ConnectionSettings() map[string]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return map[string]string{"auth": d.auth, "bootstrap": d.bootstrap, "error": d.startupError}
}
func (d *Desktop) Call(path, body string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.node == nil {
		if path == "/api/state" {
			b, _ := json.Marshal(map[string]any{"org": nil, "startupError": d.startupError})
			return string(b), nil
		}
		return "", errors.New("connect to a relay using Connection settings")
	}
	allowed := map[string]bool{"/api/services": true, "/api/economy": true, "/api/runner": true, "/api/state": true, "/api/messages": true, "/api/auth/request": true, "/api/auth/verify": true, "/api/org/create": true, "/api/org/join": true, "/api/invites": true, "/api/org/policy": true, "/api/accounts": true}
	if !allowed[path] {
		return "", errors.New("unsupported operation")
	}
	method := "GET"
	if body != "" {
		method = "POST"
	}
	r := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+d.node.APIToken())
	w := httptest.NewRecorder()
	d.node.Handler().ServeHTTP(w, r)
	res := w.Result()
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 {
		var e struct{ Error string }
		json.Unmarshal(data, &e)
		return "", errors.New(e.Error)
	}
	return string(data), nil
}
func (d *Desktop) SaveRecovery(password string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.node == nil {
		return errors.New("device not connected")
	}
	data, err := d.node.Export(password)
	if err != nil {
		return err
	}
	path, err := runtime.SaveFileDialog(d.ctx, runtime.SaveDialogOptions{Title: "Save encrypted device recovery", DefaultFilename: "radchat.recovery", Filters: []runtime.FileFilter{{DisplayName: "Rad Chat recovery", Pattern: "*.recovery"}}})
	if err != nil {
		return err
	}
	if path == "" {
		return errors.New("export canceled")
	}
	return os.WriteFile(path, data, 0600)
}
