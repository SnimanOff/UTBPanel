//go:build darwin && cgo

package main

import (
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const autostartLabel = "Открывать при входе в систему"

func agentFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "LaunchAgents", "com.tgpanel.gate.plist")
}

func autostart() bool {
	_, err := os.Stat(agentFile())
	return err == nil
}

func setAutostart(on bool) error {
	path := agentFile()
	if !on {
		return os.Remove(path)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>com.tgpanel.gate</string>
<key>ProgramArguments</key><array><string>` + html.EscapeString(exe) + `</string></array>
<key>RunAtLoad</key><true/>
</dict></plist>
`
	return os.WriteFile(path, []byte(plist), 0o644)
}

func clipboard(s string) {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(s)
	cmd.Run()
}

// toast passes the texts as script arguments: nothing in them is
// interpreted by AppleScript.
func toast(title, text string) {
	exec.Command("osascript",
		"-e", "on run argv",
		"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
		"-e", "end run", title, text).Start()
}
