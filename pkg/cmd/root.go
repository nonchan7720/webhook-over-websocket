package cmd

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func rootCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "webhook-over-websocket",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	cmd.AddCommand(serverCommand())
	cmd.AddCommand(clientCommand())
	cmd.AddCommand(echoCommand())
	return cmd
}

func init() {
	viper.SetEnvPrefix("WOW")
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	viper.AutomaticEnv()
}
