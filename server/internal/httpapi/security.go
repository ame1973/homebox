package httpapi

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const sessionCookieName = "homebox_session"
const supportedClientVersion = "0.1.0"

var errRequestOrigin = errors.New("request does not match the configured application origin")

// PublicOrigin is fixed by the operator, never learned from request/proxy headers.
// HTTP is allowed only for same-machine loopback development, without proxies.
type originPolicy struct {
	origin  string
	secure  bool
	proxies []netip.Prefix
}

func newOriginPolicy(origin string, proxies []netip.Prefix) (originPolicy, error) {
	canonical, local, err := canonicalOrigin(origin)
	if err != nil {
		return originPolicy{}, err
	}
	secure := strings.HasPrefix(canonical, "https://")
	if !secure && (!local || len(proxies) != 0) {
		return originPolicy{}, errors.New("HTTP is restricted to direct loopback development; use HTTPS for LAN/VPN")
	}
	for _, proxy := range proxies {
		if !proxy.IsValid() || proxy.Bits() == 0 || proxy.Addr().Zone() != "" {
			return originPolicy{}, errors.New("trusted proxies must be explicit nonempty IP prefixes")
		}
	}
	return originPolicy{origin: canonical, secure: secure, proxies: append([]netip.Prefix(nil), proxies...)}, nil
}

func canonicalOrigin(value string) (string, bool, error) {
	u, err := url.Parse(value)
	if err != nil || u == nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" ||
		u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(value, "#") || u.Opaque != "" || strings.HasSuffix(u.Host, ":") {
		return "", false, errors.New("public origin must be an absolute HTTP(S) origin without credentials, path, query, or fragment")
	}
	host := u.Hostname()
	for i := range len(host) {
		if host[i] >= 0x80 {
			return "", false, errors.New("public origin requires an ASCII hostname or IP address")
		}
	}
	host = strings.ToLower(host)
	local := host == "localhost"
	if address, err := netip.ParseAddr(host); err == nil {
		if address.Zone() != "" {
			return "", false, errors.New("public origin cannot contain an IP zone")
		}
		local = address.IsLoopback()
		host = address.String()
		if address.Is6() {
			host = "[" + host + "]"
		}
	} else {
		if strings.ContainsAny(u.Host, "[]") || len(host) > 253 {
			return "", false, errors.New("invalid public origin hostname")
		}
		for _, label := range strings.Split(host, ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return "", false, errors.New("invalid public origin hostname")
			}
			for _, ch := range label {
				if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
					return "", false, errors.New("public origin requires an ASCII hostname or IP address")
				}
			}
		}
	}
	if port := u.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return "", false, errors.New("invalid public origin port")
		}
		if !(u.Scheme == "https" && number == 443 || u.Scheme == "http" && number == 80) {
			host += ":" + strconv.Itoa(number)
		}
	}
	return u.Scheme + "://" + host, local, nil
}

func socketPeer(r *http.Request) (netip.Addr, error) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, errRequestOrigin
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, errRequestOrigin
	}
	return address.WithZone("").Unmap(), nil
}

func (p originPolicy) trusted(address netip.Addr) bool {
	for _, prefix := range p.proxies {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func (p originPolicy) https(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	peer, err := socketPeer(r)
	return err == nil && p.trusted(peer) && len(r.Header.Values("X-Forwarded-Proto")) == 1 && r.Header.Get("X-Forwarded-Proto") == "https"
}

func (p originPolicy) check(r *http.Request) (netip.Addr, error) {
	peer, err := socketPeer(r)
	if err != nil {
		return netip.Addr{}, err
	}
	scheme := "https"
	if !p.secure {
		scheme = "http"
		if !peer.IsLoopback() || r.TLS != nil {
			return netip.Addr{}, errRequestOrigin
		}
	} else if !p.https(r) {
		return netip.Addr{}, errRequestOrigin
	}
	requestOrigin, _, err := canonicalOrigin(scheme + "://" + r.Host)
	if err != nil || requestOrigin != p.origin || r.URL.User != nil || (r.URL.IsAbs() && r.URL.Scheme != scheme) {
		return netip.Addr{}, errRequestOrigin
	}
	origins := r.Header.Values("Origin")
	unsafe := r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
	if len(origins) > 1 || unsafe && len(origins) != 1 || len(origins) == 1 && origins[0] != p.origin {
		return netip.Addr{}, errRequestOrigin
	}
	if values := r.Header.Values("Sec-Fetch-Site"); len(values) > 1 || len(values) == 1 && values[0] != "same-origin" && values[0] != "none" {
		return netip.Addr{}, errRequestOrigin
	}
	// Only an explicitly trusted socket peer can supply a forwarding chain. Walk
	// right to left and stop at the nearest untrusted hop, never the leftmost claim.
	if p.trusted(peer) {
		values := r.Header.Values("X-Forwarded-For")
		if len(values) > 1 {
			return netip.Addr{}, errRequestOrigin
		}
		if len(values) == 1 {
			parts := strings.Split(values[0], ",")
			if len(values[0]) > 1024 || len(parts) > 8 {
				return netip.Addr{}, errRequestOrigin
			}
			for i := len(parts) - 1; i >= 0 && p.trusted(peer); i-- {
				next, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
				if err != nil || next.Zone() != "" || next.IsUnspecified() {
					return netip.Addr{}, errRequestOrigin
				}
				peer = next.Unmap()
			}
		}
	}
	return peer, nil
}

type rateWindow struct {
	count int
	reset time.Time
}

type requestLimiter struct {
	mu     sync.Mutex
	peers  map[netip.Addr]*[2]rateWindow
	global [2]rateWindow
	now    func() time.Time
}

func newRequestLimiter() *requestLimiter {
	return &requestLimiter{peers: make(map[netip.Addr]*[2]rateWindow), now: time.Now}
}

// ponytail: bounded process-local fixed windows suit one household; persistent
// admission state is required before supporting hostile public-network exposure.
func (l *requestLimiter) allow(peer netip.Addr, credential bool) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	kind, perPeer, global := 0, 60, 600
	if credential {
		kind, perPeer, global = 1, 5, 30
	}
	windows := l.peers[peer]
	if windows == nil {
		if len(l.peers) >= 1024 {
			for address, candidate := range l.peers {
				if !now.Before(candidate[0].reset) && !now.Before(candidate[1].reset) {
					delete(l.peers, address)
				}
			}
			if len(l.peers) >= 1024 {
				return 60
			}
		}
		windows = new([2]rateWindow)
		l.peers[peer] = windows
	}
	local, total := &windows[kind], &l.global[kind]
	for _, window := range []*rateWindow{local, total} {
		if !now.Before(window.reset) {
			*window = rateWindow{reset: now.Add(time.Minute)}
		}
	}
	retry := 0
	for _, bound := range []struct {
		window *rateWindow
		limit  int
	}{{local, perPeer}, {total, global}} {
		if bound.window.count >= bound.limit {
			seconds := max(1, int((bound.window.reset.Sub(now)+time.Second-1)/time.Second))
			retry = max(retry, seconds)
		}
	}
	if retry == 0 {
		local.count++
		total.count++
	}
	return retry
}
