package auth

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

var (
	// ErrEmptyPassword occurs when attempting to hash an empty password.
	ErrEmptyPassword = errors.New("password cannot be empty")
	// ErrInvalidPassword occurs when password validation/comparison fails.
	ErrInvalidPassword = errors.New("invalid password")
	// ErrInvalidPasswordLength occurs when a password is less than 8 characters or exceeds 72 bytes.
	ErrInvalidPasswordLength = errors.New("password length must be between 8 and 72 bytes")
)

const defaultBcryptCost = 12

// PasswordService defines operations for hashing and comparing passwords.
type PasswordService interface {
	HashPassword(password string) (string, error)
	ComparePassword(hashedPassword, password string) error
}

type bcryptPasswordService struct {
	cost int
}

// NewPasswordService creates a new PasswordService using bcrypt.
func NewPasswordService() PasswordService {
	return &bcryptPasswordService{
		cost: defaultBcryptCost,
	}
}

// HashPassword hashes a plain text password using bcrypt.
func (s *bcryptPasswordService) HashPassword(password string) (string, error) {
	if len(password) < 8 || len([]byte(password)) > 72 {
		return "", ErrInvalidPasswordLength
	}

	bytes, err := bcrypt.GenerateFromPassword([]byte(password), s.cost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}

	return string(bytes), nil
}

// ComparePassword compares a hashed password with a plain text password.
func (s *bcryptPasswordService) ComparePassword(hashedPassword, password string) error {
	err := bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(password))
	if err != nil {
		return ErrInvalidPassword
	}

	return nil
}
