// Package app управляет запуском CLI агента PipelineAI и построением cobra-команд.
package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/zvuk/pipelineai/internal/logger"
	"github.com/zvuk/pipelineai/pkg/config"
)

// RunCLI инициализирует окружение, собирает команду и выполняет её.
func RunCLI(args []string, stdout, stderr io.Writer) int {
	if err := config.LoadEnvFileIfExists(config.DefaultEnvFile, false); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	log, err := logger.New()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	root := newRootCommand(log, stdout, stderr)
	root.SetArgs(args)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	return 0
}

func newRootCommand(log *slog.Logger, stdout, stderr io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:           "pipelineai",
		Short:         "CLI для управления агентом PipelineAI",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.SetOut(stdout)
	root.SetErr(stderr)

	root.AddCommand(newLLMSmokeCommand(log))
	root.AddCommand(newRunCommand(log))
	root.AddCommand(newMockLLMCommand(log))

	return root
}
