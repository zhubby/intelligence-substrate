package main

import "testing"

func TestPublisherActionModesCannotCrossAuthority(t *testing.T) {
	for _, row := range []struct {
		status, continuation bool
		action               string
		allow                bool
	}{
		{true, false, "status", true}, {true, false, "publish", false}, {true, false, "continue", false},
		{false, true, "continue", true}, {false, true, "publish", false}, {false, true, "status", false},
		{true, true, "status", false}, {true, true, "continue", false}, {false, false, "publish", true},
	} {
		if err := publisherActionMode(row.status, row.continuation, row.action); (err == nil) != row.allow {
			t.Fatalf("mode status=%v continue=%v action=%s: %v", row.status, row.continuation, row.action, err)
		}
	}
}

func TestPublisherRecoveryModeCannotBeUsedByWorkerProviders(t *testing.T) {
	for _, row := range []struct {
		name                                  string
		status, continuation, recovery, renew bool
		inspect, initKey                      string
		allow                                 bool
	}{
		{name: "administrator recovery", recovery: true, allow: true},
		{name: "status provider", status: true, recovery: true},
		{name: "continuation provider", continuation: true, recovery: true},
		{name: "inspection", recovery: true, inspect: "operation"},
		{name: "renewal", recovery: true, renew: true},
		{name: "key creation", recovery: true, initKey: "private-key"},
		{name: "normal publication", allow: true},
	} {
		t.Run(row.name, func(t *testing.T) {
			if err := publisherModes(row.status, row.continuation, row.recovery, false, false, false, row.renew, row.inspect, "", row.initKey); (err == nil) != row.allow {
				t.Fatal(err)
			}
		})
	}
}

func TestPublisherRecoveryModesAreExclusive(t *testing.T) {
	for mask := 0; mask < 1024; mask++ {
		var flags [10]bool
		count := 0
		for i := range flags {
			flags[i] = mask&(1<<i) != 0
			if flags[i] {
				count++
			}
		}
		inspect, inspectQuery, key := "", "", ""
		if flags[7] {
			inspect = "operation"
		}
		if flags[8] {
			inspectQuery = "query"
		}
		if flags[9] {
			key = "seed"
		}
		if err := publisherModes(flags[0], flags[1], flags[2], flags[3], flags[4], flags[5], flags[6], inspect, inspectQuery, key); (err == nil) != (count <= 1) {
			t.Fatal("mode combination crossed authority", mask, err)
		}
	}
}
