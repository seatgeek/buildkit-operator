// Copyright 2026 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

// Package tlsutil generates per-instance buildkitd server certificates and validates client CAs.
package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"time"
)

const pemCertificate = "CERTIFICATE"

// ClockSkew is how far apart the clocks of the machines issuing and checking a certificate may be.
const ClockSkew = 5 * time.Minute

// parseOneCertPEM parses data that must hold exactly one PEM certificate and nothing else.
func parseOneCertPEM(data []byte) (*x509.Certificate, error) {
	block, rest := pem.Decode(data)
	if block == nil || block.Type != pemCertificate {
		return nil, errors.New("expected a PEM-encoded CERTIFICATE block")
	}
	if next, _ := pem.Decode(rest); next != nil {
		return nil, errors.New("expected exactly one certificate")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing certificate: %w", err)
	}
	return cert, nil
}

// ParseCACertPEM checks that data holds exactly one PEM certificate usable as a CA.
func ParseCACertPEM(data string) (*x509.Certificate, error) { //nolint:unparam // other packages use the certificate
	cert, err := parseOneCertPEM([]byte(data))
	if err != nil {
		return nil, err
	}
	if !cert.IsCA || !cert.BasicConstraintsValid {
		return nil, errors.New("certificate is not a CA (basicConstraints CA:TRUE required)")
	}
	// No key usage means no restriction; any other set must allow signing certificates, or Go rejects
	// client certificates it issued.
	if cert.KeyUsage != 0 && cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, errors.New("CA certificate's key usage does not include certificate signing (keyCertSign)")
	}
	// Likewise an extended key usage, if present, must allow client authentication: Go checks it
	// along the whole chain.
	if (len(cert.ExtKeyUsage) > 0 || len(cert.UnknownExtKeyUsage) > 0) &&
		!slices.Contains(cert.ExtKeyUsage, x509.ExtKeyUsageClientAuth) && !slices.Contains(cert.ExtKeyUsage, x509.ExtKeyUsageAny) {
		return nil, errors.New("CA certificate's extended key usage does not allow client authentication (clientAuth)")
	}
	// Go refuses to verify any chain through a certificate with a critical extension it doesn't
	// understand, so no client could authenticate.
	if len(cert.UnhandledCriticalExtensions) > 0 {
		return nil, errors.New("CA certificate has critical extensions Go cannot process")
	}

	return cert, nil
}

// GenerateServerCert returns a self-signed ECDSA P-256 certificate and key, both PEM-encoded, valid
// for serverName. Clients pin the certificate itself as their trust root. It is not a CA, so its key
// cannot sign other certificates.
func GenerateServerCert(serverName string, validity time.Duration, now time.Time) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generating key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("generating serial: %w", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: serverName},
		DNSNames:              []string{serverName},
		NotBefore:             now.Add(-ClockSkew),
		NotAfter:              now.Add(validity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("creating certificate: %w", err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshaling key: %w", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: pemCertificate, Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		nil
}

// ParseServerCertPEM checks that data holds exactly one PEM server certificate valid for serverName.
func ParseServerCertPEM(data []byte, serverName string) (*x509.Certificate, error) {
	cert, err := parseOneCertPEM(data)
	if err != nil {
		return nil, err
	}
	if cert.IsCA {
		return nil, errors.New("server certificate must not be a CA")
	}
	if err := cert.VerifyHostname(serverName); err != nil {
		return nil, err
	}
	return cert, nil
}

// Names of the files WriteServerFiles creates.
const (
	CertFile     = "tls.crt"
	KeyFile      = "tls.key"
	ClientCAFile = "ca.crt"
)

// WriteServerFiles writes buildkitd's TLS material into dir: a new server key and certificate for
// serverName, valid from now for validFor, and the client CA. It returns the certificate PEM. Each
// file is replaced atomically, so a rerun (after a pod sandbox restart) can overwrite read-only files.
func WriteServerFiles(dir, serverName, clientCA string, validFor time.Duration, now time.Time) ([]byte, error) {
	if validFor <= 0 {
		return nil, fmt.Errorf("validity must be positive, got %s", validFor)
	}
	if _, err := ParseCACertPEM(clientCA); err != nil {
		return nil, fmt.Errorf("client CA: %w", err)
	}
	certPEM, keyPEM, err := GenerateServerCert(serverName, validFor, now)
	if err != nil {
		return nil, err
	}
	for _, f := range []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{ClientCAFile, []byte(clientCA), 0o444},
		{CertFile, certPEM, 0o444},
		{KeyFile, keyPEM, 0o400},
	} {
		if err := writeFileAtomic(filepath.Join(dir, f.name), f.data, f.mode); err != nil {
			return nil, err
		}
	}
	return certPEM, nil
}

func writeFileAtomic(name string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(name), "."+filepath.Base(name)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // gone after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close() //nolint:errcheck,gosec
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), name)
}
