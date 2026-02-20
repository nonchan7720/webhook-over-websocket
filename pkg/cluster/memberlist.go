package cluster

import (
	"context"
	"log/slog"
	"net"
	"time"

	"github.com/hashicorp/memberlist"
)

type Memberlist struct {
	myIP  string
	mlist *memberlist.Memberlist
}

func (m *Memberlist) Start(ctx context.Context, peerDomain string, tickTime time.Duration) {
	go startAutoJoin(ctx, m.mlist, peerDomain, m.myIP, tickTime)
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
