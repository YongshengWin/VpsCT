package mieruconfig

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

type Client struct {
	Username       string `json:"username"`
	Password       string `json:"password"`
	Transport      string `json:"transport"`
	Multiplexing   string `json:"multiplexing,omitempty"`
	Handshake      string `json:"handshake-mode,omitempty"`
	TrafficPattern string `json:"traffic-pattern,omitempty"`
	UDP            bool   `json:"udp"`
}

func Field(k string) bool {
	switch k {
	case "username", "password", "transport", "multiplexing", "handshake-mode", "traffic-pattern", "udp":
		return true
	}
	return false
}
func Decode(params map[string]any) (Client, error) {
	c := Client{Transport: "TCP", UDP: true}
	for k := range params {
		if !Field(k) {
			return c, errors.New("mieru 暂只支持单端口节点，不接受端口范围或未知字段")
		}
	}
	raw, err := json.Marshal(params)
	if err != nil || len(raw) > 16384 || json.Unmarshal(raw, &c) != nil {
		return c, errors.New("mieru 参数格式或大小无效")
	}
	if c.Username == "" || len(c.Username) > 128 || c.Password == "" || len(c.Password) > 4096 || strings.ContainsAny(c.Username+c.Password, "\r\n\x00") {
		return c, errors.New("请提供有效的 mieru 用户名和密码")
	}
	if c.Transport != "TCP" && c.Transport != "UDP" {
		return c, errors.New("mieru 传输须为 TCP 或 UDP")
	}
	switch c.Multiplexing {
	case "", "MULTIPLEXING_OFF", "MULTIPLEXING_LOW", "MULTIPLEXING_MIDDLE", "MULTIPLEXING_HIGH":
	default:
		return c, errors.New("mieru 多路复用级别无效")
	}
	switch c.Handshake {
	case "", "HANDSHAKE_STANDARD", "HANDSHAKE_NO_WAIT":
	default:
		return c, errors.New("mieru 握手模式无效")
	}
	if c.TrafficPattern != "" {
		if _, err = base64.StdEncoding.DecodeString(c.TrafficPattern); err != nil || len(c.TrafficPattern) > 8192 {
			return c, errors.New("mieru 流量特征须为有效 Base64")
		}
	}
	return c, nil
}
func (c Client) Params() map[string]any {
	raw, _ := json.Marshal(c)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}
