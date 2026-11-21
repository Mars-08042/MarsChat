// Package clientip 解析请求的客户端 IP，用于按 IP 限流。
//
// X-Forwarded-For 可以被客户端随意伪造，只有当直接连接方是可信代理（网关）时才读取它：
// 从右往左跳过可信代理，第一个不可信的地址就是真实客户端。
package clientip

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"

	khttp "github.com/go-kratos/kratos/v3/transport/http"
	"google.golang.org/grpc/peer"
)

// Resolver 根据可信代理列表解析客户端 IP。
type Resolver struct {
	trusted []netip.Prefix
}

// NewResolver 解析 CIDR 列表（例如 10.0.0.0/8），也接受单个 IP。
func NewResolver(cidrs []string) (*Resolver, error) {
	r := &Resolver{}
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !strings.Contains(c, "/") {
			addr, err := netip.ParseAddr(c)
			if err != nil {
				return nil, fmt.Errorf("parse trusted proxy %q: %w", c, err)
			}
			r.trusted = append(r.trusted, netip.PrefixFrom(addr, addr.BitLen()))
			continue
		}
		p, err := netip.ParsePrefix(c)
		if err != nil {
			return nil, fmt.Errorf("parse trusted proxy %q: %w", c, err)
		}
		r.trusted = append(r.trusted, p.Masked())
	}
	return r, nil
}

// FromContext 返回客户端 IP，无法确定时返回空字符串。
func (r *Resolver) FromContext(ctx context.Context) string {
	if req, ok := khttp.RequestFromServerContext(ctx); ok {
		return r.resolve(req.RemoteAddr, req.Header.Values("X-Forwarded-For"))
	}
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		// gRPC 只在服务之间使用，直接取对端地址
		return hostOf(p.Addr.String())
	}
	return ""
}

func (r *Resolver) resolve(remoteAddr string, xff []string) string {
	remote := hostOf(remoteAddr)
	if !r.isTrusted(remote) {
		return remote
	}
	var hops []string
	for _, v := range xff {
		for _, h := range strings.Split(v, ",") {
			if h = strings.TrimSpace(h); h != "" {
				hops = append(hops, h)
			}
		}
	}
	for i := len(hops) - 1; i >= 0; i-- {
		if !r.isTrusted(hops[i]) {
			return hops[i]
		}
	}
	return remote
}

func (r *Resolver) isTrusted(ip string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, p := range r.trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func hostOf(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}
