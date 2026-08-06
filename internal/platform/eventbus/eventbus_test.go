package eventbus

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"

	"flowforge/internal/execution/executor"
	"flowforge/internal/platform/eventbus/eventspb"
)

// recordingPub records which transport handled a publish.
type recordingPub struct {
	name    string
	targets []string
	handled []executor.PublishInput
	fail    error
}

func (r *recordingPub) Publish(ctx context.Context, in executor.PublishInput) error {
	r.handled = append(r.handled, in)
	r.targets = append(r.targets, in.Target)
	return r.fail
}

// Part E: the Router dispatches by transport, defaults "" to Internal, and
// rejects an unknown transport as an error (never a silent fallback).
func TestRouter_Publish(t *testing.T) {
	internal := &recordingPub{name: "internal"}
	grpcPub := &recordingPub{name: "grpc"}
	natsPub := &recordingPub{name: "nats"}
	r := Router{Internal: internal, GRPC: grpcPub, NATS: natsPub}

	base := executor.PublishInput{EventType: "e", Payload: []byte("{}")}

	cases := []struct {
		name      string
		transport string
		want      *recordingPub
	}{
		{"default is internal", "", internal},
		{"internal explicit", "internal", internal},
		{"grpc", "grpc", grpcPub},
		{"nats", "nats", natsPub},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			in.Transport = tc.transport
			in.Target = "host:9090"
			require.NoError(t, r.Publish(context.Background(), in))
			assert.Equal(t, "host:9090", tc.want.targets[len(tc.want.targets)-1])
		})
	}

	t.Run("unknown transport is an error", func(t *testing.T) {
		in := base
		in.Transport = "kafka"
		before := len(internal.handled)
		err := r.Publish(context.Background(), in)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown transport")
		assert.Equal(t, before, len(internal.handled), "no silent fallback to internal")
	})
}

type fakeHandler struct {
	resolved bool
	received string
	lastKey  string
	orphans  int
}

func (f *fakeHandler) HandleEvent(ctx context.Context, tenantID uuid.UUID, correlationKey string, payload []byte) (bool, error) {
	f.lastKey = correlationKey
	f.received = string(payload)
	return f.resolved, nil
}

func (f *fakeHandler) RecordOrphanEvent(ctx context.Context, tenantID uuid.UUID, correlationKey string, payload []byte, reason string) error {
	f.orphans++
	return nil
}

const testSecret = "eventbus-test-secret"

func fixedSecret(ctx context.Context, tenantID uuid.UUID) (string, error) {
	return testSecret, nil
}

// Part C: a gRPC DeliverEvent through the auth interceptor reaches the shared
// handler, and a bad signature is rejected before the handler is invoked.
func TestGRPC_ServerAndClientRoundTrip(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	handler := &fakeHandler{resolved: true}
	grpcSrv := grpc.NewServer(grpc.UnaryInterceptor(AuthInterceptor(fixedSecret)))
	eventspb.RegisterEventListenerServer(grpcSrv, NewEventListenerServer(handler))
	go func() {
		_ = grpcSrv.Serve(lis)
	}()
	defer grpcSrv.Stop()

	// The publisher dials through the same bufconn.
	pub := NewGRPCPublisher(fixedSecret)
	pub.dial = func(ctx context.Context, target string) (*grpc.ClientConn, error) {
		return grpc.DialContext(ctx, "bufnet", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}), grpc.WithInsecure())
	}

	err := pub.Publish(context.Background(), executor.PublishInput{
		EventType:      "payment.paid",
		CorrelationKey: "ORD-1",
		TenantID:       uuid.New(),
		Payload:        []byte(`{"paid":true}`),
	})
	require.NoError(t, err)
	assert.Equal(t, "ORD-1", handler.lastKey)
	assert.Equal(t, `{"paid":true}`, handler.received)
}

// Part C: missing/invalid x-signature metadata is rejected before DeliverEvent.
func TestGRPC_AuthInterceptorRejectsBadSignature(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	handler := &fakeHandler{resolved: true}
	grpcSrv := grpc.NewServer(grpc.UnaryInterceptor(AuthInterceptor(fixedSecret)))
	eventspb.RegisterEventListenerServer(grpcSrv, NewEventListenerServer(handler))
	go func() {
		_ = grpcSrv.Serve(lis)
	}()
	defer grpcSrv.Stop()

	conn, err := grpc.DialContext(context.Background(), "bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithInsecure())
	require.NoError(t, err)
	defer conn.Close()

	client := eventspb.NewEventListenerClient(conn)
	req := &eventspb.DeliverEventRequest{CorrelationKey: "K", PayloadJson: []byte(`{}`)}

	// No signature metadata.
	_, err = client.DeliverEvent(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Unauthenticated")
	assert.Equal(t, "", handler.lastKey, "handler must not run on bad auth")
}

// Phase AN (AB-1): One bad message (e.g., bad signature) must not kill the subscriber loop;
// subsequent valid messages must still be processed.
func TestSubscribe_OneBadMessageDoesNotStopSubsequentProcessing(t *testing.T) {
	natsURL := "nats://localhost:4222"
	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Skipf("nats not reachable: %v", err)
	}
	defer nc.Close()

	js, err := nc.JetStream()
	require.NoError(t, err)

	suffix := uuid.New().String()[:8]
	subject := "flowforge.events.testsub." + suffix
	streamName := "FLOWFORGE_EVENTS_TESTSUB_" + suffix
	require.NoError(t, EnsureStream(js, streamName, subject))

	tenantID := uuid.New()
	secret := "test-sub-secret"
	secretGetter := func(ctx context.Context, tid uuid.UUID) (string, error) {
		return secret, nil
	}

	handler := &fakeHandler{resolved: true}
	subCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = Subscribe(subCtx, nc, subject, "durable-"+suffix, secretGetter, handler, nil)
	}()

	// 1. Publish bad message (invalid signature header)
	badHeader := nats.Header{}
	badHeader.Set("Tenant-Id", tenantID.String())
	badHeader.Set("Signature", "invalid-sig")
	_, err = js.PublishMsg(&nats.Msg{
		Subject: subject,
		Header:  badHeader,
		Data:    []byte(`{"correlationKey":"BAD-1"}`),
	})
	require.NoError(t, err)

	// 2. Publish valid message
	pub := NewNATSPublisher(js, secretGetter)
	err = pub.Publish(context.Background(), executor.PublishInput{
		EventType:      "test.event",
		CorrelationKey: "GOOD-1",
		TenantID:       tenantID,
		Transport:      "nats",
		Target:         subject,
		Payload:        []byte(`{"correlationKey":"GOOD-1"}`),
	})
	require.NoError(t, err)

	// Poll handler to confirm GOOD-1 was processed
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if handler.lastKey == "GOOD-1" {
			assert.Equal(t, "GOOD-1", handler.lastKey)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("subscriber stopped processing after bad message; GOOD-1 was never received")
}

