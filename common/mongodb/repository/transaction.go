package repository

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/mongo"
)

// WithTransaction runs fn inside a MongoDB session with a transaction.
// The transaction commits on nil error and aborts otherwise. It also retries
// transient transaction errors automatically per driver semantics.
func WithTransaction(
	ctx context.Context,
	client *mongo.Client,
	fn func(sessCtx mongo.SessionContext) error,
) error {
	return withTx(ctx, client, func(sessCtx mongo.SessionContext) (any, error) {
		return nil, fn(sessCtx)
	}).err
}

// WithTransactionResult is the generic-typed version of WithTransaction.
func WithTransactionResult[R any](
	ctx context.Context,
	client *mongo.Client,
	fn func(sessCtx mongo.SessionContext) (R, error),
) (R, error) {
	res := withTx(ctx, client, func(sessCtx mongo.SessionContext) (any, error) {
		return fn(sessCtx)
	})
	if res.err != nil {
		var zero R
		return zero, res.err
	}
	out, _ := res.value.(R)
	return out, nil
}

type txResult struct {
	value any
	err   error
}

func withTx(
	ctx context.Context,
	client *mongo.Client,
	body func(sessCtx mongo.SessionContext) (any, error),
) txResult {
	if client == nil {
		return txResult{err: fmt.Errorf("mongodb: client is nil")}
	}
	session, err := client.StartSession()
	if err != nil {
		return txResult{err: fmt.Errorf("mongodb: start session: %w", err)}
	}
	defer session.EndSession(ctx)

	value, err := session.WithTransaction(ctx, func(sessCtx mongo.SessionContext) (any, error) {
		return body(sessCtx)
	})
	return txResult{value: value, err: err}
}
