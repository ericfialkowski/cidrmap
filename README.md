# cidrmap

A generic Go map keyed by non-overlapping CIDR prefixes. Given an IPv4 or
IPv6 address, it finds the prefix that contains it, and that prefix's
value, in O(log n) with no allocations.

```go
var m cidrmap.Map[string]
m.Insert(netip.MustParsePrefix("10.0.0.0/8"), "internal")
m.Insert(netip.MustParsePrefix("2001:db8::/32"), "docs")

name, prefix, ok := m.Lookup(netip.MustParseAddr("10.1.2.3"))
// "internal", 10.0.0.0/8, true
```

It suits lookup tables loaded from IP range lists: crawler and bot
ranges, cloud provider ranges, GeoIP-style allocations, allow and deny
lists. Prefixes must not overlap. That is checked, not assumed.

## Install

```sh
go get github.com/ericfialkowski/cidrmap
```

Requires Go 1.25 or later. The package uses only the standard library.

## Loading a JSON list from the internet

Range lists are usually published as unsorted JSON. This example loads
Google's crawler ranges:

```json
{
  "creationTime": "2026-10-12T14:46:34.000000",
  "prefixes": [
    { "ipv6Prefix": "2001:4860:4801:10::/64" },
    { "ipv4Prefix": "66.249.64.0/27" }
  ]
}
```

`netip.Prefix` implements `encoding.TextUnmarshaler`, so it can be a JSON
field directly with no parsing code. A `Builder` collects the prefixes in
any order and sorts once at the end:

```go
// googleRanges matches the format of Google's published IP range files.
// Each entry has either an IPv4 or an IPv6 prefix; netip.Prefix decodes
// directly from JSON strings like "66.249.64.0/27".
type googleRanges struct {
	CreationTime string `json:"creationTime"`
	Prefixes     []struct {
		IPv4 netip.Prefix `json:"ipv4Prefix"`
		IPv6 netip.Prefix `json:"ipv6Prefix"`
	} `json:"prefixes"`
}

func fetchRanges(ctx context.Context, client *http.Client, url string) (*cidrmap.Map[string], error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: %s", url, resp.Status)
	}

	var data googleRanges
	// Cap the body size so a bad response can't exhaust memory.
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode %s: %w", url, err)
	}

	b := cidrmap.Builder[string]{
		// Duplicates are rare and harmless here, so keep the first.
		OnDuplicate: func(p netip.Prefix, existing, dup string) (string, error) {
			return existing, nil
		},
	}
	for _, p := range data.Prefixes {
		prefix := p.IPv4
		if !prefix.IsValid() {
			prefix = p.IPv6
		}
		// If neither field was set, prefix is invalid and Build reports it.
		b.Add(prefix, "googlebot")
	}
	return b.Build()
}
```

Points to note:

- **Missing fields are caught.** A missing prefix field decodes to the
  zero `netip.Prefix`, which is invalid. `Add` records it, and `Build`
  fails with `ErrInvalidPrefix` naming the item number. So a change in the
  feed's format fails the load instead of silently dropping entries.
- **Single addresses need a prefix length.** `netip.Prefix` does not
  accept a bare address like `"1.2.3.4"`; write it as `1.2.3.4/32` (or
  `/128` for IPv6).
- **Host bits are fine.** `10.1.2.3/16` is stored as `10.1.0.0/16`.
- **The value can be any type**, such as a struct holding the whole JSON
  record.

[`ExampleBuilder`](example_test.go) is a runnable version that
needs no network access.

## Duplicates and overlaps

| Input | Result |
|---|---|
| Same prefix twice, `OnDuplicate` unset (the default) | `Build` fails with `ErrDuplicate` |
| Same prefix twice, `OnDuplicate` set | Your function picks the value to keep |
| Overlapping prefixes, such as `10.0.0.0/8` and `10.1.0.0/16` | `Build` always fails with `ErrOverlap` |

`OnDuplicate(p, existing, dup)` receives the values in the order they were
added, so `existing` is always the earlier one. Return `existing` to keep
the first value, `dup` to keep the last, or a merged value. Return an
error to fail the build; for example, reject duplicates whose values
differ:

```go
OnDuplicate: func(p netip.Prefix, existing, dup Record) (Record, error) {
	if existing != dup {
		return existing, fmt.Errorf("conflicting records for %s", p)
	}
	return existing, nil
},
```

Duplicates are matched after normalization, so `10.1.2.3/8` and
`::ffff:10.0.0.0/104` are both duplicates of `10.0.0.0/8`.

`ErrDuplicate` wraps `ErrOverlap`, so `errors.Is(err, cidrmap.ErrOverlap)`
matches both. `Insert` uses the same errors.

## Refreshing while serving lookups

`AtomicMap` holds the current `Map` behind an `atomic.Pointer`. Lookups
never take a lock. A refresh builds a complete new map and swaps it in, so
readers see either the old data or the new data, never a mix:

```go
func refreshRanges(ctx context.Context, live *cidrmap.AtomicMap[string], url string, every time.Duration) {
	client := &http.Client{Timeout: 30 * time.Second}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		m, err := fetchRanges(ctx, client, url)
		if err != nil {
			log.Printf("refresh failed, keeping current ranges: %v", err)
			continue
		}
		live.Store(m)
	}
}

func main() {
	ctx := context.Background()
	client := &http.Client{Timeout: 30 * time.Second}

	// Fail fast at startup; after that, a failed refresh keeps the old data.
	m, err := fetchRanges(ctx, client, url)
	if err != nil {
		log.Fatal(err)
	}
	live := cidrmap.NewAtomic(m)
	go refreshRanges(ctx, live, url, 6*time.Hour)

	if name, prefix, ok := live.Lookup(netip.MustParseAddr("66.249.64.1")); ok {
		fmt.Printf("%s (%s)\n", name, prefix) // googlebot (66.249.64.0/27)
	}
}
```

Before calling `Store`, you may want a sanity check, such as refusing an
empty list or one much smaller than the current map
(`live.Load().Len()`). A feed that returns valid but truncated JSON would
otherwise replace good data.

For small edits rather than full reloads, `Update` copies the current map,
applies your function, and publishes the copy only if the function
succeeds:

```go
err := live.Update(func(m *cidrmap.Map[string]) error {
	return m.Insert(netip.MustParsePrefix("203.0.113.0/24"), "partner")
})
```

`Update` copies the whole map each time. Put many changes in one call, or
use `Builder` and `Store` for a full reload.

## Concurrency

| Type | Concurrent lookups | Writes while reading |
|---|---|---|
| `Map` | Safe | Not safe; `Insert` and `Delete` need your own locking |
| `AtomicMap` | Safe and lock-free | Safe through `Store` and `Update` |

A `Map` passed to `Store`, or returned by `Load`, must not be modified
afterwards, because readers may still be using it. Modify a `Clone()`
instead. `Clone` is shallow: values that hold pointers, slices or maps
share what they point to.

## API

| | |
|---|---|
| `Builder[V]` | `Add(prefix, v)`, `Build() (*Map[V], error)`, `OnDuplicate` field. Build resets the builder for reuse. |
| `Build(seq)` | Builds from an `iter.Seq2[netip.Prefix, V]`, such as `maps.All(m)`. Duplicates are an error. |
| `Map[V]` | `Lookup(ip)`, `Insert(prefix, v)`, `Get(prefix)`, `Delete(prefix)`, `Len()`, `All()`, `Clone()` |
| `AtomicMap[V]` | `NewAtomic(m)`, `Lookup(ip)`, `Load()`, `Store(m)`, `Update(fn)` |
| Errors | `ErrInvalidPrefix`, `ErrOverlap`, `ErrDuplicate` (wraps `ErrOverlap`) |

The zero values of `Map`, `Builder` and `AtomicMap` are ready to use.

Address handling:

- IPv4-mapped IPv6 addresses (`::ffff:10.1.2.3`) are treated as IPv4,
  both in lookups and in stored prefixes.
- IPv6 zones (`fe80::1%eth0`) are ignored.
- `Insert` is O(n) because it keeps the array sorted. Use `Builder` for
  bulk loads.

## Performance

Measured on an Apple M2 Pro (12 cores) with about 1M prefixes per family
(IPv4 `/24`s, IPv6 `/48`s). Lookups allocate nothing.

| Operation | IPv4 | IPv6 |
|---|---|---|
| `Map.Lookup` | ~105 ns | ~130 ns |
| `AtomicMap.Lookup` | ~110 ns | ~130 ns |
| Parallel lookups, all cores (time per lookup) | ~13 ns | ~16 ns |
| Parallel lookups during constant `Store` calls | ~25 ns | ~29 ns |
| `Builder`: 1M shuffled prefixes | ~600 ms | |

The parallel rows are throughput, about 80M lookups per second across the
machine; each individual lookup still takes about 100 ns. The constant
`Store` benchmark swaps maps about 6.5M times per second, far more often
than any real refresh. Building 1M prefixes allocates about 330 MB in
total, most of it short-lived. A small list like Google's (317 prefixes)
takes about 0.1 ms to decode from JSON and build.

Run the benchmarks yourself:

```sh
go test -run '^$' -bench . -benchmem
```

## How it works

Since prefixes never overlap, each one is a plain range of addresses. Each
address family keeps a sorted array of range start addresses, stored as
128-bit integers, alongside a parallel array of entries. A lookup is a
binary search over the start addresses for the last range starting at or
before the IP, then one check that the IP isn't past that range's end.
Keeping the start addresses in their own compact array means the search
touches as little memory as possible.

## Testing

```sh
go test -race ./...
```
