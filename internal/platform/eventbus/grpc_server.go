package eventbus

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"flowforge/internal/execution"
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
// port, mirroring the HTTP webhook path's semantics, or triggers runs via RunTriggerer.
type EventListenerServer struct {
	eventspb.UnimplementedEventListenerServer
	handler   EventHandler
	triggerer RunTriggerer
}

// NewEventListenerServer builds the gRPC ingress around the shared handler.
func NewEventListenerServer(handler EventHandler) *EventListenerServer {
	return NewEventListenerServerWithTriggerer(handler, nil)
}

// NewEventListenerServerWithTriggerer builds the gRPC ingress with both event delivery and run triggering capabilities.
func NewEventListenerServerWithTriggerer(handler EventHandler, triggerer RunTriggerer) *EventListenerServer {
	return &EventListenerServer{handler: handler, triggerer: triggerer}
}

// DeliverEvent resolves the wait token for the correlation key, or triggers a new workflow run if a workflow ID is provided.
func (s *EventListenerServer) DeliverEvent(ctx context.Context, req *eventspb.DeliverEventRequest) (*eventspb.DeliverEventResponse, error) {
	tenantID, ok := TenantIDFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing tenant identity")
	}

	// Case A: check if payload is a workflow trigger request.
	// triggerType is intentionally not read from the request — this ingress
	// path is gRPC, so the run's recorded trigger_type must always be "grpc"
	// regardless of what the caller claims. Trusting a caller-supplied value
	// would let a gRPC client report triggerType:"manual" and corrupt the
	// audit trail trigger_type exists to provide.
	var triggerReq struct {
		WorkflowID     string          `json:"workflowId"`
		InputContext   json.RawMessage `json:"inputContext"`
		IdempotencyKey *string         `json:"idempotencyKey"`
	}
	if err := json.Unmarshal(req.GetPayloadJson(), &triggerReq); err == nil && triggerReq.WorkflowID != "" && s.triggerer != nil {
		wfID, err := uuid.Parse(triggerReq.WorkflowID)
		if err == nil {
			_, err := s.triggerer.CreateRun(ctx, execution.CreateRunCommand{
				TenantID:       tenantID,
				WorkflowID:     wfID,
				TriggerType:    "grpc",
				InputContext:   triggerReq.InputContext,
				IdempotencyKey: triggerReq.IdempotencyKey,
			})
			if err != nil {
				return nil, status.Errorf(codes.Internal, "unable to trigger workflow run: %v", err)
			}
			return &eventspb.DeliverEventResponse{Accepted: true}, nil
		}
	}

	if s.handler == nil {
		return nil, status.Error(codes.Unimplemented, "event handler not configured")
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
