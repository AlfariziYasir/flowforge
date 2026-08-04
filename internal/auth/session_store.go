package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	redisclient "github.com/redis/go-redis/v9"
)

var (
	ErrSessionNotFound = errors.New("session not found")
)

const (
	sessionKeyPrefix           = "session:"
	sessionIndexPrefix         = "session:index:"
	revocationKeyPrefix        = "user:revoked_before:"
	sessionIndexMigratedMember = "__migrated__"
)

// UserSession metadata stored in Redis.
type UserSession struct {
	SessionID    uuid.UUID `json:"sessionId"`
	UserID       uuid.UUID `json:"userId"`
	TenantID     uuid.UUID `json:"tenantId"`
	IPAddress    string    `json:"ipAddress,omitempty"`
	UserAgent    string    `json:"userAgent,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	LastActiveAt time.Time `json:"lastActiveAt"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

// SessionStore handles session persistence, lookup, revocation, and user-level token invalidation.
type SessionStore interface {
	CreateSession(ctx context.Context, session *UserSession, ttl time.Duration) error
	GetSession(ctx context.Context, tenantID, userID, sessionID uuid.UUID) (*UserSession, error)
	RevokeSession(ctx context.Context, tenantID, userID, sessionID uuid.UUID) error
	RevokeAllUserSessions(ctx context.Context, tenantID, userID uuid.UUID) error
	SetUserRevokedBefore(ctx context.Context, userID uuid.UUID, revokedAt time.Time, ttl time.Duration) error
	IsUserRevoked(ctx context.Context, userID uuid.UUID, issuedAt time.Time) (bool, error)
	ListUserSessions(ctx context.Context, tenantID, userID uuid.UUID, page, pageSize int) ([]*UserSession, int64, error)
}

type redisSessionStore struct {
	client     redisclient.Cmdable
	defaultTTL time.Duration
}

// NewRedisSessionStore creates a new Redis-backed SessionStore.
func NewRedisSessionStore(client redisclient.Cmdable) SessionStore {
	return NewRedisSessionStoreWithTTL(client, 7*24*time.Hour)
}

// NewRedisSessionStoreWithTTL creates a new Redis-backed SessionStore with specified default TTL.
func NewRedisSessionStoreWithTTL(client redisclient.Cmdable, defaultTTL time.Duration) SessionStore {
	if client == nil {
		return NewNoopSessionStore()
	}
	if defaultTTL <= 0 {
		defaultTTL = 7 * 24 * time.Hour
	}
	return &redisSessionStore{
		client:     client,
		defaultTTL: defaultTTL,
	}
}

func (s *redisSessionStore) sessionKey(tenantID, userID, sessionID uuid.UUID) string {
	return fmt.Sprintf("%s%s:%s:%s", sessionKeyPrefix, tenantID, userID, sessionID)
}

func (s *redisSessionStore) sessionIndexKey(tenantID, userID uuid.UUID) string {
	return fmt.Sprintf("%s%s:%s", sessionIndexPrefix, tenantID, userID)
}

func (s *redisSessionStore) revocationKey(userID uuid.UUID) string {
	return fmt.Sprintf("%s%s", revocationKeyPrefix, userID)
}

func (s *redisSessionStore) CreateSession(ctx context.Context, session *UserSession, ttl time.Duration) error {
	if session == nil || session.SessionID == uuid.Nil || session.UserID == uuid.Nil || session.TenantID == uuid.Nil {
		return errors.New("invalid session data")
	}
	if ttl <= 0 {
		return errors.New("ttl must be greater than zero")
	}

	data, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("marshal user session: %w", err)
	}

	key := s.sessionKey(session.TenantID, session.UserID, session.SessionID)
	indexKey := s.sessionIndexKey(session.TenantID, session.UserID)

	if err := s.client.Set(ctx, key, data, ttl).Err(); err != nil {
		return fmt.Errorf("set user session in redis: %w", err)
	}

	if err := s.client.SAdd(ctx, indexKey, session.SessionID.String()).Err(); err != nil {
		return fmt.Errorf("add session to user index in redis: %w", err)
	}
	if err := s.client.Expire(ctx, indexKey, ttl).Err(); err != nil {
		return fmt.Errorf("set user index ttl in redis: %w", err)
	}

	return nil
}

func (s *redisSessionStore) GetSession(ctx context.Context, tenantID, userID, sessionID uuid.UUID) (*UserSession, error) {
	if tenantID == uuid.Nil || userID == uuid.Nil || sessionID == uuid.Nil {
		return nil, ErrSessionNotFound
	}

	key := s.sessionKey(tenantID, userID, sessionID)
	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redisclient.Nil) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("get user session from redis: %w", err)
	}

	var session UserSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("unmarshal user session: %w", err)
	}

	return &session, nil
}

func (s *redisSessionStore) RevokeSession(ctx context.Context, tenantID, userID, sessionID uuid.UUID) error {
	if tenantID == uuid.Nil || userID == uuid.Nil || sessionID == uuid.Nil {
		return nil
	}

	key := s.sessionKey(tenantID, userID, sessionID)
	indexKey := s.sessionIndexKey(tenantID, userID)

	_ = s.client.SRem(ctx, indexKey, sessionID.String()).Err()
	if err := s.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("revoke user session in redis: %w", err)
	}

	return nil
}

func (s *redisSessionStore) scanUserSessionKeys(ctx context.Context, tenantID, userID uuid.UUID) ([]string, error) {
	pattern := fmt.Sprintf("%s%s:%s:*", sessionKeyPrefix, tenantID, userID)
	var keys []string
	var cursor uint64

	for {
		resKeys, nextCursor, err := s.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return nil, fmt.Errorf("scan user session keys in redis: %w", err)
		}
		keys = append(keys, resKeys...)
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
	return keys, nil
}

func filterMigratedMember(sids []string) []string {
	res := make([]string, 0, len(sids))
	for _, sid := range sids {
		if sid != sessionIndexMigratedMember {
			res = append(res, sid)
		}
	}
	return res
}

func hasMigratedMember(sids []string) bool {
	for _, sid := range sids {
		if sid == sessionIndexMigratedMember {
			return true
		}
	}
	return false
}

// REMOVE AFTER 2026-08-15: legacy pre-index session backfill
func (s *redisSessionStore) backfillIndexIfEmpty(ctx context.Context, tenantID, userID uuid.UUID) ([]string, error) {
	indexKey := s.sessionIndexKey(tenantID, userID)
	sids, err := s.client.SMembers(ctx, indexKey).Result()
	if err != nil && !errors.Is(err, redisclient.Nil) {
		return nil, fmt.Errorf("smembers user session index in redis: %w", err)
	}

	if hasMigratedMember(sids) {
		return filterMigratedMember(sids), nil
	}

	if len(sids) == 0 {
		sessionKeys, err := s.scanUserSessionKeys(ctx, tenantID, userID)
		if err != nil {
			return nil, err
		}

		backfillSIDs := []interface{}{sessionIndexMigratedMember}
		if len(sessionKeys) > 0 {
			for _, k := range sessionKeys {
				parts := strings.Split(k, ":")
				if len(parts) == 4 {
					sidStr := parts[3]
					sids = append(sids, sidStr)
					backfillSIDs = append(backfillSIDs, sidStr)
				}
			}
		}
		if err := s.client.SAdd(ctx, indexKey, backfillSIDs...).Err(); err != nil {
			return nil, fmt.Errorf("sadd backfill index in redis: %w", err)
		}
		if err := s.client.Expire(ctx, indexKey, s.defaultTTL).Err(); err != nil {
			return nil, fmt.Errorf("set backfill index ttl in redis: %w", err)
		}
	}

	return filterMigratedMember(sids), nil
}

func (s *redisSessionStore) RevokeAllUserSessions(ctx context.Context, tenantID, userID uuid.UUID) error {
	if tenantID == uuid.Nil || userID == uuid.Nil {
		return nil
	}

	indexKey := s.sessionIndexKey(tenantID, userID)
	sids, err := s.backfillIndexIfEmpty(ctx, tenantID, userID)
	if err != nil {
		return err
	}

	keys := make([]string, 0, len(sids)+1)
	for _, sid := range sids {
		if parsed, err := uuid.Parse(sid); err == nil {
			keys = append(keys, s.sessionKey(tenantID, userID, parsed))
		}
	}
	keys = append(keys, indexKey)

	if len(keys) > 0 {
		if err := s.client.Del(ctx, keys...).Err(); err != nil {
			return fmt.Errorf("delete user session keys in redis: %w", err)
		}
	}

	// Restore migration sentinel so index remains in "migrated, empty" state and avoids re-scanning.
	if err := s.client.SAdd(ctx, indexKey, sessionIndexMigratedMember).Err(); err != nil {
		return fmt.Errorf("restore migration sentinel in redis: %w", err)
	}
	if err := s.client.Expire(ctx, indexKey, s.defaultTTL).Err(); err != nil {
		return fmt.Errorf("set migration sentinel ttl in redis: %w", err)
	}

	return nil
}

func (s *redisSessionStore) SetUserRevokedBefore(ctx context.Context, userID uuid.UUID, revokedAt time.Time, ttl time.Duration) error {
	if userID == uuid.Nil {
		return errors.New("user ID is required")
	}
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}

	key := s.revocationKey(userID)
	val := strconv.FormatInt(revokedAt.UnixMilli(), 10)

	if err := s.client.Set(ctx, key, val, ttl).Err(); err != nil {
		return fmt.Errorf("set user revocation timestamp in redis: %w", err)
	}

	return nil
}

func (s *redisSessionStore) IsUserRevoked(ctx context.Context, userID uuid.UUID, issuedAt time.Time) (bool, error) {
	if userID == uuid.Nil {
		return false, nil
	}

	key := s.revocationKey(userID)
	val, err := s.client.Get(ctx, key).Result()
	if err != nil {
		if errors.Is(err, redisclient.Nil) {
			return false, nil
		}
		return false, fmt.Errorf("check user revocation timestamp in redis: %w", err)
	}

	revokedUnixMilli, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return false, fmt.Errorf("parse revocation timestamp integer: %w", err)
	}

	// Revoke tokens issued strictly before the revocation instant.
	// Invariant: iat claim precision is set to milliseconds by init() in jwt.go.
	return issuedAt.UnixMilli() < revokedUnixMilli, nil
}

func (s *redisSessionStore) ListUserSessions(ctx context.Context, tenantID, userID uuid.UUID, page, pageSize int) ([]*UserSession, int64, error) {
	if tenantID == uuid.Nil || userID == uuid.Nil {
		return []*UserSession{}, 0, nil
	}

	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	indexKey := s.sessionIndexKey(tenantID, userID)
	sids, err := s.backfillIndexIfEmpty(ctx, tenantID, userID)
	if err != nil {
		return nil, 0, err
	}

	if len(sids) == 0 {
		return []*UserSession{}, 0, nil
	}

	// Fetch sessions and filter out expired/missing keys
	activeSessions := make([]*UserSession, 0, len(sids))
	var expiredSIDs []interface{}

	for _, sidStr := range sids {
		parsedSID, err := uuid.Parse(sidStr)
		if err != nil {
			expiredSIDs = append(expiredSIDs, sidStr)
			continue
		}

		key := s.sessionKey(tenantID, userID, parsedSID)
		data, err := s.client.Get(ctx, key).Bytes()
		if err != nil {
			if errors.Is(err, redisclient.Nil) {
				expiredSIDs = append(expiredSIDs, sidStr)
				continue
			}
			return nil, 0, fmt.Errorf("get session key %s: %w", key, err)
		}

		var sess UserSession
		if err := json.Unmarshal(data, &sess); err != nil {
			expiredSIDs = append(expiredSIDs, sidStr)
			continue
		}
		activeSessions = append(activeSessions, &sess)
	}

	// Prune stale SIDs asynchronously/best-effort
	if len(expiredSIDs) > 0 {
		_ = s.client.SRem(ctx, indexKey, expiredSIDs...).Err()
	}

	sort.Slice(activeSessions, func(i, j int) bool {
		return activeSessions[i].SessionID.String() < activeSessions[j].SessionID.String()
	})

	total := int64(len(activeSessions))
	if total == 0 {
		return []*UserSession{}, 0, nil
	}

	start := (page - 1) * pageSize
	if start >= len(activeSessions) {
		return []*UserSession{}, total, nil
	}

	end := start + pageSize
	if end > len(activeSessions) {
		end = len(activeSessions)
	}

	return activeSessions[start:end], total, nil
}

type noopSessionStore struct{}

// NewNoopSessionStore returns a no-op implementation of SessionStore.
// Note: CreateSession and GetSession fail closed so unauthenticated
// or unverified session operations are never accepted when Redis is offline.
func NewNoopSessionStore() SessionStore {
	return &noopSessionStore{}
}

func (n *noopSessionStore) CreateSession(ctx context.Context, session *UserSession, ttl time.Duration) error {
	return errors.New("session store unavailable: redis is required for authentication")
}

func (n *noopSessionStore) GetSession(ctx context.Context, tenantID, userID, sessionID uuid.UUID) (*UserSession, error) {
	return nil, ErrSessionNotFound
}

func (n *noopSessionStore) RevokeSession(ctx context.Context, tenantID, userID, sessionID uuid.UUID) error {
	return nil
}

func (n *noopSessionStore) RevokeAllUserSessions(ctx context.Context, tenantID, userID uuid.UUID) error {
	return nil
}

func (n *noopSessionStore) SetUserRevokedBefore(ctx context.Context, userID uuid.UUID, revokedAt time.Time, ttl time.Duration) error {
	return nil
}

func (n *noopSessionStore) IsUserRevoked(ctx context.Context, userID uuid.UUID, issuedAt time.Time) (bool, error) {
	return false, nil
}

func (n *noopSessionStore) ListUserSessions(ctx context.Context, tenantID, userID uuid.UUID, page, pageSize int) ([]*UserSession, int64, error) {
	return []*UserSession{}, 0, nil
}
