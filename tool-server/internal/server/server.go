// Package server exposes both tool variants over HTTP so the benchmark
// runner (a separate process/module) can call them exactly as an agent
// framework would call any tool server: list available tools, then call
// one by name with JSON input.
package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"integration-harness/tool-server/internal/linear"
	"integration-harness/tool-server/internal/tools"
)

var writeTools = map[string]bool{
	"assign_issue": true,
	"add_comment":  true,
	"create_issue": true,
}

type Server struct {
	variants map[string]*tools.Variant
	logger   *slog.Logger
}

func New(logger *slog.Logger, variants ...*tools.Variant) *Server {
	m := make(map[string]*tools.Variant, len(variants))
	for _, v := range variants {
		m[v.ID] = v
	}
	return &Server{variants: m, logger: logger}
}

func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/{variant}/tools", s.handleListTools)
	mux.HandleFunc("POST /v1/{variant}/tools/{name}/call", s.handleCallTool)
}

func (s *Server) variant(w http.ResponseWriter, r *http.Request) *tools.Variant {
	id := r.PathValue("variant")
	v, ok := s.variants[id]
	if !ok {
		http.Error(w, `{"error":"unknown variant: `+id+`"}`, http.StatusNotFound)
		return nil
	}
	return v
}

func (s *Server) handleListTools(w http.ResponseWriter, r *http.Request) {
	v := s.variant(w, r)
	if v == nil {
		return
	}
	type toolListing struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"input_schema"`
	}
	listing := make([]toolListing, 0, len(v.Tools))
	for _, t := range v.Tools {
		listing = append(listing, toolListing{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	writeJSON(w, http.StatusOK, listing)
}

// handleCallTool enforces one more property the hardened variant has and
// the naive one doesn't: write operations require an explicit write-scoped
// credential (X-Scope header), modeling least-privilege access rather than
// one token that can do anything. For the naive variant this check is
// skipped entirely -- a single token, read or write, does whatever it wants,
// which is how most first-pass integrations are actually built.
func (s *Server) handleCallTool(w http.ResponseWriter, r *http.Request) {
	v := s.variant(w, r)
	if v == nil {
		return
	}
	name := r.PathValue("name")
	t := v.ToolByName(name)
	if t == nil {
		http.Error(w, `{"error":"unknown tool: `+name+`"}`, http.StatusNotFound)
		return
	}

	if v.ID == "hardened" && writeTools[name] {
		scope := r.Header.Get("X-Scope")
		if !strings.Contains(scope, "write") {
			writeJSON(w, http.StatusForbidden, tools.ToolResult{
				OK: false,
				Error: &tools.ToolError{
					Code:      "permission_denied",
					Message:   "this tool requires a write-scoped credential (X-Scope: write)",
					Retryable: false,
				},
			})
			return
		}
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":"failed to read body"}`, http.StatusBadRequest)
		return
	}

	// Carry the chaos scenario/seed from the agent's call down into the
	// Linear requests this tool will make, so the chaos proxy applies the
	// right fault profile for this run.
	ctx := linear.WithChaos(r.Context(), r.Header.Get("X-Chaos-Scenario"), r.Header.Get("X-Chaos-Seed"))
	result, err := t.Handler(ctx, body)
	if err != nil {
		// Handlers are written to never return a bare Go error for expected
		// failure modes -- this branch is only hit for genuine bugs.
		s.logger.Error("tool handler returned unexpected error", "tool", name, "variant", v.ID, "err", err)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
