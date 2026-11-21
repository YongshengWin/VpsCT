package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"ctlvps/internal/geoip"
	"ctlvps/internal/httpx"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/store"
)

// refreshGeoIP updates all persisted country-deny node policies to the
// current fixed provider release and republishes each affected server. It is
// explicit so a provider release cannot silently change live access policy.
func (a *API) refreshGeoIP(w http.ResponseWriter, r *http.Request) error {
	if a.GeoCountry == nil {
		return httpx.BadRequest("控制端未配置 GeoIP 数据源")
	}
	nodes, err := a.Store.ListNodes(r.Context(), store.NodeFilter{})
	if err != nil {
		return err
	}
	version := time.Now().UTC().Format("2006-01")
	provider := geoip.CountryProviderDBIP
	if source := a.GeoCountry.Source(); source.Provider != "" {
		provider = source.Provider
	}
	updated, servers := 0, map[int64]bool{}
	var failures []string
	var providerVersion string
	for _, node := range nodes {
		if node.Network == nil || node.Network.Ingress == nil || node.Network.Ingress.Mode != networkconfig.IngressDenyCountries || node.ServerID == nil {
			continue
		}
		ranges, e := a.GeoCountry.Materialize(r.Context(), node.Network.Ingress.Countries, version)
		if e != nil {
			failures = append(failures, node.Name+"："+e.Error())
			continue
		}
		providerVersion = ranges.Version
		serverID, changed, e := a.Store.RefreshGeoIPPolicy(r.Context(), node.ID, ranges)
		if e != nil {
			failures = append(failures, node.Name+"："+e.Error())
			continue
		}
		if changed {
			updated++
			servers[serverID] = true
		}
	}
	for serverID := range servers {
		if _, _, e := a.Desired.Publish(r.Context(), serverID); e != nil {
			failures = append(failures, "服务器 "+fmt.Sprint(serverID)+" 发布失败："+e.Error())
		}
	}
	if len(failures) > 0 {
		return httpx.E(http.StatusBadGateway, "geoip_refresh_partial", "部分 GeoIP 策略刷新失败："+strings.Join(failures, "；"))
	}
	a.audit(r, "geoip.refresh", provider, mustJSON(map[string]any{"version": providerVersion, "updated_nodes": updated}))
	httpx.OK(w, map[string]any{"provider": provider, "version": providerVersion, "updated_nodes": updated, "servers": len(servers)})
	return nil
}

func (a *API) refreshGeoLookup(w http.ResponseWriter, r *http.Request) error {
	if a.Geo == nil {
		return httpx.BadRequest("控制端未配置本地 IP 归属地数据源")
	}
	if err := a.Geo.Ensure(r.Context()); err != nil {
		return httpx.E(http.StatusBadGateway, "geoip_lookup_refresh_failed", "IP 归属地数据库刷新失败："+err.Error())
	}
	a.audit(r, "geoip.lookup_refresh", "ip2region", nil)
	httpx.OK(w, map[string]any{"provider": "ip2region", "ready": a.Geo.Ready()})
	return nil
}

func (a *API) geoipCountries(w http.ResponseWriter, r *http.Request) error {
	if a.GeoCountry == nil {
		return httpx.BadRequest("控制端未配置 GeoIP 数据源")
	}
	list, err := a.GeoCountry.Countries(r.Context())
	if err != nil {
		return httpx.E(http.StatusBadGateway, "geoip_countries_unavailable", "无法读取当前 GeoIP 国家清单："+err.Error())
	}
	httpx.OK(w, map[string]any{"provider": a.GeoCountry.Source().Provider, "countries": list})
	return nil
}
