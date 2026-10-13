package cidrmap

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
)

func pfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }

func TestBuilderUnordered(t *testing.T) {
	var b Builder[string]
	for _, s := range []string{"192.168.0.0/16", "2001:db8::/32", "10.0.0.0/8", "172.16.0.0/12", "fe80::/10"} {
		b.Add(pfx(s), s)
	}
	m, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if m.Len() != 5 {
		t.Errorf("Len = %d; want 5", m.Len())
	}
	for ip, want := range map[string]string{"10.9.9.9": "10.0.0.0/8", "172.31.0.1": "172.16.0.0/12", "fe80::1": "fe80::/10"} {
		if v, _, _ := m.Lookup(netip.MustParseAddr(ip)); v != want {
			t.Errorf("Lookup(%s) = %q; want %q", ip, v, want)
		}
	}
}

func TestBuilderDuplicateDefaultError(t *testing.T) {
	var b Builder[int]
	b.Add(pfx("10.0.0.0/8"), 1)
	b.Add(pfx("192.168.0.0/16"), 2)
	b.Add(pfx("10.0.0.0/8"), 3)
	_, err := b.Build()
	if !errors.Is(err, ErrDuplicate) || !errors.Is(err, ErrOverlap) {
		t.Fatalf("Build = %v; want ErrDuplicate wrapping ErrOverlap", err)
	}
	if !strings.Contains(err.Error(), "10.0.0.0/8") {
		t.Errorf("error %q does not name the prefix", err)
	}
}

func TestBuilderOnDuplicate(t *testing.T) {
	keepFirst := func(_ netip.Prefix, existing, _ string) (string, error) { return existing, nil }
	keepLast := func(_ netip.Prefix, _, dup string) (string, error) { return dup, nil }
	join := func(_ netip.Prefix, existing, dup string) (string, error) { return existing + "+" + dup, nil }

	tests := []struct {
		name string
		fn   func(netip.Prefix, string, string) (string, error)
		want string
	}{
		{"keep first", keepFirst, "a"},
		{"keep last", keepLast, "c"},
		{"merge in Add order", join, "a+b+c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := Builder[string]{OnDuplicate: tt.fn}
			// Unrelated prefixes before, between and after the duplicates,
			// so the stable sort has something to reorder.
			b.Add(pfx("10.2.0.0/16"), "x")
			b.Add(pfx("10.1.0.0/16"), "a")
			b.Add(pfx("10.0.0.0/16"), "y")
			b.Add(pfx("10.1.0.0/16"), "b")
			b.Add(pfx("10.3.0.0/16"), "z")
			b.Add(pfx("10.1.0.0/16"), "c")
			m, err := b.Build()
			if err != nil {
				t.Fatal(err)
			}
			if m.Len() != 4 {
				t.Errorf("Len = %d; want 4", m.Len())
			}
			if v, _ := m.Get(pfx("10.1.0.0/16")); v != tt.want {
				t.Errorf("value = %q; want %q", v, tt.want)
			}
		})
	}
}

func TestBuilderOnDuplicateError(t *testing.T) {
	errConflict := errors.New("conflicting values")
	b := Builder[int]{OnDuplicate: func(p netip.Prefix, existing, dup int) (int, error) {
		if existing != dup {
			return 0, errConflict
		}
		return existing, nil
	}}
	b.Add(pfx("10.0.0.0/8"), 1)
	b.Add(pfx("10.0.0.0/8"), 1) // identical value: fine
	if _, err := b.Build(); err != nil {
		t.Fatalf("Build with equal duplicates = %v", err)
	}
	b.Add(pfx("10.0.0.0/8"), 1)
	b.Add(pfx("10.0.0.0/8"), 2)
	if _, err := b.Build(); !errors.Is(err, errConflict) {
		t.Fatalf("Build = %v; want errConflict", err)
	}
}

func TestBuilderNormalizedDuplicates(t *testing.T) {
	calls := 0
	b := Builder[int]{OnDuplicate: func(_ netip.Prefix, existing, _ int) (int, error) {
		calls++
		return existing, nil
	}}
	b.Add(pfx("10.0.0.0/8"), 1)
	b.Add(pfx("10.1.2.3/8"), 2)          // host bits set
	b.Add(pfx("::ffff:10.0.0.0/104"), 3) // IPv4-mapped form
	m, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || m.Len() != 1 {
		t.Errorf("calls = %d, Len = %d; want 2, 1", calls, m.Len())
	}
}

func TestBuilderOverlapNotDuplicate(t *testing.T) {
	b := Builder[int]{OnDuplicate: func(_ netip.Prefix, existing, _ int) (int, error) { return existing, nil }}
	b.Add(pfx("10.0.0.0/16"), 1)
	b.Add(pfx("10.0.0.0/8"), 2) // same start address, different length
	_, err := b.Build()
	if !errors.Is(err, ErrOverlap) || errors.Is(err, ErrDuplicate) {
		t.Fatalf("Build = %v; want ErrOverlap but not ErrDuplicate", err)
	}
}

func TestBuilderInvalidPrefix(t *testing.T) {
	var b Builder[int]
	b.Add(pfx("10.0.0.0/8"), 1)
	b.Add(netip.Prefix{}, 2) // e.g. a missing JSON field
	b.Add(pfx("11.0.0.0/8"), 3)
	_, err := b.Build()
	if !errors.Is(err, ErrInvalidPrefix) || !strings.Contains(err.Error(), "item 2") {
		t.Fatalf("Build = %v; want ErrInvalidPrefix naming item 2", err)
	}
}

func TestBuilderReuse(t *testing.T) {
	calls := 0
	b := Builder[int]{OnDuplicate: func(_ netip.Prefix, existing, _ int) (int, error) {
		calls++
		return existing, nil
	}}
	b.Add(netip.Prefix{}, 0)
	if _, err := b.Build(); err == nil {
		t.Fatal("Build with invalid prefix succeeded")
	}
	// The failed Build reset the Builder, including the error.
	b.Add(pfx("10.0.0.0/8"), 1)
	m1, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	b.Add(pfx("10.0.0.0/8"), 2)
	b.Add(pfx("10.0.0.0/8"), 3)
	m2, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("OnDuplicate calls = %d; want 1 (kept across Build)", calls)
	}
	if v, _ := m1.Get(pfx("10.0.0.0/8")); v != 1 {
		t.Errorf("first map value = %d; want 1", v)
	}
	if v, _ := m2.Get(pfx("10.0.0.0/8")); v != 2 {
		t.Errorf("second map value = %d; want 2", v)
	}
	if m, _ := b.Build(); m.Len() != 0 {
		t.Errorf("Build after reset has %d entries; want 0", m.Len())
	}
}

func TestInsertDuplicate(t *testing.T) {
	var m Map[int]
	mustInsert(t, &m, "10.0.0.0/8", 1)
	err := m.Insert(pfx("10.0.0.0/8"), 2)
	if !errors.Is(err, ErrDuplicate) || !errors.Is(err, ErrOverlap) {
		t.Fatalf("Insert = %v; want ErrDuplicate wrapping ErrOverlap", err)
	}
	if err := m.Insert(pfx("10.0.0.0/16"), 2); errors.Is(err, ErrDuplicate) || !errors.Is(err, ErrOverlap) {
		t.Fatalf("Insert = %v; want ErrOverlap but not ErrDuplicate", err)
	}
}
