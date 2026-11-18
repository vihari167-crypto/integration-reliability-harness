package main

import (
	"log/slog"
	"net/http"
	"os"

	"integration-harness/tool-server/internal/idempotency"
	"integration-harness/tool-server/internal/linear"
	"integration-harness/tool-server/internal/server"
	"integration-harness/tool-server/internal/tools"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	apiKey := os.Getenv("LINEAR_API_KEY")
	if apiKey == "" {
		logger.Error("LINEAR_API_KEY is not set")
		os.Exit(1)
	}
	// LINEAR_BASE_URL lets the benchmark runner point this server at the
	// chaos proxy instead of the real api.linear.app, without any code
	// change here -- the tool server never needs to know chaos is happening.
	baseURL := os.Getenv("LINEAR_BASE_URL")

	client := linear.NewClient(apiKey, baseURL)
	idemStore := idempotency.NewStore()

	srv := server.New(
		logger,
		tools.NaiveVariant(client),
		tools.HardenedVariant(client, idemStore),
		tools.PaginationOnlyVariant(client),  // ablation: naive + pagination only
		tools.TypedErrorsOnlyVariant(client), // ablation: naive + typed errors only
	)
	mux := http.NewServeMux()
	srv.Routes(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	addr := os.Getenv("TOOLSERVER_ADDR")
	if addr == "" {
		addr = ":8081"
	}
	logger.Info("tool server listening", "addr", addr, "linear_base_url", baseURL)
	if err := http.ListenAndServe(addr, mux); err != nil {
		logger.Error("server exited", "err", err)
		os.Exit(1)
	}
}
