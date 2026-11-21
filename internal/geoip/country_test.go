package geoip

import (
	"compress/gzip"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCountryFileMaterializesBothFamilies(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dbip.csv.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	_, _ = zw.Write([]byte("ip_start,ip_end,country\n8.8.8.0,8.8.8.255,US\n2001:db8::,2001:db8::ff,US\n10.0.0.0,10.0.0.255,CN\n"))
	if err = zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	ranges, sum, err := parseCountryFile(path, []string{"us"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum) != 64 || strings.Join(ranges.IPv4, ",") != "8.8.8.0/24" || strings.Join(ranges.IPv6, ",") != "2001:db8::/120" {
		t.Fatalf("unexpected materialized ranges: %#v", ranges)
	}
}

func TestRangePrefixesSplitsUnalignedIPv4Range(t *testing.T) {
	got := rangePrefixes4(mustAddr("192.0.2.1"), mustAddr("192.0.2.3"))
	want := []string{"192.0.2.1/32", "192.0.2.2/31"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v want %v", got, want)
	}
}

func mustAddr(raw string) (a netip.Addr) {
	a, _ = netip.ParseAddr(raw)
	return
}
