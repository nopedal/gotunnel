package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/nopedal/gotunnel/internal/server"
)

var (
	buildVersion = "dev"
	buildCommit  = "none"
	buildDate    = "unknown"
)

func main() {
	var configPath string

	root := &cobra.Command{
		Use:   "tunnel-server",
		Short: "Self-hosted tunnel server",
		Long:  "A lightweight reverse tunnel server. Expose localhost to the internet with zero account friction.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServer(configPath)
		},
	}

	root.Flags().StringVarP(&configPath, "config", "c", "/etc/tunnel/server.yaml", "path to config file")

	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("tunnel-server %s (%s) built %s\n", buildVersion, buildCommit, buildDate)
		},
	}

	root.AddCommand(versionCmd)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func runServer(configPath string) error {
	log := zerolog.New(zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: time.RFC3339,
	}).With().Timestamp().Str("component", "server").Logger()

	cfg, err := server.LoadConfig(configPath)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Info().Str("signal", sig.String()).Msg("shutting down")
		cancel()
	}()

	srv := server.New(cfg, log)
	return srv.Run(ctx)
}
