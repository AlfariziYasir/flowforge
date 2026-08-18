package eventstream

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"flowforge/internal/domain"
)

// ClientManager manages client connections and subscribes to Redis Pub/Sub channels
// on demand with reference counting per tenant.
type ClientManager interface {
	Register(tenantID uuid.UUID) (connID string, events <-chan domain.Event, unregister func())
	Close() error
}

type tenantSubscription struct {
	pubsub  *redis.PubSub
	cancel  context.CancelFunc
	clients map[string]chan domain.Event
}

type redisClientManager struct {
	rdb    *redis.Client
	logger *slog.Logger
	mu     sync.Mutex
	subs   map[uuid.UUID]*tenantSubscription
	closed bool
}

// NewClientManager constructs a reference-counted Redis ClientManager.
func NewClientManager(rdb *redis.Client, logger ...*slog.Logger) ClientManager {
	var l *slog.Logger
	if len(logger) > 0 && logger[0] != nil {
		l = logger[0]
	} else {
		l = slog.Default()
	}

	return &redisClientManager{
		rdb:    rdb,
		logger: l,
		subs:   make(map[uuid.UUID]*tenantSubscription),
	}
}

// Register registers a local client connection for a tenant. It lazily opens a Redis Pub/Sub
// subscription for the tenant if none exists, and returns a dedicated receive channel and
// an unregister function that cleans up when the client disconnects.
func (m *redisClientManager) Register(tenantID uuid.UUID) (string, <-chan domain.Event, func()) {
	connID := uuid.NewString()
	eventCh := make(chan domain.Event, 64)

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed || m.rdb == nil {
		return connID, eventCh, func() {}
	}

	sub, exists := m.subs[tenantID]
	if !exists {
		channel := ChannelForTenant(tenantID)
		pubsub := m.rdb.Subscribe(context.Background(), channel)
		ctx, cancel := context.WithCancel(context.Background())

		sub = &tenantSubscription{
			pubsub:  pubsub,
			cancel:  cancel,
			clients: make(map[string]chan domain.Event),
		}
		m.subs[tenantID] = sub

		go m.listenTenant(ctx, tenantID, pubsub)
	}

	sub.clients[connID] = eventCh

	var once sync.Once
	unregister := func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()

			if currentSub, ok := m.subs[tenantID]; ok {
				if ch, clientExists := currentSub.clients[connID]; clientExists {
					delete(currentSub.clients, connID)
					close(ch)
				}

				// If no active clients remain for this tenant, close the Redis subscription (D-5 ref-counting)
				if len(currentSub.clients) == 0 {
					currentSub.cancel()
					_ = currentSub.pubsub.Close()
					delete(m.subs, tenantID)
				}
			}
		})
	}

	return connID, eventCh, unregister
}

func (m *redisClientManager) listenTenant(ctx context.Context, tenantID uuid.UUID, pubsub *redis.PubSub) {
	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if msg == nil {
				continue
			}

			var ev domain.Event
			if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
				m.logger.Warn("eventstream: failed to unmarshal event payload",
					slog.String("tenantID", tenantID.String()),
					slog.Any("error", err))
				continue
			}

			m.fanOut(tenantID, ev)
		}
	}
}

func (m *redisClientManager) fanOut(tenantID uuid.UUID, ev domain.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sub, exists := m.subs[tenantID]
	if !exists {
		return
	}

	for _, clientCh := range sub.clients {
		select {
		case clientCh <- ev:
		default:
			// Buffer full: drop rather than block other subscribers
		}
	}
}

func (m *redisClientManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return nil
	}
	m.closed = true

	var errs []error
	for _, sub := range m.subs {
		sub.cancel()
		if err := sub.pubsub.Close(); err != nil {
			errs = append(errs, err)
		}
		for _, ch := range sub.clients {
			close(ch)
		}
	}
	m.subs = make(map[uuid.UUID]*tenantSubscription)

	return errors.Join(errs...)
}

type noopClientManager struct{}

// NewNoopClientManager returns a ClientManager that yields no events.
func NewNoopClientManager() ClientManager {
	return &noopClientManager{}
}

func (noopClientManager) Register(tenantID uuid.UUID) (string, <-chan domain.Event, func()) {
	ch := make(chan domain.Event)
	return uuid.NewString(), ch, func() {}
}

func (noopClientManager) Close() error {
	return nil
}
