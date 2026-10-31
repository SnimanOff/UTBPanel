//go:build !windows

package main

import "fmt"

func main() { fmt.Println("the tpm experiment runs only on Windows") }
