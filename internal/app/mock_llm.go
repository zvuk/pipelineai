package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/zvuk/pipelineai/internal/mockllm"
)

func newMockLLMCommand(log *slog.Logger) *cobra.Command {
	var (
		addr     string
		urlFile  string
		printURL bool
	)

	cmd := &cobra.Command{
		Use:   "mock-llm",
		Short: "Starts a local mock OpenAI-compatible Chat Completions server (for smoke/CI)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if log == nil {
				log = slog.Default()
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			srv, err := mockllm.Start(addr, log)
			if err != nil {
				return err
			}

			baseURL := srv.BaseURL()

			if strings.TrimSpace(urlFile) != "" {
				if err := os.MkdirAll(filepath.Dir(urlFile), 0o755); err != nil {
					return fmt.Errorf("failed to create dir for url file: %w", err)
				}
				if err := os.WriteFile(urlFile, []byte(baseURL+"\n"), 0o644); err != nil {
					return fmt.Errorf("failed to write url file: %w", err)
				}
			}

			if printURL {
				fmt.Fprintln(cmd.OutOrStdout(), baseURL)
			}

			<-ctx.Done()

			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
			return nil
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:0", "listen address")
	cmd.Flags().StringVar(&urlFile, "write-url-file", "", "write base URL to a file when ready")
	cmd.Flags().BoolVar(&printURL, "print-url", false, "print base URL to stdout")

	return cmd
}
