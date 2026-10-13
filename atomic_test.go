package cidrmap

import (
	"errors"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
)

func TestAtomicZeroValue(t *testing.T) {
	var a AtomicMap[string]
	ip := netip.MustParseAddr("10.1.1.1")
	if _, _, ok := a.Lookup(ip); ok {
		t.Error("Lookup on empty AtomicMap found entry")
	}
	if a.Load() == nil || a.Load().Len() != 0 {
		t.Error("Load on empty AtomicMap should return an empty Map")
	}
	err := a.Update(func(m *Map[string]) error {
		return m.Insert(netip.MustParsePrefix("10.0.0.0/8"), "ten")
	})
	if err != nil {
		t.Fatal(err)
	}
	if v, _, ok := a.Lookup(ip); !ok || v != "ten" {
		t.Errorf("Lookup = %q, %v; want ten, true", v, ok)
	}
	a.Store(nil)
	if _, _, ok := a.Lookup(ip); ok {
		t.Error("Lookup after Store(nil) found entry")
	}
}

func TestAtomicUpdateError(t *testing.T) {
	var m Map[int]
	mustInsert(t, &m, "10.0.0.0/8", 1)
	a := NewAtomic(&m)
	before := a.Load()
	err := a.Update(func(m *Map[int]) error {
		m.Delete(netip.MustParsePrefix("10.0.0.0/8"))
		return m.Insert(netip.MustParsePrefix("192.168.0.0/16"), 2) // succeeds...
	})
	if err != nil {
		t.Fatal(err)
	}
	after := a.Load()
	err = a.Update(func(m *Map[int]) error {
		mustInsert(t, m, "172.16.0.0/12", 3)
		return m.Insert(netip.MustParsePrefix("192.168.1.0/24"), 4) // ...this overlaps
	})
	if !errors.Is(err, ErrOverlap) {
		t.Fatalf("Update = %v; want ErrOverlap", err)
	}
	if a.Load() != after {
		t.Error("failed Update replaced the Map")
	}
	if _, _, ok := a.Lookup(netip.MustParseAddr("172.16.0.1")); ok {
		t.Error("changes from failed Update are visible")
	}
	// The original snapshot is untouched by the successful Update.
	if v, _, ok := before.Lookup(netip.MustParseAddr("10.1.1.1")); !ok || v != 1 {
		t.Errorf("old snapshot Lookup = %d, %v; want 1, true", v, ok)
	}
}

func TestClone(t *testing.T) {
	var m Map[int]
	mustInsert(t, &m, "10.0.0.0/8", 1)
	mustInsert(t, &m, "2001:db8::/32", 2)
	c := m.Clone()
	c.Delete(netip.MustParsePrefix("10.0.0.0/8"))
	mustInsert(t, c, "2001:db9::/32", 3)
	if m.Len() != 2 || c.Len() != 2 {
		t.Fatalf("Len original = %d, clone = %d; want 2, 2", m.Len(), c.Len())
	}
	if _, _, ok := m.Lookup(netip.MustParseAddr("10.1.1.1")); !ok {
		t.Error("Delete on clone affected original")
	}
	if _, _, ok := m.Lookup(netip.MustParseAddr("2001:db9::1")); ok {
		t.Error("Insert on clone affected original")
	}
}

// TestAtomicConcurrentSwap swaps in new versions while readers run.
// Every prefix in version v has value v, so a reader using one snapshot
// must see the same value everywhere, and versions must never go backwards.
// Run with -race.
func TestAtomicConcurrentSwap(t *testing.T) {
	const versions = 200
	build := func(v int) *Map[int] {
		m, err := Build(func(yield func(netip.Prefix, int) bool) {
			for i := range 256 {
				p := netip.PrefixFrom(netip.AddrFrom4([4]byte{10, 0, byte(i), 0}), 24)
				if !yield(p, v) {
					return
				}
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	a := NewAtomic(build(0))

	var done atomic.Bool
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			last := 0
			for !done.Load() {
				snap := a.Load()
				lo, _, ok1 := snap.Lookup(netip.MustParseAddr("10.0.0.1"))
				hi, _, ok2 := snap.Lookup(netip.MustParseAddr("10.0.255.1"))
				if !ok1 || !ok2 || lo != hi {
					t.Errorf("inconsistent snapshot: %d, %v / %d, %v", lo, ok1, hi, ok2)
					return
				}
				if lo < last {
					t.Errorf("version went backwards: %d after %d", lo, last)
					return
				}
				last = lo
				if v, _, ok := a.Lookup(netip.MustParseAddr("10.0.128.1")); !ok || v < last {
					t.Errorf("a.Lookup = %d, %v; want >= %d", v, ok, last)
					return
				}
			}
		})
	}
	for v := 1; v <= versions; v++ {
		if v%2 == 0 {
			a.Store(build(v))
			continue
		}
		err := a.Update(func(m *Map[int]) error {
			for p := range m.Clone().All() {
				m.Delete(p)
				if err := m.Insert(p, v); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	done.Store(true)
	wg.Wait()
	if v, _, _ := a.Lookup(netip.MustParseAddr("10.0.0.1")); v != versions {
		t.Errorf("final version = %d; want %d", v, versions)
	}
}

// The AtomicMap benchmarks use the same data as the plain Map benchmarks
// (benchData for IPv4, benchData6 for IPv6), so the difference is the cost
// of the wrapper.

func BenchmarkAtomicLookup(b *testing.B)     { benchAtomicLookup(b, benchData) }
func BenchmarkAtomicLookupIPv6(b *testing.B) { benchAtomicLookup(b, benchData6) }

func BenchmarkAtomicLookupParallel(b *testing.B)     { benchAtomicLookupParallel(b, benchData) }
func BenchmarkAtomicLookupIPv6Parallel(b *testing.B) { benchAtomicLookupParallel(b, benchData6) }

// The DuringSwaps benchmarks run parallel lookups while another goroutine
// calls Store in a tight loop. That is far more swapping than a real
// reload would do, so they show the worst-case cost to readers.
func BenchmarkAtomicLookupDuringSwaps(b *testing.B)     { benchAtomicLookupDuringSwaps(b, benchData) }
func BenchmarkAtomicLookupIPv6DuringSwaps(b *testing.B) { benchAtomicLookupDuringSwaps(b, benchData6) }

func benchAtomicLookup(b *testing.B, data func(*testing.B) (*Map[int], []netip.Addr)) {
	m, ips := data(b)
	a := NewAtomic(m)
	for i := 0; b.Loop(); i++ {
		a.Lookup(ips[i&1023])
	}
}

func benchAtomicLookupParallel(b *testing.B, data func(*testing.B) (*Map[int], []netip.Addr)) {
	m, ips := data(b)
	a := NewAtomic(m)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for i := 0; pb.Next(); i++ {
			a.Lookup(ips[i&1023])
		}
	})
}

func benchAtomicLookupDuringSwaps(b *testing.B, data func(*testing.B) (*Map[int], []netip.Addr)) {
	m, ips := data(b)
	snaps := [2]*Map[int]{m, m.Clone()}
	a := NewAtomic(m)
	var stop atomic.Bool
	var swaps atomic.Int64
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 0; !stop.Load(); i++ {
			a.Store(snaps[i&1])
			swaps.Add(1)
		}
	})
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for i := 0; pb.Next(); i++ {
			a.Lookup(ips[i&1023])
		}
	})
	b.StopTimer()
	stop.Store(true)
	wg.Wait()
	b.ReportMetric(float64(swaps.Load())/b.Elapsed().Seconds(), "swaps/s")
}
