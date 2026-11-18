package main

import "testing"

// The whole point of seeded fault injection is reproducibility: the naive and
// hardened variants must face the SAME sequence of faults for a given seed, or
// the comparison isn't fair. These tests pin that determinism down.

func TestPseudoRandIsDeterministic(t *testing.T) {
	a := pseudoRand("seed-x", 3)
	b := pseudoRand("seed-x", 3)
	if a != b {
		t.Fatalf("same (seed, index) must give same value: %v vs %v", a, b)
	}
}

func TestPseudoRandInUnitRange(t *testing.T) {
	for i := 0; i < 1000; i++ {
		v := pseudoRand("seed", i)
		if v < 0 || v >= 1 {
			t.Fatalf("value out of [0,1): %v at index %d", v, i)
		}
	}
}

func TestCleanScenarioNeverInjectsFaults(t *testing.T) {
	decide := scenarios["clean"].decide
	for i := 0; i < 100; i++ {
		if f := decide(i, []byte(`{"query":"issueCreate"}`)); f.kind != faultNone {
			t.Fatalf("clean scenario injected a fault at index %d", i)
		}
	}
}

func TestScenarioDecisionIsReproducible(t *testing.T) {
	decide := scenarios["rate_limit_429"].decide
	body := []byte(`{"query":"issues"}`)
	for i := 0; i < 100; i++ {
		if decide(i, body).kind != decide(i, body).kind {
			t.Fatalf("decision at index %d was not reproducible", i)
		}
	}
}

func TestSilentTimeoutOnlyTargetsWrites(t *testing.T) {
	decide := scenarios["silent_timeout_after_write"].decide
	// A read query must never be hit by the write-only silent-timeout fault.
	for i := 0; i < 200; i++ {
		if f := decide(i, []byte(`{"query":"issues(filter...)"}`)); f.kind != faultNone {
			t.Fatalf("silent-timeout wrongly fired on a read at index %d", i)
		}
	}
}
