package cluster

import (
	"fmt"
	"os"

	"github.com/google/uuid"
	"github.com/hashicorp/memberlist"
)

func Config(port int, myIP string) *memberlist.Config {
	const localhost = "127.0.0.1"
	mConfig := memberlist.DefaultLANConfig()
	hostname := os.Getenv("HOSTNAME")
	if hostname == "" {
		hostname = "node-" + uuid.New().String()[:8]
	}
	mConfig.Name = hostname
	mConfig.BindPort = port
	if myIP != localhost {
		mConfig.BindAddr = myIP
	}
	return mConfig
}

func SetUp(port int, myIP string) (*Memberlist, error) {
	mConfig := Config(port, myIP)
	mlist, err := memberlist.Create(mConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create memberlist: %w", err)
	}
	return &Memberlist{mlist: mlist, myIP: myIP}, nil
}
