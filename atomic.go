package cidrmap

import (
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
)

// AtomicMap wraps a Map so it can be replaced while readers are using it.
// Readers never take a lock: each read loads the current *Map through an
// atomic.Pointer. Writers build a new Map (with Build, or with Update,
// which copies the current one) and swap it in.
//
// A Map passed to Store, or modified inside Update, must not be changed
// after it is published; readers may still be using it.
//
// The zero value is an empty AtomicMap ready to use.
type AtomicMap[V any] struct {
	p  atomic.Pointer[Map[V]]
	mu sync.Mutex // serializes writers so Update never loses a Store
}

// NewAtomic returns an AtomicMap holding m. A nil m means an empty map.
func NewAtomic[V any](m *Map[V]) *AtomicMap[V] {
	a := &AtomicMap[V]{}
	a.p.Store(m)
	return a
}

// Load returns the current Map snapshot. Use it when several reads must
// see the same version. The returned Map must not be modified.
func (a *AtomicMap[V]) Load() *Map[V] {
	if m := a.p.Load(); m != nil {
		return m
	}
	return &Map[V]{}
}

// Store replaces the current Map with m. A nil m means an empty map.
func (a *AtomicMap[V]) Store(m *Map[V]) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.p.Store(m)
}

// Update copies the current Map, calls fn on the copy, and publishes it
// if fn returns nil. If fn returns an error the current Map is unchanged.
// Each Update copies the whole Map, so batch changes into one call, or
// use Build and Store for a full reload.
func (a *AtomicMap[V]) Update(fn func(m *Map[V]) error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	m := a.Load().Clone()
	if err := fn(m); err != nil {
		return err
	}
	a.p.Store(m)
	return nil
}

// Lookup calls Lookup on the current Map.
func (a *AtomicMap[V]) Lookup(ip netip.Addr) (V, netip.Prefix, bool) {
	if m := a.p.Load(); m != nil {
		return m.Lookup(ip)
	}
	var zero V
	return zero, netip.Prefix{}, false
}

// Clone returns a copy of m that can be modified independently.
// Values are copied with ordinary assignment, so a V holding pointers,
// slices or maps still shares what they point to.
func (m *Map[V]) Clone() *Map[V] {
	return &Map[V]{v4: m.v4.clone(), v6: m.v6.clone()}
}

func (t table[V]) clone() table[V] {
	return table[V]{first: slices.Clone(t.first), entries: slices.Clone(t.entries)}
}
