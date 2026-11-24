package core

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/runner"
	"github.com/SnimanOff/UTBPanel/backend/sdk/flags"
)

func TestDeployRegistersSDKFlagsBeforeReady(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "deploybot")
	if output, err := exec.Command("go", "build", "-o", binary, "./testdata/deploybot").CombinedOutput(); err != nil {
		t.Fatalf("build SDK bot: %v\n%s", err, output)
	}
	process, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	for _, scheduled := range []bool{false, true} {
		name := "direct"
		if scheduled {
			name = "scheduled"
		}
		t.Run(name, func(t *testing.T) {
			c := newCore(t)
			const botID int64 = 1
			if _, err := c.DB.W.Exec(`INSERT INTO bots (id,name,port,created_at) VALUES (1,'SDK',20001,'')`); err != nil {
				t.Fatal(err)
			}
			storeRelease(t, c, botID, "v1", "")
			path := filepath.Join(c.Releases.Dir(botID), "v1", "bot")
			if runtime.GOOS == "windows" {
				path += ".exe"
			} else if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, process, 0o700); err != nil {
				t.Fatal(err)
			}
			c.Runner = runner.New(runner.Config{
				DataDir: c.Cfg.DataDir, DB: c.DB, Secrets: c.Sec, SDKReadyTimeout: 3 * time.Second,
				Hooks: runner.Hooks{
					Prepare: func(_ context.Context, b runner.Bot) error { return c.SDK.Start(b.ID, b.Instance, testSDKUID()) },
					Env: func(b runner.Bot, env map[string]string) {
						env["TGPANEL_SDK"] = "unix://" + c.SDK.Path(b.ID, b.Instance)
					},
					Readiness: func(b runner.Bot) (bool, bool) { return c.SDK.Readiness(b.ID, b.Instance) },
				},
			})
			t.Cleanup(func() {
				_ = c.Runner.Stop(context.Background(), botID)
				_ = c.SDK.Close()
			})
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			in := SwitchVersion{BotID: botID, Version: "v1"}
			if scheduled {
				when := time.Now().Add(time.Minute)
				if err := c.scheduleDeploy(ctx, ScheduleDeploy{SwitchVersion: in, StartsAt: when}); err != nil {
					t.Fatal(err)
				}
				if err := c.tickScheduledDeploys(ctx, when); err != nil {
					t.Fatal(err)
				}
			} else if _, err := c.Deploy(ctx, in); err != nil {
				t.Fatal(err)
			}
			if _, ready := c.SDK.Readiness(botID, ""); !ready {
				t.Fatal("SDK bot did not signal readiness")
			}
			registered, err := c.SDKFlags.List(ctx, botID)
			if err != nil || len(registered) != 1 || registered[0].Name != "checkout" {
				t.Fatalf("flags = %+v, error = %v", registered, err)
			}
			if scheduled {
				return
			}

			if err := c.SDKFlags.Set(ctx, botID, "checkout", flags.Value{Kind: "bool", Bool: true}, false); err != nil {
				t.Fatal(err)
			}
			storeRelease(t, c, botID, "broken", "")
			if _, err := c.Deploy(ctx, SwitchVersion{BotID: botID, Version: "broken"}); code(err) != "rolled_back" {
				t.Fatalf("broken version should roll back, got %v", err)
			}
			if current, _, err := c.versions(ctx, botID); err != nil || current != "v1" || !c.Runner.IsRunning(botID) {
				t.Fatalf("previous version did not return to service: current = %q, error = %v", current, err)
			}
			registered, err = c.SDKFlags.List(ctx, botID)
			if err != nil || len(registered) != 1 || !registered[0].Value.Bool || !registered[0].Overridden {
				t.Fatalf("rollback lost the flag override: %+v, error = %v", registered, err)
			}
		})
	}
}
