package proxy

import (
	"context"
	"fmt"
	xproxy "golang.org/x/net/proxy"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

type config struct {
	enabled bool
	url     *url.URL
}

var current atomic.Pointer[config]

func Validate(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil && u.User.Username() == "" {
		return "", fmt.Errorf("代理地址格式无效")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
		return u.String(), nil
	default:
		return "", fmt.Errorf("代理协议仅支持 http、https、socks5 或 socks5h")
	}
}

func Apply(enabled bool, raw string) error {
	if !enabled {
		current.Store(&config{})
		return nil
	}
	value, err := Validate(raw)
	if err != nil {
		return err
	}
	if value == "" {
		return fmt.Errorf("启用代理时必须填写代理地址")
	}
	u, err := url.Parse(value)
	if err != nil {
		return err
	}
	current.Store(&config{enabled: true, url: u})
	return nil
}

func Enabled() bool {
	c := current.Load()
	return c != nil && c.enabled
}

func URL() string {
	c := current.Load()
	if c == nil || !c.enabled || c.url == nil {
		return ""
	}
	return c.url.String()
}

// ProxyFunc returns the configured proxy and fails closed when it is enabled.
func ProxyFunc() func(*http.Request) (*url.URL, error) {
	return func(*http.Request) (*url.URL, error) {
		c := current.Load()
		if c == nil || !c.enabled || c.url == nil {
			return nil, nil
		}
		return c.url, nil
	}
}

func DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	c := current.Load()
	if c == nil || !c.enabled || c.url == nil {
		return (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, address)
	}
	switch strings.ToLower(c.url.Scheme) {
	case "socks5", "socks5h":
		var auth *xproxy.Auth
		if c.url.User != nil {
			password, _ := c.url.User.Password()
			auth = &xproxy.Auth{User: c.url.User.Username(), Password: password}
		}
		forward := xproxy.Dialer(xproxy.Direct)
		if c.url.Scheme == "socks5h" {
			forward = localResolvingDialer{}
		}
		dialer, err := xproxy.SOCKS5("tcp", c.url.Host, auth, forward)
		if err != nil {
			return nil, err
		}
		if contextual, ok := dialer.(xproxy.ContextDialer); ok {
			return contextual.DialContext(ctx, network, address)
		}
		return dialer.Dial(network, address)
	default:
		return (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, address)
	}
}

type localResolvingDialer struct{}

func (localResolvingDialer) Dial(network, address string) (net.Conn, error) {
	return (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).Dial(network, address)
}

func (localResolvingDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, address)
}

func Transport() *http.Transport {
	c := current.Load()
	if c != nil && c.enabled && c.url != nil && (c.url.Scheme == "socks5" || c.url.Scheme == "socks5h") {
		return &http.Transport{DialContext: DialContext}
	}
	return &http.Transport{Proxy: ProxyFunc(), DialContext: DialContext}
}

func Client(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: Transport()}
}

func EffectiveURL(accountProxy string) string {
	if Enabled() {
		return URL()
	}
	return accountProxy
}
