package radchat

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	lc "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"golang.org/x/crypto/argon2"
)

var b64 = base64.RawURLEncoding

func random(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}
func token() string { return b64.EncodeToString(random(24)) }
func emailHash(email string) string {
	s := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return hex.EncodeToString(s[:])
}
func orgID(root []byte) string { s := sha256.Sum256(root); return hex.EncodeToString(s[:]) }
func pack(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

type Signed struct {
	Payload   json.RawMessage `json:"payload"`
	Signature []byte          `json:"signature"`
}

func sign(v any, key ed25519.PrivateKey) Signed { b := pack(v); return Signed{b, ed25519.Sign(key, b)} }
func (s Signed) verify(key []byte, v any) error {
	if len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, s.Payload, s.Signature) {
		return errors.New("invalid signature")
	}
	return json.Unmarshal(s.Payload, v)
}
func seal(key, plain []byte, scope string) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := random(g.NonceSize())
	return g.Seal(nonce, nonce, plain, []byte(scope)), nil
}
func open(key, data []byte, scope string) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(data) < g.NonceSize() {
		return nil, errors.New("short ciphertext")
	}
	return g.Open(nil, data[:g.NonceSize()], data[g.NonceSize():], []byte(scope))
}
func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

type Certificate struct {
	Org           string `json:"org"`
	Peer          string `json:"peer"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	EncryptionKey []byte `json:"encryptionKey"`
}
type Invitation struct {
	Org       string   `json:"org"`
	Name      string   `json:"name"`
	Root      []byte   `json:"root"`
	EmailHash string   `json:"emailHash"`
	Kind      string   `json:"kind"`
	ID        string   `json:"id"`
	Expires   int64    `json:"expires"`
	Owner     []string `json:"owner"`
}
type Org struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Root            []byte            `json:"root"`
	RootPrivate     []byte            `json:"rootPrivate,omitempty"`
	Secret          []byte            `json:"secret"`
	Member          Signed            `json:"member"`
	Used            map[string]string `json:"used,omitempty"`
	Members         []Signed          `json:"members"`
	Policy          Signed            `json:"policy"`
	Access          Signed            `json:"access"`
	PreviousSecrets [][]byte          `json:"previousSecrets,omitempty"`
}
type Vault struct {
	Bootstrap string `json:"bootstrap,omitempty"`
	Authority []byte `json:"authority,omitempty"`
	AuthURL   string `json:"authURL,omitempty"`
	Identity  []byte `json:"identity"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Org       *Org   `json:"org,omitempty"`
	APIToken  string `json:"apiToken"`
}

func loadVault(dir string) (*Vault, []byte, error) {
	keyPath := filepath.Join(dir, "device.key")
	path := filepath.Join(dir, "vault.enc")
	key, err := os.ReadFile(keyPath)
	if os.IsNotExist(err) {
		if _, e := os.Stat(path); !os.IsNotExist(e) {
			return nil, nil, errors.New("device key missing; restore recovery export")
		}
		key = random(32)
		if err = atomicWrite(keyPath, key); err != nil {
			return nil, nil, err
		}
	} else if err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		priv, _, err := lc.GenerateEd25519Key(rand.Reader)
		if err != nil {
			return nil, nil, err
		}
		raw, err := lc.MarshalPrivateKey(priv)
		if err != nil {
			return nil, nil, err
		}
		return &Vault{Identity: raw, APIToken: token(), Kind: "human"}, key, nil
	}
	if err != nil {
		return nil, nil, err
	}
	plain, err := open(key, data, "radchat-vault-v1")
	if err != nil {
		return nil, nil, err
	}
	var v Vault
	err = json.Unmarshal(plain, &v)
	return &v, key, err
}
func saveVault(dir string, v *Vault, key []byte) error {
	data, err := seal(key, pack(v), "radchat-vault-v1")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(dir, "vault.enc"), data)
}

type Recovery struct {
	Version int    `json:"version"`
	Salt    []byte `json:"salt"`
	Data    []byte `json:"data"`
}

func ExportRecovery(v *Vault, password string) ([]byte, error) {
	if len(password) < 16 {
		return nil, errors.New("use a recovery passphrase of at least 16 characters")
	}
	salt := random(16)
	key := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	data, err := seal(key, pack(v), "radchat-recovery-v1")
	return pack(Recovery{1, salt, data}), err
}
func RestoreRecovery(data []byte, password string) (*Vault, error) {
	var r Recovery
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	if r.Version != 1 || len(r.Salt) != 16 || len(r.Data) > 4<<20 {
		return nil, errors.New("invalid recovery export")
	}
	key := argon2.IDKey([]byte(password), r.Salt, 3, 64*1024, 2, 32)
	plain, err := open(key, r.Data, "radchat-recovery-v1")
	if err != nil {
		return nil, errors.New("incorrect passphrase or damaged export")
	}
	var v Vault
	err = json.Unmarshal(plain, &v)
	if err != nil {
		return nil, err
	}
	if _, err = lc.UnmarshalPrivateKey(v.Identity); err != nil {
		return nil, err
	}
	v.APIToken = token()
	return &v, nil
}
func verifyMember(o *Org, s Signed, id peer.ID) (Certificate, error) {
	var c Certificate
	err := s.verify(o.Root, &c)
	if err != nil {
		return c, err
	}
	if c.Org != o.ID || c.Peer != id.String() || (c.Kind != "human" && c.Kind != "agent") || len(c.Name) > 80 || len(c.EncryptionKey) != 32 {
		return c, errors.New("membership mismatch")
	}
	return c, nil
}
func decodeInvite(raw string) (Signed, Invitation, error) {
	var s Signed
	var inv Invitation
	b, err := b64.DecodeString(raw)
	if err != nil || len(b) > 16*1024 {
		return s, inv, errors.New("invalid invite")
	}
	if err = json.Unmarshal(b, &s); err != nil {
		return s, inv, err
	}
	if err = json.Unmarshal(s.Payload, &inv); err != nil {
		return s, inv, err
	}
	if err = s.verify(inv.Root, &inv); err != nil {
		return s, inv, err
	}
	if inv.Org != orgID(inv.Root) || inv.Expires < time.Now().Unix() || inv.Expires > time.Now().Add(8*24*time.Hour).Unix() || len(inv.Owner) == 0 || (inv.Kind != "human" && inv.Kind != "agent") {
		return s, inv, errors.New("invite expired or invalid")
	}
	return s, inv, nil
}
