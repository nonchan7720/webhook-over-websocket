package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/hashicorp/memberlist"
)

var _ PeerCluster = (*Memberlist)(nil)

type Memberlist struct {
	myIP         string
	serverPort   int
	peerDomain   string
	syncDuration time.Duration
	mlist        *memberlist.Memberlist
	delegate     *clusterDelegate
}

func (m *Memberlist) Start(ctx context.Context) error {
	go startAutoJoin(ctx, m.mlist, m.peerDomain, m.myIP, m.syncDuration)
	go func() {
		<-ctx.Done()
		if err := m.mlist.Shutdown(); err != nil {
			slog.Warn("failed to shutdown memberlist", slog.String("error", err.Error()))
		}
	}()
	return nil
}

func (m *Memberlist) Subscribe(fn NotifyMsgFn) {
	m.delegate.callbackNotifyMsg = fn
}

func (m *Memberlist) Broadcast(_ context.Context, msg any) error {
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal broadcast message: %w", err)
	}
	m.delegate.broadcastQueue.QueueBroadcast(&broadcaster{msg: msgBytes})
	return nil
}

func (m *Memberlist) Peers(_ context.Context) ([]string, error) {
	nodes := m.ActiveNodesWithoutSelf()
	peers := make([]string, 0, len(nodes))
	for _, node := range nodes {
		peers = append(peers, net.JoinHostPort(node.Addr.String(), strconv.Itoa(m.serverPort)))
	}
	return peers, nil
}

func (m *Memberlist) ActiveNodes() []*memberlist.Node {
	allNode := m.mlist.Members()
	nodes := make([]*memberlist.Node, 0, len(allNode))
	for _, node := range allNode {
		if node.State == memberlist.StateAlive {
			nodes = append(nodes, node)
		}
	}
	return nodes
}

func (m *Memberlist) ActiveNodesWithoutSelf() []*memberlist.Node {
	selfNodeName := m.MyNodeName()
	allNode := m.mlist.Members()
	nodes := make([]*memberlist.Node, 0, len(allNode))
	for _, node := range allNode {
		if node.Name != selfNodeName && node.State == memberlist.StateAlive {
			nodes = append(nodes, node)
		}
	}
	return nodes
}

func (m *Memberlist) MyNodeName() string {
	return m.mlist.LocalNode().Name
}

func startAutoJoin(
	ctx context.Context,
	mlist *memberlist.Memberlist,
	peerDomain string,
	myIP string,
	tickTime time.Duration,
) {
	if peerDomain == "" {
		return
	}

	ticker := time.NewTicker(tickTime)
	defer ticker.Stop()

	tryJoin := func() {
		ips, err := net.LookupIP(peerDomain)
		if err != nil {
			// NOTE: Common immediately after starting k8s
			slog.Info("DNS lookup failed, will retry", slog.String("domain", peerDomain), slog.String("error", err.Error()))
			return
		}

		var joinAddrs []string
		for _, ip := range ips {
			ipStr := ip.String()
			if ipStr != myIP {
				joinAddrs = append(joinAddrs, ipStr)
			}
		}

		if len(joinAddrs) > 0 {
			// NOTE: If idempotent and their respective IPs match, they begin clustering.
			num, err := mlist.Join(joinAddrs)
			if err != nil {
				slog.Warn("Failed to join peers", slog.String("error", err.Error()))
			} else if num > 0 {
				slog.Info("Successfully contacted peers", slog.Int("contacted_nodes", num))
			}
		}
	}

	tryJoin()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Stopping auto-join routine")
			return
		case <-ticker.C:
			tryJoin()
		}
	}
}
