package main

import (
	"context"
	"os"

	"github.com/regb/workitem/internal/wimain"
)

func main() {
	os.Exit(wimain.Run(context.Background(), os.Args[1:]))
}
