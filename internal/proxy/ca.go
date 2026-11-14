package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// LoadCA reads the certificate authority called name from dir, creating it
// on first run. The authority can vouch for host and nothing else, so a
// leaked key cannot be used to impersonate other sites. LoadCA also returns
// the path of the certificate, which the client has to trust.
func LoadCA(dir, name, host string) (ca tls.Certificate, certPath string, err error) {
	certPath = filepath.Join(dir, name+".pem")
	keyPath := filepath.Join(dir, name+"-key.pem")
	if _, err := os.Stat(certPath); errors.Is(err, fs.ErrNotExist) {
		if err := createCA(certPath, keyPath, host); err != nil {
			return ca, "", err
		}
	}
	if ca, err = tls.LoadX509KeyPair(certPath, keyPath); err != nil {
		return ca, "", err
	}
	ca.Leaf, err = x509.ParseCertificate(ca.Certificate[0])
	return ca, certPath, err
}

func createCA(certPath, keyPath, host string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return err
	}
	template := &x509.Certificate{
		SerialNumber:                serial,
		Subject:                     pkix.Name{CommonName: "AutoClaude Local CA for " + host},
		NotBefore:                   time.Now().Add(-time.Hour),
		NotAfter:                    time.Now().AddDate(10, 0, 0),
		IsCA:                        true,
		BasicConstraintsValid:       true,
		MaxPathLenZero:              true,
		KeyUsage:                    x509.KeyUsageCertSign,
		PermittedDNSDomains:         []string{host},
		PermittedDNSDomainsCritical: true,
	}
	cert, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return err
	}
	return os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert}), 0o644)
}
