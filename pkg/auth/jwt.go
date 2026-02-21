package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// TokenTypeSession is the token type for session tokens (issued after GitHub auth).
	TokenTypeSession = "session"
	// TokenTypeChannel is the token type for channel tokens (issued per channel_id).
	TokenTypeChannel = "channel"
)

// Claims is the custom JWT claims structure.
type Claims struct {
	jwt.RegisteredClaims
	// TokenType distinguishes session tokens from channel tokens.
	TokenType string `json:"type"`
}

// IssueSessionToken creates a signed JWT session token for the given user.
// The token is valid for 24 hours.
func IssueSessionToken(secret []byte, username string) (string, error) {
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   username,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
		},
		TokenType: TokenTypeSession,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(secret)
}

// IssueChannelToken creates a signed JWT channel token with subject = channelID.
// The token is valid for 30 days.
func IssueChannelToken(secret []byte, channelID string) (string, error) {
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   channelID,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(30 * 24 * time.Hour)),
		},
		TokenType: TokenTypeChannel,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(secret)
}

// ParseToken parses and validates a signed JWT token, returning the claims.
func ParseToken(secret []byte, tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return secret, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid token claims")
	}
	return claims, nil
}

// ValidateChannelToken validates a channel token and ensures its subject matches channelID.
func ValidateChannelToken(secret []byte, tokenString, channelID string) error {
	claims, err := ParseToken(secret, tokenString)
	if err != nil {
		return err
	}
	if claims.TokenType != TokenTypeChannel {
		return errors.New("invalid token type: expected channel token")
	}
	if claims.Subject != channelID {
		return errors.New("token subject does not match channel_id")
	}
	return nil
}
