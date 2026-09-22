package api

import "context"

type contextKey string

const accountIDContextKey contextKey = "accountID"

func withAccountID(ctx context.Context, accountID string) context.Context {
	return context.WithValue(ctx, accountIDContextKey, accountID)
}

func accountIDFromContext(ctx context.Context) (string, bool) {
	accountID, ok := ctx.Value(accountIDContextKey).(string)
	return accountID, ok
}
