// chaos-proxy sits between the tool server and the real Linear API.
// It forwards every request unchanged except for deliberate, deterministic
// fault injection controlled per-request by two headers the benchmark
// runner sets: X-Chaos-Scenario (which fault profile to apply) and
// X-Chaos-Seed (a run identifier used to make the fault sequence
// reproducible across repeated runs, so naive and hardened face the
// *exact same* sequence of failures for a fair comparison).
//
// This proxy has no opinion about tool design -- it only knows about
// GraphQL requests/responses at the HTTP layer. That separation matters:
// it lets the harness vary tool design and failure conditions independently.
package main

import (
	"bytes"
	"hash/fnv"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

var logger = slog.New(slog.NewTextHandler(os.Stdout, nil))

// forwardClient performs the direct upstream call used by the silent-timeout
// fault (where we must guarantee the write commits before dropping the client).
var forwardClient = &http.Client{Timeout: 30 * time.Second}

type scenario struct {
	description string
	// decide is called once per incoming request. It returns a fault to
	// apply (or nil to forward cleanly). callIndex is a deterministic,
	// per-seed counter so repeated runs with the same seed see the same
	// sequence of faults at the same points.
	decide func(callIndex int, body []byte) fault
}

type faultKind int

const (
	faultNone faultKind = iota
	faultRateLimit
	faultServerError
	faultSilentTimeout // forward the real request, but never respond to the client
	faultDelay
)

type fault struct {
	kind       faultKind
	retryAfter string
	delay      time.Duration
}

// deterministic pseudo-random in [0,1) from (seed, callIndex) -- same
// inputs always produce the same output, independent of process restarts.
func pseudoRand(seed string, callIndex int) float64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(seed + "#" + strconv.Itoa(callIndex)))
	return float64(h.Sum64()%1_000_000) / 1_000_000.0
}

var scenarios = map[string]scenario{
	"clean": {
		description: "No faults. Baseline.",
		decide: func(callIndex int, body []byte) fault {
			return fault{kind: faultNone}
		},
	},
	"rate_limit_429": {
		description: "30% of calls get a 429 with Retry-After: 2 instead of being forwarded.",
		decide: func(callIndex int, body []byte) fault {
			if pseudoRand("rate_limit_429", callIndex) < 0.30 {
				return fault{kind: faultRateLimit, retryAfter: "2"}
			}
			return fault{kind: faultNone}
		},
	},
	"flaky_500": {
		description: "25% of calls get a 500 instead of being forwarded.",
		decide: func(callIndex int, body []byte) fault {
			if pseudoRand("flaky_500", callIndex) < 0.25 {
				return fault{kind: faultServerError}
			}
			return fault{kind: faultNone}
		},
	},
	"silent_timeout_after_write": {
		description: "For issueCreate mutations only: the write actually succeeds against the real API, " +
			"but 40% of the time the client never receives a response (simulating a dropped connection " +
			"*after* the server-side effect already happened). This is the scenario idempotency keys exist for.",
		decide: func(callIndex int, body []byte) fault {
			if strings.Contains(string(body), "issueCreate") && pseudoRand("silent_timeout_after_write", callIndex) < 0.40 {
				return fault{kind: faultSilentTimeout}
			}
			return fault{kind: faultNone}
		},
	},
}

// perSeedCounters tracks the next call index for each seed, so the Nth
// call within a given run deterministically maps to the Nth decision --
// regardless of which tool variant is making it.
var perSeedCounters = map[string]int{}

func nextCallIndex(seed string) int {
	i := perSeedCounters[seed]
	perSeedCounters[seed] = i + 1
	return i
}

func main() {
	targetURL := os.Getenv("LINEAR_UPSTREAM_URL")
	if targetURL == "" {
		targetURL = "https://api.linear.app/graphql"
	}
	target, err := url.Parse(targetURL)
	if err != nil {
		logger.Error("invalid LINEAR_UPSTREAM_URL", "err", err)
		os.Exit(1)
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	origDirector := proxy.Director
	proxy.Director = func(r *http.Request) {
		origDirector(r)
		r.Host = target.Host
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		scenarioID := r.Header.Get("X-Chaos-Scenario")
		seed := r.Header.Get("X-Chaos-Seed")
		if scenarioID == "" {
			scenarioID = "clean"
		}
		if seed == "" {
			seed = "default"
		}
		sc, ok := scenarios[scenarioID]
		if !ok {
			http.Error(w, `{"errors":[{"message":"unknown chaos scenario: `+scenarioID+`"}]}`, http.StatusBadRequest)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, `{"errors":[{"message":"failed to read body"}]}`, http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))

		callIndex := nextCallIndex(seed)
		f := sc.decide(callIndex, body)

		logger.Info("chaos decision", "scenario", scenarioID, "seed", seed, "call_index", callIndex, "fault", f.kind)

		switch f.kind {
		case faultRateLimit:
			w.Header().Set("Retry-After", f.retryAfter)
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"errors":[{"message":"rate limited"}]}`))
			return
		case faultServerError:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"errors":[{"message":"internal server error"}]}`))
			return
		case faultSilentTimeout:
			// Genuinely perform the write against the real API and wait for it
			// to commit, THEN drop the client's connection. This models the
			// nastiest real-world fault: the server-side effect happened, but
			// the response was lost in transit, so the caller cannot know
			// whether it succeeded. This is exactly the case idempotency keys
			// exist for.
			endpoint := target.Scheme + "://" + target.Host + r.URL.Path
			upReq, _ := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
			upReq.Header = r.Header.Clone()
			if up, uerr := forwardClient.Do(upReq); uerr == nil {
				_, _ = io.Copy(io.Discard, up.Body)
				up.Body.Close()
				logger.Info("silent_timeout_after_write: real write committed, dropping response",
					"upstream_status", up.StatusCode)
			} else {
				logger.Error("silent_timeout upstream call failed", "err", uerr)
			}
			// Drop the client connection so the caller sees a transport error.
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, herr := hj.Hijack(); herr == nil {
					_ = conn.Close()
					return
				}
			}
			time.Sleep(35 * time.Second)
			return
		default:
			proxy.ServeHTTP(w, r)
		}
	})

	addr := os.Getenv("CHAOS_PROXY_ADDR")
	if addr == "" {
		addr = ":8082"
	}
	logger.Info("chaos proxy listening", "addr", addr, "upstream", targetURL)
	if err := http.ListenAndServe(addr, mux); err != nil {
		logger.Error("server exited", "err", err)
		os.Exit(1)
	}
}
