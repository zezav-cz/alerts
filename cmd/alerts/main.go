// Command alerts runs monitoring-plugins style checks and delivers alerts.
package main

import (
	"fmt"
	"os"
)

// version is set at release time with -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(version)
		return
	}
	fmt.Fprintln(os.Stderr, "usage: alerts --version")
	os.Exit(2)
}
