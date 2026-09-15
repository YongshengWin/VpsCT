package auth

import "testing"

func TestNewSubscriptionTokenLength(t *testing.T) {
	tok := NewSubscriptionToken()
	if len(tok) != 256 {
		t.Fatalf("token length %d want 256", len(tok))
	}
	other := NewSubscriptionToken()
	if tok == other {
		t.Fatal("tokens should be unique")
	}
	if TokenHint(tok) != tok[:6] {
		t.Fatalf("hint %q", TokenHint(tok))
	}
}
