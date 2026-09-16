package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/user"
	"strconv"
	"strings"
	"time"

	"ctlvps/internal/agentnet"
	"ctlvps/internal/buildinfo"
	"ctlvps/internal/secureupdate"
	"runtime"
)

func repository() (string, error) {
	b, err := os.ReadFile("/opt/ctlvps/REPOSITORY")
	if err != nil {
		return "", err
	}
	r := strings.TrimSpace(string(b))
	if !repoPattern.MatchString(r) {
		return "", errors.New("安装来源无效")
	}
	return r, nil
}

// Serve exposes no TCP listener. systemd creates the root-owned runtime dir;
// only members of the dedicated ctlvps group can open the socket.
func Serve() error {
	group, err := user.LookupGroup("ctlvps")
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return err
	}
	if err := os.MkdirAll("/run/ctlvps-maintenance", 0750); err != nil {
		return err
	}
	if err := os.Chown("/run/ctlvps-maintenance", 0, gid); err != nil {
		return err
	}
	_ = os.Remove(Socket)
	l, err := net.Listen("unix", Socket)
	if err != nil {
		return err
	}
	defer l.Close()
	if err := os.Chown(Socket, 0, gid); err != nil {
		return err
	}
	if err := os.Chmod(Socket, 0660); err != nil {
		return err
	}
	m := NewManager()
	m.Recover()
	h := http.NewServeMux()
	h.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		repo, err := repository()
		m.Recover()
		info := Info{Available: err == nil, Version: buildinfo.Version, Repository: repo, Jobs: m.List()}
		if err != nil {
			info.Reason = "未检测到安装器管理的控制端，请先从终端升级安装"
		}
		if _, e := secureupdate.LoadPolicy(); e != nil {
			info.Available = false
			info.Reason = "本机独立发布信任根未配置，维护操作已关闭"
		}
		// Agent jobs share admission control, but are displayed on server details.
		jobs := []Job{}
		for _, j := range info.Jobs {
			if j.Role == "controller" {
				jobs = append(jobs, j)
			}
		}
		info.Jobs = jobs
		if len(info.Jobs) > 20 {
			info.Jobs = info.Jobs[:20]
		}
		socketJSON(w, 200, info)
	})
	h.HandleFunc("GET /latest", func(w http.ResponseWriter, r *http.Request) {
		repo, err := repository()
		if err != nil {
			socketJSON(w, 409, map[string]string{"error": "未找到安装来源"})
			return
		}
		release, err := Latest(r.Context(), repo)
		if err != nil {
			socketJSON(w, 502, map[string]string{"error": "无法查询 GitHub 正式发行版，请稍后重试"})
			return
		}
		socketJSON(w, 200, release)
	})
	h.HandleFunc("POST /jobs", func(w http.ResponseWriter, r *http.Request) {
		var req Request
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil || req.Role != "controller" {
			socketJSON(w, 400, map[string]string{"error": "请求参数无效"})
			return
		}
		if err := req.Validate(); err != nil {
			socketJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		repo, err := repository()
		if err != nil {
			socketJSON(w, 409, map[string]string{"error": "未找到安装来源"})
			return
		}
		j, err := m.Start(Spec{Request: req, Repository: repo})
		if err != nil {
			socketJSON(w, 409, map[string]string{"error": "无法创建维护任务，请检查是否已有任务运行或查看本机维护日志"})
			return
		}
		socketJSON(w, 202, j)
	})
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	return srv.Serve(l)
}

func socketJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func Latest(ctx context.Context, repo string) (Release, error) {
	if !repoPattern.MatchString(repo) {
		return Release{}, errors.New("invalid repository")
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/"+repo+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "VpsCT-maintenance")
	c := &http.Client{Timeout: 20 * time.Second}
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		c.Transport = agentnet.Transport{Public: true}
	}
	resp, err := c.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Release{}, errors.New("release lookup failed")
	}
	var data struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Published  string `json:"published_at"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&data); err != nil {
		return Release{}, err
	}
	if data.Draft || data.Prerelease || !versionPattern.MatchString(data.Tag) {
		return Release{}, errors.New("invalid stable release")
	}
	return Release{Version: data.Tag, URL: "https://github.com/" + repo + "/releases/tag/" + data.Tag, PublishedAt: data.Published}, nil
}

// Client is used by the unprivileged API process. The path is not HTTP input.
type Client struct{ Path string }

func (c Client) Call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		p := c.Path
		if p == "" {
			p = Socket
		}
		return (&net.Dialer{}).DialContext(ctx, "unix", p)
	}}
	defer transport.CloseIdleConnections()
	hc := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, method, "http://maintenance"+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return errors.New("维护服务不可用，请先通过终端安装支持网页维护的版本")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var v struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&v)
		if v.Error == "" {
			v.Error = "维护服务请求失败"
		}
		return errors.New(v.Error)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 128<<10)).Decode(out)
}
