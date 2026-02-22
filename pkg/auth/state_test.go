package auth

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateOAuthState_Format(t *testing.T) {
	secret := []byte("test-secret")
	sessionID := uuid.NewString()
	state, err := GenerateOAuthState(secret, sessionID)
	require.NoError(t, err)
	assert.NotEmpty(t, state)
	parts := strings.SplitN(state, ".", 2)
	assert.Len(t, parts, 2, "state should have nonce and signature separated by '.'")
}

func TestValidateOAuthState_Valid(t *testing.T) {
	secret := []byte("test-secret")
	sessionID := uuid.NewString()
	state, err := GenerateOAuthState(secret, sessionID)
	require.NoError(t, err)

	parseSessionID, err := ValidateOAuthState(secret, state)
	assert.NoError(t, err)
	assert.Equal(t, sessionID, parseSessionID)
}

func TestValidateOAuthState_WrongSecret(t *testing.T) {
	sessionID := uuid.NewString()
	state, err := GenerateOAuthState([]byte("secret-a"), sessionID)
	require.NoError(t, err)

	parseSessionID, err := ValidateOAuthState([]byte("secret-b"), state)
	assert.Error(t, err)
	assert.Empty(t, parseSessionID)
}

func TestValidateOAuthState_Tampered(t *testing.T) {
	sessionID := uuid.NewString()
	secret := []byte("test-secret")
	state, err := GenerateOAuthState(secret, sessionID)
	require.NoError(t, err)

	// Tamper with the nonce part
	parts := strings.SplitN(state, ".", 2)
	tampered := "tamperednonce." + parts[1]
	parseSessionID, err := ValidateOAuthState(secret, tampered)
	assert.Error(t, err)
	assert.Empty(t, parseSessionID)
}

func TestValidateOAuthState_InvalidFormat(t *testing.T) {
	parseSessionID, err := ValidateOAuthState([]byte("secret"), "nodothere")
	assert.Error(t, err)
	assert.Empty(t, parseSessionID)
}
