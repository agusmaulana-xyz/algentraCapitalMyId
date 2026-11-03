package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	dbstore "github.com/rnpproject/algentra-backend/internal/database"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var ErrInvalidCredentials = errors.New("username atau password salah")

var dummyAdminHash = []byte("$2b$12$1t6MFEyCHEBEyRJ7oV1kjOC0tnri72BQkJncJC6bKlkyaBEO6u5Ni")

func EnsureAdmin(ctx context.Context, pool *dbstore.Pool, username, password string) error {
	var existing string
	err := pool.QueryRow(ctx, `SELECT password_hash FROM admin_users WHERE username = $1`, username).Scan(&existing)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("baca akun admin: %w", err)
	}
	if err == nil && VerifyAdminPassword(password, existing) {
		return nil
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return errors.New("password admin tidak dapat diproses bcrypt")
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO admin_users (username, password_hash, created_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (username) DO UPDATE SET password_hash = EXCLUDED.password_hash
	`, username, string(passwordHash), time.Now().UTC())
	if err != nil {
		return fmt.Errorf("simpan akun admin: %w", err)
	}
	return nil
}

func AuthenticateAdmin(ctx context.Context, pool *dbstore.Pool, username, password string) error {
	var stored string
	err := pool.QueryRow(ctx, `SELECT password_hash FROM admin_users WHERE username = $1`, username).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		_ = VerifyAdminPassword(password, string(dummyAdminHash))
		return ErrInvalidCredentials
	}
	if err != nil {
		return fmt.Errorf("baca akun admin: %w", err)
	}
	if !VerifyAdminPassword(password, stored) {
		return ErrInvalidCredentials
	}
	return nil
}

func AuthenticateClient(ctx context.Context, pool *dbstore.Pool, email, password string) (int64, error) {
	var userID int64
	var stored string
	err := pool.QueryRow(ctx, `SELECT id, password_hash FROM client_users WHERE email = $1`, email).Scan(&userID, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		_ = VerifyClientPassword(password, string(dummyAdminHash))
		return 0, ErrInvalidCredentials
	}
	if err != nil {
		return 0, fmt.Errorf("baca akun client: %w", err)
	}
	if !VerifyClientPassword(password, stored) {
		return 0, ErrInvalidCredentials
	}
	return userID, nil
}

func VerifyAdminPassword(password, storedHash string) bool {
	if bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(password)) == nil {
		return true
	}
	digest := sha256.Sum256([]byte(password))
	return bcrypt.CompareHashAndPassword([]byte(storedHash), digest[:]) == nil
}

func HashClientPassword(password string) (string, error) {
	digest := sha256.Sum256([]byte(password))
	hash, err := bcrypt.GenerateFromPassword(digest[:], 12)
	if err != nil {
		return "", errors.New("password tidak dapat diproses bcrypt")
	}
	return string(hash), nil
}

func VerifyClientPassword(password, storedHash string) bool {
	if bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(password)) == nil {
		return true
	}
	digest := sha256.Sum256([]byte(password))
	return bcrypt.CompareHashAndPassword([]byte(storedHash), digest[:]) == nil
}
