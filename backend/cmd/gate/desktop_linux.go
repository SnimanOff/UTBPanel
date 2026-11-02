//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const autostartLabel = "Запускать при входе в систему"

func autostartFile() string {
	dir, err := os.UserConfigDir() // ~/.config
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "autostart", "tgpanel-gate.desktop")
}

func autostart() bool {
	_, err := os.Stat(autostartFile())
	return err == nil
}

func setAutostart(on bool) error {
	path := autostartFile()
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
	// Exec quoting of the desktop entry spec, then its string escaping:
	// a backslash becomes four.
	quoted := `"` + strings.NewReplacer(`\`, `\\\\`, `"`, `\\"`, "`", "\\\\`", `$`, `\\$`).Replace(exe) + `"`
	entry := "[Desktop Entry]\nType=Application\nName=TG Panel gate\nExec=" + quoted + "\nX-GNOME-Autostart-enabled=true\n"
	return os.WriteFile(path, []byte(entry), 0o644)
}

// clipboard tries Wayland first, then X11 tools.
func clipboard(s string) {
	for _, args := range [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}} {
		if _, err := exec.LookPath(args[0]); err != nil {
			continue
		}
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stdin = strings.NewReader(s)
		if cmd.Run() == nil {
			return
		}
	}
}

// toast passes the texts as arguments: nothing in them is interpreted.
func toast(title, text string) {
	exec.Command("notify-send", "--app-name=TG Panel", "--", title, text).Start()
}
