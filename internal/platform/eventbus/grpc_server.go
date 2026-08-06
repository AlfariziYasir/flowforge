package eventbus

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"flowforge/internal/platform/eventbus/eventspb"
	"flowforge/internal/platform/webhookauth"
)

type ctxKey struct{}

var tenantIDKey ctxKey

// TenantIDFromContext returns the tenant id the auth interceptor injected.
func TenantIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(tenantIDKey).(uuid.UUID)
	return id, ok
}

// AuthInterceptor verifies the x-tenant-id + x-signature metadata before any
// handler runs, signing over the canonical (proto.Marshal) bytes of the request
// message — the same webhookauth.VerifyHMAC every transport shares. The tenant
// id is injected into the context for the handler.
func AuthInterceptor(secretGetter SecretGetter) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing event metadata")
		}
		tenantIDs := md.Get("x-tenant-id")
		sigs := md.Get("x-signature")
		if len(tenantIDs) == 0 || len(sigs) == 0 {
			return nil, status.Error(codes.Unauthenticated, "missing tenant-id or signature")
		}
		tenantID, err := uuid.Parse(tenantIDs[0])
		if err != nil {
			return nil, status.Error(codes.Unauthenticated, "invalid tenant id")
		}
		secret, err := secretGetter(ctx, tenantID)
		if err != nil {
			return nil, status.Error(codes.Internal, "unable to load tenant secret")
		}
		msg, ok := req.(proto.Message)
		if !ok {
			return nil, status.Error(codes.Internal, "request is not a protobuf message")
		}
		body, err := proto.Marshal(msg)
		if err != nil {
			return nil, status.Error(codes.Internal, "unable to serialize request")
		}
		if !webhookauth.VerifyHMAC(secret, body, sigs[0]) {
			return nil, status.Error(codes.Unauthenticated, "invalid signature")
		}
		return handler(context.WithValue(ctx, tenantIDKey, tenantID), req)
	}
}

// EventListenerServer is the gRPC ingress: it calls the shared EventHandler
// port, mirroring the HTTP webhook path's semantics.
type EventListenerServer struct {
	eventspb.UnimplementedEventListenerServer
	handler EventHandler
}

// NewEventListenerServer builds the gRPC ingress around the shared handler.
func NewEventListenerServer(handler EventHandler) *EventListenerServer {
	return &EventListenerServer{handler: handler}
}

// DeliverEvent resolves the wait token for the correlation key, exactly as the
// HTTP webhook path does. accepted is always true on a handled event —
// match/no-match/duplicate are internal outcomes.
func (s *EventListenerServer) DeliverEvent(ctx context.Context, req *eventspb.DeliverEventRequest) (*eventspb.DeliverEventResponse, error) {
	tenantID, ok := TenantIDFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing tenant identity")
	}
	resolved, err := s.handler.HandleEvent(ctx, tenantID, req.GetCorrelationKey(), req.GetPayloadJson())
	if err != nil {
		return nil, status.Error(codes.Internal, "unable to deliver event")
	}
	if !resolved {
		_ = s.handler.RecordOrphanEvent(ctx, tenantID, req.GetCorrelationKey(), req.GetPayloadJson(), "no matching wait token")
	}
	return &eventspb.DeliverEventResponse{Accepted: true}, nil
}

// ErrNoSecret is returned by the gRPC publisher when a tenant has no signing
// secret configured — it cannot produce a verifiable signature.
var ErrNoSecret = errors.New("no webhook secret configured for tenant")
