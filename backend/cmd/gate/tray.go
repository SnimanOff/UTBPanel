//go:build windows || linux || (darwin && cgo)

package main

import (
	"context"

	"fyne.io/systray"

	"github.com/SnimanOff/UTBPanel/backend/internal/gate"
)

// tray shows the icon with the menu and system notifications; it blocks
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
		auto := systray.AddMenuItemCheckbox(autostartLabel, "", autostart())
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
