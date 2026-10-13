package cidrmap

import (
	"fmt"
	"iter"
	"net/netip"
	"slices"
)

// Builder collects prefixes in any order and builds a Map from them with a
// single sort, which is much faster than repeated Insert calls.
//
//	var b cidrmap.Builder[Record]
//	for _, r := range records {
//		b.Add(r.CIDR, r)
//	}
//	m, err := b.Build()
//
// The zero value is ready to use.
type Builder[V any] struct {
	// OnDuplicate is called when the same prefix is added more than once.
	// existing is the value added first and dup the one added after it;
	// the returned value is kept. Returning an error fails Build with that
	// error. If OnDuplicate is nil, duplicates fail Build with ErrDuplicate.
	//
	// Prefixes are compared after normalization, so 10.1.2.3/16 is a
	// duplicate of 10.1.0.0/16. Prefixes that overlap without being
	// identical always fail Build with ErrOverlap.
	OnDuplicate func(p netip.Prefix, existing, dup V) (V, error)

	v4, v6 []entry[V]
	n      int   // number of Add calls, for error messages
	err    error // first invalid prefix
}

// Add queues prefix p with value v. It does no checking beyond recording
// an invalid prefix (for example the zero Prefix from a missing JSON
// field), which Build then reports. Add is O(1).
func (b *Builder[V]) Add(p netip.Prefix, v V) {
	b.n++
	if b.err != nil {
		return
	}
	np, ok := normalize(p)
	if !ok {
		b.err = fmt.Errorf("%w: item %d: %s", ErrInvalidPrefix, b.n, p)
		return
	}
	e := entry[V]{prefix: np, last: lastKey(np), value: v}
	if np.Addr().Is4() {
		b.v4 = append(b.v4, e)
	} else {
		b.v6 = append(b.v6, e)
	}
}

// Build returns a Map of everything added. It fails if any prefix was
// invalid, if prefixes overlap, or if a duplicate is not resolved by
// OnDuplicate. Build resets the Builder (keeping OnDuplicate), so it can
// be reused for the next load.
func (b *Builder[V]) Build() (*Map[V], error) {
	v4, v6, err := b.v4, b.v6, b.err
	b.v4, b.v6, b.n, b.err = nil, nil, 0, nil
	if err != nil {
		return nil, err
	}
	m := &Map[V]{}
	if m.v4, err = b.buildTable(v4); err != nil {
		return nil, err
	}
	if m.v6, err = b.buildTable(v6); err != nil {
		return nil, err
	}
	return m, nil
}

func (b *Builder[V]) buildTable(entries []entry[V]) (table[V], error) {
	// Sorting by (first address, prefix length) puts identical prefixes
	// next to each other, and the stable sort keeps them in Add order.
	slices.SortStableFunc(entries, func(x, y entry[V]) int {
		if c := x.prefix.Addr().Compare(y.prefix.Addr()); c != 0 {
			return c
		}
		return x.prefix.Bits() - y.prefix.Bits()
	})
	out := entries[:0] // compact in place, merging duplicates
	for _, e := range entries {
		if len(out) == 0 {
			out = append(out, e)
			continue
		}
		prev := &out[len(out)-1]
		switch {
		case prev.prefix == e.prefix:
			if b.OnDuplicate == nil {
				return table[V]{}, fmt.Errorf("%w: %s", ErrDuplicate, e.prefix)
			}
			v, err := b.OnDuplicate(e.prefix, prev.value, e.value)
			if err != nil {
				return table[V]{}, fmt.Errorf("cidrmap: duplicate prefix %s: %w", e.prefix, err)
			}
			prev.value = v
		case !prev.last.less(key(e.prefix.Addr())):
			return table[V]{}, fmt.Errorf("%w: %s and %s", ErrOverlap, prev.prefix, e.prefix)
		default:
			out = append(out, e)
		}
	}
	// Clear the tail so dropped duplicates don't keep their values alive.
	clear(entries[len(out):])
	t := table[V]{entries: slices.Clip(out), first: make([]u128, len(out))}
	for i, e := range out {
		t.first[i] = key(e.prefix.Addr())
	}
	return t, nil
}

// Build creates a Map from a sequence of prefix/value pairs, failing on
// duplicates or overlaps. Use maps.All to build from a map[netip.Prefix]V,
// or a Builder to handle duplicates or to add from a loop.
func Build[V any](seq iter.Seq2[netip.Prefix, V]) (*Map[V], error) {
	var b Builder[V]
	for p, v := range seq {
		b.Add(p, v)
	}
	return b.Build()
}
