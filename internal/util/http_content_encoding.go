package util

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"fmt"
	"io"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

func parseHTTPContentEncodings(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.ToLower(strings.TrimSpace(part))
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func hasGzipMagic(body []byte) bool {
	return len(body) >= 2 && body[0] == 0x1f && body[1] == 0x8b
}

func decodeSingleHTTPEncoding(body []byte, encoding string) ([]byte, error) {
	switch encoding {
	case "", "identity":
		return body, nil
	case "gzip":
		reader, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("gzip reader: %w", err)
		}
		defer func() { _ = reader.Close() }()
		return io.ReadAll(reader)
	case "deflate":
		reader := flate.NewReader(bytes.NewReader(body))
		defer func() { _ = reader.Close() }()
		return io.ReadAll(reader)
	case "br":
		return io.ReadAll(brotli.NewReader(bytes.NewReader(body)))
	case "zstd":
		reader, err := zstd.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("zstd reader: %w", err)
		}
		defer reader.Close()
		return io.ReadAll(reader)
	default:
		return nil, fmt.Errorf("unsupported content encoding: %s", encoding)
	}
}

func DecodeMaybeCompressedHTTPBody(body []byte, contentEncoding string) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}

	encodings := parseHTTPContentEncodings(contentEncoding)
	if len(encodings) == 0 {
		if !hasGzipMagic(body) {
			return body, nil
		}
		return decodeSingleHTTPEncoding(body, "gzip")
	}

	decoded := append([]byte(nil), body...)
	for i := len(encodings) - 1; i >= 0; i-- {
		next, err := decodeSingleHTTPEncoding(decoded, encodings[i])
		if err != nil {
			return nil, err
		}
		decoded = next
	}
	return decoded, nil
}
