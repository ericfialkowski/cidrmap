// Package cidrmap provides a generic map keyed by non-overlapping CIDR
// prefixes, supporting fast lookup of the prefix that contains a given
// IPv4 or IPv6 address.
//
// Each address family is stored as a sorted array of range start addresses
// (as 128-bit integers) alongside a parallel array of entries. Lookup is a
// binary search over the start addresses: O(log n), no allocations.
//
// A Map is safe for concurrent readers, but writes (Insert, Delete) must
// not run concurrently with any other method. To change the data while
// readers are running, use AtomicMap, which swaps in whole new Maps.
package cidrmap

import (
	"encoding/binary"
	"errors"
	"fmt"
	"iter"
	"math"
	"net/netip"
	"slices"
)

var (
	// ErrInvalidPrefix is returned when inserting an invalid prefix.
	ErrInvalidPrefix = errors.New("cidrmap: invalid prefix")
	// ErrOverlap is returned when a prefix overlaps one already in the map.
	ErrOverlap = errors.New("cidrmap: prefix overlaps existing prefix")
	// ErrDuplicate is returned when the same prefix is added twice. It wraps
	// ErrOverlap, so errors.Is(err, ErrOverlap) is also true.
	ErrDuplicate = fmt.Errorf("%w: duplicate prefix", ErrOverlap)
)

// Map stores values of type V keyed by non-overlapping CIDR prefixes.
// The zero value is an empty map ready to use.
type Map[V any] struct {
	v4 table[V]
	v6 table[V]
}

// table holds the prefixes of one address family. first[i] is the first
// address of entries[i]; it is kept in its own slice so the binary search
// touches as little memory as possible.
type table[V any] struct {
	first   []u128
	entries []entry[V]
}

type entry[V any] struct {
	prefix netip.Prefix // normalized (masked, unzoned, unmapped)
	last   u128         // last address covered by prefix
	value  V
}

// Insert adds prefix p with value v. It returns ErrDuplicate if p is
// already in the map, or ErrOverlap if p overlaps another prefix.
// Insert is O(n); prefer Builder or Build for bulk loading.
func (m *Map[V]) Insert(p netip.Prefix, v V) error {
	p, ok := normalize(p)
	if !ok {
		return fmt.Errorf("%w: %s", ErrInvalidPrefix, p)
	}
	t := m.table(p.Addr())
	first, last := key(p.Addr()), lastKey(p)
	i := upperBound(t.first, first)
	if i > 0 && t.entries[i-1].prefix == p {
		return fmt.Errorf("%w: %s", ErrDuplicate, p)
	}
	if i > 0 && !t.entries[i-1].last.less(first) {
		return fmt.Errorf("%w: %s and %s", ErrOverlap, p, t.entries[i-1].prefix)
	}
	if i < len(t.first) && !last.less(t.first[i]) {
		return fmt.Errorf("%w: %s and %s", ErrOverlap, p, t.entries[i].prefix)
	}
	t.first = slices.Insert(t.first, i, first)
	t.entries = slices.Insert(t.entries, i, entry[V]{prefix: p, last: last, value: v})
	return nil
}

// Lookup returns the value and prefix of the entry containing ip.
// IPv4-mapped IPv6 addresses (::ffff:a.b.c.d) are treated as IPv4,
// and IPv6 zones are ignored.
func (m *Map[V]) Lookup(ip netip.Addr) (V, netip.Prefix, bool) {
	ip = ip.Unmap()
	t := m.table(ip)
	k := key(ip)
	if i := upperBound(t.first, k); i > 0 && !t.entries[i-1].last.less(k) {
		e := &t.entries[i-1]
		return e.value, e.prefix, true
	}
	var zero V
	return zero, netip.Prefix{}, false
}

// Get returns the value stored for exactly prefix p.
func (m *Map[V]) Get(p netip.Prefix) (V, bool) {
	if t, i, ok := m.find(p); ok {
		return t.entries[i].value, true
	}
	var zero V
	return zero, false
}

// Delete removes exactly prefix p, reporting whether it was present.
func (m *Map[V]) Delete(p netip.Prefix) bool {
	t, i, ok := m.find(p)
	if ok {
		t.first = slices.Delete(t.first, i, i+1)
		t.entries = slices.Delete(t.entries, i, i+1)
	}
	return ok
}

// Len returns the number of prefixes in the map.
func (m *Map[V]) Len() int {
	return len(m.v4.entries) + len(m.v6.entries)
}

// All iterates over all prefixes and values in address order,
// IPv4 first, then IPv6.
func (m *Map[V]) All() iter.Seq2[netip.Prefix, V] {
	return func(yield func(netip.Prefix, V) bool) {
		for _, t := range []*table[V]{&m.v4, &m.v6} {
			for _, e := range t.entries {
				if !yield(e.prefix, e.value) {
					return
				}
			}
		}
	}
}

func (m *Map[V]) table(a netip.Addr) *table[V] {
	if a.Is4() {
		return &m.v4
	}
	return &m.v6
}

func (m *Map[V]) find(p netip.Prefix) (*table[V], int, bool) {
	p, ok := normalize(p)
	if !ok {
		return nil, 0, false
	}
	t := m.table(p.Addr())
	i := upperBound(t.first, key(p.Addr())) - 1
	return t, i, i >= 0 && t.entries[i].prefix == p
}

// upperBound returns the index of the first element of s greater than k,
// so the only range that can contain k starts at index-1.
func upperBound(s []u128, k u128) int {
	lo, hi := 0, len(s)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if k.less(s[mid]) {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return lo
}

// normalize masks p, strips any zone, and converts IPv4-mapped IPv6
// prefixes (::ffff:0:0/96 and longer) to plain IPv4 prefixes.
func normalize(p netip.Prefix) (netip.Prefix, bool) {
	if !p.IsValid() {
		return p, false
	}
	a, bits := p.Addr().WithZone(""), p.Bits()
	if a.Is4In6() && bits >= 96 {
		a, bits = a.Unmap(), bits-96
	}
	return netip.PrefixFrom(a, bits).Masked(), true
}

// u128 is an address as a 128-bit big-endian integer. IPv4 addresses use
// their IPv4-mapped IPv6 form, which preserves ordering within the family.
type u128 struct{ hi, lo uint64 }

func key(a netip.Addr) u128 {
	b := a.As16()
	return u128{binary.BigEndian.Uint64(b[:8]), binary.BigEndian.Uint64(b[8:])}
}

func (a u128) less(b u128) bool {
	return a.hi < b.hi || (a.hi == b.hi && a.lo < b.lo)
}

// lastKey returns the highest address in masked prefix p.
func lastKey(p netip.Prefix) u128 {
	k, bits := key(p.Addr()), p.Bits()
	if p.Addr().Is4() {
		bits += 96
	}
	// Shifts of 64 or more yield 0 in Go, which handles /0, /64 and /128.
	if bits < 64 {
		k.hi |= math.MaxUint64 >> bits
		k.lo = math.MaxUint64
	} else {
		k.lo |= math.MaxUint64 >> (bits - 64)
	}
	return k
}
