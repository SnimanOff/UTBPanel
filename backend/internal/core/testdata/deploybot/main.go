package main

import (
	"os"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/sdk"
)

func main() {
	kit := sdk.New()
	kit.Flag("checkout", false, "registered before readiness")
	if err := kit.Ready(); err != nil {
		os.Exit(1)
	}
	time.Sleep(time.Hour)
}
