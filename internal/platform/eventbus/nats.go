package eventbus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"flowforge/internal/execution/executor"
	"flowforge/internal/platform/webhookauth"
)

// NATSPublisher is the NATS/JetStream egress adapter for EVENT_PUBLISH
// (transport=="nats"). JetStream's MsgId gives free dedup-on-republish at the
// transport layer, on top of HandleEvent's own token-consume idempotency.
type NATSPublisher struct {
	js     nats.JetStreamContext
	secret SecretGetter
}

// NewNATSPublisher builds the egress publisher around a JetStream context.
func NewNATSPublisher(js nats.JetStreamContext, secret SecretGetter) *NATSPublisher {
	return &NATSPublisher{js: js, secret: secret}
}

// Publish implements executor.EventPublisher for transport=="nats". Target is
// the subject; tenant id and signature ride in the message headers, which the
// subscriber verifies.
func (p *NATSPublisher) Publish(ctx context.Context, in executor.PublishInput) error {
	if in.TenantID == uuid.Nil {
		return fmt.Errorf("nats publish: missing tenant identity")
	}
	secret, err := p.secret(ctx, in.TenantID)
	if err != nil {
		return fmt.Errorf("nats publish: %w", err)
	}
	if secret == "" {
		return ErrNoSecret
	}

	h := nats.Header{}
	h.Set("Tenant-Id", in.TenantID.String())
	h.Set("Signature", sign(secret, in.Payload))

	// MsgId: dedup on republish for the same logical event.
	msgID := in.TenantID.String() + "/" + in.EventType + "/" + in.CorrelationKey
	if _, err := p.js.PublishMsg(&nats.Msg{
		Subject: in.Target,
		Header:  h,
		Data:    in.Payload,
	}, nats.MsgId(msgID)); err != nil {
		return fmt.Errorf("nats publish: %w", err)
	}
	return nil
}

// Subscribe starts a durable JetStream consumer on subject and calls handle for
// each message, acking only after handle returns nil — so a crash between
// receipt and HandleEvent's commit redelivers rather than loses the event.
// Combined with HandleEvent's own idempotent token-consume, a redelivery after
// a successful-but-unacked message is a safe no-op. Blocks until ctx is done.
func Subscribe(ctx context.Context, nc *nats.Conn, subject, durableName string,
	secret SecretGetter, handler EventHandler, logger *slog.Logger) error {

	if logger == nil {
		logger = slog.Default()
	}

	js, err := nc.JetStream()
	if err != nil {
		return fmt.Errorf("nats subscribe: jetstream: %w", err)
	}
	sub, err := js.PullSubscribe(subject, durableName)
	if err != nil {
		return fmt.Errorf("nats subscribe: %w", err)
	}
	defer sub.Unsubscribe()

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		msgs, err := sub.Fetch(10, nats.MaxWait(2*time.Second))
		if err != nil {
			if err == nats.ErrTimeout {
				continue
			}
			return fmt.Errorf("nats subscribe: fetch: %w", err)
		}
		for _, m := range msgs {
			if err := handleNATSMessage(ctx, m, secret, handler); err != nil {
				logger.Error("nats message handling failed, leaving unacked for redelivery",
					slog.String("subject", m.Subject), slog.Any("error", err))
				continue
			}
			_ = m.Ack()
		}
	}
}

func handleNATSMessage(ctx context.Context, m *nats.Msg, secret SecretGetter, handler EventHandler) error {
	tenantID, err := uuid.Parse(m.Header.Get("Tenant-Id"))
	if err != nil {
		return fmt.Errorf("nats: invalid tenant header: %w", err)
	}
	tenantSecret, err := secret(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("nats: load tenant secret: %w", err)
	}
	if !webhookauth.VerifyHMAC(tenantSecret, m.Data, m.Header.Get("Signature")) {
		return fmt.Errorf("nats: invalid signature for tenant %s", tenantID)
	}

	// The correlation key rides in the payload ({"correlationKey": ...}) for the
	// generic contract; the subject itself carries no per-tenant cardinality.
	var envelope struct {
		CorrelationKey string `json:"correlationKey"`
	}
	if err := json.Unmarshal(m.Data, &envelope); err != nil {
		return fmt.Errorf("nats: malformed payload: %w", err)
	}

	resolved, err := handler.HandleEvent(ctx, tenantID, envelope.CorrelationKey, m.Data)
	if err != nil {
		return err
	}
	if !resolved {
		_ = handler.RecordOrphanEvent(ctx, tenantID, envelope.CorrelationKey, m.Data, "no matching wait token")
	}
	return nil
}

// EnsureStream provisions the JetStream stream idempotently (AddStream fails
// with ErrStreamNameAlreadyInUse when it already exists, which is fine).
func EnsureStream(js nats.JetStreamContext, name, subject string) error {
	_, err := js.AddStream(&nats.StreamConfig{
		Name:      name,
		Subjects:  []string{subject},
		Storage:   nats.FileStorage,
		Retention: nats.WorkQueuePolicy,
	})
	if err != nil && err != nats.ErrStreamNameAlreadyInUse {
		return fmt.Errorf("ensure stream %s: %w", name, err)
	}
	return nil
}
