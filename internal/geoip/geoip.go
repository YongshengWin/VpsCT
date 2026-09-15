// Package geoip turns client IPs into a short place label (city / province).
package geoip

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lionsoul2014/ip2region/binding/golang/xdb"
)

// Info is a best-effort location for one IP.
type Info struct {
	Country  string `json:"country,omitempty"`
	Province string `json:"province,omitempty"`
	City     string `json:"city,omitempty"`
	District string `json:"district,omitempty"`
	ISP      string `json:"isp,omitempty"`
	Label    string `json:"label"`
}

var mirrorsV4 = []string{
	"https://testingcf.jsdelivr.net/gh/lionsoul2014/ip2region@master/data/ip2region_v4.xdb",
	"https://cdn.jsdelivr.net/gh/lionsoul2014/ip2region@master/data/ip2region_v4.xdb",
	"https://raw.githubusercontent.com/lionsoul2014/ip2region/master/data/ip2region_v4.xdb",
}

var mirrorsV6 = []string{
	"https://testingcf.jsdelivr.net/gh/lionsoul2014/ip2region@master/data/ip2region_v6.xdb",
	"https://cdn.jsdelivr.net/gh/lionsoul2014/ip2region@master/data/ip2region_v6.xdb",
	"https://raw.githubusercontent.com/lionsoul2014/ip2region/master/data/ip2region_v6.xdb",
}

const minXDBSize = 2 << 20

// Lookup resolves public IPv4/IPv6 using local ip2region databases. Online
// lookups can be enabled explicitly by the operator.
type Lookup struct {
	dir    string
	path   string
	pathV6 string

	mu            sync.Mutex
	searcher      *xdb.Searcher
	searcherV6    *xdb.Searcher
	mem           map[string]Info
	disk          map[string]cacheItem
	http          *http.Client
	disableOnline bool
}

// Open loads a persisted online cache and ip2region xdb files when present.
func Open(dir string) *Lookup {
	l := &Lookup{
		dir:           dir,
		path:          filepath.Join(dir, "ip2region_v4.xdb"),
		pathV6:        filepath.Join(dir, "ip2region_v6.xdb"),
		mem:           map[string]Info{},
		disk:          loadDisk(dir),
		http:          &http.Client{Timeout: 2 * time.Second},
		disableOnline: true,
	}
	_ = l.tryLoadV4()
	_ = l.tryLoadV6()
	return l
}

// SetOnlineEnabled controls third-party queries. Existing cached labels remain
// available locally. Call before serving requests.
func (l *Lookup) SetOnlineEnabled(enabled bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.disableOnline = !enabled
}

// Ensure downloads the city databases if missing. Safe to call in the background.
func (l *Lookup) Ensure(ctx context.Context) error {
	if l == nil {
		return nil
	}
	err4 := l.ensureXDB(ctx, l.path, mirrorsV4, l.tryLoadV4)
	_ = l.ensureXDB(ctx, l.pathV6, mirrorsV6, l.tryLoadV6)
	return err4
}

func (l *Lookup) ensureXDB(ctx context.Context, path string, mirrors []string, load func() error) error {
	l.mu.Lock()
	ready := false
	if path == l.path {
		ready = l.searcher != nil
	} else {
		ready = l.searcherV6 != nil
	}
	l.mu.Unlock()
	if ready {
		return nil
	}
	if st, err := os.Stat(path); err == nil && st.Size() >= minXDBSize {
		return load()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	var last error
	for _, u := range mirrors {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := download(ctx, u, path); err != nil {
			last = err
			continue
		}
		if err := load(); err != nil {
			_ = os.Remove(path)
			last = err
			continue
		}
		return nil
	}
	return last
}

func (l *Lookup) tryLoadV4() error {
	return l.loadXDB(l.path, xdb.IPv4, validIPv4XDB, func(s *xdb.Searcher) {
		if l.searcher != nil {
			l.searcher.Close()
		}
		l.searcher = s
	})
}

func (l *Lookup) tryLoadV6() error {
	return l.loadXDB(l.pathV6, xdb.IPv6, validIPv6XDB, func(s *xdb.Searcher) {
		if l.searcherV6 != nil {
			l.searcherV6.Close()
		}
		l.searcherV6 = s
	})
}

func (l *Lookup) loadXDB(path string, ver *xdb.Version, valid func([]byte) error, set func(*xdb.Searcher)) error {
	cBuff, err := xdb.LoadContentFromFile(path)
	if err != nil {
		return err
	}
	if err := valid(cBuff); err != nil {
		return err
	}
	s, err := xdb.NewWithBuffer(ver, cBuff)
	if err != nil {
		return err
	}
	l.mu.Lock()
	set(s)
	l.mu.Unlock()
	return nil
}

func validIPv4XDB(buf []byte) error {
	if len(buf) < minXDBSize {
		return fmt.Errorf("xdb too small: %d", len(buf))
	}
	h, err := xdb.LoadHeaderFromBuff(buf)
	if err != nil {
		return err
	}
	if h.IPVersion != 0 && h.IPVersion != xdb.IPv4VersionNo {
		return fmt.Errorf("xdb ip version %d, want IPv4", h.IPVersion)
	}
	return nil
}

func validIPv6XDB(buf []byte) error {
	if len(buf) < minXDBSize {
		return fmt.Errorf("xdb too small: %d", len(buf))
	}
	h, err := xdb.LoadHeaderFromBuff(buf)
	if err != nil {
		return err
	}
	if h.IPVersion != xdb.IPv6VersionNo {
		return fmt.Errorf("xdb ip version %d, want IPv6", h.IPVersion)
	}
	return nil
}

// Ready is true when lookups can return a city.
func (l *Lookup) Ready() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.searcher != nil
}

// Warm prefetches public IPs (online, then offline) so the page does not stall per row.
func (l *Lookup) Warm(ctx context.Context, ips []string) {
	if l == nil {
		return
	}
	seen := map[string]struct{}{}
	var need []string
	for _, ip := range ips {
		ip = strings.TrimSpace(ip)
		if ip == "" {
			continue
		}
		if _, ok := seen[ip]; ok {
			continue
		}
		seen[ip] = struct{}{}
		if l.cached(ip) {
			continue
		}
		need = append(need, ip)
	}
	if len(need) == 0 {
		return
	}
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for _, ip := range need {
		if err := ctx.Err(); err != nil {
			break
		}
		wg.Add(1)
		go func(ip string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			_ = l.lookup(ctx, ip)
		}(ip)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// Find returns a label for ip, or nil when unknown / not a public address / db missing.
func (l *Lookup) Find(ip string) *Info {
	return l.lookup(context.Background(), ip)
}

func (l *Lookup) cached(ip string) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if info, ok := l.mem[ip]; ok {
		return info.Label != ""
	}
	if item, ok := l.disk[ip]; ok && item.Info.Label != "" && !item.expired() {
		return true
	}
	return false
}

func (l *Lookup) lookup(ctx context.Context, ip string) *Info {
	ip = strings.TrimSpace(ip)
	if l == nil || ip == "" {
		return nil
	}
	parsed := net.ParseIP(ip)
	if parsed == nil || !publicIP(parsed) {
		return nil
	}
	l.mu.Lock()
	if info, ok := l.mem[ip]; ok {
		l.mu.Unlock()
		if info.Label == "" {
			return nil
		}
		return &info
	}
	if item, ok := l.disk[ip]; ok && item.Info.Label != "" && !item.expired() {
		info := item.Info
		l.mem[ip] = info
		l.mu.Unlock()
		return &info
	}
	stale := Info{}
	if item, ok := l.disk[ip]; ok {
		stale = item.Info
	}
	onlineEnabled := !l.disableOnline
	l.mu.Unlock()

	if onlineEnabled {
		if info, ok := l.fetchOnline(ctx, ip); ok {
			l.store(ip, info, true)
			return &info
		}
	}
	if stale.Label != "" {
		l.store(ip, stale, false)
		return &stale
	}
	info := l.fromXDB(ip, parsed)
	l.store(ip, info, false)
	if info.Label == "" {
		return nil
	}
	return &info
}

func (l *Lookup) fromXDB(ip string, parsed net.IP) Info {
	l.mu.Lock()
	defer l.mu.Unlock()
	query := ip
	s := l.searcherV6
	if v4 := parsed.To4(); v4 != nil {
		s = l.searcher
		query = v4.String()
	} else {
		query = parsed.String()
	}
	if s == nil {
		return Info{}
	}
	region, err := s.Search(query)
	if err != nil {
		return Info{}
	}
	return ParseRegion(region)
}

func (l *Lookup) store(ip string, info Info, persist bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.mem) > 20000 {
		l.mem = map[string]Info{}
	}
	l.mem[ip] = info
	if persist && info.Label != "" {
		if l.disk == nil {
			l.disk = map[string]cacheItem{}
		}
		l.disk[ip] = cacheItem{Info: info, At: time.Now().Unix()}
		saveDisk(l.dir, l.disk)
	}
}

func publicIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() {
		return false
	}
	return true
}

// ParseRegion turns an official ip2region v4 line into Info.
// Format: Country|Province|City|ISP|ISO. Missing fields are empty or "0".
// Mobile / cable cities are dropped: those allocations wander and look like random jumps.
func ParseRegion(s string) Info {
	parts := strings.Split(s, "|")
	clean := func(i int) string {
		if i >= len(parts) {
			return ""
		}
		return tidyPlace(parts[i])
	}
	info := Info{Country: clean(0), Province: clean(1), City: clean(2), ISP: clean(3)}
	return compose(info, true)
}

func compose(info Info, dropUnreliableCity bool) Info {
	info.Country = tidyPlace(info.Country)
	info.Province = trimAdmin(tidyPlace(info.Province))
	info.City = trimAdmin(tidyPlace(info.City))
	info.District = trimAdmin(tidyPlace(info.District))
	info.ISP = normalizeISP(tidyPlace(info.ISP))
	if info.City == info.Province {
		info.City = ""
	}
	if info.District == info.City || info.District == info.Province {
		info.District = ""
	}
	if dropUnreliableCity && cityUnreliable(info.ISP) {
		info.City = ""
		info.District = ""
	}
	if info.Country != "" && info.Country != "中国" {
		info.City = ""
		info.District = ""
	}
	if !hasPlace(info) {
		return Info{}
	}
	var loc []string
	if info.Country != "" && info.Country != "中国" {
		loc = append(loc, info.Country)
	}
	if info.Province != "" {
		loc = append(loc, info.Province)
	}
	if info.City != "" {
		loc = append(loc, info.City)
	}
	if info.District != "" {
		loc = append(loc, info.District)
	}
	label := strings.Join(loc, " ")
	if info.ISP != "" {
		if label == "" {
			return Info{}
		}
		label += " · " + info.ISP
	}
	info.Label = label
	return info
}

func normalizeISP(s string) string {
	repl := []struct{ old, neu string }{
		{"移通", "移动"},
		{"中国移动", "移动"},
		{"China Mobile communications corporation", "移动"},
		{"China Mobile", "移动"},
		{"中国电信", "电信"},
		{"China Telecom", "电信"},
		{"China Networks Inter-Exchange", "科技网"},
		{"中国联通", "联通"},
		{"China Unicom", "联通"},
	}
	for _, r := range repl {
		if strings.EqualFold(s, r.old) || s == r.old {
			return r.neu
		}
	}
	return s
}

func hasPlace(info Info) bool {
	if info.Country == "中国" {
		return info.Province != "" || info.City != ""
	}
	return info.Country != "" || info.Province != ""
}

func cityUnreliable(isp string) bool {
	for _, k := range []string{"移动", "广电", "铁通", "CMCC", "China Mobile"} {
		if strings.Contains(isp, k) {
			return true
		}
	}
	return false
}

func tidyPlace(s string) string {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "", "0", "未知", "unknown", "内网ip", "局域网", "保留地址", "本机地址", "zz":
		return ""
	}
	return s
}

func trimAdmin(s string) string {
	for _, suf := range []string{"维吾尔自治区", "壮族自治区", "回族自治区", "自治区", "特别行政区", "省", "市", "区"} {
		if strings.HasSuffix(s, suf) && len([]rune(s)) > len([]rune(suf)) {
			return strings.TrimSuffix(s, suf)
		}
	}
	return s
}

func download(ctx context.Context, url, dest string) error {
	tmp := dest + ".tmp"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, 64<<20))
	cerr := f.Close()
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if cerr != nil {
		_ = os.Remove(tmp)
		return cerr
	}
	if n < minXDBSize {
		_ = os.Remove(tmp)
		return fmt.Errorf("download %s: file too small (%d)", url, n)
	}
	return os.Rename(tmp, dest)
}
