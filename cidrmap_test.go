package cidrmap

import (
	"errors"
	"maps"
	"math/rand/v2"
	"net/netip"
	"sync"
	"testing"
)

func mustInsert[V any](t *testing.T, m *Map[V], cidr string, v V) {
	t.Helper()
	if err := m.Insert(netip.MustParsePrefix(cidr), v); err != nil {
		t.Fatalf("Insert(%s): %v", cidr, err)
	}
}

func TestLookup(t *testing.T) {
	var m Map[string]
	mustInsert(t, &m, "10.0.0.0/8", "ten")
	mustInsert(t, &m, "192.168.1.0/24", "home")
	mustInsert(t, &m, "192.168.2.5/32", "host")
	mustInsert(t, &m, "2001:db8::/32", "doc")
	mustInsert(t, &m, "fe80::/10", "link-local")

	tests := []struct {
		ip     string
		want   string
		prefix string
		ok     bool
	}{
		{"10.0.0.0", "ten", "10.0.0.0/8", true},
		{"10.255.255.255", "ten", "10.0.0.0/8", true},
		{"11.0.0.0", "", "", false},
		{"9.255.255.255", "", "", false},
		{"192.168.1.77", "home", "192.168.1.0/24", true},
		{"192.168.2.5", "host", "192.168.2.5/32", true},
		{"192.168.2.6", "", "", false},
		{"::ffff:10.1.2.3", "ten", "10.0.0.0/8", true},
		{"2001:db8:1234::1", "doc", "2001:db8::/32", true},
		{"2001:db9::", "", "", false},
		{"fe80::1%eth0", "link-local", "fe80::/10", true},
		{"::1", "", "", false},
	}
	for _, tt := range tests {
		v, p, ok := m.Lookup(netip.MustParseAddr(tt.ip))
		if ok != tt.ok || v != tt.want || (ok && p.String() != tt.prefix) {
			t.Errorf("Lookup(%s) = %q, %v, %v; want %q, %s, %v", tt.ip, v, p, ok, tt.want, tt.prefix, tt.ok)
		}
	}
}

func TestInsertOverlap(t *testing.T) {
	var m Map[int]
	mustInsert(t, &m, "10.1.0.0/16", 1)
	mustInsert(t, &m, "10.3.0.0/16", 3)
	for _, cidr := range []string{"10.1.0.0/16", "10.0.0.0/8", "10.1.2.0/24", "10.3.255.255/32", "::ffff:10.1.0.0/112"} {
		if err := m.Insert(netip.MustParsePrefix(cidr), 0); !errors.Is(err, ErrOverlap) {
			t.Errorf("Insert(%s) = %v; want ErrOverlap", cidr, err)
		}
	}
	// Adjacent but non-overlapping prefixes are fine.
	mustInsert(t, &m, "10.2.0.0/16", 2)
	mustInsert(t, &m, "10.0.255.255/32", 0)
	if err := m.Insert(netip.Prefix{}, 0); !errors.Is(err, ErrInvalidPrefix) {
		t.Errorf("Insert(invalid) = %v; want ErrInvalidPrefix", err)
	}
	if m.Len() != 4 {
		t.Errorf("Len = %d; want 4", m.Len())
	}
}

func TestUnmaskedPrefix(t *testing.T) {
	var m Map[int]
	mustInsert(t, &m, "10.1.2.3/16", 1) // stored as 10.1.0.0/16
	if _, p, ok := m.Lookup(netip.MustParseAddr("10.1.0.1")); !ok || p.String() != "10.1.0.0/16" {
		t.Errorf("Lookup = %v, %v", p, ok)
	}
}

func TestGetDelete(t *testing.T) {
	var m Map[int]
	mustInsert(t, &m, "10.0.0.0/8", 1)
	mustInsert(t, &m, "2001:db8::/32", 2)
	if v, ok := m.Get(netip.MustParsePrefix("2001:db8::/32")); !ok || v != 2 {
		t.Errorf("Get = %v, %v", v, ok)
	}
	if _, ok := m.Get(netip.MustParsePrefix("10.0.0.0/9")); ok {
		t.Error("Get(10.0.0.0/9) found; want not found")
	}
	if m.Delete(netip.MustParsePrefix("10.0.0.0/16")) {
		t.Error("Delete(10.0.0.0/16) = true; want false")
	}
	if !m.Delete(netip.MustParsePrefix("10.0.0.0/8")) {
		t.Error("Delete(10.0.0.0/8) = false; want true")
	}
	if _, _, ok := m.Lookup(netip.MustParseAddr("10.1.1.1")); ok {
		t.Error("Lookup after delete found entry")
	}
	if m.Len() != 1 {
		t.Errorf("Len = %d; want 1", m.Len())
	}
}

func TestBuild(t *testing.T) {
	src := map[netip.Prefix]int{
		netip.MustParsePrefix("10.0.0.0/8"):     1,
		netip.MustParsePrefix("172.16.0.0/12"):  2,
		netip.MustParsePrefix("2001:db8::/32"):  3,
		netip.MustParsePrefix("192.168.0.0/16"): 4,
	}
	m, err := Build(maps.All(src))
	if err != nil {
		t.Fatal(err)
	}
	if v, _, ok := m.Lookup(netip.MustParseAddr("172.20.1.1")); !ok || v != 2 {
		t.Errorf("Lookup = %v, %v", v, ok)
	}
	var got []string
	for p := range m.All() {
		got = append(got, p.String())
	}
	want := []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "2001:db8::/32"}
	if len(got) != len(want) {
		t.Fatalf("All = %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("All = %v; want %v", got, want)
		}
	}

	src[netip.MustParsePrefix("10.5.0.0/16")] = 5
	if _, err := Build(maps.All(src)); !errors.Is(err, ErrOverlap) {
		t.Errorf("Build with overlap = %v; want ErrOverlap", err)
	}
}

// TestRandomized cross-checks Lookup against a linear scan.
func TestRandomized(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	var m Map[int]
	var prefixes []netip.Prefix
	for i := 0; len(prefixes) < 2000; i++ {
		var b [4]byte
		for j := range b {
			b[j] = byte(r.IntN(256))
		}
		p := netip.PrefixFrom(netip.AddrFrom4(b), 8+r.IntN(25)).Masked()
		if m.Insert(p, len(prefixes)) == nil {
			prefixes = append(prefixes, p)
		}
	}
	for range 20000 {
		var b [4]byte
		for j := range b {
			b[j] = byte(r.IntN(256))
		}
		ip := netip.AddrFrom4(b)
		want := -1
		for i, p := range prefixes {
			if p.Contains(ip) {
				want = i
				break
			}
		}
		v, _, ok := m.Lookup(ip)
		if (want >= 0) != ok || (ok && v != want) {
			t.Fatalf("Lookup(%s) = %d, %v; want %d", ip, v, ok, want)
		}
	}
}

// benchData returns a Map of ~1M IPv4 /24s and 1024 random IPs to look up,
// all of which hit.
func benchData(b *testing.B) (*Map[int], []netip.Addr) {
	b.Helper()
	r := rand.New(rand.NewPCG(1, 2))
	src := map[netip.Prefix]int{}
	for i := range 1 << 20 {
		src[netip.PrefixFrom(netip.AddrFrom4([4]byte{byte(i >> 16), byte(i >> 8), byte(i), 0}), 24)] = i
	}
	m, err := Build(maps.All(src))
	if err != nil {
		b.Fatal(err)
	}
	ips := make([]netip.Addr, 1024)
	for i := range ips {
		ips[i] = netip.AddrFrom4([4]byte{byte(r.IntN(16)), byte(r.IntN(256)), byte(r.IntN(256)), byte(r.IntN(256))})
	}
	return m, ips
}

func BenchmarkLookup(b *testing.B) {
	m, ips := benchData(b)
	for i := 0; b.Loop(); i++ {
		m.Lookup(ips[i&1023])
	}
}

func BenchmarkLookupParallel(b *testing.B)     { benchLookupParallel(b, benchData) }
func BenchmarkLookupIPv6Parallel(b *testing.B) { benchLookupParallel(b, benchData6) }

func benchLookupParallel(b *testing.B, data func(*testing.B) (*Map[int], []netip.Addr)) {
	m, ips := data(b)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for i := 0; pb.Next(); i++ {
			m.Lookup(ips[i&1023])
		}
	})
}

// benchData6 is the IPv6 version of benchData: ~1M /48s under 2001::/16
// and 1024 random IPs to look up, all of which hit.
func benchData6(b *testing.B) (*Map[int], []netip.Addr) {
	b.Helper()
	r := rand.New(rand.NewPCG(1, 2))
	src := map[netip.Prefix]int{}
	for i := range 1 << 20 {
		a := [16]byte{0x20, 0x01, byte(i >> 16), byte(i >> 8), byte(i)}
		src[netip.PrefixFrom(netip.AddrFrom16(a), 48)] = i
	}
	m, err := Build(maps.All(src))
	if err != nil {
		b.Fatal(err)
	}
	ips := make([]netip.Addr, 1024)
	for i := range ips {
		n := r.IntN(1 << 20)
		a := [16]byte{0x20, 0x01, byte(n >> 16), byte(n >> 8), byte(n)}
		for j := 6; j < 16; j++ { // random host bits
			a[j] = byte(r.IntN(256))
		}
		ips[i] = netip.AddrFrom16(a)
	}
	return m, ips
}

func BenchmarkLookupIPv6(b *testing.B) {
	m, ips := benchData6(b)
	for i := 0; b.Loop(); i++ {
		if _, _, ok := m.Lookup(ips[i&1023]); !ok {
			b.Fatalf("Lookup(%s) missed", ips[i&1023])
		}
	}
}

func TestExtremePrefixes(t *testing.T) {
	var m Map[string]
	mustInsert(t, &m, "::/0", "all6")
	mustInsert(t, &m, "255.255.255.255/32", "bcast")
	for ip, want := range map[string]string{"::": "all6", "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff": "all6", "255.255.255.255": "bcast", "255.255.255.254": ""} {
		if v, _, _ := m.Lookup(netip.MustParseAddr(ip)); v != want {
			t.Errorf("Lookup(%s) = %q; want %q", ip, v, want)
		}
	}
	if err := m.Insert(netip.MustParsePrefix("2001:db8::1/128"), "x"); !errors.Is(err, ErrOverlap) {
		t.Errorf("Insert inside ::/0 = %v; want ErrOverlap", err)
	}
	mustInsert(t, &m, "0.0.0.0/1", "low")
	if v, _, _ := m.Lookup(netip.MustParseAddr("127.0.0.1")); v != "low" {
		t.Errorf("Lookup(127.0.0.1) = %q; want low", v)
	}
}

// TestConcurrentReads runs read methods from many goroutines at once.
// Run with -race to verify that concurrent readers are safe.
func TestConcurrentReads(t *testing.T) {
	// Every other /24 in 0.0.0.0/15 and every other /48 under 2001:db8::/32,
	// so random lookups produce both hits and misses.
	const n = 1 << 16
	src := map[netip.Prefix]int{}
	for i := 0; i < n; i += 2 {
		src[netip.PrefixFrom(netip.AddrFrom4([4]byte{byte(i >> 16), byte(i >> 8), byte(i), 0}), 24)] = i
		src[netip.PrefixFrom(netip.AddrFrom16([16]byte{0x20, 0x01, 0x0d, 0xb8, byte(i >> 8), byte(i)}), 48)] = -i
	}
	m, err := Build(maps.All(src))
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for g := range 16 {
		wg.Go(func() {
			r := rand.New(rand.NewPCG(uint64(g), 0))
			for range 20000 {
				i := r.IntN(n)
				ip4 := netip.AddrFrom4([4]byte{byte(i >> 16), byte(i >> 8), byte(i), byte(r.IntN(256))})
				ip6 := netip.AddrFrom16([16]byte{0x20, 0x01, 0x0d, 0xb8, byte(i >> 8), byte(i), 15: byte(r.IntN(256))})
				for _, c := range []struct {
					ip   netip.Addr
					want int
				}{{ip4, i}, {ip6, -i}} {
					v, _, ok := m.Lookup(c.ip)
					if ok != (i%2 == 0) || (ok && v != c.want) {
						t.Errorf("Lookup(%s) = %d, %v; want %d, %v", c.ip, v, ok, c.want, i%2 == 0)
						return
					}
				}
				if _, ok := m.Get(netip.PrefixFrom(ip4, 24).Masked()); ok != (i%2 == 0) {
					t.Errorf("Get(%s/24) ok = %v", ip4, ok)
					return
				}
			}
			if got := m.Len(); got != n {
				t.Errorf("Len = %d; want %d", got, n)
			}
			count := 0
			for range m.All() {
				count++
			}
			if count != n {
				t.Errorf("All yielded %d; want %d", count, n)
			}
		})
	}
	wg.Wait()
}
