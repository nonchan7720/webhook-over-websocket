package cluster

import (
	"encoding/json"
	"log/slog"

	"github.com/hashicorp/memberlist"
)

var broadcastQueue *memberlist.TransmitLimitedQueue

type clusterDelegate struct {
	CallbackNotifyMsg func(msg *NotifyMsg)
}

func (d *clusterDelegate) NodeMeta(limit int) []byte              { return nil }
func (d *clusterDelegate) LocalState(join bool) []byte            { return nil }
func (d *clusterDelegate) MergeRemoteState(buf []byte, join bool) {}

func (d *clusterDelegate) NotifyMsg(b []byte) {
	var msg NotifyMsg
	if err := json.Unmarshal(b, &msg); err != nil {
		slog.Error(err.Error())
	} else {
		if d.CallbackNotifyMsg != nil {
			d.CallbackNotifyMsg(&msg)
		}
	}
}

func (d *clusterDelegate) GetBroadcasts(overhead, limit int) [][]byte {
	return broadcastQueue.GetBroadcasts(overhead, limit)
}

// Broadcast の実装
type broadcaster struct {
	msg []byte
}

func (b *broadcaster) Invalidates(other memberlist.Broadcast) bool { return false }
func (b *broadcaster) Message() []byte                             { return b.msg }
func (b *broadcaster) Finished()                                   {}

func SendBroadcastQueue[T any](msg *T) {
	msgBytes, _ := json.Marshal(msg) //nolint: errcheck,errchkjson
	broadcastQueue.QueueBroadcast(&broadcaster{msg: msgBytes})
}
