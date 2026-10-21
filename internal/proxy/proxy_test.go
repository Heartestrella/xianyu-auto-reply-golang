package proxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestProxyFuncUsesConfiguredProxy(t *testing.T) {
	if err := Apply(true, "http://127.0.0.1:8123"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Apply(false, "") })
	u, err := ProxyFunc()(&http.Request{URL: &url.URL{Scheme: "https", Host: "example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if u == nil || u.Host != "127.0.0.1:8123" {
		t.Fatalf("proxy = %v", u)
	}
}

func TestSOCKSProxyDoesNotDialDestinationDirectly(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	proxyAddress := listener.Addr().String()
	if err := Apply(true, "socks5://user:pass@"+proxyAddress); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Apply(false, "") })
	dialDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 3)
		_, readErr := io.ReadFull(conn, buf)
		dialDone <- readErr
	}()
	_, _ = DialContext(context.Background(), "tcp", "203.0.113.99:443")
	select {
	case dialErr := <-dialDone:
		if dialErr != nil {
			t.Fatal(dialErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SOCKS5 proxy endpoint was never contacted")
	}
}
