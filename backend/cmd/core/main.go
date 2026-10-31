package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/SnimanOff/UTBPanel/backend/internal/buildinfo"
	"github.com/SnimanOff/UTBPanel/backend/internal/config"
)

func main() {
	printConfig := flag.Bool("print-config", false, "print the resulting config and exit")
	configPath := flag.String("config", "", "YAML config file")
	flag.Parse()
	cfg := config.DefaultCore()
	if err := config.Load(&cfg, *configPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if *printConfig {
		fmt.Print(config.Print(cfg))
		return
	}
	fmt.Println(buildinfo.String("core"))
}
