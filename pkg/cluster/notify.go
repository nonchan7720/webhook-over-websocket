package cluster

import "encoding/json"

type NotifyMsgFn func(msg *NotifyMsg)

type NotifyMsgType string

const (
	NotifyMsgType_AuthMessage NotifyMsgType = "auth_message"
)

type NotifyMsg struct {
	MsgType NotifyMsgType   `json:"msg_type"`
	Raw     json.RawMessage `json:"-"`
}

// parseNotifyMsg decodes the message type and keeps the original bytes for DecodeNotifyMsg.
// NOTE: NotifyMsg must not implement json.Unmarshaler, because it is embedded in concrete
// messages (e.g. AuthMessage) and the promoted method would skip decoding their own fields.
func parseNotifyMsg(b []byte) (*NotifyMsg, error) {
	var msg NotifyMsg
	if err := json.Unmarshal(b, &msg); err != nil {
		return nil, err
	}
	msg.Raw = b
	return &msg, nil
}

func DecodeNotifyMsg[T any](msg *NotifyMsg) (*T, error) {
	var value T
	if err := json.Unmarshal(msg.Raw, &value); err != nil {
		return nil, err
	}
	return &value, nil
}

type AuthMessage struct {
	NotifyMsg
	SessionID string `json:"session_id"`
	Token     string `json:"token"`
}
