// Package idempotency provides a tiny in-memory store mapping a
// caller-supplied idempotency key to the result of the write it guarded.
// This is exactly the mechanism the *hardened* tool variant has and the
// *naive* variant lacks: when an agent retries a create_issue call after
// a timeout (not knowing whether the first attempt actually succeeded),
// the hardened variant returns the original result instead of creating
// a second issue. The naive variant has no such memory and will duplicate.
package idempotency

import "sync"

type Store struct {
	mu   sync.Mutex
	seen map[string]string // idempotency key -> created resource ID
}

func NewStore() *Store {
	return &Store{seen: make(map[string]string)}
}

// CheckAndReserve returns (existingID, true) if this key was already used
// (so the caller should short-circuit and return the prior result), or
// ("", false) if this is the first time the key is seen (so the caller
// should proceed and then call Record).
func (s *Store) CheckAndReserve(key string) (string, bool) {
	if key == "" {
		return "", false // no key supplied -> no idempotency guarantee possible
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.seen[key]; ok {
		return id, true
	}
	return "", false
}

func (s *Store) Record(key, resourceID string) {
	if key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen[key] = resourceID
}
