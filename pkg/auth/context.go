package auth

import (
	"context"
	"errors"
)

type contextKey struct{}

var ErrUnauthorized = errors.New("Unauthorized")

func FromContextKey(ctx context.Context, claims *Claims) context.Context {
	return context.WithValue(ctx, contextKey{}, claims)
}

func ToContext(ctx context.Context) (*Claims, error) {
	claims, ok := ctx.Value(contextKey{}).(*Claims)
	if claims == nil || !ok {
		return nil, ErrUnauthorized
	}
	return claims, nil
}
