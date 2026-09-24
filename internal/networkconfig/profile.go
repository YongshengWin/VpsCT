package networkconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"strings"
)

// DecodeNode preserves explicit null (reset) while rejecting omitted input or
// unsupported runtime/core fields. API callers can therefore distinguish reset
// from old clients which did not send a network policy at all.
func DecodeNode(raw []byte) (*Node, error) {
	if len(raw) == 0 || len(raw) > 4096 {
		return nil, errors.New("网络配置缺失或超出大小限制")
	}
	var n *Node
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&n); err != nil {
		return nil, errors.New("网络配置字段或类型无效")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("网络配置包含多余内容")
	}
	if n == nil {
		return nil, nil
	}
	return n, n.Validate()
}

// DecodeDirect rejects misspelled or unsupported options instead of accepting
// an arbitrary core configuration. Direct profiles have no credentials.
func DecodeDirect(raw []byte) (Direct, error) {
	var d Direct
	if len(raw) == 0 || len(raw) > 16384 {
		return d, errors.New("直连配置大小无效")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&d); err != nil {
		return d, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return d, errors.New("直连配置包含多余内容")
	}
	return d, d.Validate()
}

func ValidateAdvertiseHost(host string) error {
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.IsGlobalUnicast() && ip.Zone() == "" && !ip.Is4In6() {
			return nil
		}
		return errors.New("客户端访问 IP 地址无效")
	}
	if host == "" || len(host) > 253 {
		return errors.New("客户端访问地址无效")
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("客户端访问域名无效")
		}
		for _, ch := range label {
			if ch != '-' && !(ch >= 'a' && ch <= 'z') && !(ch >= 'A' && ch <= 'Z') && !(ch >= '0' && ch <= '9') {
				return errors.New("客户端访问地址只能填写 IP 或域名")
			}
		}
	}
	return nil
}
