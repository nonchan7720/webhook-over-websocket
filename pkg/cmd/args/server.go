package args

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/pflag"
)

type Server struct {
	Port       int    `mapstructure:"port"`
	PeerDomain string `mapstructure:"peer-domain"`

	CleanupDuration        time.Duration `mapstructure:"cleanup-duration"`
	MemberListPort         int           `mapstructure:"memberlist-port"`
	MemberlistSyncDuration time.Duration `mapstructure:"memberlist-sync-duration"`

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
	return err
}

func (args *Server) AuthEnabled() bool {
	return args.GithubClientID != ""
}
