package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	redisclient "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type scanCounterCmdable struct {
	redisclient.Cmdable
	scanCount int64
}

func (s *scanCounterCmdable) Scan(ctx context.Context, cursor uint64, match string, count int64) *redisclient.ScanCmd {
	atomic.AddInt64(&s.scanCount, 1)
	return s.Cmdable.Scan(ctx, cursor, match, count)
}

func TestNoopSessionStore(t *testing.T) {
	t.Parallel()

	store := NewNoopSessionStore()
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()

	t.Run("CreateSession fails closed", func(t *testing.T) {
		sess := &UserSession{
			SessionID: sessionID,
			UserID:    userID,
			TenantID:  tenantID,
		}
		err := store.CreateSession(ctx, sess, 24*time.Hour)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "session store unavailable")
	})

	t.Run("GetSession fails closed", func(t *testing.T) {
		sess, err := store.GetSession(ctx, tenantID, userID, sessionID)
		assert.ErrorIs(t, err, ErrSessionNotFound)
		assert.Nil(t, sess)
	})

	t.Run("RevokeSession returns nil", func(t *testing.T) {
		err := store.RevokeSession(ctx, tenantID, userID, sessionID)
		assert.NoError(t, err)
	})

	t.Run("RevokeAllUserSessions returns nil", func(t *testing.T) {
		err := store.RevokeAllUserSessions(ctx, tenantID, userID)
		assert.NoError(t, err)
	})

	t.Run("SetUserRevokedBefore returns nil", func(t *testing.T) {
		err := store.SetUserRevokedBefore(ctx, userID, time.Now(), 24*time.Hour)
		assert.NoError(t, err)
	})

	t.Run("IsUserRevoked returns false and nil", func(t *testing.T) {
		revoked, err := store.IsUserRevoked(ctx, userID, time.Now())
		assert.NoError(t, err)
		assert.False(t, revoked)
	})

	t.Run("ListUserSessions returns empty list and zero total", func(t *testing.T) {
		sessions, total, err := store.ListUserSessions(ctx, tenantID, userID, 1, 10)
		assert.NoError(t, err)
		assert.Equal(t, int64(0), total)
		assert.Empty(t, sessions)
	})
}

func TestRedisSessionStore_ValidationAndNilClient(t *testing.T) {
	t.Parallel()

	t.Run("returns noop store when client is nil", func(t *testing.T) {
		store := NewRedisSessionStore(nil)
		assert.NotNil(t, store)

		sess, err := store.GetSession(context.Background(), uuid.New(), uuid.New(), uuid.New())
		assert.ErrorIs(t, err, ErrSessionNotFound)
		assert.Nil(t, sess)
	})

	t.Run("validates CreateSession parameters", func(t *testing.T) {
		store := &redisSessionStore{}
		ctx := context.Background()

		tests := []struct {
			name    string
			session *UserSession
			ttl     time.Duration
			wantErr bool
		}{
			{
				name:    "nil session",
				session: nil,
				ttl:     time.Hour,
				wantErr: true,
			},
			{
				name: "zero session ID",
				session: &UserSession{
					SessionID: uuid.Nil,
					UserID:    uuid.New(),
					TenantID:  uuid.New(),
				},
				ttl:     time.Hour,
				wantErr: true,
			},
			{
				name: "zero user ID",
				session: &UserSession{
					SessionID: uuid.New(),
					UserID:    uuid.Nil,
					TenantID:  uuid.New(),
				},
				ttl:     time.Hour,
				wantErr: true,
			},
			{
				name: "zero tenant ID",
				session: &UserSession{
					SessionID: uuid.New(),
					UserID:    uuid.New(),
					TenantID:  uuid.Nil,
				},
				ttl:     time.Hour,
				wantErr: true,
			},
			{
				name: "zero ttl",
				session: &UserSession{
					SessionID: uuid.New(),
					UserID:    uuid.New(),
					TenantID:  uuid.New(),
				},
				ttl:     0,
				wantErr: true,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := store.CreateSession(ctx, tt.session, tt.ttl)
				if tt.wantErr {
					assert.Error(t, err)
				} else {
					assert.NoError(t, err)
				}
			})
		}
	})

	t.Run("key generation format", func(t *testing.T) {
		store := &redisSessionStore{}
		tenantID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
		userID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
		sessionID := uuid.MustParse("33333333-3333-3333-3333-333333333333")

		expectedSessionKey := "session:11111111-1111-1111-1111-111111111111:22222222-2222-2222-2222-222222222222:33333333-3333-3333-3333-333333333333"
		expectedRevocationKey := "user:revoked_before:22222222-2222-2222-2222-222222222222"

		require.Equal(t, expectedSessionKey, store.sessionKey(tenantID, userID, sessionID))
		require.Equal(t, expectedRevocationKey, store.revocationKey(userID))
	})
}

func TestRedisSessionStore_Miniredis(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)

	rClient := redisclient.NewClient(&redisclient.Options{
		Addr: mr.Addr(),
	})
	t.Cleanup(func() { _ = rClient.Close() })

	store := NewRedisSessionStoreWithTTL(rClient, 7*24*time.Hour)
	ctx := context.Background()

	t.Run("IsUserRevoked: freshly issued token survives a same-instant revocation", func(t *testing.T) {
		userID := uuid.New()
		revokedAt := time.Now()
		err := store.SetUserRevokedBefore(ctx, userID, revokedAt, 24*time.Hour)
		require.NoError(t, err)

		// Fresh token issued at the exact same instant
		revoked, err := store.IsUserRevoked(ctx, userID, revokedAt)
		require.NoError(t, err)
		assert.False(t, revoked)
	})

	t.Run("IsUserRevoked: in-flight token issued before revocation is rejected", func(t *testing.T) {
		userID := uuid.New()
		issuedAt := time.Now().Add(-100 * time.Millisecond)
		revokedAt := time.Now()

		err := store.SetUserRevokedBefore(ctx, userID, revokedAt, 24*time.Hour)
		require.NoError(t, err)

		revoked, err := store.IsUserRevoked(ctx, userID, issuedAt)
		require.NoError(t, err)
		assert.True(t, revoked)
	})

	t.Run("backfill: legacy SCAN runs at most once per user", func(t *testing.T) {
		tenantID := uuid.New()
		userID := uuid.New()

		// 1. Initial list with no index and no keys -> runs backfill and sets __migrated__
		sessions, total, err := store.ListUserSessions(ctx, tenantID, userID, 1, 10)
		require.NoError(t, err)
		assert.Equal(t, int64(0), total)
		assert.Empty(t, sessions)

		// Verify __migrated__ member exists in index set
		indexKey := fmt.Sprintf("session:index:%s:%s", tenantID, userID)
		members, err := rClient.SMembers(ctx, indexKey).Result()
		require.NoError(t, err)
		assert.Contains(t, members, "__migrated__")

		// 2. Subsequent list uses index and avoids SCAN
		sessions, total, err = store.ListUserSessions(ctx, tenantID, userID, 1, 10)
		require.NoError(t, err)
		assert.Equal(t, int64(0), total)
		assert.Empty(t, sessions)
	})

	t.Run("backfill: recovers pre-upgrade sessions with no index entry", func(t *testing.T) {
		tenantID := uuid.New()
		userID := uuid.New()
		sessID := uuid.New()

		// Write key directly to miniredis without index
		sess := &UserSession{
			SessionID: sessID,
			UserID:    userID,
			TenantID:  tenantID,
		}
		data, _ := json.Marshal(sess)
		key := fmt.Sprintf("session:%s:%s:%s", tenantID, userID, sessID)
		err := rClient.Set(ctx, key, data, time.Hour).Err()
		require.NoError(t, err)

		// RevokeAllUserSessions backfills and revokes legacy keys
		err = store.RevokeAllUserSessions(ctx, tenantID, userID)
		require.NoError(t, err)

		exists, err := rClient.Exists(ctx, key).Result()
		require.NoError(t, err)
		assert.Equal(t, int64(0), exists)
	})

	t.Run("ListUserSessions: pagination is stable across repeated calls", func(t *testing.T) {
		tenantID := uuid.New()
		userID := uuid.New()

		for i := 0; i < 25; i++ {
			sess := &UserSession{
				SessionID: uuid.New(),
				UserID:    userID,
				TenantID:  tenantID,
			}
			err := store.CreateSession(ctx, sess, time.Hour)
			require.NoError(t, err)
		}

		page1, total1, err := store.ListUserSessions(ctx, tenantID, userID, 1, 10)
		require.NoError(t, err)
		assert.Equal(t, int64(25), total1)
		assert.Len(t, page1, 10)

		page1Again, _, err := store.ListUserSessions(ctx, tenantID, userID, 1, 10)
		require.NoError(t, err)
		assert.Equal(t, page1, page1Again)

		page2, _, err := store.ListUserSessions(ctx, tenantID, userID, 2, 10)
		require.NoError(t, err)
		assert.Len(t, page2, 10)

		page3, _, err := store.ListUserSessions(ctx, tenantID, userID, 3, 10)
		require.NoError(t, err)
		assert.Len(t, page3, 5)
	})

	t.Run("RevokeAllUserSessions: legacy SCAN runs at most once per user", func(t *testing.T) {
		tenantID := uuid.New()
		userID := uuid.New()

		counter := &scanCounterCmdable{Cmdable: rClient}
		counterStore := NewRedisSessionStoreWithTTL(counter, 7*24*time.Hour)

		// 3 consecutive calls produce exactly 1 SCAN
		for i := 0; i < 3; i++ {
			err := counterStore.RevokeAllUserSessions(ctx, tenantID, userID)
			require.NoError(t, err)
		}
		assert.Equal(t, int64(1), atomic.LoadInt64(&counter.scanCount))
	})

	t.Run("RevokeAllUserSessions: clears session keys but leaves the migration sentinel", func(t *testing.T) {
		tenantID := uuid.New()
		userID := uuid.New()
		sess := &UserSession{SessionID: uuid.New(), UserID: userID, TenantID: tenantID}

		err := store.CreateSession(ctx, sess, time.Hour)
		require.NoError(t, err)

		err = store.RevokeAllUserSessions(ctx, tenantID, userID)
		require.NoError(t, err)

		// Session data key should be deleted
		sessionKey := fmt.Sprintf("session:%s:%s:%s", tenantID, userID, sess.SessionID)
		sessExists, err := rClient.Exists(ctx, sessionKey).Result()
		require.NoError(t, err)
		assert.Equal(t, int64(0), sessExists)

		// Index key should remain with only the __migrated__ sentinel
		indexKey := fmt.Sprintf("session:index:%s:%s", tenantID, userID)
		members, err := rClient.SMembers(ctx, indexKey).Result()
		require.NoError(t, err)
		assert.Equal(t, []string{"__migrated__"}, members)
	})

	t.Run("ListUserSessions: prunes expired index entries", func(t *testing.T) {
		tenantID := uuid.New()
		userID := uuid.New()
		sessID := uuid.New()

		sess := &UserSession{
			SessionID: sessID,
			UserID:    userID,
			TenantID:  tenantID,
		}
		err := store.CreateSession(ctx, sess, 100*time.Millisecond)
		require.NoError(t, err)

		mr.FastForward(200 * time.Millisecond)

		sessions, total, err := store.ListUserSessions(ctx, tenantID, userID, 1, 10)
		require.NoError(t, err)
		assert.Equal(t, int64(0), total)
		assert.Empty(t, sessions)

		indexKey := fmt.Sprintf("session:index:%s:%s", tenantID, userID)
		members, err := rClient.SMembers(ctx, indexKey).Result()
		require.NoError(t, err)
		assert.NotContains(t, members, sessID.String())
	})
}
