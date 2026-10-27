package cluster

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/memberlist"
)

type MemberlistConfig struct {
	// NodeName defaults to $HOSTNAME (or a random name when unset).
	NodeName string
	// BindPort is the gossip protocol port.
	BindPort int
	MyIP     string
	// ServerPort is the HTTP port of the server, used to build peer addresses.
	ServerPort int
	PeerDomain string
	// SyncDuration is the interval for retrying to join peers resolved from PeerDomain.
	SyncDuration time.Duration
}

func Config(nodeName string, port int, myIP string) *memberlist.Config {
	const localhost = "127.0.0.1"
	mConfig := memberlist.DefaultLANConfig()
	if nodeName == "" {
		nodeName = os.Getenv("HOSTNAME")
	}
	if nodeName == "" {
		nodeName = "node-" + uuid.New().String()[:8]
	}
	mConfig.Name = nodeName
	mConfig.BindPort = port
	if myIP != localhost {
		mConfig.BindAddr = myIP
	}
	mConfig.Logger = log.New(&slogWriter{}, "", log.LstdFlags)
	return mConfig
}

func NewMemberlist(cfg MemberlistConfig) (*Memberlist, error) {
	mConfig := Config(cfg.NodeName, cfg.BindPort, cfg.MyIP)
	m := &Memberlist{
		myIP:         cfg.MyIP,
		serverPort:   cfg.ServerPort,
		peerDomain:   cfg.PeerDomain,
		syncDuration: cfg.SyncDuration,
		delegate:     &clusterDelegate{},
	}
	mConfig.Delegate = m.delegate
	mlist, err := memberlist.Create(mConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create memberlist: %w", err)
	}
	m.mlist = mlist
	m.delegate.broadcastQueue = &memberlist.TransmitLimitedQueue{
		NumNodes:       func() int { return mlist.NumMembers() },
		RetransmitMult: mConfig.RetransmitMult,
	}
	return m, nil
}
