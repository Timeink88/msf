package server

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientAcceptsGzipRespectsQValue(t *testing.T) {
	cases := map[string]bool{
		"gzip":                true,
		"gzip, deflate":       true,
		"gzip; q=0.5":         true,
		"gzip;q=0":            false,
		"gzip;q=0.0, deflate": false,
		"deflate":             false,
		"*;q=0":               false,
		"*;q=0, gzip":         true, // 显式 gzip 覆盖通配 q=0
		"":                    false,
	}
	for header, want := range cases {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if header != "" {
			r.Header.Set("Accept-Encoding", header)
		}
		if got := clientAcceptsGzip(r); got != want {
			t.Fatalf("clientAcceptsGzip(%q) = %v, want %v", header, got, want)
		}
	}
}

func TestResponseCompressionSetsVaryAndHonorsQZero(t *testing.T) {
	handler := withResponseCompression(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	// gzip;q=0：绝不压缩，但表示随 Accept-Encoding 变化，Vary 仍必须在。
	req := httptest.NewRequest(http.MethodGet, "/api/v1/x", nil)
	req.Header.Set("Accept-Encoding", "gzip;q=0")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if enc := rec.Header().Get("Content-Encoding"); enc != "" {
		t.Fatalf("gzip;q=0 client got Content-Encoding %q", enc)
	}
	if vary := rec.Header().Get("Vary"); !strings.Contains(vary, "Accept-Encoding") {
		t.Fatalf("Vary = %q, want Accept-Encoding", vary)
	}
	if rec.Body.String() != `{"ok":true}` {
		t.Fatalf("plain body = %q", rec.Body.String())
	}

	// 正常 gzip 客户端：压缩表示 + 可解压还原。
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/x", nil)
	req2.Header.Set("Accept-Encoding", "gzip")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if enc := rec2.Header().Get("Content-Encoding"); enc != "gzip" {
		t.Fatalf("gzip client got Content-Encoding %q", enc)
	}
	if vary := rec2.Header().Get("Vary"); !strings.Contains(vary, "Accept-Encoding") {
		t.Fatalf("Vary = %q, want Accept-Encoding", vary)
	}
	zr, err := gzip.NewReader(rec2.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("roundtrip body = %q", string(body))
	}
}
