package auth

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateOAuthState_Format(t *testing.T) {
	secret := []byte("test-secret")
	state, err := GenerateOAuthState(secret)
	require.NoError(t, err)
	assert.NotEmpty(t, state)
	parts := strings.SplitN(state, ".", 2)
	assert.Len(t, parts, 2, "state should have nonce and signature separated by '.'")
}

func TestValidateOAuthState_Valid(t *testing.T) {
	secret := []byte("test-secret")
	state, err := GenerateOAuthState(secret)
	require.NoError(t, err)

	err = ValidateOAuthState(secret, state)
	assert.NoError(t, err)
}

func TestValidateOAuthState_WrongSecret(t *testing.T) {
	state, err := GenerateOAuthState([]byte("secret-a"))
	require.NoError(t, err)

	err = ValidateOAuthState([]byte("secret-b"), state)
	assert.Error(t, err)
}

func TestValidateOAuthState_Tampered(t *testing.T) {
	secret := []byte("test-secret")
	state, err := GenerateOAuthState(secret)
	require.NoError(t, err)

	// Tamper with the nonce part
	parts := strings.SplitN(state, ".", 2)
	tampered := "tamperednonce." + parts[1]
	err = ValidateOAuthState(secret, tampered)
	assert.Error(t, err)
}

func TestValidateOAuthState_InvalidFormat(t *testing.T) {
	err := ValidateOAuthState([]byte("secret"), "nodothere")
	assert.Error(t, err)
}
