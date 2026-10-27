package args

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/pflag"
)

const (
	ClusterBackendMemberlist = "memberlist"
	ClusterBackendRedis      = "redis"
)

type Server struct {
	Port       int    `mapstructure:"port"`
	PeerDomain string `mapstructure:"peer-domain"`

	CleanupDuration        time.Duration `mapstructure:"cleanup-duration"`
	MemberListPort         int           `mapstructure:"memberlist-port"`
	MemberlistSyncDuration time.Duration `mapstructure:"memberlist-sync-duration"`

	ClusterBackend             string        `mapstructure:"cluster-backend"`
	RedisURL                   string        `mapstructure:"redis-url"`
	RedisKeyPrefix             string        `mapstructure:"redis-key-prefix"`
	RedisHeartbeatDuration     time.Duration `mapstructure:"redis-heartbeat-duration"`
	RedisClusterMode           bool          `mapstructure:"redis-cluster-mode"`
	RedisTLSCAFile             string        `mapstructure:"redis-tls-ca-file"`
	RedisTLSInsecureSkipVerify bool          `mapstructure:"redis-tls-insecure-skip-verify"`

	LogLevel  string `mapstructure:"log-level"`
	LogFormat string `mapstructure:"log-format"`

	GithubClientID     string `mapstructure:"github-client-id"`
	GithubClientSecret string `mapstructure:"github-client-secret"`
	GithubOrg          string `mapstructure:"github-org"`
	JwtSigningKey      string `mapstructure:"jwt-signing-key"`
}

func (args *Server) BindFlags(flag *pflag.FlagSet) {
	flag.IntVarP(&args.Port, "port", "p", 8080, "server port")
	flag.StringVar(&args.PeerDomain, "peer-domain", "", "peer domain name")
	flag.DurationVar(&args.CleanupDuration, "cleanup-duration", 5*time.Minute, "channel_id cleanup duration")
	flag.IntVar(&args.MemberListPort, "memberlist-port", 7946, "memberlist port(gossip protocol)")
	flag.DurationVar(&args.MemberlistSyncDuration, "memberlist-sync-duration", 5*time.Second, "channel_id cleanup duration")
	flag.StringVar(&args.ClusterBackend, "cluster-backend", ClusterBackendMemberlist, "backend for sharing state between replicas (memberlist|redis)")
	flag.StringVar(&args.RedisURL, "redis-url", "", "redis connection url (redis://[user:password@]host:port[/db], rediss:// for TLS)")
	flag.StringVar(&args.RedisKeyPrefix, "redis-key-prefix", "webhook-over-websocket", "prefix of redis keys and pub/sub channel")
	flag.DurationVar(&args.RedisHeartbeatDuration, "redis-heartbeat-duration", 5*time.Second, "interval for refreshing channel ownership in redis")
	flag.BoolVar(&args.RedisClusterMode, "redis-cluster-mode", false, "connect to redis in cluster mode")
	flag.StringVar(&args.RedisTLSCAFile, "redis-tls-ca-file", "", "CA certificate file to verify the redis server (enables TLS)")
	flag.BoolVar(&args.RedisTLSInsecureSkipVerify, "redis-tls-insecure-skip-verify", false, "skip verifying the redis server certificate (enables TLS)")
	flag.StringVar(&args.LogLevel, "log-level", "INFO", "log level")
	flag.StringVar(&args.LogFormat, "log-format", "text", "log format")
	flag.StringVar(&args.GithubClientID, "github-client-id", "", "GitHub OAuth App client ID (enables authentication)")
	flag.StringVar(&args.GithubClientSecret, "github-client-secret", "", "GitHub OAuth App client secret")
	flag.StringVar(&args.GithubOrg, "github-org", "", "required GitHub organization for access (optional)")
	flag.StringVar(&args.JwtSigningKey, "jwt-signing-key", "", "secret key for signing JWT tokens (required when auth is enabled)")
}

func (args *Server) Validate() error {
	var err error
	authEnabled := args.AuthEnabled()
	if authEnabled && args.GithubClientSecret == "" {
		err = errors.Join(err, fmt.Errorf("--github-client-secret is required when --github-client-id is set"))
	}
	if authEnabled && args.JwtSigningKey == "" {
		err = errors.Join(err, fmt.Errorf("--jwt-signing-key is required when --github-client-id is set"))
	}
	switch args.ClusterBackend {
	case ClusterBackendMemberlist:
	case ClusterBackendRedis:
		if args.RedisURL == "" {
			err = errors.Join(err, fmt.Errorf("--redis-url is required when --cluster-backend is %s", ClusterBackendRedis))
		}
	default:
		err = errors.Join(err, fmt.Errorf("--cluster-backend must be one of %s, %s", ClusterBackendMemberlist, ClusterBackendRedis))
	}
	return err
}

func (args *Server) AuthEnabled() bool {
	return args.GithubClientID != ""
}
