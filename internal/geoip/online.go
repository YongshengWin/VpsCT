package geoip

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
)

const (
	cacheName = "online-cache.json"
	cacheTTL  = 30 * 24 * time.Hour
)

type cacheItem struct {
	Info
	At int64 `json:"at"`
}

func (c cacheItem) expired() bool {
	if c.At <= 0 {
		return true
	}
	return time.Since(time.Unix(c.At, 0)) > cacheTTL
}

type cacheFile struct {
	Items map[string]cacheItem `json:"items"`
}

func loadDisk(dir string) map[string]cacheItem {
	b, err := os.ReadFile(filepath.Join(dir, cacheName))
	if err != nil {
		return map[string]cacheItem{}
	}
	var f cacheFile
	if json.Unmarshal(b, &f) != nil || f.Items == nil {
		return map[string]cacheItem{}
	}
	return f.Items
}

func saveDisk(dir string, items map[string]cacheItem) {
	if dir == "" {
		return
	}
	_ = os.MkdirAll(dir, 0o750)
	b, err := json.Marshal(cacheFile{Items: items})
	if err != nil {
		return
	}
	tmp := filepath.Join(dir, cacheName+".tmp")
	if os.WriteFile(tmp, b, 0o640) != nil {
		return
	}
	_ = os.Rename(tmp, filepath.Join(dir, cacheName))
}

func (l *Lookup) fetchOnline(ctx context.Context, ip string) (Info, bool) {
	if l.http == nil {
		return Info{}, false
	}
	if info, ok := lookupPconline(ctx, l.http, ip); ok {
		return info, true
	}
	if info, ok := lookupIPAPI(ctx, l.http, ip); ok {
		return info, true
	}
	return Info{}, false
}

type pconlineResp struct {
	Pro    string `json:"pro"`
	City   string `json:"city"`
	Region string `json:"region"`
	Addr   string `json:"addr"`
	Err    string `json:"err"`
}

func lookupPconline(ctx context.Context, client *http.Client, ip string) (Info, bool) {
	u := "https://whois.pconline.com.cn/ipJson.jsp?json=true&ip=" + url.QueryEscape(ip)
	body, err := getBody(ctx, client, u)
	if err != nil {
		return Info{}, false
	}
	var r pconlineResp
	if err := decodeMaybeGBK(body, &r); err != nil || strings.TrimSpace(r.Err) != "" {
		return Info{}, false
	}
	out := parsePconline(r)
	return out, out.Label != ""
}

func parsePconline(r pconlineResp) Info {
	info := Info{Province: r.Pro, City: r.City, District: r.Region, ISP: ispFromAddr(r.Addr)}
	if chinaAdmin(info.Province) || chinaAdmin(info.City) {
		info.Country = "中国"
	} else if info.Province == "" && info.City == "" {
		info.Country = firstToken(r.Addr)
	} else {
		info.Country = firstToken(r.Addr)
		if info.Country == "" {
			info.Country = info.Province
			info.Province = ""
		}
	}
	out := compose(info, false)
	if out.Label == "" {
		return Info{}
	}
	return out
}

func ispFromAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	parts := strings.Fields(addr)
	if len(parts) == 0 {
		return ""
	}
	last := parts[len(parts)-1]
	if chinaAdmin(last) || last == "美国" || last == "日本" || last == "韩国" {
		return ""
	}
	return last
}

func firstToken(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if i := strings.IndexAny(addr, " \t"); i > 0 {
		return addr[:i]
	}
	return addr
}

func chinaAdmin(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	for _, suf := range []string{"省", "市", "自治区", "特别行政区", "区", "州"} {
		if strings.HasSuffix(s, suf) {
			return true
		}
	}
	switch trimAdmin(s) {
	case "北京", "上海", "天津", "重庆", "香港", "澳门", "台湾":
		return true
	}
	return false
}

type ipAPIResp struct {
	Status     string `json:"status"`
	Country    string `json:"country"`
	RegionName string `json:"regionName"`
	City       string `json:"city"`
	ISP        string `json:"isp"`
}

func lookupIPAPI(ctx context.Context, client *http.Client, ip string) (Info, bool) {
	u := "http://ip-api.com/json/" + url.PathEscape(ip) + "?lang=zh-CN&fields=status,country,regionName,city,isp"
	body, err := getBody(ctx, client, u)
	if err != nil {
		return Info{}, false
	}
	var r ipAPIResp
	if json.Unmarshal(body, &r) != nil || r.Status != "success" {
		return Info{}, false
	}
	out := compose(Info{Country: r.Country, Province: r.RegionName, City: r.City, ISP: r.ISP}, false)
	return out, out.Label != ""
}

func getBody(ctx context.Context, client *http.Client, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; VpsCT/1.0)")
	req.Header.Set("Accept", "application/json,text/plain,*/*")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

func decodeMaybeGBK(body []byte, v any) error {
	if json.Unmarshal(body, v) == nil {
		return nil
	}
	utf8, err := simplifiedchinese.GBK.NewDecoder().Bytes(body)
	if err != nil {
		return err
	}
	return json.Unmarshal(utf8, v)
}
