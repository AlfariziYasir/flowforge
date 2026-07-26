package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

var (
	ErrInvalidToken     = errors.New("invalid token")
	ErrExpiredToken     = errors.New("token has expired")
	ErrTokenTypeMismatch = errors.New("token type mismatch")
)

const (
	TokenTypeAccess  = "access"
	TokenTypeRefresh = "refresh"
)

// TokenPair contains generated access and refresh tokens.
type TokenPair struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int64  `json:"expiresIn"` // Access token expiration in seconds
}

// CustomClaims defines the JWT claims payload.
type CustomClaims struct {
	JTI       string    `json:"jti,omitempty"`
	UserID    uuid.UUID `json:"sub"`
	TenantID  uuid.UUID `json:"tenantId"`
	Email     string    `json:"email,omitempty"`
	Role      string    `json:"role,omitempty"`
	TokenType string    `json:"type"`
	jwt.RegisteredClaims
}

// JWTService handles token generation and validation.
type JWTService interface {
	GenerateTokenPair(userID, tenantID uuid.UUID, email, role string) (*TokenPair, error)
	ValidateAccessToken(tokenStr string) (*CustomClaims, error)
	ValidateRefreshToken(tokenStr string) (*CustomClaims, error)
}

type jwtService struct {
	secretKey     []byte
	accessExpiry  time.Duration
	refreshExpiry time.Duration
}

// NewJWTService creates a new JWTService.
func NewJWTService(secretKey string, accessExpiry, refreshExpiry time.Duration) JWTService {
	return &jwtService{
		secretKey:     []byte(secretKey),
		accessExpiry:  accessExpiry,
		refreshExpiry: refreshExpiry,
	}
}

// GenerateTokenPair creates access and refresh tokens for a user and tenant.
func (s *jwtService) GenerateTokenPair(userID, tenantID uuid.UUID, email, role string) (*TokenPair, error) {
	now := time.Now()

	accessJTI := uuid.New().String()
	// Access Token
	accessClaims := CustomClaims{
		JTI:       accessJTI,
		UserID:    userID,
		TenantID:  tenantID,
		Email:     email,
		Role:      role,
		TokenType: TokenTypeAccess,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        accessJTI,
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.accessExpiry)),
		},
	}
	accessToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims).SignedString(s.secretKey)
	if err != nil {
		return nil, fmt.Errorf("sign access token: %w", err)
	}

	refreshJTI := uuid.New().String()
	// Refresh Token
	refreshClaims := CustomClaims{
		JTI:       refreshJTI,
		UserID:    userID,
		TenantID:  tenantID,
		TokenType: TokenTypeRefresh,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        refreshJTI,
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.refreshExpiry)),
		},
	}
	refreshToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims).SignedString(s.secretKey)
	if err != nil {
		return nil, fmt.Errorf("sign refresh token: %w", err)
	}

	return &TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    int64(s.accessExpiry.Seconds()),
	}, nil
}

// ValidateAccessToken validates an access token string and returns parsed claims.
func (s *jwtService) ValidateAccessToken(tokenStr string) (*CustomClaims, error) {
	claims, err := s.parseAndValidate(tokenStr)
	if err != nil {
		return nil, err
	}

	if claims.TokenType != TokenTypeAccess {
		return nil, ErrTokenTypeMismatch
	}

	return claims, nil
}

// ValidateRefreshToken validates a refresh token string and returns parsed claims.
func (s *jwtService) ValidateRefreshToken(tokenStr string) (*CustomClaims, error) {
	claims, err := s.parseAndValidate(tokenStr)
	if err != nil {
		return nil, err
	}

	if claims.TokenType != TokenTypeRefresh {
		return nil, ErrTokenTypeMismatch
	}

	return claims, nil
}

func (s *jwtService) parseAndValidate(tokenStr string) (*CustomClaims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &CustomClaims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.secretKey, nil
	})

	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrExpiredToken
		}
		return nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}

	claims, ok := token.Claims.(*CustomClaims)
	if !ok || !token.Valid {
		return nil, ErrInvalidToken
	}

	return claims, nil
}
