package botconfig

import "github.com/SnimanOff/UTBPanel/backend/internal/journal"

type ApplyRequest struct {
	BotID    int64         `cbor:"1,keyasint"`
	Version  string        `cbor:"2,keyasint"`
	Document Document      `cbor:"3,keyasint"`
	Actor    journal.Actor `cbor:"9,keyasint"`
}

// ReleaseRequest names a stored version whose tgpanel.yaml is read.
type ReleaseRequest struct {
	BotID   int64  `cbor:"1,keyasint"`
	Version string `cbor:"2,keyasint"`
}

type ApplyResult struct {
	Snapshot       Snapshot          `cbor:"1,keyasint"`
	Applied        bool              `cbor:"2,keyasint" json:"applied"`
	Restart        bool              `cbor:"3,keyasint" json:"restart"`
	MissingSecrets []string          `cbor:"4,keyasint" json:"missing_secrets"`
	ProfileErrors  map[string]string `cbor:"5,keyasint" json:"profile_errors"`
}
