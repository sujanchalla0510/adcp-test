package mockserver

import (
	"fmt"
	"strings"
	"sync"
)

// Media-buy lifecycle state machine (M4 built-in).
//
//	draft -> active -> paused -> active ...
//	                 \-> completed | cancelled
//	draft -> cancelled, paused -> cancelled
//
// Transitions outside this graph are rejected with a structured
// TransitionError, the way a real seller rejects illegal moves.
var mediaBuyTransitions = map[string][]string{
	"draft":     {"active", "cancelled"},
	"active":    {"paused", "completed", "cancelled"},
	"paused":    {"active", "cancelled"},
	"completed": {},
	"cancelled": {},
}

// Lifecycle tracks per-entity states for one mock route.
type Lifecycle struct {
	mu     sync.Mutex
	states map[string]string
}

// NewLifecycle builds an empty lifecycle tracker.
func NewLifecycle() *Lifecycle {
	return &Lifecycle{states: map[string]string{}}
}

// TransitionError is returned for illegal state moves.
type TransitionError struct {
	Entity  string
	From    string
	To      string
	Allowed []string
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("illegal transition: %s -> %s (allowed: %s)", e.From, e.To, strings.Join(e.Allowed, ", "))
}

// Apply requests a move to target for entity and returns the entity's
// (new) state. An empty target means "no status change requested".
// Unknown entities are created in "draft".
func (l *Lifecycle) Apply(entity, target string) (string, error) {
	if strings.TrimSpace(entity) == "" {
		return "", fmt.Errorf("media-buy-lifecycle: args must include a non-empty id (or media_buy_id)")
	}
	target = strings.ToLower(strings.TrimSpace(target))
	l.mu.Lock()
	defer l.mu.Unlock()
	cur, known := l.states[entity]
	if !known {
		cur = "draft"
		l.states[entity] = cur
	}
	if target == "" || target == cur {
		return cur, nil
	}
	allowed, ok := mediaBuyTransitions[cur]
	if !ok {
		return "", fmt.Errorf("media-buy-lifecycle: unknown current state %q", cur)
	}
	if _, ok := mediaBuyTransitions[target]; !ok {
		return "", &TransitionError{Entity: entity, From: cur, To: target, Allowed: allowed}
	}
	for _, a := range allowed {
		if a == target {
			l.states[entity] = target
			return target, nil
		}
	}
	return "", &TransitionError{Entity: entity, From: cur, To: target, Allowed: allowed}
}

// State returns the current state of entity, or "" if unknown.
func (l *Lifecycle) State(entity string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.states[entity]
}
