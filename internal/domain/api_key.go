package domain

import "time"

// APIKey is a revocable developer credential bound to an account. Only its
// digest is stored; the original credential is returned once by the API.
type APIKey struct {
	SecurityVersion int64      `json:"-"`
	ID              int64      `json:"id"`
	UserID          int64      `json:"user_id"`
	Name            string     `json:"name"`
	TokenHash       string     `json:"-"`
	TokenHint       string     `json:"token_hint"`
	Scopes          []string   `json:"scopes"`
	CreatedAt       time.Time  `json:"created_at"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	LastUsedAt      *time.Time `json:"last_used_at,omitempty"`
	RevokedAt       *time.Time `json:"revoked_at,omitempty"`
}
