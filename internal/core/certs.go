package core

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
)

// CertFiles are the on-disk cert/key of a domain.
type CertFiles struct {
	Cert string
	Key  string
}

// EnsureSelfSigned creates (or renews when < 30 days left) a self-signed
// ECDSA certificate for domain and returns the file paths.
func EnsureSelfSigned(certDir, domain string) (CertFiles, error) {
	safe := strings.Map(func(r rune) rune {
		if r == '.' || r == '-' || r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == ':' {
			return r
		}
		return '_'
	}, domain)
	if safe == "" {
		safe = "default"
	}
	files := CertFiles{Cert: filepath.Join(certDir, safe+".crt"), Key: filepath.Join(certDir, safe+".key")}
	if pemData, err := os.ReadFile(files.Cert); err == nil {
		if blk, _ := pem.Decode(pemData); blk != nil {
			if c, err := x509.ParseCertificate(blk.Bytes); err == nil && time.Until(c.NotAfter) > 30*24*time.Hour {
				if _, err := os.Stat(files.Key); err == nil {
					return files, nil
				}
			}
		}
	}
	if err := os.MkdirAll(certDir, 0o700); err != nil {
		return files, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return files, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: domain},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(2, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(domain); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else if domain != "" {
		tmpl.DNSNames = []string{domain}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return files, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return files, err
	}
	if err := os.WriteFile(files.Cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return files, err
	}
	if err := os.WriteFile(files.Key, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return files, err
	}
	return files, nil
}

// CertInfo reads expiry information for diagnostics.
func CertInfo(path, domain, mode string) (agentproto.CertStatus, bool) {
	pemData, err := os.ReadFile(path)
	if err != nil {
		return agentproto.CertStatus{}, false
	}
	blk, _ := pem.Decode(pemData)
	if blk == nil {
		return agentproto.CertStatus{}, false
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return agentproto.CertStatus{}, false
	}
	return agentproto.CertStatus{Domain: domain, Mode: mode, NotAfter: c.NotAfter, Issuer: c.Issuer.CommonName}, true
}
