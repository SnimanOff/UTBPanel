package backup

import (
	"github.com/SnimanOff/UTBPanel/backend/internal/botconfig"
	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
)

type Options struct {
	Files       bool `json:"files"`
	AllVersions bool `json:"all_versions"`
	Metrics     bool `json:"metrics"`
}
type ExportRequest struct {
	BotID    int64              `cbor:"1,keyasint"`
	Config   botconfig.Snapshot `cbor:"2,keyasint"`
	Password string             `cbor:"3,keyasint"`
	Options  Options            `cbor:"4,keyasint"`
	Metrics  *File              `cbor:"5,keyasint,omitempty"`
	Actor    journal.Actor      `cbor:"9,keyasint"`
}

type ImportChunk struct {
	ID     string `cbor:"1,keyasint"`
	Offset int64  `cbor:"2,keyasint"`
	Data   []byte `cbor:"3,keyasint"`
}
type ImportInspect struct {
	ID       string `cbor:"1,keyasint"`
	Password string `cbor:"2,keyasint"`
}
type SecretInput struct {
	Kind  string `cbor:"1,keyasint"`
	Value []byte `cbor:"2,keyasint"`
}
type ImportApply struct {
	ID      string                 `cbor:"1,keyasint"`
	Name    string                 `cbor:"2,keyasint"`
	Token   string                 `cbor:"3,keyasint"`
	Secrets map[string]SecretInput `cbor:"4,keyasint"`
	Actor   journal.Actor          `cbor:"9,keyasint"`
}
