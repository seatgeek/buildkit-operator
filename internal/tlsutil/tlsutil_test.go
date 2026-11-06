// Copyright 2026 SeatGeek, Inc.
//
// Licensed under the terms of the Apache-2.0 license. See LICENSE file in project root for terms.

package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
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

	// Kubernetes names run to 253 characters, past a common name's 64-character limit.
	long := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63)
	longPEM, _, err := GenerateServerCert(long, time.Hour, now)
	require.NoError(t, err)
	_, err = ParseServerCertPEM(longPEM, long)
	require.NoError(t, err)
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

// withPEMHeader re-encodes a PEM certificate with a header, which CertPool.AppendCertsFromPEM ignores.
func withPEMHeader(t *testing.T, certPEM string) string {
	t.Helper()
	block, _ := pem.Decode([]byte(certPEM))
	require.NotNil(t, block)
	block.Headers = map[string]string{"Comment": "client CA"}
	return string(pem.EncodeToMemory(block))
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

func TestGenerateServerCert_ipShapedName(t *testing.T) {
	t.Parallel()

	// A legal Buildkit name that Go verifies as an IP address.
	certPEM, _, err := GenerateServerCert("10.0.0.1", time.Hour, time.Now())
	require.NoError(t, err)
	_, err = ParseServerCertPEM(certPEM, "10.0.0.1")
	require.NoError(t, err)
}

func TestParseServerCertPEM(t *testing.T) {
	t.Parallel()

	certPEM, _, err := GenerateServerCert("buildkit-a", time.Hour, time.Now())
	require.NoError(t, err)

	tests := []struct {
		name    string
		data    string
		wantErr string
	}{
		{name: "valid", data: string(certPEM)},
		{name: "wrong name", data: string(certPEM), wantErr: "buildkit-b"},
		{name: "CA", data: test.CACertPEM(), wantErr: "must not be a CA"},
		{name: "two certificates", data: string(certPEM) + string(certPEM), wantErr: "exactly one certificate"},
		{name: "garbage", data: "not pem", wantErr: "expected a PEM-encoded CERTIFICATE block"},
		{name: "empty", data: "", wantErr: "expected a PEM-encoded CERTIFICATE block"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			serverName := "buildkit-a"
			if tt.name == "wrong name" {
				serverName = "buildkit-b"
			}
			_, err := ParseServerCertPEM([]byte(tt.data), serverName)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestWriteServerFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	now := time.Now().Truncate(time.Second)
	caPEM := test.CACertPEM()

	certPEM, err := WriteServerFiles(dir, "buildkit-a", caPEM, 2*time.Hour, now)
	require.NoError(t, err)

	read := func(name string, mode os.FileMode) []byte {
		t.Helper()
		info, err := os.Stat(filepath.Join(dir, name))
		require.NoError(t, err)
		assert.Equal(t, mode, info.Mode().Perm(), name)
		data, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // test temp dir
		require.NoError(t, err)
		return data
	}
	assert.Equal(t, caPEM, string(read(ClientCAFile, 0o444)))
	assert.Equal(t, certPEM, read(CertFile, 0o444))
	keyPEM := read(KeyFile, 0o400)

	_, err = tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err, "certificate and key must form a pair")
	cert, err := ParseServerCertPEM(certPEM, "buildkit-a")
	require.NoError(t, err)
	assert.True(t, cert.NotBefore.Equal(now.Add(-ClockSkew)), "NotBefore %s", cert.NotBefore)
	assert.True(t, cert.NotAfter.Equal(now.Add(2*time.Hour)), "NotAfter %s", cert.NotAfter)

	// A rerun replaces the read-only files with a new key.
	again, err := WriteServerFiles(dir, "buildkit-a", caPEM, 2*time.Hour, now)
	require.NoError(t, err)
	assert.NotEqual(t, certPEM, again)
	assert.NotEqual(t, keyPEM, read(KeyFile, 0o400))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 3, "no temporary files may be left behind")

	// buildkitd loads the CA with CertPool.AppendCertsFromPEM, which skips blocks with headers; the file
	// holds only the validated certificate.
	headered := "issued by: tests\n" + withPEMHeader(t, caPEM)
	_, err = WriteServerFiles(dir, "buildkit-a", headered, 2*time.Hour, now)
	require.NoError(t, err)
	assert.Equal(t, caPEM, string(read(ClientCAFile, 0o444)))
	pool := x509.NewCertPool()
	assert.True(t, pool.AppendCertsFromPEM(read(ClientCAFile, 0o444)))
}

func TestWriteServerFiles_rejectsBadClientCA(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	_, err := WriteServerFiles(dir, "buildkit-a", "not pem", time.Hour, time.Now())
	require.ErrorContains(t, err, "client CA")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing may be written for a bad client CA")
}
