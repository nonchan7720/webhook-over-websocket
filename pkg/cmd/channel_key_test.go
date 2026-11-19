package cmd

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testMachineID  = "4c4c4544-0042-3010-8058-b4c04f4d4e31"
	testFixedIDApp = "my-app"
)

// Same rule as the server: 1 to 128 characters of [A-Za-z0-9._-].
var validChannelIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

func fixedMachineID(id string, err error) func() (string, error) {
	return func() (string, error) { return id, err }
}

func TestDeriveChannelKeyAndIDAreStableAndIndependent(t *testing.T) {
	key := deriveChannelKey(testMachineID)
	id := deriveMachineChannelID(testMachineID)

	assert.Equal(t, key, deriveChannelKey(testMachineID), "the key must be stable on the machine")
	assert.Equal(t, id, deriveMachineChannelID(testMachineID), "the ID must be stable on the machine")
	assert.NotEqual(t, key, deriveChannelKey("another-machine"))
	assert.NotEqual(t, id, deriveMachineChannelID("another-machine"))
	assert.NotContains(t, key, testMachineID, "the machine ID must not be exposed")
	assert.NotContains(t, id, testMachineID, "the machine ID must not be exposed")
	assert.NotContains(t, key, id, "the public channel ID must not reveal the key")
	assert.Regexp(t, validChannelIDPattern, id)
}

func TestResolveChannelCredentials(t *testing.T) {
	tests := []struct {
		name      string
		args      clientArgs
		machineID func() (string, error)
		wantID    string
		wantKey   string
		wantErr   bool
	}{
		{
			name:      "random channel with the machine key",
			machineID: fixedMachineID(testMachineID, nil),
			wantKey:   deriveChannelKey(testMachineID),
		},
		{
			name:      "fixed channel with the machine key",
			args:      clientArgs{channelID: testFixedIDApp},
			machineID: fixedMachineID(testMachineID, nil),
			wantID:    testFixedIDApp,
			wantKey:   deriveChannelKey(testMachineID),
		},
		{
			name:      "machine channel",
			args:      clientArgs{machineChannelID: true},
			machineID: fixedMachineID(testMachineID, nil),
			wantID:    deriveMachineChannelID(testMachineID),
			wantKey:   deriveChannelKey(testMachineID),
		},
		{
			name:      "explicit key takes precedence",
			args:      clientArgs{channelID: testFixedIDApp, channelKey: "shared-secret"},
			machineID: fixedMachineID(testMachineID, nil),
			wantID:    testFixedIDApp,
			wantKey:   "shared-secret",
		},
		{
			name:      "no machine ID: no key",
			args:      clientArgs{channelID: testFixedIDApp},
			machineID: fixedMachineID("", errors.New("unavailable")),
			wantID:    testFixedIDApp,
		},
		{
			name:      "machine channel needs the machine ID",
			args:      clientArgs{machineChannelID: true},
			machineID: fixedMachineID("", errors.New("unavailable")),
			wantErr:   true,
		},
		{
			name:      "channel ID and machine channel are exclusive",
			args:      clientArgs{channelID: testFixedIDApp, machineChannelID: true},
			machineID: fixedMachineID(testMachineID, nil),
			wantErr:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, key, err := resolveChannelCredentials(&tt.args, tt.machineID)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, id)
			assert.Equal(t, tt.wantKey, key)
		})
	}
}

func TestGetNewChannelSendsChannelKey(t *testing.T) {
	var gotKey, gotID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get(channelKeyHeader)
		gotID = r.URL.Query().Get("channel_id")
		_, _ = w.Write([]byte(`{"channel_id":"my-app"}`)) //nolint: errcheck
	}))
	t.Cleanup(srv.Close)

	id, err := getNewChannel(srv.URL, "", testFixedIDApp, "secret")
	require.NoError(t, err)
	assert.Equal(t, testFixedIDApp, id)
	assert.Equal(t, testFixedIDApp, gotID)
	assert.Equal(t, "secret", gotKey)
}
