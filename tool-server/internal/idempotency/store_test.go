package idempotency

import "testing"

func TestEmptyKeyIsNeverIdempotent(t *testing.T) {
	s := NewStore()
	// An empty key carries no idempotency guarantee: it must always report
	// "not seen" so the caller performs the operation rather than silently
	// deduping unrelated calls.
	if _, seen := s.CheckAndReserve(""); seen {
		t.Fatal("empty key should never be reported as seen")
	}
}

func TestRecordThenCheckReturnsStoredID(t *testing.T) {
	s := NewStore()
	if _, seen := s.CheckAndReserve("k1"); seen {
		t.Fatal("key k1 should be unseen before it is recorded")
	}
	s.Record("k1", "issue-123")

	id, seen := s.CheckAndReserve("k1")
	if !seen {
		t.Fatal("key k1 should be seen after Record")
	}
	if id != "issue-123" {
		t.Fatalf("want issue-123, got %q", id)
	}
}

func TestDistinctKeysDoNotCollide(t *testing.T) {
	s := NewStore()
	s.Record("a", "1")
	s.Record("b", "2")
	if id, _ := s.CheckAndReserve("a"); id != "1" {
		t.Fatalf("key a: want 1, got %q", id)
	}
	if id, _ := s.CheckAndReserve("b"); id != "2" {
		t.Fatalf("key b: want 2, got %q", id)
	}
}
