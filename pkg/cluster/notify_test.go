package cluster

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseAndDecodeAuthMessage(t *testing.T) {
	b, err := json.Marshal(&AuthMessage{
		NotifyMsg: NotifyMsg{MsgType: NotifyMsgType_AuthMessage},
		SessionID: testSessionID,
		Token:     testToken,
	})
	require.NoError(t, err)

	msg, err := parseNotifyMsg(b)
	require.NoError(t, err)
	assert.Equal(t, NotifyMsgType_AuthMessage, msg.MsgType)

	auth, err := DecodeNotifyMsg[AuthMessage](msg)
	require.NoError(t, err)
	assert.Equal(t, NotifyMsgType_AuthMessage, auth.MsgType)
	assert.Equal(t, testSessionID, auth.SessionID)
	assert.Equal(t, testToken, auth.Token)
}

func TestParseNotifyMsgInvalidJSON(t *testing.T) {
	_, err := parseNotifyMsg([]byte("not json"))
	assert.Error(t, err)
}

const (
	testSessionID = "session-1"
	testToken     = "token-1"
)
