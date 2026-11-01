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
