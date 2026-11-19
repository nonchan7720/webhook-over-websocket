package cluster

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTLSMiniredis starts a TLS-enabled miniredis with a self-signed certificate and
// returns it together with the path of the CA (= the certificate itself) PEM file.
func newTLSMiniredis(t *testing.T) (*miniredis.Miniredis, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))

	mr, err := miniredis.RunTLS(&tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS12,
	})
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	return mr, caFile
}

func startRedis(t *testing.T, cfg RedisConfig) error {
	t.Helper()
	r, err := NewRedis(cfg)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return r.Start(ctx)
}

func TestRedisTLSWithCAFile(t *testing.T) {
	mr, caFile := newTLSMiniredis(t)
	err := startRedis(t, RedisConfig{
		URL:               "rediss://" + mr.Addr(),
		TLSCAFile:         caFile,
		HeartbeatInterval: time.Second,
	})
	assert.NoError(t, err)
}

func TestRedisTLSInsecureSkipVerify(t *testing.T) {
	mr, _ := newTLSMiniredis(t)
	err := startRedis(t, RedisConfig{
		URL:                   "rediss://" + mr.Addr(),
		TLSInsecureSkipVerify: true,
		HeartbeatInterval:     time.Second,
	})
	assert.NoError(t, err)
}

func TestRedisTLSOptionsEnableTLSWithoutRedissScheme(t *testing.T) {
	mr, caFile := newTLSMiniredis(t)
	err := startRedis(t, RedisConfig{
		URL:               "redis://" + mr.Addr(),
		TLSCAFile:         caFile,
		HeartbeatInterval: time.Second,
	})
	assert.NoError(t, err)
}

func TestRedisTLSFailsWithUntrustedCertificate(t *testing.T) {
	mr, _ := newTLSMiniredis(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, err := NewRedis(RedisConfig{
		URL:               "rediss://" + mr.Addr(),
		HeartbeatInterval: time.Second,
	})
	require.NoError(t, err)
	assert.Error(t, r.Start(ctx))
}

func TestNewRedisCAFileNotFound(t *testing.T) {
	_, err := NewRedis(RedisConfig{
		URL:       "rediss://127.0.0.1:6379",
		TLSCAFile: filepath.Join(t.TempDir(), "missing.pem"),
	})
	assert.Error(t, err)
}

func TestNewRedisInvalidCAFile(t *testing.T) {
	caFile := filepath.Join(t.TempDir(), "invalid.pem")
	require.NoError(t, os.WriteFile(caFile, []byte("not a pem"), 0o600))
	_, err := NewRedis(RedisConfig{URL: "rediss://127.0.0.1:6379", TLSCAFile: caFile})
	assert.Error(t, err)
}

func TestRedisClusterMode(t *testing.T) {
	mr := miniredis.RunT(t)
	newNode := func() *Redis {
		r, err := NewRedis(RedisConfig{
			URL:               "redis://" + mr.Addr(),
			ClusterMode:       true,
			KeyPrefix:         "test",
			HeartbeatInterval: time.Second,
		})
		require.NoError(t, err)
		return r
	}
	owner := newNode()
	receiver := newNode()
	ctx := context.Background()

	requests := make(chan string, 1)
	owner.OnRequest(func(from, channelID string, _ []byte) { requests <- channelID })
	startTestRedis(t, owner)
	startTestRedis(t, receiver)

	require.NoError(t, owner.RegisterChannel(ctx, testChannelID, ""))
	require.NoError(t, owner.ClaimChannel(ctx, testChannelID))
	require.NoError(t, receiver.ForwardRequest(ctx, testChannelID, []byte(`{}`)))
	select {
	case channelID := <-requests:
		assert.Equal(t, testChannelID, channelID)
	case <-time.After(3 * time.Second):
		t.Fatal("owner did not get the forwarded request in cluster mode")
	}
}
