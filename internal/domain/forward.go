package domain

import (
	"time"

	"ctlvps/internal/networkconfig"
)

// PortForward is independent of Node: it cannot own a share, appear in an
// all-node subscription or inherit client protocol credentials.
type PortForward struct {
	ID        int64                 `json:"id"`
	ServerID  int64                 `json:"server_id"`
	Name      string                `json:"name"`
	Config    networkconfig.Forward `json:"config"`
	Enabled   bool                  `json:"enabled"`
	Revision  int64                 `json:"revision"`
	Retired   bool                  `json:"retired"`
	CreatedAt time.Time             `json:"created_at"`
	UpdatedAt time.Time             `json:"updated_at"`
}
