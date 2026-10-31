package main

import (
	"fmt"
	"os"

	"github.com/SnimanOff/UTBPanel/backend/internal/buildinfo"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println(buildinfo.String("gate"))
		return
	}
	fmt.Println(buildinfo.String("gate"))
}
