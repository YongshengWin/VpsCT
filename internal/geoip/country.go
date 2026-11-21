package geoip

import (
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"math/bits"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	CountryProviderDBIP    = "dbip-country-lite"
	CountryProviderMaxMind = "maxmind-geolite2-country"
	maxCountryDownload     = 64 << 20
	maxCountryUncompressed = 256 << 20
	maxGeoPrefixes         = 200000
)

type CountrySourceConfig struct {
	Provider       string
	MaxMindAccount string
	MaxMindKey     string
}

type CountryOption struct {
	Code string `json:"code"`
	Name string `json:"name,omitempty"`
}

// CountryRanges is the controller-materialized address set used by an
// ingress policy. It contains no client IP observations.
type CountryRanges struct {
	Provider string
	Version  string
	SHA256   string
	IPv4     []string
	IPv6     []string
}

type CountryManager struct {
	dir    string
	client *http.Client
	mu     sync.Mutex
	cache  map[string]map[string]CountryRanges
	source CountrySourceConfig
}

// OpenCountry creates the country-range GeoIP manager. Provider URLs are
// constructed in code; callers cannot submit arbitrary download URLs.
func OpenCountry(dir string) *CountryManager {
	return &CountryManager{dir: dir, client: &http.Client{Timeout: 2 * time.Minute}, cache: map[string]map[string]CountryRanges{}, source: CountrySourceConfig{Provider: CountryProviderDBIP}}
}

// CountryProvider exposes the data source used in API responses and audit
// records without exporting the manager's mutable state.
func CountryProvider() string { return CountryProviderDBIP }

func (m *CountryManager) SetSource(config CountrySourceConfig) error {
	if m == nil {
		return errors.New("GeoIP 数据源未配置")
	}
	if config.Provider == "" {
		config.Provider = CountryProviderDBIP
	}
	if err := ValidateCountrySource(config); err != nil {
		return err
	}
	m.mu.Lock()
	if m.source != config {
		m.cache = map[string]map[string]CountryRanges{}
	}
	m.source = config
	m.mu.Unlock()
	return nil
}

func ValidateCountrySource(config CountrySourceConfig) error {
	if config.Provider == "" {
		config.Provider = CountryProviderDBIP
	}
	if config.Provider != CountryProviderDBIP && config.Provider != CountryProviderMaxMind {
		return fmt.Errorf("不支持的 GeoIP 数据源：%s", config.Provider)
	}
	if config.Provider == CountryProviderMaxMind && (strings.TrimSpace(config.MaxMindAccount) == "" || strings.TrimSpace(config.MaxMindKey) == "") {
		return errors.New("MaxMind GeoLite2 需要 Account ID 和 License Key")
	}
	return nil
}

func (m *CountryManager) Source() CountrySourceConfig {
	if m == nil {
		return CountrySourceConfig{Provider: CountryProviderDBIP}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.source
}

func (m *CountryManager) Countries(ctx context.Context) ([]CountryOption, error) {
	if m == nil {
		return nil, errors.New("GeoIP 数据源未配置")
	}
	source := m.Source()
	version := time.Now().UTC().Format("2006-01")
	codes := map[string]bool{}
	if source.Provider == CountryProviderMaxMind {
		path, err := m.ensureMaxMind(ctx, version, source)
		if err != nil {
			return nil, err
		}
		st, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		zr, err := zip.NewReader(f, st.Size())
		if err != nil {
			return nil, errors.New("MaxMind GeoLite2 压缩包无效")
		}
		locations := map[string]string{}
		for _, entry := range zr.File {
			if strings.Contains(filepath.Base(entry.Name), "Country-Locations-en.csv") {
				if err := readMaxMindLocations(entry, locations); err != nil {
					return nil, err
				}
			}
		}
		for _, code := range locations {
			if len(code) == 2 {
				codes[code] = true
			}
		}
	} else {
		path, err := m.ensure(ctx, version)
		if err != nil {
			return nil, err
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, errors.New("GeoIP gzip 文件无效")
		}
		r := csv.NewReader(io.LimitReader(gz, maxCountryUncompressed))
		for {
			row, err := r.Read()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				_ = gz.Close()
				return nil, err
			}
			if len(row) >= 3 && strings.EqualFold(row[0], "ip_start") {
				continue
			}
			if len(row) >= 3 {
				code := strings.ToUpper(strings.TrimSpace(row[2]))
				if len(code) == 2 {
					codes[code] = true
				}
			}
		}
		_ = gz.Close()
	}
	list := make([]CountryOption, 0, len(codes))
	for code := range codes {
		list = append(list, CountryOption{Code: code})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Code < list[j].Code })
	return list, nil
}

func (m *CountryManager) Materialize(ctx context.Context, countries []string, version string) (CountryRanges, error) {
	if m == nil {
		return CountryRanges{}, errors.New("GeoIP 数据源未配置")
	}
	selected := normalizeCountries(countries)
	if len(selected) == 0 {
		return CountryRanges{}, errors.New("至少选择一个国家或地区")
	}
	if version == "" {
		version = time.Now().UTC().Format("2006-01")
	}
	key := version + ":" + strings.Join(selected, ",")
	m.mu.Lock()
	if value, ok := m.cache[version][key]; ok {
		m.mu.Unlock()
		return value, nil
	}
	m.mu.Unlock()

	source := m.Source()
	var ranges CountryRanges
	var sum string
	var err error
	if source.Provider == CountryProviderMaxMind {
		ranges, sum, err = m.materializeMaxMind(ctx, version, selected, source)
	} else {
		path, e := m.ensure(ctx, version)
		if e != nil {
			return CountryRanges{}, e
		}
		ranges, sum, err = parseCountryFile(path, selected)
	}
	if err != nil {
		return CountryRanges{}, err
	}
	ranges.Provider, ranges.Version, ranges.SHA256 = source.Provider, version, sum
	if len(ranges.IPv4) > maxGeoPrefixes || len(ranges.IPv6) > maxGeoPrefixes {
		return CountryRanges{}, fmt.Errorf("GeoIP 地址集合过大：IPv4 %d、IPv6 %d", len(ranges.IPv4), len(ranges.IPv6))
	}
	m.mu.Lock()
	if m.cache[version] == nil {
		m.cache[version] = map[string]CountryRanges{}
	}
	m.cache[version][key] = ranges
	m.mu.Unlock()
	return ranges, nil
}

func normalizeCountries(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToUpper(strings.TrimSpace(value))
		if len(value) == 2 && value[0] >= 'A' && value[0] <= 'Z' && value[1] >= 'A' && value[1] <= 'Z' && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func (m *CountryManager) ensure(ctx context.Context, version string) (string, error) {
	if err := os.MkdirAll(m.dir, 0o750); err != nil {
		return "", err
	}
	path := filepath.Join(m.dir, "dbip-country-lite-"+version+".csv.gz")
	if st, err := os.Stat(path); err == nil && st.Size() > 0 && st.Size() <= maxCountryDownload {
		return path, nil
	}
	url := "https://download.db-ip.com/free/dbip-country-lite-" + version + ".csv.gz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GeoIP 数据源返回 HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxCountryDownload {
		return "", errors.New("GeoIP 下载文件超过大小上限")
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	_, copyErr := io.CopyN(f, resp.Body, maxCountryDownload+1)
	closeErr := f.Close()
	if copyErr != nil && !errors.Is(copyErr, io.EOF) {
		_ = os.Remove(tmp)
		return "", copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return "", closeErr
	}
	st, err := os.Stat(tmp)
	if err != nil || st.Size() == 0 || st.Size() > maxCountryDownload {
		_ = os.Remove(tmp)
		return "", errors.New("GeoIP 下载文件大小无效")
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return path, nil
}

func (m *CountryManager) ensureMaxMind(ctx context.Context, version string, source CountrySourceConfig) (string, error) {
	if err := os.MkdirAll(m.dir, 0o750); err != nil {
		return "", err
	}
	path := filepath.Join(m.dir, "maxmind-geolite2-country-"+version+".zip")
	if st, err := os.Stat(path); err == nil && st.Size() > 0 && st.Size() <= maxCountryDownload {
		return path, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://download.maxmind.com/geoip/databases/GeoLite2-Country-CSV/download?suffix=zip", nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(strings.TrimSpace(source.MaxMindAccount), strings.TrimSpace(source.MaxMindKey))
	resp, err := m.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("MaxMind 数据源返回 HTTP %d", resp.StatusCode)
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(f, io.LimitReader(resp.Body, maxCountryDownload+1))
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(tmp)
		if copyErr != nil {
			return "", copyErr
		}
		return "", closeErr
	}
	st, err := os.Stat(tmp)
	if err != nil || st.Size() == 0 || st.Size() > maxCountryDownload {
		_ = os.Remove(tmp)
		return "", errors.New("MaxMind 下载文件大小无效")
	}
	if err := validateMaxMindArchive(tmp); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return path, nil
}

func validateMaxMindArchive(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := zip.NewReader(f, st.Size())
	if err != nil {
		return errors.New("MaxMind GeoLite2 压缩包无效")
	}
	locations, blocks4, blocks6 := false, false, false
	for _, entry := range zr.File {
		name := filepath.Base(entry.Name)
		locations = locations || strings.Contains(name, "Country-Locations-en.csv")
		blocks4 = blocks4 || strings.Contains(name, "Country-Blocks-IPv4.csv")
		blocks6 = blocks6 || strings.Contains(name, "Country-Blocks-IPv6.csv")
	}
	if !locations || !blocks4 || !blocks6 {
		return errors.New("MaxMind 压缩包缺少 Country CSV 文件")
	}
	return nil
}

func (m *CountryManager) materializeMaxMind(ctx context.Context, version string, selected []string, source CountrySourceConfig) (CountryRanges, string, error) {
	path, err := m.ensureMaxMind(ctx, version, source)
	if err != nil {
		return CountryRanges{}, "", err
	}
	st, err := os.Stat(path)
	if err != nil {
		return CountryRanges{}, "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return CountryRanges{}, "", err
	}
	defer f.Close()
	zr, err := zip.NewReader(f, st.Size())
	if err != nil {
		return CountryRanges{}, "", errors.New("MaxMind GeoLite2 压缩包无效")
	}
	wanted := map[string]bool{}
	for _, country := range normalizeCountries(selected) {
		wanted[country] = true
	}
	locations := map[string]string{}
	var blocks4, blocks6 *zip.File
	for _, entry := range zr.File {
		name := filepath.Base(entry.Name)
		switch {
		case strings.Contains(name, "Country-Locations-en.csv"):
			if err := readMaxMindLocations(entry, locations); err != nil {
				return CountryRanges{}, "", err
			}
		case strings.Contains(name, "Country-Blocks-IPv4.csv"):
			blocks4 = entry
		case strings.Contains(name, "Country-Blocks-IPv6.csv"):
			blocks6 = entry
		}
	}
	result := CountryRanges{}
	for _, entry := range []*zip.File{blocks4, blocks6} {
		if entry == nil {
			continue
		}
		ipv4, err := readMaxMindBlocks(entry, locations, wanted, &result)
		if err != nil {
			return CountryRanges{}, "", err
		}
		if ipv4 && len(result.IPv4) > maxGeoPrefixes || !ipv4 && len(result.IPv6) > maxGeoPrefixes {
			return CountryRanges{}, "", errors.New("所选国家的 GeoIP 地址集合超过大小上限")
		}
	}
	sum, err := fileSHA256(path)
	return result, sum, err
}

func readMaxMindLocations(entry *zip.File, locations map[string]string) error {
	f, err := entry.Open()
	if err != nil {
		return err
	}
	defer f.Close()
	r := csv.NewReader(io.LimitReader(f, maxCountryUncompressed))
	header, err := r.Read()
	if err != nil {
		return err
	}
	indices := csvIndices(header)
	idIndex, countryIndex := indices["geoname_id"], indices["country_iso_code"]
	if idIndex < 0 || countryIndex < 0 {
		return errors.New("MaxMind locations CSV 缺少必要字段")
	}
	for {
		row, err := r.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if idIndex >= len(row) || countryIndex >= len(row) {
			return errors.New("MaxMind locations CSV 字段数量无效")
		}
		id := strings.TrimSpace(row[idIndex])
		country := strings.ToUpper(strings.TrimSpace(row[countryIndex]))
		if id != "" && country != "" {
			locations[id] = country
		}
	}
}

func readMaxMindBlocks(entry *zip.File, locations map[string]string, wanted map[string]bool, result *CountryRanges) (bool, error) {
	f, err := entry.Open()
	if err != nil {
		return false, err
	}
	defer f.Close()
	r := csv.NewReader(io.LimitReader(f, maxCountryUncompressed))
	header, err := r.Read()
	if err != nil {
		return false, err
	}
	indices := csvIndices(header)
	networkIndex := indices["network"]
	if networkIndex < 0 {
		return false, errors.New("MaxMind blocks CSV 缺少 network 字段")
	}
	is4 := strings.Contains(filepath.Base(entry.Name), "IPv4")
	for {
		row, err := r.Read()
		if errors.Is(err, io.EOF) {
			return is4, nil
		}
		if err != nil {
			return is4, err
		}
		if networkIndex >= len(row) {
			return is4, errors.New("MaxMind blocks CSV 字段数量无效")
		}
		country := ""
		for _, key := range []string{"geoname_id", "registered_country_geoname_id", "represented_country_geoname_id"} {
			if index := indices[key]; index >= 0 && index < len(row) {
				if value := locations[strings.TrimSpace(row[index])]; value != "" {
					country = value
					break
				}
			}
		}
		if !wanted[country] {
			continue
		}
		prefix, err := netip.ParsePrefix(strings.TrimSpace(row[networkIndex]))
		if err != nil {
			return is4, errors.New("MaxMind blocks CSV 网络前缀无效")
		}
		if prefix.Addr().Is4() {
			result.IPv4 = append(result.IPv4, prefix.String())
		} else {
			result.IPv6 = append(result.IPv6, prefix.String())
		}
	}
}

func csvIndices(header []string) map[string]int {
	indices := map[string]int{}
	for i, name := range header {
		indices[strings.ToLower(strings.TrimSpace(name))] = i
	}
	for _, name := range []string{"network", "geoname_id", "registered_country_geoname_id", "represented_country_geoname_id", "country_iso_code"} {
		if _, ok := indices[name]; !ok {
			indices[name] = -1
		}
	}
	return indices
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func parseCountryFile(path string, selected []string) (CountryRanges, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return CountryRanges{}, "", err
	}
	defer f.Close()
	h := sha256.New()
	tee := io.TeeReader(f, h)
	gz, err := gzip.NewReader(tee)
	if err != nil {
		return CountryRanges{}, "", errors.New("GeoIP gzip 文件无效")
	}
	defer gz.Close()
	r := csv.NewReader(io.LimitReader(gz, maxCountryUncompressed))
	r.FieldsPerRecord = 3
	wanted := map[string]bool{}
	for _, country := range normalizeCountries(selected) {
		wanted[country] = true
	}
	out := CountryRanges{}
	for {
		row, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return CountryRanges{}, "", err
		}
		if strings.EqualFold(row[0], "ip_start") {
			continue
		}
		country := strings.ToUpper(strings.TrimSpace(row[2]))
		if !wanted[country] {
			continue
		}
		start, err1 := netip.ParseAddr(strings.TrimSpace(row[0]))
		end, err2 := netip.ParseAddr(strings.TrimSpace(row[1]))
		if err1 != nil || err2 != nil || start.Is4() != end.Is4() || start.Compare(end) > 0 {
			return CountryRanges{}, "", errors.New("GeoIP 地址范围无效")
		}
		prefixes, err := rangePrefixes(start, end)
		if err != nil {
			return CountryRanges{}, "", err
		}
		if start.Is4() {
			out.IPv4 = append(out.IPv4, prefixes...)
		} else {
			out.IPv6 = append(out.IPv6, prefixes...)
		}
		if len(out.IPv4) > maxGeoPrefixes || len(out.IPv6) > maxGeoPrefixes {
			return CountryRanges{}, "", errors.New("所选国家的 GeoIP 地址集合超过大小上限")
		}
	}
	return out, hex.EncodeToString(h.Sum(nil)), nil
}

func rangePrefixes(start, end netip.Addr) ([]string, error) {
	if start.Is4() {
		return rangePrefixes4(start, end), nil
	}
	return rangePrefixes6(start, end), nil
}

func rangePrefixes4(start, end netip.Addr) []string {
	a, b := binary4(start), binary4(end)
	out := []string{}
	for a <= b {
		alignment := uint(bits.TrailingZeros32(a))
		remainingCount := uint64(b) - uint64(a) + 1
		remaining := uint(bits.Len64(remainingCount) - 1)
		block := alignment
		if remaining < block {
			block = remaining
		}
		prefixLen := 32 - int(block)
		out = append(out, netip.PrefixFrom(netip.AddrFrom4([4]byte{byte(a >> 24), byte(a >> 16), byte(a >> 8), byte(a)}), prefixLen).String())
		step := uint64(1) << block
		if step >= remainingCount {
			break
		}
		a += uint32(step)
	}
	return out
}

func binary4(a netip.Addr) uint32 {
	b := a.As4()
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func rangePrefixes6(start, end netip.Addr) []string {
	start16, end16 := start.As16(), end.As16()
	a := new(big.Int).SetBytes(start16[:])
	b := new(big.Int).SetBytes(end16[:])
	out := []string{}
	for a.Cmp(b) <= 0 {
		alignment := trailingZeroBits128(a)
		remaining := new(big.Int).Sub(new(big.Int).Set(b), a)
		remaining.Add(remaining, big.NewInt(1))
		maxBlock := remaining.BitLen() - 1
		if maxBlock < alignment {
			alignment = maxBlock
		}
		prefixLen := 128 - alignment
		var raw [16]byte
		a.FillBytes(raw[:])
		addr := netip.AddrFrom16(raw)
		out = append(out, netip.PrefixFrom(addr, prefixLen).String())
		step := new(big.Int).Lsh(big.NewInt(1), uint(alignment))
		a.Add(a, step)
	}
	return out
}

func trailingZeroBits128(v *big.Int) int {
	if v.Sign() == 0 {
		return 128
	}
	for i := 0; i < 128; i++ {
		if v.Bit(i) != 0 {
			return i
		}
	}
	return 128
}
