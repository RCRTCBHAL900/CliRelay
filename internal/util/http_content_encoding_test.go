package util

import (
	"bytes"
	"compress/gzip"
	"testing"
)

func gzipBytes(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write(raw); err != nil {
		t.Fatalf("gzip write failed: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("gzip close failed: %v", err)
	}
	return buf.Bytes()
}

func TestDecodeMaybeCompressedHTTPBody_WithGzipHeader(t *testing.T) {
	raw := []byte(`{"error":{"message":"quota exhausted"}}`)
	decoded, err := DecodeMaybeCompressedHTTPBody(gzipBytes(t, raw), "gzip")
	if err != nil {
		t.Fatalf("DecodeMaybeCompressedHTTPBody returned error: %v", err)
	}
	if string(decoded) != string(raw) {
		t.Fatalf("decoded body = %q, want %q", decoded, raw)
	}
}

func TestDecodeMaybeCompressedHTTPBody_DetectsGzipMagicWithoutHeader(t *testing.T) {
	raw := []byte(`{"error":{"message":"unauthorized"}}`)
	decoded, err := DecodeMaybeCompressedHTTPBody(gzipBytes(t, raw), "")
	if err != nil {
		t.Fatalf("DecodeMaybeCompressedHTTPBody returned error: %v", err)
	}
	if string(decoded) != string(raw) {
		t.Fatalf("decoded body = %q, want %q", decoded, raw)
	}
}
