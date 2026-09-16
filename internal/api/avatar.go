package api

import (
	"bytes"
	"encoding/base64"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"strings"

	"ctlvps/internal/httpx"
)

// normalizeAvatar validates actual pixels and re-encodes a single inert frame.
func normalizeAvatar(v string) (string, error) {
	if v == "" || presetRe.MatchString(v) {
		return v, nil
	}
	if len(v) > avatarMaxBytes*2 {
		return "", httpx.BadRequest("头像过大")
	}
	m := dataURLRe.FindStringSubmatch(v)
	if m == nil {
		return "", httpx.BadRequest("头像格式无效")
	}
	b, err := base64.StdEncoding.DecodeString(m[2])
	if err != nil || len(b) > avatarMaxBytes {
		return "", httpx.BadRequest("头像数据无效或过大")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 1024 || cfg.Height > 1024 || cfg.Width*cfg.Height > 1024*1024 {
		return "", httpx.BadRequest("头像必须是有效图片，最大 1024×1024")
	}
	declared := strings.TrimPrefix(m[1], "image/")
	if declared == "jpeg" {
		declared = "jpeg"
	}
	if format != declared {
		return "", httpx.BadRequest("头像内容与格式不一致")
	}
	im, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return "", httpx.BadRequest("头像无法解码")
	}
	var out bytes.Buffer
	if err = png.Encode(&out, im); err != nil || out.Len() > avatarMaxBytes {
		return "", httpx.BadRequest("处理后的头像过大")
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(out.Bytes()), nil
}
