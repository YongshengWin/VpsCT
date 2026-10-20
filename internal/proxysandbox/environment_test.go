package proxysandbox

import (
	"strings"
	"testing"
)

func TestProxyEnvironmentBoundsMemoryWithoutInheritingRuntimeOverrides(t *testing.T) {
	t.Setenv("GOMEMLIMIT", "192MiB")
	t.Setenv("GODEBUG", "madvdontneed=0")
	t.Setenv("HTTP_PROXY", "http://untrusted.fixture.invalid")
	t.Setenv("MITA_CONFIG_JSON_FILE", "/etc/ctlvps-proxy/mita-30001/config.json")
	for _, bin := range []string{"/opt/ctlvps/bin/sing-box", "/opt/ctlvps/bin/mita", "/opt/ctlvps/bin/snell-server"} {
		t.Run(bin, func(t *testing.T) {
			values := map[string]string{}
			for _, item := range proxyEnvironment(bin) {
				key, value, ok := strings.Cut(item, "=")
				if !ok || values[key] != "" {
					t.Fatal("invalid or duplicate environment entry")
				}
				values[key] = value
			}
			if values["GOMEMLIMIT"] != "192MiB" || values["HTTP_PROXY"] != "" {
				t.Fatal("memory budget lost or inherited proxy override")
			}
			if bin == "/opt/ctlvps/bin/sing-box" {
				if values["GODEBUG"] != "disablethp=1" {
					t.Fatal("sing-box THP workaround missing or inherited override")
				}
			} else if _, exists := values["GODEBUG"]; exists {
				t.Fatal("sing-box runtime setting leaked to another core")
			}
			if bin == "/opt/ctlvps/bin/mita" && values["MITA_CONFIG_JSON_FILE"] != "/etc/ctlvps-proxy/mita-30001/config.json" {
				t.Fatal("mita configuration path lost")
			}
		})
	}
}
