package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"ctlvps/internal/agentproto"
)

func TestDesiredClientDeclaresNetworkContractOnEveryFetch(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := ""
		if runtime.GOOS == "linux" {
			want = "1"
		}
		if r.URL.Path != agentproto.PathDesired || r.Header.Get(agentproto.NetworkBindingHeader) != want {
			t.Error("missing or unsupported request capability")
		}
		if r.Header.Get(agentproto.NetworkSSHHeader) != want {
			t.Error("missing SSH request capability")
		}
		if r.Header.Get(agentproto.NetworkEgressHeader) != want {
			t.Error("missing or unsupported transit request capability")
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "fixture", "fixture")
	c.HTTP = srv.Client()
	for i := 0; i < 2; i++ {
		if _, err := c.Desired(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}
