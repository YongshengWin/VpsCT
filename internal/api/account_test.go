package api

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"ctlvps/internal/auth"
)

func TestTwoFactorLoginFlow(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)

	// setup needs the password, then a live code to enable
	c.do("POST", "/api/v1/auth/2fa/setup", map[string]any{"password": "wrong"}, 400)
	st := c.do("POST", "/api/v1/auth/2fa/setup", map[string]any{"password": "password123"}, 200)
	secret := st["secret"].(string)
	if !strings.HasPrefix(st["otpauth_url"].(string), "otpauth://totp/VpsCT:admin?") {
		t.Fatalf("otpauth: %v", st["otpauth_url"])
	}
	me := c.do("GET", "/api/v1/auth/me", nil, 200)
	if me["totp_enabled"] != false {
		t.Fatal("pending secret must not count as enabled")
	}
	c.do("POST", "/api/v1/auth/2fa/enable", map[string]any{"code": "000000"}, 400)
	code, _ := auth.TOTPCode(secret, auth.TOTPStep(time.Now()))
	en := c.do("POST", "/api/v1/auth/2fa/enable", map[string]any{"code": code}, 200)
	codes := en["recovery_codes"].([]any)
	if len(codes) != 8 {
		t.Fatalf("recovery codes: %v", codes)
	}
	me = c.do("GET", "/api/v1/auth/me", nil, 200)
	if me["totp_enabled"] != true {
		t.Fatal("expected enabled")
	}

	// fresh login: password alone yields a challenge, no cookie
	c.cookie = nil
	lg := c.do("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "password123"}, 200)
	if lg["requires_2fa"] != true || c.cookie != nil {
		t.Fatalf("expected challenge without session: %v", lg)
	}
	ch := lg["challenge"].(string)
	c.do("GET", "/api/v1/auth/me", nil, 401)
	c.do("POST", "/api/v1/auth/login/2fa", map[string]any{"challenge": ch, "code": "123456"}, 401)
	c.do("POST", "/api/v1/auth/login/2fa", map[string]any{"challenge": "bogus", "code": code}, 401)
	// the code used for enabling is the current step: replay must fail, next step passes
	c.do("POST", "/api/v1/auth/login/2fa", map[string]any{"challenge": ch, "code": code}, 401)
	next, _ := auth.TOTPCode(secret, auth.TOTPStep(time.Now())+1)
	c.do("POST", "/api/v1/auth/login/2fa", map[string]any{"challenge": ch, "code": next}, 200)
	if c.cookie == nil {
		t.Fatal("expected session cookie after second factor")
	}
	me = c.do("GET", "/api/v1/auth/me", nil, 200)
	if me["last_login_at"] == nil {
		t.Fatal("last_login_at not recorded")
	}

	// recovery code works once
	c.cookie = nil
	lg = c.do("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "password123"}, 200)
	rc := codes[0].(string)
	c.do("POST", "/api/v1/auth/login/2fa", map[string]any{"challenge": lg["challenge"], "code": strings.ToUpper(rc)}, 200)
	c.cookie = nil
	lg = c.do("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "password123"}, 200)
	c.do("POST", "/api/v1/auth/login/2fa", map[string]any{"challenge": lg["challenge"], "code": rc}, 401)
	c.do("POST", "/api/v1/auth/login/2fa", map[string]any{"challenge": lg["challenge"], "code": codes[1].(string)}, 200)

	// disable needs password + factor; afterwards plain login works again
	c.do("POST", "/api/v1/auth/2fa/disable", map[string]any{"password": "password123", "code": "000000"}, 400)
	c.do("POST", "/api/v1/auth/2fa/disable", map[string]any{"password": "password123", "code": codes[2].(string)}, 204)
	c.cookie = nil
	lg = c.do("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "password123"}, 200)
	if lg["requires_2fa"] == true || c.cookie == nil {
		t.Fatal("2fa should be off")
	}
}

func TestAdminResets2FA(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	u := c.do("POST", "/api/v1/users", map[string]any{"username": "bob", "password": "password123"}, 201)
	admin := c.cookie
	c.cookie = nil
	c.do("POST", "/api/v1/auth/login", map[string]any{"username": "bob", "password": "password123"}, 200)
	st := c.do("POST", "/api/v1/auth/2fa/setup", map[string]any{"password": "password123"}, 200)
	code, _ := auth.TOTPCode(st["secret"].(string), auth.TOTPStep(time.Now()))
	c.do("POST", "/api/v1/auth/2fa/enable", map[string]any{"code": code}, 200)
	c.cookie = admin
	out := c.do("POST", "/api/v1/users/"+itoa(u["id"])+"/2fa/reset", nil, 200)
	if out["totp_enabled"] != false {
		t.Fatalf("reset: %v", out)
	}
	c.cookie = nil
	lg := c.do("POST", "/api/v1/auth/login", map[string]any{"username": "bob", "password": "password123"}, 200)
	if lg["requires_2fa"] == true {
		t.Fatal("2fa still on after reset")
	}
}

func TestNickname(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	me := c.do("GET", "/api/v1/auth/me", nil, 200)
	if me["display_name"] != "admin" || me["nickname"] != "" {
		t.Fatalf("default display: %v", me)
	}
	me = c.do("PUT", "/api/v1/auth/profile", map[string]any{"nickname": "  阿狸  "}, 200)
	if me["nickname"] != "阿狸" || me["display_name"] != "阿狸" || me["username"] != "admin" {
		t.Fatalf("set nickname: %v", me)
	}
	c.do("PUT", "/api/v1/auth/profile", map[string]any{"nickname": strings.Repeat("超", 33)}, 400)
	bob := c.do("POST", "/api/v1/users", map[string]any{"username": "bob", "password": "password123", "nickname": "小王"}, 201)
	if bob["display_name"] != "小王" {
		t.Fatalf("create: %v", bob)
	}
	c.do("PUT", "/api/v1/users/"+itoa(bob["id"]), map[string]any{"nickname": ""}, 200)
	list := c.do("GET", "/api/v1/users", nil, 200)["list"].([]any)
	var cleared bool
	for _, row := range list {
		m := row.(map[string]any)
		if m["username"] == "bob" {
			cleared = m["nickname"] == "" && m["display_name"] == "bob"
		}
	}
	if !cleared {
		t.Fatalf("cleared nickname: %v", list)
	}
}

func TestAvatar(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	me := c.do("PUT", "/api/v1/auth/avatar", map[string]any{"avatar": "preset:mint-wave"}, 200)
	if me["avatar"] != "preset:mint-wave" {
		t.Fatalf("preset avatar: %v", me["avatar"])
	}
	c.do("PUT", "/api/v1/auth/avatar", map[string]any{"avatar": "preset:Bad Name"}, 400)
	c.do("PUT", "/api/v1/auth/avatar", map[string]any{"avatar": "data:text/html;base64,PGI+"}, 400)
	c.do("PUT", "/api/v1/auth/avatar", map[string]any{"avatar": "data:image/png;base64," + base64.StdEncoding.EncodeToString(make([]byte, avatarMaxBytes+1))}, 400)

	var imageBytes bytes.Buffer
	_ = png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	png := imageBytes.Bytes()
	me = c.do("PUT", "/api/v1/auth/avatar", map[string]any{"avatar": "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)}, 200)
	url, _ := me["avatar"].(string)
	if !strings.HasPrefix(url, "/api/v1/users/1/avatar?v=") {
		t.Fatalf("uploaded avatar should be served by URL, got %q", url)
	}
	req, _ := http.NewRequest("GET", c.srv.URL+url, nil)
	req.AddCookie(c.cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" || string(body) != string(png) {
		t.Fatalf("avatar endpoint: %d %s %q", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	// list output never inlines the image data
	list := c.do("GET", "/api/v1/users", nil, 200)["list"].([]any)
	if a := list[0].(map[string]any)["avatar"].(string); strings.HasPrefix(a, "data:") {
		t.Fatal("data URL leaked into list")
	}
	c.do("PUT", "/api/v1/auth/avatar", map[string]any{"avatar": ""}, 200)
	req, _ = http.NewRequest("GET", c.srv.URL+url, nil)
	req.AddCookie(c.cookie)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("cleared avatar should 404, got %d", resp.StatusCode)
	}
}

// itoa renders a JSON number (decoded as float64) as an integer path segment.
func itoa(v any) string {
	f, _ := v.(float64)
	return strconv.FormatInt(int64(f), 10)
}
