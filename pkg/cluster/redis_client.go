package cluster

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/redis/go-redis/v9"
)

// newRedisClient creates a standalone or cluster client from the config.
// TLS is enabled by the rediss:// scheme, or implicitly when a TLS option is set.
func newRedisClient(cfg RedisConfig) (redis.UniversalClient, error) {
	if cfg.ClusterMode {
		opts, err := redis.ParseClusterURL(cfg.URL)
		if err != nil {
			return nil, fmt.Errorf("failed to parse redis url: %w", err)
		}
		if len(opts.Addrs) == 0 {
			return nil, errors.New("redis url has no address")
		}
		opts.TLSConfig, err = applyTLSOptions(opts.TLSConfig, opts.Addrs[0], cfg)
		if err != nil {
			return nil, err
		}
		return redis.NewClusterClient(opts), nil
	}
	opts, err := redis.ParseURL(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse redis url: %w", err)
	}
	opts.TLSConfig, err = applyTLSOptions(opts.TLSConfig, opts.Addr, cfg)
	if err != nil {
		return nil, err
	}
	return redis.NewClient(opts), nil
}

func applyTLSOptions(tlsConfig *tls.Config, addr string, cfg RedisConfig) (*tls.Config, error) {
	if cfg.TLSCAFile == "" && !cfg.TLSInsecureSkipVerify {
		return tlsConfig, nil
	}
	if tlsConfig == nil {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			host = addr
		}
		tlsConfig = &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	}
	if cfg.TLSCAFile != "" {
		pem, err := os.ReadFile(cfg.TLSCAFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read redis tls ca file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no valid certificate found in redis tls ca file: %s", cfg.TLSCAFile)
		}
		tlsConfig.RootCAs = pool
	}
	tlsConfig.InsecureSkipVerify = cfg.TLSInsecureSkipVerify //nolint: gosec
	return tlsConfig, nil
}
