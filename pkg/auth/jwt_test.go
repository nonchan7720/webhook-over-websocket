package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testSecret = []byte("test-secret-key")

func TestIssueSessionToken(t *testing.T) {
	token, err := IssueSessionToken(testSecret, "octocat")
	require.NoError(t, err)
	assert.NotEmpty(t, token)

	claims, err := ParseToken(testSecret, token)
	require.NoError(t, err)
	assert.Equal(t, "octocat", claims.Subject)
	assert.Equal(t, TokenTypeSession, claims.TokenType)
	assert.True(t, claims.ExpiresAt.After(time.Now()))
}

func TestIssueChannelToken(t *testing.T) {
	channelID := "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
	token, err := IssueChannelToken(testSecret, channelID)
	require.NoError(t, err)
	assert.NotEmpty(t, token)

	claims, err := ParseToken(testSecret, token)
	require.NoError(t, err)
	assert.Equal(t, channelID, claims.Subject)
	assert.Equal(t, TokenTypeChannel, claims.TokenType)
}

func TestParseToken_InvalidSignature(t *testing.T) {
	token, err := IssueSessionToken(testSecret, "octocat")
	require.NoError(t, err)

	_, err = ParseToken([]byte("wrong-secret"), token)
	assert.Error(t, err)
}

func TestParseToken_ExpiredToken(t *testing.T) {
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "octocat",
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)),
		},
		TokenType: TokenTypeSession,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := tok.SignedString(testSecret)
	require.NoError(t, err)

	_, err = ParseToken(testSecret, tokenString)
	assert.Error(t, err)
}

func TestValidateChannelToken_Valid(t *testing.T) {
	channelID := "test-channel-id"
	token, err := IssueChannelToken(testSecret, channelID)
	require.NoError(t, err)

	err = ValidateChannelToken(testSecret, token, channelID)
	assert.NoError(t, err)
}

func TestValidateChannelToken_WrongChannelID(t *testing.T) {
	token, err := IssueChannelToken(testSecret, "channel-a")
	require.NoError(t, err)

	err = ValidateChannelToken(testSecret, token, "channel-b")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "subject does not match")
}

func TestValidateChannelToken_WrongTokenType(t *testing.T) {
	// Use a session token where a channel token is expected
	token, err := IssueSessionToken(testSecret, "some-channel-id")
	require.NoError(t, err)

	err = ValidateChannelToken(testSecret, token, "some-channel-id")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid token type")
}
