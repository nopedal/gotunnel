package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/nopedal/gotunnel/internal/client"
)

var (
	buildVersion = "dev"
	buildCommit  = "none"
	buildDate    = "unknown"
)

func main() {
	cfg := client.LoadClientConfig()

	root := &cobra.Command{
		Use:   "tunnel",
		Short: "Expose localhost to the internet",
		Long:  "A self-hosted tunnel client. Connect to your tunnel server and share local services with the world.",
	}

	var serverFlag, tokenFlag, subdomainFlag, modeFlag string
	var portFlag int

	connectCmd := &cobra.Command{
		Use:   "connect",
		Short: "Establish a tunnel to the server",
		Example: `  tunnel connect --server tunnel.example.com --port 3000
  tunnel connect --server tunnel.example.com --port 8080 --subdomain myapp
  tunnel connect --server tunnel.example.com --port 5432 --mode tcp`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// CLI flags override config file
			if serverFlag != "" {
				cfg.Server = serverFlag
			}
			if tokenFlag != "" {
				cfg.Token = tokenFlag
			}
			if cmd.Flags().Changed("port") {
				cfg.Port = portFlag
			}
			if subdomainFlag != "" {
				cfg.Subdomain = subdomainFlag
			}
			if modeFlag != "" {
				cfg.Mode = modeFlag
			}

			// Env var fallback for token
			if cfg.Token == "" {
				cfg.Token = os.Getenv("TUNNEL_TOKEN")
			}

			if cfg.Server == "" {
				return fmt.Errorf("server address required (use --server or set in ~/.tunnel/config.yaml)")
			}
			if cfg.Token == "" {
				return fmt.Errorf("token required (use --token, TUNNEL_TOKEN env var, or set in ~/.tunnel/config.yaml)")
			}
			if cfg.Port == 0 {
				return fmt.Errorf("local port required (use --port)")
			}

			return runConnect(cfg)
		},
	}

	connectCmd.Flags().StringVar(&serverFlag, "server", "", "tunnel server address (e.g. tunnel.example.com)")
	connectCmd.Flags().StringVar(&tokenFlag, "token", "", "authentication token")
	connectCmd.Flags().IntVar(&portFlag, "port", 0, "local port to expose")
	connectCmd.Flags().StringVar(&subdomainFlag, "subdomain", "", "request a specific subdomain")
	connectCmd.Flags().StringVar(&modeFlag, "mode", "http", "tunnel mode: http or tcp")

	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("tunnel %s (%s) built %s\n", buildVersion, buildCommit, buildDate)
		},
	}

	root.AddCommand(connectCmd, versionCmd)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func runConnect(cfg *client.Config) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintf(os.Stderr, "\n  shutting down...\n")
		cancel()
	}()

	c := client.New(cfg)
	return c.Connect(ctx)
}
