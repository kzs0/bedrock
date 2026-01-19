package bedrock

import (
	"context"
)

type contextKey int

const (
	bedrockKey contextKey = iota
	operationKey
	sourceKey
)

// WithBedrock returns a context with the bedrock instance attached.
// This is the primary way to propagate bedrock through your application.
func WithBedrock(ctx context.Context, b *Bedrock) context.Context {
	return context.WithValue(ctx, bedrockKey, b)
}

// bedrockFromContext returns the bedrock instance from the context.
// If none exists, returns a noop instance.
func bedrockFromContext(ctx context.Context) *Bedrock {
	if b, ok := ctx.Value(bedrockKey).(*Bedrock); ok {
		return b
	}
	return noopBedrock()
}

// FromContext returns the bedrock instance from the context.
// Returns nil if no bedrock instance exists (use this for optional access).
func FromContext(ctx context.Context) *Bedrock {
	b, _ := ctx.Value(bedrockKey).(*Bedrock)
	return b
}

// withOperationState stores operation state in the context.
func withOperationState(ctx context.Context, state *operationState) context.Context {
	return context.WithValue(ctx, operationKey, state)
}

// operationStateFromContext retrieves operation state from the context.
func operationStateFromContext(ctx context.Context) *operationState {
	if state, ok := ctx.Value(operationKey).(*operationState); ok {
		return state
	}
	return nil
}

// withSourceConfig stores source configuration in the context.
func withSourceConfig(ctx context.Context, cfg *sourceConfig) context.Context {
	return context.WithValue(ctx, sourceKey, cfg)
}

// sourceConfigFromContext retrieves source configuration from the context.
func sourceConfigFromContext(ctx context.Context) *sourceConfig {
	if cfg, ok := ctx.Value(sourceKey).(*sourceConfig); ok {
		return cfg
	}
	return nil
}
