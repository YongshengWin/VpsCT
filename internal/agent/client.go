package agent

import (
	"bytes"
	"compress/gzip"
	"context"
	"ctlvps/internal/agentbudget"
	"ctlvps/internal/agentnet"
	"ctlvps/internal/safehttp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/networkconfig"
)

// Client talks to ctlvpsd.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
	Version string
}

// NewClient builds a client.
func NewClient(baseURL, token, version string) *Client {
	c := &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, Version: version, HTTP: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		c.HTTP.Transport = agentnet.Transport{}
	}
	return c
}

// ErrUnauthorized is returned when the agent token was revoked.
var ErrUnauthorized = errors.New("agent token rejected (401); re-enrol")

func (c *Client) do(ctx context.Context, method, path string, in, out any, gz bool) error {
	u, e := url.Parse(c.BaseURL)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return errors.New("agent requires an HTTPS controller URL")
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		if len(b) > 8<<20 {
			return errors.New("agent request exceeds budget")
		}
		if gz {
			var buf bytes.Buffer
			zw := gzip.NewWriter(&buf)
			_, _ = zw.Write(b)
			_ = zw.Close()
			if buf.Len() > 2<<20 {
				return errors.New("compressed batch exceeds budget")
			}
			body = &buf
		} else {
			body = bytes.NewReader(b)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ctlvps-agent/"+c.Version)
	if path == agentproto.PathDesired && runtime.GOOS == "linux" {
		req.Header.Set(agentproto.NetworkBindingHeader, fmt.Sprint(agentproto.NetworkBindingVersion))
		req.Header.Set(agentproto.NetworkWireGuardHeader, fmt.Sprint(agentproto.NetworkWireGuardVersion))
		req.Header.Set(networkconfig.MitaHeader, fmt.Sprint(networkconfig.MitaVersion))
		req.Header.Set(agentproto.NetworkSSHHeader, fmt.Sprint(agentproto.NetworkSSHVersion))
		req.Header.Set(agentproto.NetworkEgressHeader, fmt.Sprint(agentproto.NetworkEgressVersion))
		req.Header.Set(agentproto.NetworkForwardHeader, fmt.Sprint(agentproto.NetworkForwardVersion))
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
		if gz {
			req.Header.Set("Content-Encoding", "gzip")
		}
	}
	if c.Token != "" {
		req.Header.Set(agentproto.AuthHeader, "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode == http.StatusNotModified {
		return errNotModified
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: HTTP %d", method, path, resp.StatusCode)
	}
	if out != nil {
		limit := int64(256 << 10)
		if path == agentproto.PathDesired {
			limit = agentbudget.ConfigBytes
		}
		b, e := safehttp.ReadBounded(resp.Body, limit)
		if e != nil {
			return e
		}
		if e = safehttp.CheckJSONBudget(b); e != nil {
			return e
		}
		return json.Unmarshal(b, out)
	}
	return nil
}

var errNotModified = errors.New("not modified")

// Enroll exchanges the one-time token.
func (c *Client) Enroll(ctx context.Context, req agentproto.EnrollRequest) (agentproto.EnrollResponse, error) {
	var out agentproto.EnrollResponse
	err := c.do(ctx, http.MethodPost, agentproto.PathEnroll, req, &out, false)
	return out, err
}

// Heartbeat posts metrics and returns the server's instructions.
func (c *Client) Heartbeat(ctx context.Context, hb agentproto.Heartbeat) (agentproto.HeartbeatResponse, error) {
	var out agentproto.HeartbeatResponse
	err := c.do(ctx, http.MethodPost, agentproto.PathHeartbeat, hb, &out, false)
	return out, err
}

// Desired fetches the latest desired state.
func (c *Client) Desired(ctx context.Context) (*agentproto.DesiredState, error) {
	var out agentproto.DesiredState
	if err := c.do(ctx, http.MethodGet, agentproto.PathDesired, nil, &out, false); err != nil {
		return nil, err
	}
	return &out, nil
}

// Report sends the outcome of a reconcile.
func (c *Client) Report(ctx context.Context, rep agentproto.ApplyReport) error {
	return c.do(ctx, http.MethodPost, agentproto.PathApplyReport, rep, nil, false)
}

// UploadConnlog sends a gzip batch.
func (c *Client) UploadConnlog(ctx context.Context, batch agentproto.ConnlogBatch) (agentproto.ConnlogAck, error) {
	var out agentproto.ConnlogAck
	err := c.do(ctx, http.MethodPost, agentproto.PathConnlog, batch, &out, true)
	return out, err
}
