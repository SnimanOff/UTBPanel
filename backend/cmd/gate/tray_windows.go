//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"unicode/utf16"

	"fyne.io/systray"
	"golang.org/x/sys/windows/registry"

	"github.com/SnimanOff/UTBPanel/backend/internal/gate"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const runName = "TGPanelGate"

// tray shows the icon with the menu and Windows notifications; it blocks
// until "Выход".
func tray(ctx context.Context, stop context.CancelFunc, g *gate.Gate) {
	var win mini
	systray.Run(func() {
		systray.SetIcon(icon(false))
		systray.SetTooltip("TG Panel")
		open := systray.AddMenuItem("Открыть панель", "")
		server := systray.AddMenuItem("Сервер: "+g.Server(), "Изменить адрес сервера")
		systray.AddSeparator()
		copyTok := systray.AddMenuItem("Токен для MCP: скопировать", "")
		newTok := systray.AddMenuItem("Токен для MCP: заменить", "")
		systray.AddSeparator()
		auto := systray.AddMenuItemCheckbox("Запускать вместе с Windows", "", autostart())
		quit := systray.AddMenuItem("Выход", "")
		// A click on the icon opens the small window; the menu opens the panel.
		systray.SetOnTapped(func() { win.open(g) })

		go g.Watch(ctx, func(n gate.Note) {
			if n.Severity == "ok" || n.Severity == "error" || n.Severity == "warn" {
				toast("TG Panel", n.Text)
			}
		}, func(ok bool) {
			systray.SetIcon(icon(!ok))
			if ok {
				systray.SetTooltip("TG Panel")
				toast("TG Panel", "Сервер снова доступен")
			} else {
				systray.SetTooltip("TG Panel: сервер недоступен")
				toast("TG Panel", "Сервер недоступен: "+g.Server())
			}
		})
		go func() {
			for {
				select {
				case <-open.ClickedCh:
					g.OpenPanel()
				case <-server.ClickedCh:
					g.OpenPage("/__gate/server")
				case <-copyTok.ClickedCh:
					clipboard(g.MCPToken())
					toast("TG Panel", "Токен для MCP скопирован")
				case <-newTok.ClickedCh:
					if t, err := g.RotateMCPToken(); err == nil {
						clipboard(t)
						toast("TG Panel", "Новый токен для MCP скопирован; старый больше не действует")
					}
				case <-auto.ClickedCh:
					if auto.Checked() {
						setAutostart(false)
						auto.Uncheck()
					} else if setAutostart(true) == nil {
						auto.Check()
					}
				case <-quit.ClickedCh:
					win.close()
					stop()
					systray.Quit()
					return
				case <-ctx.Done():
					systray.Quit()
					return
				}
			}
		}()
	}, func() { stop() })
}

func autostart() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runName)
	return err == nil
}

func setAutostart(on bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		return k.DeleteValue(runName)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return k.SetStringValue(runName, `"`+exe+`"`)
}

func hidden(cmd *exec.Cmd) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	return cmd
}

func clipboard(s string) {
	cmd := hidden(exec.Command("clip"))
	cmd.Stdin = strings.NewReader(s)
	cmd.Run()
}

// toast shows a Windows notification through PowerShell's WinRT access;
// texts are passed as base64 so nothing in them is interpreted.
func toast(title, text string) {
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	script := `$d=[Text.Encoding]::UTF8;$t=$d.GetString([Convert]::FromBase64String('` + b64(title) + `'));` +
		`$m=$d.GetString([Convert]::FromBase64String('` + b64(text) + `'));` +
		`[Windows.UI.Notifications.ToastNotificationManager,Windows.UI.Notifications,ContentType=WindowsRuntime]|Out-Null;` +
		`$x=[Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02);` +
		`$n=$x.GetElementsByTagName('text');$n.Item(0).AppendChild($x.CreateTextNode($t))|Out-Null;$n.Item(1).AppendChild($x.CreateTextNode($m))|Out-Null;` +
		`$a='{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe';` +
		`[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($a).Show([Windows.UI.Notifications.ToastNotification]::new($x))`
	u := utf16.Encode([]rune(script))
	buf := make([]byte, len(u)*2)
	for i, c := range u {
		binary.LittleEndian.PutUint16(buf[i*2:], c)
	}
	hidden(exec.Command("powershell", "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(buf))).Start()
}

// icon draws a 32×32 square: violet, or red when the server is down.
func icon(down bool) []byte {
	const n = 32
	r, g, b := byte(0x6b), byte(0x2f), byte(0xa3)
	if down {
		r, g, b = 0xc8, 0x10, 0x2e
	}
	var img bytes.Buffer
	le := func(v any) { binary.Write(&img, binary.LittleEndian, v) }
	// BITMAPINFOHEADER: height doubled for the AND mask.
	le(uint32(40))
	le(int32(n))
	le(int32(n * 2))
	le(uint16(1))
	le(uint16(32))
	le(uint32(0))
	le(uint32(0))
	le(int32(0))
	le(int32(0))
	le(uint32(0))
	le(uint32(0))
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			edge := x < 3 || y < 3 || x >= n-3 || y >= n-3
			if edge {
				img.Write([]byte{0xee, 0xe4, 0xe9, 0xff}) // light frame, BGRA
			} else {
				img.Write([]byte{b, g, r, 0xff})
			}
		}
	}
	img.Write(make([]byte, n*n/8)) // AND mask: all opaque
	var out bytes.Buffer
	w := func(v any) { binary.Write(&out, binary.LittleEndian, v) }
	w(uint16(0))
	w(uint16(1))
	w(uint16(1))
	out.Write([]byte{n, n, 0, 0})
	w(uint16(1))
	w(uint16(32))
	w(uint32(img.Len()))
	w(uint32(6 + 16))
	out.Write(img.Bytes())
	return out.Bytes()
}
