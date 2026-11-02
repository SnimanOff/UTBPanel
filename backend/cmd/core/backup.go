package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/backup"
	"github.com/SnimanOff/UTBPanel/backend/internal/core"
)

func backupRestore(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("backup restore", flag.ContinueOnError)
	cfg, err := loadCfg(fs, args)
	if err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: core backup restore < archive.backup")
	}
	conn, err := core.DialCLI(ctx, cfg)
	if err != nil {
		return err
	}
	defer conn.Close()
	var id string
	if err := conn.Call(ctx, "restore.begin", struct{}{}, &id); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			conn.Call(cleanup, "restore.abort", id, nil)
		}
	}()
	buffer := make([]byte, backup.ChunkSize)
	offset := int64(0)
	for {
		n, err := io.ReadFull(os.Stdin, buffer)
		if n > 0 {
			if callErr := conn.Call(ctx, "restore.chunk", backup.ImportChunk{ID: id, Offset: offset, Data: buffer[:n]}, nil); callErr != nil {
				return callErr
			}
			offset += int64(n)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return err
		}
	}
	fmt.Fprintln(os.Stderr, "Проверка архива и подготовка панели…")
	var result backup.RestoreResult
	if err := conn.Call(ctx, "restore.prepare", id, &result); err != nil {
		return err
	}
	if err := conn.Call(ctx, "restore.commit", id, &result); err != nil {
		return err
	}
	committed = true
	conn.Close()
	fmt.Fprintln(os.Stderr, "Перезапуск ядра и панели…")
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("не удалось дождаться восстановления: %w", waitCtx.Err())
		case <-ticker.C:
			client, err := core.DialCLI(waitCtx, cfg)
			if err != nil {
				continue
			}
			err = client.Call(waitCtx, "restore.status", id, &result)
			client.Close()
			if err != nil || !result.Done {
				continue
			}
			if result.Error != "" {
				return fmt.Errorf("восстановление не выполнено: %s", result.Error)
			}
			fmt.Fprintf(os.Stdout, "Восстановлено ботов: %d. Боты запустятся после подключения привратника.\n", result.Bots)
			return nil
		}
	}
}
