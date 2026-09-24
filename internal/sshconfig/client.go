// Package sshconfig validates explicit SSH client credentials and pinned host
// keys. It never resolves paths or reads the operator's SSH configuration.
package sshconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"golang.org/x/crypto/ssh"
)

type Client struct {
	Username             string   `json:"username"`
	Password             string   `json:"password,omitempty"`
	PrivateKey           string   `json:"private-key,omitempty"`
	PrivateKeyPassphrase string   `json:"private-key-passphrase,omitempty"`
	HostKeys             []string `json:"host-key"`
	UDP                  bool     `json:"udp,omitempty"`
}

func Field(name string) bool {
	switch name {
	case "username", "password", "private-key", "private-key-passphrase", "host-key", "udp":
		return true
	}
	return false
}

func Decode(params map[string]any) (Client, error) {
	var c Client
	if len(params) > 6 {
		return c, errors.New("SSH 节点参数过多")
	}
	for k := range params {
		if !Field(k) {
			return c, errors.New("SSH 节点包含不支持的参数；不接受本机文件路径或跳过主机校验")
		}
	}
	raw, err := json.Marshal(params)
	if err != nil || len(raw) > 32768 {
		return c, errors.New("SSH 凭据格式或大小无效")
	}
	if err = json.Unmarshal(raw, &c); err != nil {
		return c, errors.New("SSH 凭据字段类型无效")
	}
	return c, c.Validate()
}

func (c Client) Validate() error {
	if c.Username == "" || len(c.Username) > 128 || strings.ContainsAny(c.Username, "\x00\r\n") {
		return errors.New("请明确填写 SSH 专用转发用户名")
	}
	if c.UDP {
		return errors.New("SSH 仅支持 TCP，不能启用 UDP")
	}
	if (c.Password == "") == (c.PrivateKey == "") {
		return errors.New("SSH 必须且只能选择密码或私钥认证")
	}
	if len(c.Password) > 4096 || strings.ContainsAny(c.Password, "\x00\r\n") || len(c.PrivateKeyPassphrase) > 4096 || strings.ContainsAny(c.PrivateKeyPassphrase, "\x00\r\n") {
		return errors.New("SSH 密码或密钥口令无效")
	}
	if c.PrivateKey == "" && c.PrivateKeyPassphrase != "" {
		return errors.New("未提供私钥时不能填写密钥口令")
	}
	if c.PrivateKey != "" {
		if len(c.PrivateKey) > 16384 || strings.Contains(c.PrivateKey, "\x00") || !strings.HasPrefix(strings.TrimSpace(c.PrivateKey), "-----BEGIN ") {
			return errors.New("SSH 私钥须为内联 PEM/OpenSSH 内容，不能填写文件路径")
		}
		var err error
		if c.PrivateKeyPassphrase != "" {
			_, err = ssh.ParsePrivateKeyWithPassphrase([]byte(c.PrivateKey), []byte(c.PrivateKeyPassphrase))
		} else {
			_, err = ssh.ParsePrivateKey([]byte(c.PrivateKey))
		}
		if err != nil {
			return errors.New("SSH 私钥或密钥口令无效")
		}
	}
	if len(c.HostKeys) < 1 || len(c.HostKeys) > 8 {
		return errors.New("请提供 1–8 个可信 SSH 主机公钥，不能留空或自动接受")
	}
	seen := map[string]bool{}
	for _, raw := range c.HostKeys {
		if len(raw) > 8192 || strings.ContainsAny(raw, "\x00\r\n") {
			return errors.New("SSH 主机公钥格式无效")
		}
		key, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(raw))
		if err != nil || len(options) > 0 || len(bytes.TrimSpace(rest)) > 0 {
			return errors.New("SSH 主机公钥须为单行公钥，不能填写指纹或文件路径")
		}
		canonical := string(key.Marshal())
		if seen[canonical] {
			return errors.New("SSH 主机公钥重复")
		}
		seen[canonical] = true
	}
	return nil
}

func (c Client) SingBox() map[string]any {
	out := map[string]any{"type": "ssh", "user": c.Username, "host_key": c.HostKeys}
	if c.Password != "" {
		out["password"] = c.Password
	} else {
		out["private_key"] = c.PrivateKey
		if c.PrivateKeyPassphrase != "" {
			out["private_key_passphrase"] = c.PrivateKeyPassphrase
		}
	}
	return out
}
