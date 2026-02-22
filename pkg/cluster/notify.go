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

func (m *NotifyMsg) UnmarshalJSON(data []byte) error {
	type Alias NotifyMsg
	var alias Alias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	*m = (NotifyMsg)(alias)
	m.Raw = data
	return nil
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
