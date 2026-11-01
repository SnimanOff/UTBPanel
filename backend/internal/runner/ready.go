package runner

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"
)

func (r *Runner) waitReady(ctx context.Context, b Bot, done <-chan struct{}) error {
	started := time.Now()
	deadline := started.Add(ReadyTimeout)
	sdkTimeout := r.cfg.SDKReadyTimeout
	if sdkTimeout <= 0 {
		sdkTimeout = time.Minute
	}
	sdkMode := false
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(b.Port))
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return ErrExited
		default:
		}
		usesSDK, ready := false, false
		if r.cfg.Hooks.Readiness != nil {
			usesSDK, ready = r.cfg.Hooks.Readiness(b)
		}
		if usesSDK && !sdkMode {
			sdkMode = true
			deadline = started.Add(sdkTimeout)
		}
		if sdkMode {
			if ready {
				return nil
			}
		} else {
			conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
			if err == nil {
				conn.Close()
				// SDK registration may have arrived while dialing.
				if r.cfg.Hooks.Readiness != nil {
					usesSDK, _ = r.cfg.Hooks.Readiness(b)
				}
				if !usesSDK {
					return nil
				}
				continue
			}
		}
		if time.Now().After(deadline) {
			if sdkMode {
				return fmt.Errorf("runner: SDK ready signal not received after %v", sdkTimeout)
			}
			return fmt.Errorf("runner: port %d not open after %v", b.Port, ReadyTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return ErrExited
		case <-time.After(20 * time.Millisecond):
		}
	}
}
