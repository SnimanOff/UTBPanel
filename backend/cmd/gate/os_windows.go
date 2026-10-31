//go:build windows

package main

import (
	"os"
	"os/exec"

	"golang.org/x/sys/windows"
)

func openBrowser(url string) {
	exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

// fail shows the error in a message box: gate.exe has no console.
func fail(err error) {
	title, _ := windows.UTF16PtrFromString("TG Panel gate")
	text, _ := windows.UTF16PtrFromString(err.Error())
	windows.MessageBox(0, text, title, windows.MB_OK|windows.MB_ICONERROR)
	os.Exit(1)
}
