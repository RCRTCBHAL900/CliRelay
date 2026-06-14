package util

import (
	"context"
	"errors"
	"net"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
)

func TestSourceIPDialContextFallsBackOnBindAddressFailureWhenEnabled(t *testing.T) {
	t.Setenv("CLIRELAY_SOURCEIP_BIND_FALLBACK", "true")

	primaryCalls := int32(0)
	fallbackCalls := int32(0)
	primaryNetwork := ""
	fallbackNetwork := ""

	dial := sourceIPDialContext(
		func(_ context.Context, network, addr string) (net.Conn, error) {
			atomic.AddInt32(&primaryCalls, 1)
			primaryNetwork = network
			if addr != "example.com:443" {
				t.Fatalf("primary addr = %q", addr)
			}
			return nil, &net.OpError{
				Op:  "dial",
				Net: network,
				Err: &os.SyscallError{Syscall: "bind", Err: syscall.EADDRNOTAVAIL},
			}
		},
		func(_ context.Context, network, addr string) (net.Conn, error) {
			atomic.AddInt32(&fallbackCalls, 1)
			fallbackNetwork = network
			if addr != "example.com:443" {
				t.Fatalf("fallback addr = %q", addr)
			}
			left, right := net.Pipe()
			_ = right.Close()
			return left, nil
		},
		"65.109.249.3",
		true,
		sourceIPBindFallbackEnabled(),
	)

	conn, err := dial(context.Background(), "tcp", "example.com:443")
	if err != nil {
		t.Fatalf("dial returned error: %v", err)
	}
	if conn == nil {
		t.Fatal("dial returned nil conn")
	}
	_ = conn.Close()

	if got := atomic.LoadInt32(&primaryCalls); got != 1 {
		t.Fatalf("primary calls = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&fallbackCalls); got != 1 {
		t.Fatalf("fallback calls = %d, want 1", got)
	}
	if primaryNetwork != "tcp4" {
		t.Fatalf("primary network = %q, want tcp4", primaryNetwork)
	}
	if fallbackNetwork != "tcp4" {
		t.Fatalf("fallback network = %q, want tcp4", fallbackNetwork)
	}
}

func TestSourceIPDialContextDoesNotFallbackForOtherErrors(t *testing.T) {
	t.Parallel()

	primaryCalls := int32(0)
	fallbackCalls := int32(0)
	expectedErr := errors.New("connection refused")

	dial := sourceIPDialContext(
		func(_ context.Context, network, addr string) (net.Conn, error) {
			atomic.AddInt32(&primaryCalls, 1)
			if network != "tcp" {
				t.Fatalf("primary network = %q, want tcp", network)
			}
			if addr != "example.com:443" {
				t.Fatalf("primary addr = %q", addr)
			}
			return nil, expectedErr
		},
		func(_ context.Context, network, addr string) (net.Conn, error) {
			atomic.AddInt32(&fallbackCalls, 1)
			return nil, nil
		},
		"65.109.249.3",
		false,
		sourceIPBindFallbackEnabled(),
	)

	conn, err := dial(context.Background(), "tcp", "example.com:443")
	if !errors.Is(err, expectedErr) {
		t.Fatalf("dial error = %v, want %v", err, expectedErr)
	}
	if conn != nil {
		t.Fatal("expected nil conn on primary failure")
	}
	if got := atomic.LoadInt32(&primaryCalls); got != 1 {
		t.Fatalf("primary calls = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&fallbackCalls); got != 0 {
		t.Fatalf("fallback calls = %d, want 0", got)
	}
}

func TestSourceIPDialContextDoesNotFallbackOnBindFailureByDefault(t *testing.T) {
	primaryCalls := int32(0)
	fallbackCalls := int32(0)

	dial := sourceIPDialContext(
		func(_ context.Context, network, addr string) (net.Conn, error) {
			atomic.AddInt32(&primaryCalls, 1)
			if network != "tcp4" {
				t.Fatalf("primary network = %q, want tcp4", network)
			}
			if addr != "example.com:443" {
				t.Fatalf("primary addr = %q", addr)
			}
			return nil, &net.OpError{
				Op:  "dial",
				Net: network,
				Err: &os.SyscallError{Syscall: "bind", Err: syscall.EADDRNOTAVAIL},
			}
		},
		func(_ context.Context, network, addr string) (net.Conn, error) {
			atomic.AddInt32(&fallbackCalls, 1)
			return nil, nil
		},
		"65.109.249.3",
		true,
		sourceIPBindFallbackEnabled(),
	)

	conn, err := dial(context.Background(), "tcp", "example.com:443")
	if !errors.Is(err, syscall.EADDRNOTAVAIL) {
		t.Fatalf("dial error = %v, want EADDRNOTAVAIL", err)
	}
	if conn != nil {
		t.Fatal("expected nil conn when source-ip fallback is disabled")
	}
	if got := atomic.LoadInt32(&primaryCalls); got != 1 {
		t.Fatalf("primary calls = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&fallbackCalls); got != 0 {
		t.Fatalf("fallback calls = %d, want 0", got)
	}
}
