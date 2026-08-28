package main

import (
	"context"
	"os"

	"agenthub.local/agenthub/internal/cli"
)

func main() {
	os.Exit(cli.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}
