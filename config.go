package radchat

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

func BootstrapAddresses(b *Bootstrap) []string {
	if public := os.Getenv("RADCHAT_PUBLIC_P2P"); public != "" {
		return []string{public + "/p2p/" + b.Host.ID().String()}
	}
	return addresses(b.Host)
}
func FetchBootstrap(endpoint string) (string, error) {
	// Reuse HTTPS/loopback validation and redirects policy from the authority client.
	a, err := NewAuthClient(endpoint, nil)
	if err != nil {
		return "", err
	}
	res, err := a.client.Get(a.URL + "/api/bootstrap")
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", errors.New("bootstrap endpoint unavailable")
	}
	var p struct {
		Addresses []string `json:"addresses"`
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 65536)).Decode(&p); err != nil {
		return "", err
	}
	for _, raw := range p.Addresses {
		if _, err := addrInfo(raw); err == nil {
			return raw, nil
		}
	}
	return "", errors.New("no usable bootstrap address")
}
func WriteAgentToken(dir, token string) error {
	return atomicWrite(filepath.Join(dir, "a2a.token"), []byte(token+"\n"))
}
func RestoreDevice(dir string, data []byte, password string) error {
	if _, err := os.Stat(filepath.Join(dir, "vault.enc")); !os.IsNotExist(err) {
		return errors.New("restore requires a new directory; existing device will not be overwritten")
	}
	v, err := RestoreRecovery(data, password)
	if err != nil {
		return err
	}
	key := random(32)
	if err = atomicWrite(filepath.Join(dir, "device.key"), key); err != nil {
		return err
	}
	return saveVault(dir, v, key)
}

type DeviceSettings struct {
	Auth      string `json:"auth"`
	Bootstrap string `json:"bootstrap"`
}

func LoadDeviceSettings(dir string) (DeviceSettings, error) {
	var s DeviceSettings
	_, key, err := loadVault(dir)
	if err != nil {
		return s, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "settings.enc"))
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	plain, err := open(key, b, "radchat-settings-v1")
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(plain, &s)
	return s, err
}
func SaveDeviceSettings(dir string, s DeviceSettings) error {
	v, key, err := loadVault(dir)
	if err != nil {
		return err
	}
	if v.Org != nil {
		return errors.New("organization authority cannot be changed on an enrolled device")
	}
	if _, err = NewAuthClient(s.Auth, nil); err != nil {
		return err
	}
	if s.Bootstrap != "" {
		if _, err = addrInfo(s.Bootstrap); err != nil {
			return err
		}
	}
	b, err := seal(key, pack(s), "radchat-settings-v1")
	if err != nil {
		return err
	}
	if err = atomicWrite(filepath.Join(dir, "settings.enc"), b); err != nil {
		return err
	}
	err = os.Remove(filepath.Join(dir, "authority.pin"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
