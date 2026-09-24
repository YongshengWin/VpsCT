// Package corecatalog lists installable sing-box / snell-server versions
// from upstream (GitHub + Surge CDN), cached for a few hours.
package corecatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"ctlvps/internal/desired"
	"ctlvps/internal/domain"
)

const cacheTTL = 6 * time.Hour

// Version is one selectable core release.
type Version struct {
	Version     string `json:"version"`
	Latest      bool   `json:"latest,omitempty"`
	Prerelease  bool   `json:"prerelease,omitempty"`
	PublishedAt string `json:"published_at,omitempty"`
}

// Channel is one core's dropdown data.
type Channel struct {
	Default  string    `json:"default"`
	Latest   string    `json:"latest"`
	Versions []Version `json:"versions"`
	Source   string    `json:"source"`
	Error    string    `json:"error,omitempty"`
}

// Catalog is the settings dropdown payload.
type Catalog struct {
	SingBox Channel `json:"singbox"`
	Snell   Channel `json:"snell"`
	Mita    Channel `json:"mita"`
}

// Fetcher pulls and caches upstream version lists.
type Fetcher struct {
	Client *http.Client
	Now    func() time.Time

	mu    sync.Mutex
	cache *Catalog
	until time.Time
}

func (f *Fetcher) client() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	return &http.Client{Timeout: 12 * time.Second}
}

func (f *Fetcher) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

// Get returns a cached catalog, refreshing when stale.
func (f *Fetcher) Get(ctx context.Context) Catalog {
	f.mu.Lock()
	if f.cache != nil && f.now().Before(f.until) {
		out := *f.cache
		f.mu.Unlock()
		return out
	}
	f.mu.Unlock()

	out := Catalog{
		SingBox: f.singBox(ctx),
		Snell:   f.snell(ctx),
		Mita:    f.mita(ctx),
	}
	f.mu.Lock()
	f.cache = &out
	f.until = f.now().Add(cacheTTL)
	f.mu.Unlock()
	return out
}

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
}

func (f *Fetcher) singBox(ctx context.Context) Channel {
	ch := Channel{Default: desired.DefaultSingBoxVersion, Source: "github.com/SagerNet/sing-box"}
	rels, err := f.githubReleases(ctx, "SagerNet/sing-box")
	if err != nil {
		ch.Error = err.Error()
		ch.Versions = fallbackSingBox()
		if len(ch.Versions) > 0 {
			ch.Latest = ch.Versions[0].Version
			ch.Versions[0].Latest = true
		}
		return ch
	}
	var stables, pres []Version
	for _, r := range rels {
		if r.Draft {
			continue
		}
		ver := strings.TrimPrefix(r.TagName, "v")
		if ver == "" {
			continue
		}
		item := Version{Version: ver, Prerelease: r.Prerelease, PublishedAt: r.PublishedAt.UTC().Format("2006-01-02")}
		if r.Prerelease {
			pres = append(pres, item)
		} else {
			stables = append(stables, item)
		}
	}
	if len(stables) > 12 {
		stables = stables[:12]
	}
	if len(stables) > 0 {
		stables[0].Latest = true
		ch.Latest = stables[0].Version
	}
	out := append([]Version{}, stables...)
	if len(pres) > 0 && (len(stables) == 0 || pres[0].Version != stables[0].Version) {
		p := pres[0]
		p.Latest = len(stables) == 0
		out = append(out, p)
		if ch.Latest == "" {
			ch.Latest = p.Version
		}
	}
	ch.Versions = ensureDefault(out, ch.Default)
	return ch
}

func (f *Fetcher) githubReleases(ctx context.Context, repo string) ([]ghRelease, error) {
	var all []ghRelease
	for page := 1; page <= 2; page++ {
		url := fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=30&page=%d", repo, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "ctlvps")
		res, err := f.client().Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		if res.StatusCode != 200 {
			return nil, fmt.Errorf("GitHub %d", res.StatusCode)
		}
		var batch []ghRelease
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		all = append(all, batch...)
	}
	return all, nil
}

func (f *Fetcher) mita(ctx context.Context) Channel {
	ch := Channel{Default: domain.DefaultMitaVersion, Source: "github.com/enfein/mieru"}
	releases, err := f.githubReleases(ctx, "enfein/mieru")
	if err != nil {
		ch.Error = err.Error()
	}
	for _, r := range releases {
		if r.Draft || r.Prerelease {
			continue
		}
		v := strings.TrimPrefix(r.TagName, "v")
		if compareVer(v, domain.DefaultMitaVersion) < 0 {
			continue
		}
		ch.Versions = append(ch.Versions, Version{Version: v, PublishedAt: r.PublishedAt.UTC().Format("2006-01-02")})
		if len(ch.Versions) >= 12 {
			break
		}
	}
	ch.Versions = ensureDefault(ch.Versions, ch.Default)
	if len(ch.Versions) > 0 {
		ch.Latest = ch.Versions[0].Version
		ch.Versions[0].Latest = true
	}
	return ch
}

var snellKnown = []string{"5.0.1", "5.0.0", "4.1.1", "4.1.0", "4.0.1"}

func (f *Fetcher) snell(ctx context.Context) Channel {
	ch := Channel{Default: desired.DefaultSnellVersion, Source: "dl.nssurge.com/snell"}
	seen := map[string]bool{}
	var found []string
	candidates := append([]string{}, snellKnown...)
	for _, extra := range []string{"5.0.2", "5.0.3", "5.1.0", "5.1.1", "5.2.0", "6.0.0"} {
		candidates = append(candidates, extra)
	}
	type hit struct {
		ver string
		ok  bool
	}
	chc := make(chan hit, len(candidates))
	for _, ver := range candidates {
		go func(ver string) {
			ok := f.snellExists(ctx, ver)
			chc <- hit{ver, ok}
		}(ver)
	}
	for range candidates {
		h := <-chc
		if h.ok && !seen[h.ver] {
			seen[h.ver] = true
			found = append(found, h.ver)
		}
	}
	if len(found) == 0 {
		found = append([]string{}, snellKnown...)
		ch.Error = "未能探测 Surge CDN，已用已知版本"
	}
	sort.Slice(found, func(i, j int) bool { return compareVer(found[i], found[j]) > 0 })
	var vers []Version
	for i, v := range found {
		vers = append(vers, Version{Version: v, Latest: i == 0})
	}
	if len(vers) > 0 {
		ch.Latest = vers[0].Version
	}
	ch.Versions = ensureDefault(vers, ch.Default)
	return ch
}

func (f *Fetcher) snellExists(ctx context.Context, ver string) bool {
	url := fmt.Sprintf("https://dl.nssurge.com/snell/snell-server-v%s-linux-amd64.zip", ver)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "ctlvps")
	res, err := f.client().Do(req)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	return res.StatusCode == 200
}

func ensureDefault(vers []Version, def string) []Version {
	if def == "" {
		return vers
	}
	for _, v := range vers {
		if v.Version == def {
			return vers
		}
	}
	return append(vers, Version{Version: def})
}

func fallbackSingBox() []Version {
	return []Version{
		{Version: "1.14.1"},
		{Version: "1.14.0"},
		{Version: "1.13.21"},
		{Version: desired.DefaultSingBoxVersion},
		{Version: "1.11.15"},
	}
}

func compareVer(a, b string) int {
	as, bs := verParts(a), verParts(b)
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		var x, y int
		if i < len(as) {
			x = as[i]
		}
		if i < len(bs) {
			y = bs[i]
		}
		if x != y {
			return x - y
		}
	}
	return 0
}

func verParts(s string) []int {
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	var out []int
	for _, p := range strings.Split(s, ".") {
		n := 0
		for _, c := range p {
			if c < '0' || c > '9' {
				break
			}
			n = n*10 + int(c-'0')
		}
		out = append(out, n)
	}
	return out
}
