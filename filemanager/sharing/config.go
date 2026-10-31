// Package sharing runs an explicitly enabled, read-only desktop file service.
package sharing

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"filemanager/api"
	fileindex "filemanager/remoteclient"
)

type Config struct {
	NextRootID  int64      `json:"next_root_id,omitempty"`
	Name        string     `json:"name"`
	Listen      string     `json:"listen"`
	Key         string     `json:"key"`
	Roots       []api.Root `json:"roots"`
	Certificate []byte     `json:"certificate"`
	PrivateKey  []byte     `json:"private_key"`
}

func NewConfig() (Config, error) {
	name, _ := os.Hostname()
	c := Config{Name: name, Listen: ":17891", Key: rand.Text() + rand.Text()}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return c, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return c, err
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "MyGo file sharing"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(10, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, priv)
	if err != nil {
		return c, err
	}
	key, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return c, err
	}
	c.Certificate = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	c.PrivateKey = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})
	return c, nil
}
func (c Config) TLS() (tls.Certificate, string, error) {
	cert, err := tls.X509KeyPair(c.Certificate, c.PrivateKey)
	if err != nil {
		return cert, "", err
	}
	sum := sha256.Sum256(cert.Certificate[0])
	return cert, hex.EncodeToString(sum[:]), nil
}
func Load(path string) (Config, error) {
	var c Config
	b, e := os.ReadFile(path)
	if e != nil {
		return c, e
	}
	e = json.Unmarshal(b, &c)
	return c, e
}
func Save(path string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".sharing-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}
func (c Config) Invitation(address string) (string, error) {
	_, pin, err := c.TLS()
	if err != nil {
		return "", err
	}
	if _, err = fileindex.PeerAddress(address, pin); err != nil {
		return "", err
	}
	u := url.URL{Scheme: "mygo", Host: address, RawQuery: url.Values{"name": {c.Name}, "key": {c.Key}, "fingerprint": {pin}}.Encode()}
	return u.String(), nil
}
func ParseInvitation(text string) (name, address, key string, err error) {
	u, e := url.Parse(text)
	if e != nil || u.Scheme != "mygo" || u.User != nil || u.Path != "" || u.Fragment != "" {
		err = errors.New("无效的 MyGo 连接邀请")
		return
	}
	name = u.Query().Get("name")
	key = u.Query().Get("key")
	if len(key) < 32 || len(key) > 256 {
		err = errors.New("无效的共享访问码")
		return
	}
	address, err = fileindex.PeerAddress(u.Host, u.Query().Get("fingerprint"))
	return
}
