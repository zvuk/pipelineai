package main

import (
	"os"

	"github.com/zvuk/pipelineai/internal/app"
)

// main запускает CLI PipelineAI и возвращает код завершения.
func main() {
	os.Exit(app.RunCLI(os.Args[1:], os.Stdout, os.Stderr))
}
