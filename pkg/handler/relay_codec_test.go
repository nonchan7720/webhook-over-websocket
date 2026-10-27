package handler

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayCodecRoundTrip(t *testing.T) {
	tests := map[string]TunnelMessage{
		"payload": {ReqID: testReqID, Payload: []byte("POST /webhook/ch HTTP/1.1\r\n\r\n{\"a\":1}")},
		"eof":     {ReqID: testReqID, EOF: true},
		"binary":  {ReqID: "req-2", Payload: []byte{0x00, 0xff, 0x10}},
	}
	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			out, err := decodeRelayMessage(encodeRelayMessage(in))
			require.NoError(t, err)
			assert.Equal(t, in.ReqID, out.ReqID)
			assert.Equal(t, in.EOF, out.EOF)
			assert.Equal(t, string(in.Payload), string(out.Payload))
		})
	}
}

func TestRelayCodecHasNoBase64Overhead(t *testing.T) {
	payload := make([]byte, 30*1024)
	encoded := encodeRelayMessage(TunnelMessage{ReqID: testReqID, Payload: payload})
	assert.Less(t, len(encoded), len(payload)+64)
}

func TestDecodeRelayMessageInvalid(t *testing.T) {
	for name, input := range map[string][]byte{
		"empty":     nil,
		"truncated": {0, 10, 'a'},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeRelayMessage(input)
			assert.Error(t, err)
		})
	}
}

const testReqID = "req-1"
