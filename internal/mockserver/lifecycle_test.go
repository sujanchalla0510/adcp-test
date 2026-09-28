package mockserver

import "testing"

func TestLifecycleLegalTransitions(t *testing.T) {
	l := NewLifecycle()
	// New entity starts in draft.
	if st, err := l.Apply("e1", ""); err != nil || st != "draft" {
		t.Fatalf("create: st=%q err=%v", st, err)
	}
	// draft -> active -> paused -> active -> completed.
	for _, target := range []string{"active", "paused", "active", "completed"} {
		if st, err := l.Apply("e1", target); err != nil || st != target {
			t.Fatalf("-> %s: st=%q err=%v", target, st, err)
		}
	}
	// Terminal: completed allows nothing further.
	if _, err := l.Apply("e1", "active"); err == nil {
		t.Fatalf("completed -> active should fail")
	}
	// Cancel path.
	if st, err := l.Apply("e2", "cancelled"); err != nil || st != "cancelled" {
		t.Fatalf("draft -> cancelled: st=%q err=%v", st, err)
	}
}

func TestLifecycleIllegal(t *testing.T) {
	l := NewLifecycle()
	if _, err := l.Apply("e1", ""); err != nil {
		t.Fatal(err)
	}
	// draft -> paused is illegal.
	_, err := l.Apply("e1", "paused")
	terr, ok := err.(*TransitionError)
	if !ok {
		t.Fatalf("err type = %T, want *TransitionError", err)
	}
	if terr.From != "draft" || terr.To != "paused" {
		t.Fatalf("terr = %+v", terr)
	}
	if len(terr.Allowed) != 2 || terr.Allowed[0] != "active" {
		t.Fatalf("allowed = %v", terr.Allowed)
	}
	// State unchanged after the rejected move.
	if st := l.State("e1"); st != "draft" {
		t.Fatalf("state = %q, want draft", st)
	}
	// Unknown target state.
	if _, err := l.Apply("e1", "bogus"); err == nil {
		t.Fatalf("bogus target should fail")
	}
	// Missing entity id.
	if _, err := l.Apply("", "active"); err == nil {
		t.Fatalf("empty entity should fail")
	}
	// Entities are independent.
	if st, err := l.Apply("e9", "active"); err != nil || st != "active" {
		t.Fatalf("e9 draft -> active: st=%q err=%v", st, err)
	}
	if st := l.State("e1"); st != "draft" {
		t.Fatalf("e1 state = %q, want draft", st)
	}
}
