package domain

import (
	"encoding/json"
	"time"
)

// EgressProfile is owned by the consuming server. Updating its current revision
// does not move any existing node binding to the new revision.
type EgressProfile struct {
	ManagedStage    string    `json:"managed_stage,omitempty"`
	ID              int64     `json:"id"`
	ServerID        int64     `json:"server_id"`
	Name            string    `json:"name"`
	Kind            string    `json:"kind"`
	Enabled         bool      `json:"enabled"`
	CurrentRevision int64     `json:"current_revision"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// EgressRevision contains typed, non-secret configuration. Credentials are
// versioned separately in encrypted storage and never returned by this view.
type EgressRevision struct {
	ProfileID      int64           `json:"profile_id"`
	ServerID       int64           `json:"server_id"`
	Revision       int64           `json:"revision"`
	Config         json.RawMessage `json:"config"`
	HasCredentials bool            `json:"has_credentials"`
	CreatedAt      time.Time       `json:"created_at"`
}
