package cidrmap_test

import (
	"encoding/json"
	"fmt"
	"log"
	"net/netip"
	"strings"

	"github.com/ericfialkowski/cidrmap"
)

// This example loads an unsorted JSON list, as you might download one,
// that contains a duplicate. netip.Prefix implements encoding.TextUnmarshaler,
// so it can be a JSON field directly.
func ExampleBuilder() {
	const data = `[
		{"cidr": "192.168.0.0/16", "region": "lab"},
		{"cidr": "10.0.0.0/8",     "region": "us-east"},
		{"cidr": "2001:db8::/32",  "region": "eu-west"},
		{"cidr": "10.0.0.0/8",     "region": "us-east"},
		{"cidr": "203.0.113.7/32", "region": "edge"}
	]`

	type Record struct {
		CIDR   netip.Prefix `json:"cidr"`
		Region string       `json:"region"`
	}
	var records []Record
	if err := json.NewDecoder(strings.NewReader(data)).Decode(&records); err != nil {
		log.Fatal(err)
	}

	b := cidrmap.Builder[Record]{
		OnDuplicate: func(p netip.Prefix, existing, dup Record) (Record, error) {
			if existing != dup {
				return existing, fmt.Errorf("conflicting records for %s", p)
			}
			fmt.Println("skipping duplicate", p)
			return existing, nil
		},
	}
	for _, r := range records {
		b.Add(r.CIDR, r)
	}
	m, err := b.Build()
	if err != nil {
		log.Fatal(err)
	}

	for _, ip := range []string{"10.20.30.40", "2001:db8::1", "203.0.113.7", "8.8.8.8"} {
		if r, prefix, ok := m.Lookup(netip.MustParseAddr(ip)); ok {
			fmt.Printf("%s is in %s (%s)\n", ip, prefix, r.Region)
		} else {
			fmt.Printf("%s not found\n", ip)
		}
	}
	// Output:
	// skipping duplicate 10.0.0.0/8
	// 10.20.30.40 is in 10.0.0.0/8 (us-east)
	// 2001:db8::1 is in 2001:db8::/32 (eu-west)
	// 203.0.113.7 is in 203.0.113.7/32 (edge)
	// 8.8.8.8 not found
}
