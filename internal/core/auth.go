package core

import "context"

type ownerContextKey struct{}

func WithOwnerID(ctx context.Context, ownerID string) context.Context {
	return context.WithValue(ctx, ownerContextKey{}, ownerID)
}

func OwnerID(ctx context.Context) string {
	if v, ok := ctx.Value(ownerContextKey{}).(string); ok {
		return v
	}
	return ""
}
