// Package wire wraps backend/sdk/wire for the edge, core and panel: the
// transport lives in the SDK module because the SDK cannot import
// internal packages, but the protocol names, versions and socket paths of
// the system processes are kept here.
package wire

import (
	"path/filepath"

	sw "github.com/SnimanOff/UTBPanel/backend/sdk/wire"
)

type (
	Conn          = sw.Conn
	Client        = sw.Client
	Server        = sw.Server
	Mux           = sw.Mux
	Hello         = sw.Hello
	Raw           = sw.Raw
	Error         = sw.Error
	Stream        = sw.Stream
	StreamOptions = sw.StreamOptions
	Sender        = sw.Sender
)

var (
	NewMux    = sw.NewMux
	Listen    = sw.Listen
	Dial      = sw.Dial
	Marshal   = sw.Marshal
	Unmarshal = sw.Unmarshal
	Errorf    = sw.Errorf

	ErrClosed       = sw.ErrClosed
	ErrNotConnected = sw.ErrNotConnected
)

// Error codes.
const (
	CodeNotFound    = sw.CodeNotFound
	CodeNoMethod    = sw.CodeNoMethod
	CodeBadRequest  = sw.CodeBadRequest
	CodeInternal    = sw.CodeInternal
	CodeCanceled    = sw.CodeCanceled
	CodeUnavailable = sw.CodeUnavailable
)

// Func adapts a typed function to a handler.
func Func[In, Out any](f func(ctx Context, c *Conn, in In) (Out, error)) sw.Handler {
	return sw.Func(f)
}

// Protocols between system processes.
const (
	ProtoEdgeCore  = "edge-core"
	ProtoCorePanel = "core-panel"
	ProtoEdgePanel = "edge-panel"
	// ProtoPanelCLI lets `panel keys …` run next to a live panel tell it
	// about changes.
	ProtoPanelCLI = "panel-cli"
	// ProtoCoreCLI is spoken by `core bots …` and `core masterkey …` run
	// over SSH next to a live core.
	ProtoCoreCLI = "core-cli"
)

// Socket file names inside the shared run directory.
const (
	CoreSocket  = "core.sock"
	PanelSocket = "panel.sock"
)

// CoreSock returns the core socket path in runDir.
func CoreSock(runDir string) string { return filepath.Join(runDir, CoreSocket) }

// PanelSock returns the panel socket path in runDir.
func PanelSock(runDir string) string { return filepath.Join(runDir, PanelSocket) }
