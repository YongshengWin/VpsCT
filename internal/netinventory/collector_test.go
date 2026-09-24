package netinventory

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
)

func TestConcurrentBillingAndGuardObservationsKeepOneSequence(t *testing.T) {
	c := New(t.TempDir(), "boot")
	defer c.Close()
	count := int64(0)
	c.read = func() (reading, error) {
		count++
		l := testLink(2, "wan0")
		l.Rx += count
		return reading{Links: []link{l}, Complete: true}, nil
	}
	results := make(chan *agentproto.NetworkSnapshot, 40)
	var workers sync.WaitGroup
	for worker := 0; worker < 2; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := 0; i < 20; i++ {
				results <- c.Collect()
			}
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		for i := 0; i < 20; i++ {
			c.SetPreferredIDs(map[string]bool{"11111111111111111111111111111111": true})
			c.SetBindingIDs(map[string]bool{"22222222222222222222222222222222": true})
		}
	}()
	workers.Wait()
	close(results)
	seen := map[int64]bool{}
	id := ""
	for s := range results {
		if s.Status != "ok" || seen[s.Sequence] || len(s.Interfaces) != 1 {
			t.Fatalf("invalid concurrent observation: %+v", s)
		}
		seen[s.Sequence] = true
		if id != "" && id != s.Interfaces[0].ID {
			t.Fatal("concurrent observation changed identity")
		}
		id = s.Interfaces[0].ID
	}
	if len(seen) != 40 || !seen[1] || !seen[40] {
		t.Fatal("observation sequence was lost or reused")
	}
}

func TestBindingPreferencesSurviveBillingPreferenceReplacement(t *testing.T) {
	c := New(t.TempDir(), "boot")
	defer c.Close()
	l := testLink(1000, "wan0")
	links := []link{l}
	c.read = func() (reading, error) { return reading{Links: links, Complete: true}, nil }
	id := c.Collect().Interfaces[0].ID
	c.SetBindingIDs(map[string]bool{id: true})
	c.SetPreferredIDs(nil)
	links = nil
	for index := 1; index <= agentproto.MaxNetworkInterfaces; index++ {
		v := testLink(index, "noise")
		v.Kind, v.MAC, v.Device = "dummy", "", ""
		links = append(links, v)
	}
	links = append(links, l)
	s := c.Collect()
	for _, iface := range s.Interfaces {
		if iface.ID == id {
			return
		}
	}
	t.Fatal("billing preference update dropped an active network binding")
}

func testLink(index int, name string) link {
	return link{NetworkInterface: agentproto.NetworkInterface{Index: index, Name: name, Kind: "physical", MAC: "02:00:00:00:00:01", MTU: 1500, Up: true, Carrier: true, CountersValid: true, Rx: 100, Tx: 200, Addresses: []string{"192.0.2.1/24"}}, Device: "/sys/devices/test-device"}
}

func TestIdentityRestartRenameResetAndReboot(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	raw := testLink(2, "eth0")
	configure := func(c *Collector) {
		c.read = func() (reading, error) { return reading{Links: []link{raw}, Complete: true}, nil }
		c.now = func() time.Time { return now }
	}
	c := New(dir, "boot-a")
	configure(c)
	first := c.Collect()
	if first.Status != "ok" || first.Interfaces[0].RateValid {
		t.Fatalf("first: %+v", first)
	}
	old := first.Interfaces[0]
	now = now.Add(10 * time.Second)
	raw.Name, raw.Rx, raw.Tx = "wan0", 1100, 2200
	c = New(dir, "boot-a") // agent restart reloads stable identity and sequence
	configure(c)
	second := c.Collect()
	l := second.Interfaces[0]
	if l.ID != old.ID || l.Generation != old.Generation || second.Sequence != first.Sequence+1 || !l.RateValid || l.RxRate != 100 || l.TxRate != 200 {
		t.Fatalf("rename/restart: %+v", second)
	}
	now = now.Add(10 * time.Second)
	raw.Rx, raw.Tx = 5, 8
	third := c.Collect().Interfaces[0]
	if third.ID != old.ID || third.Generation == old.Generation || third.RateValid {
		t.Fatalf("counter reset: %+v", third)
	}
	now = now.Add(10 * time.Second)
	raw.Index = 5
	c = New(dir, "boot-b")
	configure(c)
	fourth := c.Collect().Interfaces[0]
	if fourth.ID != old.ID || fourth.Generation == third.Generation || fourth.RateValid {
		t.Fatalf("reboot: %+v", fourth)
	}
}

func TestDisappearanceAndReplacementDoNotReuseIdentity(t *testing.T) {
	c := New(t.TempDir(), "boot")
	raw := testLink(2, "eth0")
	r := reading{Links: []link{raw}, Complete: true}
	c.read = func() (reading, error) { return r, nil }
	first := c.Collect().Interfaces[0]
	r.Links = nil
	c.Collect()
	r.Links = []link{raw}
	if c.Collect().Interfaces[0].ID == first.ID {
		t.Fatal("recreated device reused ID")
	}
	previous := c.Collect().Interfaces[0]
	r.Links[0].MAC = "02:00:00:00:00:02"
	if c.Collect().Interfaces[0].ID == previous.ID {
		t.Fatal("replacement reused ID")
	}
}

func TestRestartWithLostObservationKeepsOnlyProvableDeviceIdentity(t *testing.T) {
	for _, physical := range []bool{true, false} {
		t.Run(fmt.Sprint("physical=", physical), func(t *testing.T) {
			dir := t.TempDir()
			raw := testLink(2, "wan0")
			if !physical {
				raw.Kind, raw.Device = "veth", ""
			}
			first := New(dir, "same-boot")
			first.read = func() (reading, error) { return reading{Links: []link{raw}, Complete: true}, nil }
			before := first.Collect()
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}
			// newSource reports Reset on its first read because deletions during
			// the process gap were not observed. A matching name/MAC/index alone
			// cannot prove a veth has not been replaced during that gap.
			second := New(dir, "same-boot")
			defer second.Close()
			second.read = func() (reading, error) { return reading{Links: []link{raw}, Complete: true, Reset: true}, nil }
			after := second.Collect()
			if after.Status != "ok" || after.CollectorID != before.CollectorID || after.Sequence != before.Sequence+1 {
				t.Fatal("registry continuity lost", after)
			}
			old, current := before.Interfaces[0], after.Interfaces[0]
			if (old.ID == current.ID) != physical || old.Generation == current.Generation || current.RateValid {
				t.Fatal("restart reused uncertain identity or counters", current)
			}
		})
	}
}

func TestFailureNeverTurnsIntoZeroCounters(t *testing.T) {
	c := New(t.TempDir(), "boot")
	c.read = func() (reading, error) { return reading{Links: []link{testLink(2, "eth0")}, Complete: true}, nil }
	first := c.Collect()
	if first.Status != "ok" {
		t.Fatal(first)
	}
	c.read = func() (reading, error) { return reading{}, errors.New("read failed") }
	s := c.Collect()
	if s.Status != "error" || len(s.Interfaces) != 0 {
		t.Fatal("failure reported fabricated counters")
	}
	// Corrupt state must be visible, rather than silently resetting identities.
	if err := os.WriteFile(c.path, []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	c.loaded = false
	c.read = func() (reading, error) { return reading{Complete: true}, nil }
	if c.Collect().Status != "error" {
		t.Fatal("corrupt registry silently replaced")
	}
}

func TestIncompleteDumpKeepsIdentityWithoutRate(t *testing.T) {
	c := New(t.TempDir(), "boot")
	raw := testLink(2, "eth0")
	r := reading{Links: []link{raw}, Complete: true}
	c.read = func() (reading, error) { return r, nil }
	id := c.Collect().Interfaces[0].ID
	r = reading{Complete: false}
	if c.Collect().Status != "incomplete" {
		t.Fatal("expected incomplete")
	}
	r = reading{Links: []link{raw}, Complete: true}
	l := c.Collect().Interfaces[0]
	if l.ID != id || l.RateValid {
		t.Fatal("incomplete dump lost identity or invented rate")
	}
}

func TestDeletionSurvivesFailedObservation(t *testing.T) {
	c := New(t.TempDir(), "boot")
	raw := testLink(2, "eth0")
	c.read = func() (reading, error) { return reading{Links: []link{raw}, Complete: true}, nil }
	id := c.Collect().Interfaces[0].ID
	c.read = func() (reading, error) { return reading{Deleted: map[int]bool{2: true}}, errors.New("dump failed") }
	if c.Collect().Status != "error" {
		t.Fatal("expected failed dump")
	}
	c.read = func() (reading, error) { return reading{Links: []link{raw}, Complete: true}, nil }
	if c.Collect().Interfaces[0].ID == id {
		t.Fatal("lost deletion after failed observation")
	}
}

func TestPreferredBillingIdentitySurvivesInventoryLimitAndBootIndexChange(t *testing.T) {
	dir := t.TempDir()
	raw := testLink(2, "wan0")
	c := New(dir, "boot-a")
	defer c.Close()
	c.read = func() (reading, error) { return reading{Links: []link{raw}, Complete: true}, nil }
	first := c.Collect()
	id := first.Interfaces[0].ID
	// Keep the selected physical identity, even when its index changed at boot
	// and it appears after more than the reporting limit of unrelated devices.
	c2 := New(dir, "boot-b")
	defer c2.Close()
	c2.SetPreferredIDs(map[string]bool{id: true})
	raw.Index = 1000
	links := make([]link, 0, agentproto.MaxNetworkInterfaces+1)
	for n := 1; n <= agentproto.MaxNetworkInterfaces; n++ {
		v := testLink(n, "noise")
		v.Kind, v.Device, v.MAC = "dummy", "", ""
		links = append(links, v)
	}
	links = append(links, raw)
	c2.read = func() (reading, error) { return reading{Links: links, Complete: true}, nil }
	next := c2.Collect()
	if next.Status != "incomplete" || len(next.Interfaces) != agentproto.MaxNetworkInterfaces {
		t.Fatal(next)
	}
	for _, n := range next.Interfaces {
		if n.ID == id && n.Index == 1000 {
			return
		}
	}
	t.Fatal("selected billing device was dropped by inventory limit")
}

func TestIncompleteFullDumpRetainsMissingBillingIdentity(t *testing.T) {
	c := New(t.TempDir(), "boot")
	defer c.Close()
	wan := testLink(1000, "wan0")
	r := reading{Links: []link{wan}, Complete: true}
	c.read = func() (reading, error) { return r, nil }
	id := c.Collect().Interfaces[0].ID
	c.SetPreferredIDs(map[string]bool{id: true})
	r.Complete = false
	r.Links = nil
	for n := 1; n <= agentproto.MaxNetworkInterfaces; n++ {
		l := testLink(n, "noise")
		l.Kind, l.MAC, l.Device = "dummy", "", ""
		r.Links = append(r.Links, l)
	}
	partial := c.Collect()
	if partial.Status != "incomplete" {
		t.Fatal(partial)
	}
	if len(c.state.Links) != agentproto.MaxNetworkInterfaces {
		t.Fatal("identity budget exceeded")
	}
	r = reading{Links: []link{wan}, Complete: true}
	recovered := c.Collect().Interfaces[0]
	if recovered.ID != id || recovered.RateValid {
		t.Fatal("partial dump discarded billing identity or invented continuity", recovered)
	}
}
