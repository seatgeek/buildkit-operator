// Copyright 2026 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

// Package tlsutil generates per-instance buildkitd server certificates and validates client CAs.
package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"time"
)

const pemCertificate = "CERTIFICATE"

// ClockSkew tolerates a client CA issued on a machine whose clock runs a little ahead.
const ClockSkew = 5 * time.Minute

// ParseCACertPEM checks that data holds exactly one PEM certificate usable as a CA.
func ParseCACertPEM(data string) (*x509.Certificate, error) {
	block, rest := pem.Decode([]byte(data))
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
		NotBefore:             now.Add(-5 * time.Minute), // tolerate clock skew between operator and clients
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

// Fingerprint returns the hex SHA-256 of the first PEM certificate's DER bytes.
func Fingerprint(certPEM []byte) (string, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != pemCertificate {
		return "", errors.New("expected a PEM-encoded CERTIFICATE block")
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:]), nil
}

// ValidateServerKeyPair checks that the key matches the certificate and that the certificate is valid
// for serverName.
func ValidateServerKeyPair(certPEM, keyPEM []byte, serverName string) error {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("certificate and key do not form a pair: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fmt.Errorf("parsing certificate: %w", err)
	}
	return leaf.VerifyHostname(serverName)
}

// NotAfter returns the expiry of the first PEM certificate.
func NotAfter(certPEM []byte) (time.Time, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != pemCertificate {
		return time.Time{}, errors.New("expected a PEM-encoded CERTIFICATE block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing certificate: %w", err)
	}
	return cert.NotAfter, nil
}
