package eventbus

import (
	"context"
	"fmt"

	"flowforge/internal/execution/executor"
)

// Router dispatches EVENT_PUBLISH to the transport a node's config selects,
// defaulting to the internal-queue publisher when Transport is unset — v1's
// EVENT_PUBLISH behavior is preserved byte-identically for any node that does
// not opt in. An unknown transport value is a step-level error, never a silent
// fallback: silently falling back would hide a config typo pointing at a real
// integration.
type Router struct {
	Internal executor.EventPublisher // today's queue.Client
	GRPC     executor.EventPublisher
	NATS     executor.EventPublisher
}

// Publish implements executor.EventPublisher.
func (r Router) Publish(ctx context.Context, in executor.PublishInput) error {
	var pub executor.EventPublisher
	switch in.Transport {
	case "", "internal":
		pub = r.Internal
	case "grpc":
		pub = r.GRPC
	case "nats":
		pub = r.NATS
	default:
		return fmt.Errorf("eventbus: unknown transport %q", in.Transport)
	}
	if pub == nil {
		return fmt.Errorf("eventbus: transport %q has no publisher configured", in.Transport)
	}
	return pub.Publish(ctx, in)
}
