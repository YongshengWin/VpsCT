// Package maintenance executes a small, fixed set of privileged operations.
// Requests never contain shell commands, arbitrary paths, or a repository override.
package maintenance

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"regexp"
	"time"
)

const (
	Directory = "/var/lib/ctlvps-maintenance"
	Socket    = "/run/ctlvps-maintenance/control.sock"
	Protocol  = 1
)

var idPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var versionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9][A-Za-z0-9.-]*)?$`)
var repoPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var shaPattern = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)
var ErrBusy = errors.New("本机已有维护任务正在执行")

func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

type Request struct {
	ID          string `json:"id"`
	Role        string `json:"role"`
	Action      string `json:"action"`
	Version     string `json:"version,omitempty"`
	Purge       bool   `json:"purge,omitempty"`
	RemoveCaddy bool   `json:"remove_caddy,omitempty"`
}

func (r Request) Validate() error {
	if !idPattern.MatchString(r.ID) || (r.Role != "controller" && r.Role != "agent") || (r.Action != "update" && r.Action != "uninstall") {
		return errors.New("维护任务参数无效")
	}
	if r.Action == "update" {
		if r.Purge || r.RemoveCaddy || (r.Role == "controller" && !versionPattern.MatchString(r.Version)) {
			return errors.New("升级参数无效，请指定正式版本号")
		}
	} else if r.Version != "" || (r.RemoveCaddy && (r.Role != "controller" || !r.Purge)) {
		return errors.New("卸载参数无效")
	}
	return nil
}

// Job contains only displayable data. Credentials belong exclusively to Spec.
type Job struct {
	Request
	Status    string    `json:"status"`
	Stage     string    `json:"stage"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (j Job) Active() bool { return j.Status == "queued" || j.Status == "running" }
func ValidStatus(s string) bool {
	switch s {
	case "queued", "running", "succeeded", "failed", "rolled_back", "interrupted", "expired", "cancelled":
		return true
	}
	return false
}

// Spec is root-only. CallbackToken grants report access to exactly one job;
// it is never an agent enrolment token or an administrator session.
type Spec struct {
	Request
	Repository    string `json:"repository,omitempty"`
	DownloadURL   string `json:"download_url,omitempty"`
	SHA256        string `json:"sha256,omitempty"`
	CallbackURL   string `json:"callback_url,omitempty"`
	CallbackToken string `json:"callback_token,omitempty"`
}

type Info struct {
	Available  bool   `json:"available"`
	Reason     string `json:"reason,omitempty"`
	Version    string `json:"version"`
	Repository string `json:"repository,omitempty"`
	Jobs       []Job  `json:"jobs"`
}

type Release struct {
	Version     string `json:"version"`
	URL         string `json:"url"`
	PublishedAt string `json:"published_at"`
}
