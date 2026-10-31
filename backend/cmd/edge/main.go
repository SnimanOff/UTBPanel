package main

import (
	"fmt"

	"github.com/SnimanOff/UTBPanel/backend/internal/buildinfo"
)

func main() {
	fmt.Println(buildinfo.String("edge"))
}
