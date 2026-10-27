package handler

import (
	"encoding/binary"
	"errors"
)

// Binary format of a TunnelMessage relayed between nodes:
//
//	flags(1) | uvarint len + req_id | payload
//
// JSON is kept for the WebSocket protocol with clients, but between nodes the payload is
// sent as raw bytes to avoid the base64 overhead of JSON.
const (
	relayFlagEOF byte = 1 << 0
	// Upper bound of the request ID, which is a UUID.
	relayMaxReqIDSize = 1 << 10
)

var errInvalidRelayMessage = errors.New("invalid relay message")

func encodeRelayMessage(msg TunnelMessage) []byte {
	flags := byte(0)
	if msg.EOF {
		flags |= relayFlagEOF
	}
	buf := make([]byte, 0, 1+binary.MaxVarintLen64+len(msg.ReqID)+len(msg.Payload))
	buf = append(buf, flags)
	buf = binary.AppendUvarint(buf, uint64(len(msg.ReqID)))
	buf = append(buf, msg.ReqID...)
	buf = append(buf, msg.Payload...)
	return buf
}

func decodeRelayMessage(b []byte) (TunnelMessage, error) {
	if len(b) < 2 {
		return TunnelMessage{}, errInvalidRelayMessage
	}
	n, size := binary.Uvarint(b[1:])
	if size <= 0 || n > relayMaxReqIDSize {
		return TunnelMessage{}, errInvalidRelayMessage
	}
	start := 1 + size
	end := start + int(n)
	if end > len(b) {
		return TunnelMessage{}, errInvalidRelayMessage
	}
	msg := TunnelMessage{
		ReqID: string(b[start:end]),
		EOF:   b[0]&relayFlagEOF != 0,
	}
	if len(b) > end {
		msg.Payload = b[end:]
	}
	return msg, nil
}
