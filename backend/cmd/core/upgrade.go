package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/core"
)

// upgrade replaces the core without stopping the bots:
//
//	docker compose exec -T core core upgrade < core
//
// The binary (core from the new release, Linux/amd64) is kept in
// /data/core/bin; the running core hands its bots to it. The next restart
// of the container runs the core from the image again: put the same
// version in CORE_VERSION.
func upgrade(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	cfg, err := loadCfg(fs, args)
	if err != nil {
		return err
	}
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		return errors.New("pass the new core binary on stdin: core upgrade < core")
	}
	dir := filepath.Join(cfg.DataDir, "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(os.Stdin, 512<<20)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	path := filepath.Join(dir, "core-"+hex.EncodeToString(h.Sum(nil))[:12])
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}

	// It must run here and be a core.
	vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	out, err := exec.CommandContext(vctx, path, "--version").Output()
	cancel()
	if err != nil || !bytes.HasPrefix(out, []byte("core")) {
		os.Remove(path)
		return fmt.Errorf("the file is not a core binary for this system: %v", err)
	}
	fmt.Fprintf(os.Stderr, "new: %s", out)

	c, err := core.DialCLI(ctx, cfg)
	if err != nil {
		return fmt.Errorf("the core is not running (%v)", err)
	}
	defer c.Close()
	cctx, cancel := context.WithTimeout(ctx, core.HandoverTimeout+30*time.Second)
	defer cancel()
	if err := c.Call(cctx, "core.upgrade", core.Upgrade{Path: path}, nil); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "the bots are handed over; the old core is stopping")
	return nil
}
