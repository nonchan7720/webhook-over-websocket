package cluster

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/klauspost/compress/zstd"
)

// Binary format of a message published between nodes:
//
//	version(1) | flags(1) | kind(1) | uvarint len + sender | uvarint len + channel | data
//
// A binary format is used instead of JSON so that payloads are not inflated by base64,
// and data is compressed with zstd when it is large enough to be worth it. Pub/Sub messages
// are buffered per subscriber in redis, so keeping them small avoids hitting the output buffer limit.
const (
	envelopeVersion  byte = 1
	envelopeFlagZstd byte = 1 << 0

	// Data smaller than this is sent as is because compression rarely pays off.
	envelopeCompressThreshold = 1024
	// Upper bound of decompressed data, to protect against decompression bombs.
	envelopeMaxDecodedSize = 64 << 20
	// Upper bound of sender / channel IDs, which are short identifiers.
	envelopeMaxFieldSize = 1 << 10
)

type envelopeKind byte

const (
	envelopeKindNotify envelopeKind = iota
	envelopeKindRequest
	envelopeKindResponse
)

var errInvalidEnvelope = errors.New("invalid envelope")

var (
	zstdEncoder, _ = zstd.NewWriter(nil)                                                                                    //nolint: errcheck
	zstdDecoder, _ = zstd.NewReader(nil, zstd.WithDecoderMaxMemory(envelopeMaxDecodedSize), zstd.WithDecoderConcurrency(0)) //nolint: errcheck
)

// redisEnvelope wraps a message published between nodes.
type redisEnvelope struct {
	Sender  string
	Kind    envelopeKind
	Channel string
	Data    []byte
}

func encodeEnvelope(e *redisEnvelope) []byte {
	flags := byte(0)
	data := e.Data
	if len(data) >= envelopeCompressThreshold {
		if compressed := zstdEncoder.EncodeAll(data, nil); len(compressed) < len(data) {
			data = compressed
			flags |= envelopeFlagZstd
		}
	}
	buf := make([]byte, 0, 3+2*binary.MaxVarintLen64+len(e.Sender)+len(e.Channel)+len(data))
	buf = append(buf, envelopeVersion, flags, byte(e.Kind))
	buf = binary.AppendUvarint(buf, uint64(len(e.Sender)))
	buf = append(buf, e.Sender...)
	buf = binary.AppendUvarint(buf, uint64(len(e.Channel)))
	buf = append(buf, e.Channel...)
	buf = append(buf, data...)
	return buf
}

func decodeEnvelope(b []byte) (*redisEnvelope, error) {
	if len(b) < 3 {
		return nil, errInvalidEnvelope
	}
	if b[0] != envelopeVersion {
		return nil, fmt.Errorf("%w: unsupported version %d", errInvalidEnvelope, b[0])
	}
	flags := b[1]
	e := &redisEnvelope{Kind: envelopeKind(b[2])}
	rest := b[3:]
	var err error
	if e.Sender, rest, err = readString(rest); err != nil {
		return nil, err
	}
	if e.Channel, rest, err = readString(rest); err != nil {
		return nil, err
	}
	if flags&envelopeFlagZstd != 0 {
		if e.Data, err = zstdDecoder.DecodeAll(rest, nil); err != nil {
			return nil, fmt.Errorf("%w: %w", errInvalidEnvelope, err)
		}
	} else {
		e.Data = rest
	}
	return e, nil
}

func readString(b []byte) (string, []byte, error) {
	n, size := binary.Uvarint(b)
	if size <= 0 || n > envelopeMaxFieldSize {
		return "", nil, errInvalidEnvelope
	}
	end := size + int(n)
	if end > len(b) {
		return "", nil, errInvalidEnvelope
	}
	return string(b[size:end]), b[end:], nil
}
