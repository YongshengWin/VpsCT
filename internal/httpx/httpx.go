// Package httpx contains small HTTP helpers shared by API handlers.
package httpx

import (
	"ctlvps/internal/safehttp"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// Error is a JSON API error with an HTTP status.
type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

// E builds an Error.
func E(status int, code, msg string) *Error { return &Error{Status: status, Code: code, Message: msg} }

// Common errors.
var (
	ErrUnauthorized = E(http.StatusUnauthorized, "unauthorized", "登录已失效，请重新登录")
	ErrForbidden    = E(http.StatusForbidden, "forbidden", "没有权限执行该操作")
	ErrNotFound     = E(http.StatusNotFound, "not_found", "资源不存在")
)

// BadRequest builds a 400.
func BadRequest(msg string) *Error { return E(http.StatusBadRequest, "bad_request", msg) }

// Conflict builds a 409.
func Conflict(msg string) *Error { return E(http.StatusConflict, "conflict", msg) }

// JSON writes v as JSON with status.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// OK writes 200 JSON.
func OK(w http.ResponseWriter, v any) { JSON(w, http.StatusOK, v) }

// NoContent writes 204.
func NoContent(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

// WriteError writes err as JSON, mapping *Error statuses and defaulting to 500.
func WriteError(w http.ResponseWriter, err error) {
	var e *Error
	if errors.As(err, &e) {
		JSON(w, e.Status, map[string]any{"error": e})
		return
	}
	JSON(w, http.StatusInternalServerError, map[string]any{"error": Error{Code: "internal", Message: "服务暂时无法完成请求"}})
}

// Decode reads a JSON body (max 4 MiB) into v.
func Decode(r *http.Request, v any) error {
	body, err := safehttp.ReadBounded(r.Body, 4<<20)
	if err != nil {
		return BadRequest("读取请求体失败")
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return BadRequest("请求体为空")
	}
	if err := validateJSON(body); err != nil {
		return BadRequest("JSON 结构无效")
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	if strings.HasPrefix(r.URL.Path, "/api/v1/auth/") || strings.Contains(r.URL.Path, "maintenance") {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(v); err != nil {
		return BadRequest("JSON 字段或类型无效")
	}
	return nil
}

// PathInt64 parses a path value as int64.
func PathInt64(r *http.Request, name string) (int64, error) {
	v := r.PathValue(name)
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, BadRequest("非法的 " + name)
	}
	return n, nil
}

// QueryInt reads an integer query parameter with default.
func QueryInt(r *http.Request, name string, def int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	if n > 10000 {
		return 10000
	}
	return n
}

// ClientIP extracts the client address honouring X-Forwarded-For / X-Real-IP
// only when trustProxy is set.
func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[0])
		}
		if rip := r.Header.Get("X-Real-IP"); rip != "" {
			return strings.TrimSpace(rip)
		}
		if cf := r.Header.Get("CF-Connecting-IP"); cf != "" {
			return strings.TrimSpace(cf)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Handler adapts an error-returning handler.
type Handler func(w http.ResponseWriter, r *http.Request) error

// ServeHTTP implements http.Handler.
func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := h(w, r); err != nil {
		WriteError(w, err)
	}
}
