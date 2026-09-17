package agentnet

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestArtifactCannotUseMemoryDownload(t *testing.T) {
	if _, err := Download(context.Background(), "https://example.invalid", 128<<20, true); err == nil {
		t.Fatal("artifact allocated in metadata API")
	}
	for _, limit := range []int64{-1, 0, 257 << 20} {
		if _, err := DownloadTo(context.Background(), "https://example.invalid", limit, false, io.Discard); err == nil {
			t.Fatal("invalid budget accepted")
		}
	}
}

func TestStreamingResponseCompatibleWithLegacy(t *testing.T) {
	for _, body := range []string{"", "a", "ab", "abc", "binary\x00\xff"} {
		var dst bytes.Buffer
		if err := writeResponse(&dst, 201, strings.NewReader(body)); err != nil {
			t.Fatal(err)
		}
		var got response
		if err := json.Unmarshal(dst.Bytes(), &got); err != nil || got.Status != 201 || string(got.Body) != body {
			t.Fatalf("%+v %v", got, err)
		}
	}
}
