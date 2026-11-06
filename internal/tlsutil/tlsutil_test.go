// Copyright 2026 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seatgeek/buildkit-operator/internal/test"
)

func TestGenerateServerCert(t *testing.T) {
	t.Parallel()

	now := time.Now()
	certPEM, keyPEM, err := GenerateServerCert("buildkit-abc", time.Hour, now)
	require.NoError(t, err)

	_, err = tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err, "certificate and key must form a usable pair")

	block, _ := pem.Decode(certPEM)
	require.NotNil(t, block)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	assert.False(t, cert.IsCA, "the server key must not be able to sign other certificates")

	roots := x509.NewCertPool()
	roots.AddCert(cert)
	_, err = cert.Verify(x509.VerifyOptions{DNSName: "buildkit-abc", Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	require.NoError(t, err, "a client pinning the certificate must accept it for its server name")

	_, err = cert.Verify(x509.VerifyOptions{DNSName: "other-buildkit", Roots: roots})
	require.Error(t, err, "the certificate must not be valid for another instance's name")

	assert.WithinDuration(t, now.Add(time.Hour), cert.NotAfter, time.Second)
}

func TestParseCACertPEM(t *testing.T) {
	t.Parallel()

	caPEM := test.CACertPEM()
	serverPEM, _, err := GenerateServerCert("server", time.Hour, time.Now())
	require.NoError(t, err)

	tests := []struct {
		name    string
		data    string
		wantErr string
	}{
		{name: "valid CA", data: caPEM},
		{name: "garbage", data: "not pem", wantErr: "expected a PEM-encoded CERTIFICATE block"},
		{name: "private key block", data: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte{1}})), wantErr: "expected a PEM-encoded CERTIFICATE block"},
		{name: "two certificates", data: caPEM + caPEM, wantErr: "exactly one certificate"},
		{name: "leaf certificate", data: leafCertPEM(t), wantErr: "not a CA"},
		{name: "CA without certificate signing", data: caCertPEM(t, x509.KeyUsageDigitalSignature), wantErr: "keyCertSign"},
		{name: "CA without key usage", data: caCertPEM(t, 0)},
		{name: "CA limited to server auth", data: caCertPEMWithEKU(t, x509.ExtKeyUsageServerAuth), wantErr: "clientAuth"},
		{name: "CA allowing client auth", data: caCertPEMWithEKU(t, x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth)},
		{name: "CA allowing any usage", data: caCertPEMWithEKU(t, x509.ExtKeyUsageAny)},
		{name: "CA with unknown critical extension", data: caCertPEMWithCriticalExtension(t), wantErr: "critical extensions"},
		{name: "server certificate", data: string(serverPEM), wantErr: "not a CA"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := ParseCACertPEM(tt.data)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func leafCertPEM(t *testing.T) string {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "leaf"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestFingerprint(t *testing.T) {
	t.Parallel()

	certPEM, _, err := GenerateServerCert("a", time.Hour, time.Now())
	require.NoError(t, err)
	block, _ := pem.Decode(certPEM)

	got, err := Fingerprint(certPEM)
	require.NoError(t, err)
	sum := sha256.Sum256(block.Bytes)
	assert.Equal(t, hex.EncodeToString(sum[:]), got)

	_, err = Fingerprint([]byte("not pem"))
	require.Error(t, err)
}

func TestValidateServerKeyPair(t *testing.T) {
	t.Parallel()

	certPEM, keyPEM, err := GenerateServerCert("buildkit-a", time.Hour, time.Now())
	require.NoError(t, err)
	_, otherKey, err := GenerateServerCert("buildkit-a", time.Hour, time.Now())
	require.NoError(t, err)

	require.NoError(t, ValidateServerKeyPair(certPEM, keyPEM, "buildkit-a"))
	require.ErrorContains(t, ValidateServerKeyPair(certPEM, otherKey, "buildkit-a"), "do not form a pair")
	require.ErrorContains(t, ValidateServerKeyPair(certPEM, nil, "buildkit-a"), "do not form a pair")
	require.Error(t, ValidateServerKeyPair(certPEM, keyPEM, "buildkit-b"), "certificate must cover the instance name")
}

func caCertPEM(t *testing.T, usage x509.KeyUsage) string {
	t.Helper()
	return caCertPEMFull(t, usage, nil)
}

func caCertPEMWithEKU(t *testing.T, eku ...x509.ExtKeyUsage) string {
	t.Helper()
	return caCertPEMFull(t, x509.KeyUsageCertSign, eku)
}

func caCertPEMFull(t *testing.T, usage x509.KeyUsage, eku []x509.ExtKeyUsage, extra ...pkix.Extension) string {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "ca"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              usage,
		ExtKeyUsage:           eku,
		BasicConstraintsValid: true,
		IsCA:                  true,
		ExtraExtensions:       extra,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// caCertPEMWithCriticalExtension returns a CA marking an extension Go doesn't implement as critical.
func caCertPEMWithCriticalExtension(t *testing.T) string {
	t.Helper()
	ext := pkix.Extension{Id: asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1}, Critical: true, Value: []byte{0x05, 0x00}}
	return caCertPEMFull(t, x509.KeyUsageCertSign, nil, ext)
}

func TestNotAfter(t *testing.T) {
	t.Parallel()

	now := time.Now().Truncate(time.Second)
	certPEM, _, err := GenerateServerCert("a", 2*time.Hour, now)
	require.NoError(t, err)

	got, err := NotAfter(certPEM)
	require.NoError(t, err)
	assert.True(t, got.Equal(now.Add(2*time.Hour)), "got %s", got)

	_, err = NotAfter([]byte("not pem"))
	require.Error(t, err)
}
