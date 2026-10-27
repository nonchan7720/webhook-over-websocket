package cmd

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"

	"github.com/denisbrodbeck/machineid"
	"github.com/nonchan7720/webhook-over-websocket/pkg/handler"
)

const (
	channelKeyHeader = handler.ChannelKeyHeader

	// The key and the channel ID are derived with different labels, so that the public channel ID
	// (it is the webhook URL) reveals neither the key nor the machine ID.
	channelKeyLabel       = "webhook-over-websocket/channel-key"
	machineChannelIDLabel = "webhook-over-websocket/channel-id"
	machineChannelIDBytes = 16
)

func machineHMAC(machineID, label string) []byte {
	mac := hmac.New(sha256.New, []byte(machineID))
	mac.Write([]byte(label)) //nolint: errcheck
	return mac.Sum(nil)
}

// deriveChannelKey returns the secret that binds the channels to this machine.
func deriveChannelKey(machineID string) string {
	return hex.EncodeToString(machineHMAC(machineID, channelKeyLabel))
}

// deriveMachineChannelID returns a channel ID that is fixed for this machine and cannot be guessed.
func deriveMachineChannelID(machineID string) string {
	return "m-" + hex.EncodeToString(machineHMAC(machineID, machineChannelIDLabel)[:machineChannelIDBytes])
}

// resolveChannelCredentials returns the channel ID to request ("" for a random one) and the channel key.
// The key lets this machine issue the same channel again, e.g. after a restart, without logging in.
func resolveChannelCredentials(args *clientArgs, machineID func() (string, error)) (string, string, error) {
	if args.channelID != "" && args.machineChannelID {
		return "", "", fmt.Errorf("--channel-id and --machine-channel-id cannot be used together")
	}
	id, err := machineID()
	if err != nil {
		if args.machineChannelID {
			return "", "", fmt.Errorf("failed to read the machine ID for --machine-channel-id: %w", err)
		}
		if args.channelKey == "" {
			slog.Warn("Failed to read the machine ID; the channel cannot be issued again after a restart", slog.String("error", err.Error()))
		}
	}
	channelID := args.channelID
	if args.machineChannelID {
		channelID = deriveMachineChannelID(id)
	}
	key := args.channelKey
	if key == "" && err == nil {
		key = deriveChannelKey(id)
	}
	return channelID, key, nil
}

// readMachineID returns the ID the OS assigns to this machine.
func readMachineID() (string, error) {
	return machineid.ID()
}
