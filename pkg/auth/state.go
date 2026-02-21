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
func GenerateOAuthState(secret []byte) (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}
	noncePart := base64.RawURLEncoding.EncodeToString(nonce)
	sig := computeHMAC(secret, []byte(noncePart))
	sigPart := base64.RawURLEncoding.EncodeToString(sig)
	return noncePart + "." + sigPart, nil
}

// ValidateOAuthState verifies that the given state string was produced by GenerateOAuthState
// with the same secret. Returns an error if validation fails.
func ValidateOAuthState(secret []byte, state string) error {
	parts := strings.SplitN(state, ".", 2)
	if len(parts) != 2 {
		return errors.New("invalid OAuth state format")
	}
	noncePart := parts[0]
	expectedSig := base64.RawURLEncoding.EncodeToString(computeHMAC(secret, []byte(noncePart)))
	if !hmac.Equal([]byte(parts[1]), []byte(expectedSig)) {
		return errors.New("OAuth state signature mismatch")
	}
	return nil
}

func computeHMAC(secret, data []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write(data) //nolint: errcheck
	return mac.Sum(nil)
}
