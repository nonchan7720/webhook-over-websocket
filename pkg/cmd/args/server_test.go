package args

import (
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	flagClusterBackend = "--cluster-backend"
	flagRedisURL       = "--redis-url"
)

func parseServerArgs(t *testing.T, flags ...string) *Server {
	t.Helper()
	var args Server
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	args.BindFlags(fs)
	require.NoError(t, fs.Parse(flags))
	return &args
}

func TestServerDefaultClusterBackendIsMemberlist(t *testing.T) {
	args := parseServerArgs(t)
	assert.Equal(t, ClusterBackendMemberlist, args.ClusterBackend)
	assert.NoError(t, args.Validate())
}

func TestServerValidateClusterBackend(t *testing.T) {
	tests := []struct {
		name    string
		flags   []string
		wantErr bool
	}{
		{name: "memberlist", flags: []string{flagClusterBackend, ClusterBackendMemberlist}},
		{name: "redis with url", flags: []string{flagClusterBackend, ClusterBackendRedis, flagRedisURL, "rediss://example.com:6379"}},
		{name: "redis without url", flags: []string{flagClusterBackend, ClusterBackendRedis}, wantErr: true},
		{name: "unknown backend", flags: []string{flagClusterBackend, "etcd"}, wantErr: true},
		{
			name:    "redis with non-positive heartbeat",
			flags:   []string{flagClusterBackend, ClusterBackendRedis, flagRedisURL, "redis://example.com:6379", "--redis-heartbeat-duration", "0s"},
			wantErr: true,
		},
		{
			name:  "redis with tls options",
			flags: []string{flagClusterBackend, ClusterBackendRedis, flagRedisURL, "redis://example.com:6379", "--redis-tls-ca-file", "/etc/ca.pem", "--redis-cluster-mode"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := parseServerArgs(t, tt.flags...).Validate()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
