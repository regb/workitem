package main

import (
	"context"
	"os"

	"github.com/regb/workitem/internal/cli"
)

func main() {
	os.Exit(cli.Main(context.Background(), os.Args[1:]))
}
