package netguard

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestInternal(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1": true, "10.43.0.10": true, "172.16.5.4": true, "192.168.1.1": true,
		"169.254.169.254": true, "100.101.102.103": true, "0.0.0.0": true, "::1": true,
		"fd7a:115c:a1e0::1": true, "fe80::1": true, "::ffff:10.0.0.1": true,
		"204.228.156.36": false, "1.1.1.1": false, "2606:4700:4700::1111": false,
	} {
		if got := Internal(netip.MustParseAddr(addr)); got != want {
			t.Errorf("Internal(%s) = %v, want %v", addr, got, want)
		}
	}
}

// The client refuses to dial loopback; AllowPrivate turns that off.
func TestClientRefusesInternal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	_, err := Client.Get(srv.URL)
	if !errors.Is(err, ErrInternal) {
		t.Fatalf("GET %s: err = %v, want ErrInternal", srv.URL, err)
	}
	AllowPrivate = true
	defer func() { AllowPrivate = false }()
	resp, err := Client.Get(srv.URL)
	if err != nil {
		t.Fatalf("with AllowPrivate: %v", err)
	}
	resp.Body.Close()
}

func TestCheckURL(t *testing.T) {
	ctx := context.Background()
	for _, u := range []string{"http://127.0.0.1:8080/x", "http://localhost/hook", "http://[::1]/", "https://169.254.169.254/latest"} {
		if err := CheckURL(ctx, u); !errors.Is(err, ErrInternal) {
			t.Errorf("CheckURL(%s) = %v, want ErrInternal", u, err)
		}
	}
	for _, u := range []string{"https://1.1.1.1/", "https://unresolvable.example.test/hook"} {
		if err := CheckURL(ctx, u); err != nil {
			t.Errorf("CheckURL(%s) = %v, want allowed", u, err)
		}
	}
}
