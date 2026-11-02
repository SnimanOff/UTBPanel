package backup

import (
	"context"
	"fmt"
	"os"
	"time"
)

type Verification struct {
	State string    `json:"state"`
	Time  time.Time `json:"time"`
	Error string    `json:"error,omitempty"`
}

// Verifier exercises the same authenticated extraction and database/binary
// checks used by restore, without touching the live installation.
type Verifier struct {
	Dir       string
	FreeSpace func(string) (uint64, error)
}

func (v Verifier) Verify(ctx context.Context, files *Files, file File, key []byte) (result Verification) {
	result = Verification{State: "failed", Time: time.Now().UTC()}
	fail := func(err error) Verification { result.Error = err.Error(); return result }
	if err := os.MkdirAll(v.Dir, 0o700); err != nil {
		return fail(err)
	}
	space := v.FreeSpace
	if space == nil {
		space = freeSpace
	}
	available, err := space(v.Dir)
	if err != nil {
		result.State = "skipped"
		return fail(fmt.Errorf("не удалось определить свободное место: %w", err))
	}
	if file.Size < 0 {
		return fail(fmt.Errorf("invalid archive size"))
	}
	if available/2 < uint64(file.Size) {
		result.State = "skipped"
		return fail(fmt.Errorf("для проверки нужно %d байт свободного места, доступно %d", uint64(file.Size)*2, available))
	}
	dir, err := os.MkdirTemp(v.Dir, "verify-")
	if err != nil {
		return fail(err)
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			result.State = "failed"
			result.Error = fmt.Sprintf("удаление временной копии: %v", err)
		}
	}()
	input, err := files.Open(FileRef{ID: file.ID, BotID: file.BotID})
	if err != nil {
		return fail(err)
	}
	defer input.Close()
	if _, err := ExtractFull(ctx, input, key, dir); err != nil {
		return fail(err)
	}
	result.State = "passed"
	return result
}
