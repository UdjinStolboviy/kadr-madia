package pipeline

import "sync"

// notifier is an in-process broadcast keyed by asset id. complete?wait and
// POST /v1/assets?wait subscribe before reading the asset state, and the
// processing job publishes after committing, so no update is missed and no
// polling is needed.
type notifier struct {
	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{}
}

func newNotifier() *notifier {
	return &notifier{subs: map[string]map[chan struct{}]struct{}{}}
}

// subscribe returns a channel that is closed on the next publish for key,
// and a function to unsubscribe.
func (n *notifier) subscribe(key string) (<-chan struct{}, func()) {
	ch := make(chan struct{})
	n.mu.Lock()
	m, ok := n.subs[key]
	if !ok {
		m = map[chan struct{}]struct{}{}
		n.subs[key] = m
	}
	m[ch] = struct{}{}
	n.mu.Unlock()
	return ch, func() {
		n.mu.Lock()
		defer n.mu.Unlock()
		if m, ok := n.subs[key]; ok {
			if _, still := m[ch]; still {
				delete(m, ch)
				if len(m) == 0 {
					delete(n.subs, key)
				}
			}
		}
	}
}

// publish wakes every current subscriber of key.
func (n *notifier) publish(key string) {
	n.mu.Lock()
	m := n.subs[key]
	delete(n.subs, key)
	n.mu.Unlock()
	for ch := range m {
		close(ch)
	}
}
