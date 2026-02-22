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
	assert.True(t, claims.ExpiresAt.After(time.Now()))
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
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := tok.SignedString(testSecret)
	require.NoError(t, err)

	_, err = ParseToken(testSecret, tokenString)
	assert.Error(t, err)
}
