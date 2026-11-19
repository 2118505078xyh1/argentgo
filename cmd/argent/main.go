package main

import (
	"os"

	"github.com/2118505078xyh1/argent-go/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], cli.OSIO()))
}
