package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// GenerateOAuthState creates a random nonce and signs it with the given secret,
// returning a state string suitable for use in the OAuth `state` parameter.
// The format is: base64url(nonce) + "." + base64url(HMAC-SHA256(nonce, secret))
func GenerateOAuthState(secret []byte, sessionID string) (string, error) {
	rawNonce := make([]byte, 16)
	if _, err := rand.Read(rawNonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}
	nonce := base64.RawURLEncoding.EncodeToString(rawNonce)
	rawState := fmt.Sprintf("%s:%s", sessionID, nonce)
	sig := computeHMAC(secret, []byte(rawState))
	sigPart := base64.RawURLEncoding.EncodeToString(sig)
	return rawState + "." + sigPart, nil
}

// ValidateOAuthState verifies that the given state string was produced by GenerateOAuthState
// with the same secret. Returns an error if validation fails.
func ValidateOAuthState(secret []byte, state string) (string, error) {
	parts := strings.SplitN(state, ".", 2)
	if len(parts) != 2 {
		return "", errors.New("invalid OAuth state format")
	}
	rawState := parts[0]
	expectedSig := base64.RawURLEncoding.EncodeToString(computeHMAC(secret, []byte(rawState)))
	if !hmac.Equal([]byte(parts[1]), []byte(expectedSig)) {
		return "", errors.New("OAuth state signature mismatch")
	}
	sessionID, _, found := strings.Cut(rawState, ":")
	if !found {
		return "", errors.New("invalid state format")
	}
	return sessionID, nil
}

func computeHMAC(secret, data []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write(data) //nolint: errcheck
	return mac.Sum(nil)
}
