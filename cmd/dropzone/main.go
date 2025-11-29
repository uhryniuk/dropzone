package main

import (
	"fmt"
	"os"

	"github.com/uhryniuk/dropzone/internal/app"
)

func main() {
	application := app.New()
	rootCmd := application.SetupCommands()

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
