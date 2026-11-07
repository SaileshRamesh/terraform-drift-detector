package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/saileshramesh/terraform-drift-detector/internal/runner"
	"github.com/saileshramesh/terraform-drift-detector/internal/scheduler"
	"github.com/saileshramesh/terraform-drift-detector/internal/server"
)

const defaultAddr = "127.0.0.1:8080"

func newServeCmd(g *globalFlags) *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the REST API, web dashboard and scheduled scans",
		Example: `  tfdrift serve
  tfdrift serve --config drift.yaml --addr 0.0.0.0:8080`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runServe(cmd.Context(), g, addr)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "", "listen address (default api.addr from config, else "+defaultAddr+")")
	return cmd
}

func runServe(ctx context.Context, g *globalFlags, addr string) error {
	cfg, err := g.loadConfig()
	if err != nil {
		return err
	}
	st, err := g.openStore(ctx, cfg, defaultDatabase)
	if err != nil {
		return err
	}
	defer st.Close()
	if n, err := st.FailInterrupted(ctx); err != nil {
		return err
	} else if n > 0 {
		log.Printf("marked %d interrupted scan(s) as failed", n)
	}

	// Cancelling ctx (SIGINT/SIGTERM) aborts in-flight scans; they are
	// recorded as failed.
	rn := runner.New(ctx, st, cfg.Concurrency)
	sched := scheduler.New(st, rn)
	if err := sched.Start(ctx); err != nil {
		return err
	}

	if addr == "" {
		addr = cfg.API.Addr
	}
	if addr == "" {
		addr = defaultAddr
	}
	if cfg.API.APIKey == "" && !isLoopback(addr) {
		log.Printf("WARNING: listening on %s without api.api_key; anyone who can reach it can read reports and change workspaces", addr)
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           server.New(st, rn, sched, cfg.API.APIKey).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	log.Printf("tfdrift dashboard on http://%s", addr)
	err = srv.ListenAndServe()
	sched.Stop()
	rn.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
