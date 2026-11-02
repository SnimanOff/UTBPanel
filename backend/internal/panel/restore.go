package panel

import (
	"context"
	"os"
	"path/filepath"

	"github.com/SnimanOff/UTBPanel/backend/internal/app"
	"github.com/SnimanOff/UTBPanel/backend/internal/backup"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

func (p *Panel) prepareRestore(ctx context.Context, id string) error {
	p.restoreMu.Lock()
	defer p.restoreMu.Unlock()
	if p.restoring.Load() {
		return wire.Errorf("conflict", "панель уже восстанавливается")
	}
	return p.restoreUploads.With(id, func(dir string, root *os.Root) error {
		os.RemoveAll(filepath.Join(dir, "data"))
		if err := root.Mkdir("data", 0o700); err != nil {
			return err
		}
		file, err := root.Open("upload.bin")
		if err != nil {
			return err
		}
		defer file.Close()
		dataDir := filepath.Join(dir, "data")
		if err := backup.ExtractPanel(ctx, file, dataDir); err != nil {
			return err
		}
		db, err := store.Open(ctx, filepath.Join(dataDir, "panel.db"), migrations.Panel())
		if err != nil {
			return err
		}
		if err := db.Close(); err != nil {
			return err
		}
		if p.restorePrepared == nil {
			p.restorePrepared = map[string]string{}
		}
		p.restorePrepared[id] = dataDir
		return nil
	})
}
func (p *Panel) commitRestore(ctx context.Context, id string) (wire.AfterReply, error) {
	p.restoreMu.Lock()
	dir, ok := p.restorePrepared[id]
	p.restoreMu.Unlock()
	if !ok {
		return wire.AfterReply{}, wire.Errorf("conflict", "панель ещё не подготовлена")
	}
	if !p.restoring.CompareAndSwap(false, true) {
		return wire.AfterReply{}, wire.Errorf("conflict", "панель уже восстанавливается")
	}
	done := make(chan error, 1)
	replied := make(chan struct{})
	request := app.RestartRequest{Done: done, Replied: replied, Apply: func() error {
		err := backup.Install(p.Cfg.DataDir, []backup.InstallItem{{Source: filepath.Join(dir, "panel.db"), Target: filepath.Join(p.Cfg.DataDir, "panel.db")}, {Source: filepath.Join(dir, "pki"), Target: filepath.Join(p.Cfg.DataDir, "pki")}})
		if err == nil {
			p.restoreUploads.Delete(id)
		}
		return err
	}}
	select {
	case p.restart <- request:
	case <-ctx.Done():
		p.restoring.Store(false)
		return wire.AfterReply{}, ctx.Err()
	}
	select {
	case err := <-done:
		result := backup.RestoreResult{ID: id, Done: true}
		if err != nil {
			result.Error = err.Error()
		}
		return wire.AfterReply{Value: result, Run: func() { close(replied) }}, nil
	case <-ctx.Done():
		return wire.AfterReply{}, ctx.Err()
	}
}
func (p *Panel) registerRestore() {
	p.restoreUploads = &backup.Staging{Dir: filepath.Join(p.Cfg.DataDir, "restore")}
	p.Go(p.restoreUploads.Run)
	p.CoreMux.Handle("restore.panel.begin", wire.Func(func(_ context.Context, _ *wire.Conn, _ struct{}) (string, error) { return p.restoreUploads.Begin() }))
	p.CoreMux.Handle("restore.panel.chunk", wire.Func(func(_ context.Context, _ *wire.Conn, in backup.ImportChunk) (struct{}, error) {
		return struct{}{}, p.restoreUploads.Append(in.ID, in.Offset, in.Data)
	}))
	p.CoreMux.Handle("restore.panel.prepare", wire.Func(func(ctx context.Context, _ *wire.Conn, id string) (struct{}, error) {
		return struct{}{}, p.prepareRestore(ctx, id)
	}))
	p.CoreMux.Handle("restore.panel.commit", wire.Func(func(ctx context.Context, _ *wire.Conn, id string) (wire.AfterReply, error) {
		return p.commitRestore(ctx, id)
	}))
	p.CoreMux.Handle("restore.panel.abort", wire.Func(func(_ context.Context, _ *wire.Conn, id string) (struct{}, error) {
		if !p.restoring.Load() {
			p.restoreUploads.Delete(id)
		}
		return struct{}{}, nil
	}))
}
