package webhook

import "testing"

func TestKeyedRateLimiter_PerKeyIsolation(t *testing.T) {
	// burst of 2, no meaningful refill within the test.
	k := newKeyedRateLimiter(0.0001, 2)

	// "a" exhausts its own burst (two allowed, third denied).
	if !k.Allow("a") {
		t.Fatal("key a request 1 should be allowed")
	}
	if !k.Allow("a") {
		t.Fatal("key a request 2 should be allowed")
	}
	if k.Allow("a") {
		t.Fatal("key a request 3 should be rate limited")
	}

	// "b" has its own independent bucket and is unaffected by "a".
	if !k.Allow("b") {
		t.Fatal("key b request 1 should be allowed")
	}
	if !k.Allow("b") {
		t.Fatal("key b request 2 should be allowed")
	}
	if k.Allow("b") {
		t.Fatal("key b request 3 should be rate limited")
	}
}
