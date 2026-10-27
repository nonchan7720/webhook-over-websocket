package cluster

import (
	"bytes"
	"crypto/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnvelopeRoundTripSmallDataIsNotCompressed(t *testing.T) {
	in := &redisEnvelope{Sender: testNodeID, Kind: envelopeKindRequest, Channel: testChannelID, Data: []byte("hello")}
	encoded := encodeEnvelope(in)
	assert.Zero(t, encoded[1]&envelopeFlagZstd, "small data must not be compressed")

	out, err := decodeEnvelope(encoded)
	require.NoError(t, err)
	assert.Equal(t, in, out)
}

func TestEnvelopeRoundTripLargeCompressibleData(t *testing.T) {
	data := []byte(strings.Repeat(`{"event":"push","repository":"example"}`, 1000))
	in := &redisEnvelope{Sender: testNodeID, Kind: envelopeKindResponse, Data: data}
	encoded := encodeEnvelope(in)
	assert.NotZero(t, encoded[1]&envelopeFlagZstd, "large compressible data must be compressed")
	assert.Less(t, len(encoded), len(data)/2)

	out, err := decodeEnvelope(encoded)
	require.NoError(t, err)
	assert.Equal(t, in, out)
}

func TestEnvelopeIncompressibleDataIsSentAsIs(t *testing.T) {
	data := make([]byte, 64*1024)
	_, err := rand.Read(data)
	require.NoError(t, err)
	in := &redisEnvelope{Sender: testNodeID, Kind: envelopeKindRequest, Channel: testChannelID, Data: data}
	encoded := encodeEnvelope(in)
	assert.Zero(t, encoded[1]&envelopeFlagZstd, "compression must be skipped when it does not shrink the data")

	out, err := decodeEnvelope(encoded)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(data, out.Data))
}

func TestEnvelopeRoundTripEmptyFields(t *testing.T) {
	in := &redisEnvelope{Kind: envelopeKindNotify}
	encoded := encodeEnvelope(in)
	out, err := decodeEnvelope(encoded)
	require.NoError(t, err)
	assert.Equal(t, envelopeKindNotify, out.Kind)
	assert.Empty(t, out.Sender)
	assert.Empty(t, out.Channel)
	assert.Empty(t, out.Data)
}

func TestDecodeEnvelopeInvalid(t *testing.T) {
	valid := encodeEnvelope(&redisEnvelope{Sender: testNodeID, Kind: envelopeKindRequest, Channel: testChannelID, Data: []byte("x")})

	tests := map[string][]byte{
		"empty":           nil,
		"unknown version": append([]byte{99}, valid[1:]...),
		"truncated":       valid[:4],
		"bad zstd":        {envelopeVersion, envelopeFlagZstd, byte(envelopeKindRequest), 0, 0, 1, 2, 3},
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := decodeEnvelope(input)
			assert.Error(t, err)
		})
	}
}

const testNodeID = "node-1"
