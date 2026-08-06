package eventbus

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"

	"flowforge/internal/execution/executor"
	"flowforge/internal/platform/eventbus/eventspb"
)

// GRPCPublisher is the gRPC egress adapter for EVENT_PUBLISH: it dials the
// configured target, signs the message with the tenant's webhook secret, and
// calls the same DeliverEvent RPC the ingress side implements — symmetric by
// construction, so two FlowForge-shaped endpoints interoperate for free.
type GRPCPublisher struct {
	secret SecretGetter
	dial   func(ctx context.Context, target string) (*grpc.ClientConn, error)
}

// NewGRPCPublisher builds the egress publisher. The dial seam is injectable for
// tests (bufconn); production uses an insecure plaintext dial.
func NewGRPCPublisher(secret SecretGetter) *GRPCPublisher {
	return &GRPCPublisher{
		secret: secret,
		dial: func(ctx context.Context, target string) (*grpc.ClientConn, error) {
			return grpc.DialContext(ctx, target, grpc.WithTransportCredentials(insecure.NewCredentials()))
		},
	}
}

// Publish implements executor.EventPublisher for transport=="grpc".
func (p *GRPCPublisher) Publish(ctx context.Context, in executor.PublishInput) error {
	if in.TenantID == uuid.Nil {
		return fmt.Errorf("grpc publish: missing tenant identity")
	}
	secret, err := p.secret(ctx, in.TenantID)
	if err != nil {
		return fmt.Errorf("grpc publish: %w", err)
	}
	if secret == "" {
		return ErrNoSecret
	}

	req := &eventspb.DeliverEventRequest{
		CorrelationKey: in.CorrelationKey,
		PayloadJson:    in.Payload,
	}
	body, err := proto.Marshal(req)
	if err != nil {
		return fmt.Errorf("grpc publish: marshal: %w", err)
	}
	sig := sign(secret, body)

	conn, err := p.dial(ctx, in.Target)
	if err != nil {
		return fmt.Errorf("grpc publish: dial %q: %w", in.Target, err)
	}
	defer conn.Close()

	client := eventspb.NewEventListenerClient(conn)
	outCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs(
		"x-tenant-id", in.TenantID.String(),
		"x-signature", sig,
	))
	if _, err := client.DeliverEvent(outCtx, req); err != nil {
		return fmt.Errorf("grpc publish: deliver: %w", err)
	}
	return nil
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
