package cluster

import (
	"log/slog"

	"github.com/hashicorp/memberlist"
)

type clusterDelegate struct {
	broadcastQueue    *memberlist.TransmitLimitedQueue
	callbackNotifyMsg NotifyMsgFn
}

func (d *clusterDelegate) NodeMeta(limit int) []byte              { return nil }
func (d *clusterDelegate) LocalState(join bool) []byte            { return nil }
func (d *clusterDelegate) MergeRemoteState(buf []byte, join bool) {}

func (d *clusterDelegate) NotifyMsg(b []byte) {
	msg, err := parseNotifyMsg(b)
	if err != nil {
		slog.Error(err.Error())
	} else {
		if d.callbackNotifyMsg != nil {
			d.callbackNotifyMsg(msg)
		}
	}
}

func (d *clusterDelegate) GetBroadcasts(overhead, limit int) [][]byte {
	return d.broadcastQueue.GetBroadcasts(overhead, limit)
}

// Broadcast の実装
type broadcaster struct {
	msg []byte
}

func (b *broadcaster) Invalidates(other memberlist.Broadcast) bool { return false }
func (b *broadcaster) Message() []byte                             { return b.msg }
func (b *broadcaster) Finished()                                   {}
