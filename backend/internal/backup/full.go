package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

type FullBot struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	State   string `json:"state"`
}
type FullManifest struct {
	Format   string            `json:"format"`
	Version  int               `json:"version"`
	Created  time.Time         `json:"created"`
	Versions map[string]string `json:"versions"`
	Bots     []FullBot         `json:"bots"`
	Entries  map[string]Entry  `json:"entries"`
}
type FullSettings struct {
	Enabled bool   `json:"enabled"`
	Cron    string `json:"cron"`
	Keep    int    `json:"keep"`
	Files   bool   `json:"files"`
}

func DefaultFullSettings() FullSettings { return FullSettings{Cron: "0 3 * * *", Keep: 7} }
func (s FullSettings) Validate() error {
	if s.Keep < 1 || s.Keep > 100 {
		return fmt.Errorf("хранить от 1 до 100 архивов")
	}
	if len(strings.Fields(s.Cron)) != 5 {
		return fmt.Errorf("расписание: пять полей cron, UTC")
	}
	schedule, err := cron.ParseStandard(s.Cron)
	if err != nil {
		return err
	}
	if schedule.Next(time.Now().UTC()).IsZero() {
		return fmt.Errorf("расписание не имеет ближайшего запуска")
	}
	return nil
}
func (s FullSettings) Next(after time.Time) time.Time {
	schedule, err := cron.ParseStandard(s.Cron)
	if err != nil {
		return time.Time{}
	}
	return schedule.Next(after.UTC())
}
func EncryptFull(ctx context.Context, out io.Writer, key []byte, manifest FullManifest, fill func(*Archive) error) error {
	writer, err := NewChunkWriter(out, key)
	if err != nil {
		return err
	}
	manifest.Format = "utbpanel.full"
	manifest.Version = FormatVersion
	manifest.Created = time.Now().UTC()
	if manifest.Bots == nil {
		manifest.Bots = []FullBot{}
	}
	if err := writeArchive(ctx, writer, fill, func(entries map[string]Entry) ([]byte, error) {
		manifest.Entries = entries
		return json.Marshal(manifest)
	}); err != nil {
		return err
	}
	return writer.Close()
}

// Full backups use retention counts rather than short-lived export expiry.
func FullFiles(directory string) *Files {
	return &Files{Dir: directory, TTL: 100 * 365 * 24 * time.Hour}
}
func Retain(files *Files, count int) error {
	if count < 1 {
		return fmt.Errorf("retention count must be positive")
	}
	list, err := files.List()
	if err != nil {
		return err
	}
	for _, file := range list[min(count, len(list)):] {
		if err := files.Delete(FileRef{ID: file.ID, BotID: file.BotID}); err != nil {
			return err
		}
	}
	return nil
}

type PanelPart struct {
	File    File   `cbor:"1,keyasint"`
	Version string `cbor:"2,keyasint"`
}
type FullStatus struct {
	Files        []File       `json:"files"`
	Settings     FullSettings `json:"settings"`
	Busy         bool         `json:"busy"`
	Next         time.Time    `json:"next,omitzero"`
	LastError    string       `json:"last_error,omitempty"`
	LastDownload time.Time    `json:"last_download,omitzero"`
}
