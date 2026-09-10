package swu

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/iniwex5/vowifi-go/engine/swu/ikev2"
)

const (
	socks5Version          = 0x05
	socks5AuthNone         = 0x00
	socks5AuthUserPassword = 0x02
	socks5AuthNoAcceptable = 0xff
	socks5UserPassVersion  = 0x01
	socks5CmdUDPAssociate  = 0x03
	socks5AtypIPv4         = 0x01
	socks5AtypDomain       = 0x03
	socks5AtypIPv6         = 0x04
	socks5ReplySuccess     = 0x00
)

var errSOCKS5UDPClosed = errors.New("socks5 udp association closed")

type socks5UDPAssociation struct {
	proxy  ProxyConfig
	tcp    net.Conn
	udp    *net.UDPConn
	relay  *net.UDPAddr
	remote string
	mu     sync.Mutex
	closed bool
}

type socks5UDPIKETransport struct {
	Proxy           *ProxyConfig
	RemoteAddr      string
	Timeout         time.Duration
	UseNonESPMarker bool
	ReadBufferSize  int
}

var _ ikev2.InitTransport = (*socks5UDPIKETransport)(nil)

func (t *socks5UDPIKETransport) ExchangeIKE(ctx context.Context, request []byte) ([]byte, error) {
	if t == nil {
		return nil, fmt.Errorf("%w: socks5 ike transport is nil", ErrInvalidIKETunnelManager)
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	assoc, err := newSOCKS5UDPAssociation(ctx, t.Proxy, timeout)
	if err != nil {
		return nil, t.wrapIKEExchangeError("association", err, nil, len(request), timeout)
	}
	defer assoc.Close(ctx)
	wire := request
	if t.UseNonESPMarker {
		wire = append([]byte{0, 0, 0, 0}, request...)
	}
	if err := assoc.WriteToTarget(ctx, t.RemoteAddr, wire, timeout); err != nil {
		return nil, t.wrapIKEExchangeError("write", err, assoc, len(request), timeout)
	}
	payload, err := assoc.ReadFromTarget(ctx, t.RemoteAddr, timeout, t.ReadBufferSize)
	if err != nil {
		return nil, t.wrapIKEExchangeError("read", err, assoc, len(request), timeout)
	}
	if len(payload) >= 4 && payload[0] == 0 && payload[1] == 0 && payload[2] == 0 && payload[3] == 0 {
		payload = payload[4:]
	}
	return payload, nil
}

func (t *socks5UDPIKETransport) wrapIKEExchangeError(stage string, err error, assoc *socks5UDPAssociation, requestLen int, timeout time.Duration) error {
	if err == nil {
		return nil
	}
	local := ""
	relay := ""
	if assoc != nil {
		if assoc.udp != nil && assoc.udp.LocalAddr() != nil {
			local = assoc.udp.LocalAddr().String()
		}
		if assoc.relay != nil {
			relay = assoc.relay.String()
		}
	}
	return fmt.Errorf("socks5 udp ike %s failed: target=%s proxy=%s relay=%s local_udp=%s non_esp_marker=%t timeout=%s request_len=%d: %w",
		stage,
		strings.TrimSpace(t.RemoteAddr),
		socks5ProxyLabel(t.Proxy),
		relay,
		local,
		t.UseNonESPMarker,
		timeout.String(),
		requestLen,
		err)
}

type SOCKS5UDPESPPacketTransport struct {
	Proxy          *ProxyConfig
	RemoteAddr     string
	Timeout        time.Duration
	ReadBufferSize int

	mu     sync.Mutex
	assoc  *socks5UDPAssociation
	closed bool
}

var (
	_ ESPPacketReadWriteTransport = (*SOCKS5UDPESPPacketTransport)(nil)
	_ ESPPacketTransportCloser    = (*SOCKS5UDPESPPacketTransport)(nil)
)

func (t *SOCKS5UDPESPPacketTransport) SendESPPacket(ctx context.Context, packet []byte) error {
	if t == nil {
		return ErrInvalidPacketTunnel
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := contextReady(ctx); err != nil {
		return err
	}
	if len(packet) < 8 {
		return fmt.Errorf("%w: ESP packet too short", ErrInvalidPacketTunnel)
	}
	if isNonESPMarker(packet) {
		return fmt.Errorf("%w: non-ESP marker cannot be sent as ESP", ErrInvalidPacketTunnel)
	}
	assoc, err := t.getAssoc(ctx)
	if err != nil {
		return err
	}
	return assoc.WriteToTarget(ctx, t.RemoteAddr, packet, t.effectiveTimeout())
}

func (t *SOCKS5UDPESPPacketTransport) ReadESPPacket(ctx context.Context) ([]byte, error) {
	if t == nil {
		return nil, ErrInvalidPacketTunnel
	}
	if ctx == nil {
		ctx = context.Background()
	}
	assoc, err := t.getAssoc(ctx)
	if err != nil {
		return nil, err
	}
	size := t.ReadBufferSize
	if size <= 0 {
		size = 64 * 1024
	}
	for {
		if err := contextReady(ctx); err != nil {
			return nil, err
		}
		wire, err := assoc.ReadFromTarget(ctx, t.RemoteAddr, t.effectiveTimeout(), size)
		if err != nil {
			return nil, err
		}
		switch {
		case isNATTKeepalive(wire):
			continue
		case isNonESPMarker(wire):
			continue
		case len(wire) < 8:
			return nil, fmt.Errorf("%w: ESP packet too short", ErrInvalidPacketTunnel)
		default:
			return append([]byte(nil), wire...), nil
		}
	}
}

func (t *SOCKS5UDPESPPacketTransport) Close(ctx context.Context) error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	assoc := t.assoc
	t.assoc = nil
	t.mu.Unlock()
	if assoc != nil {
		return assoc.Close(ctx)
	}
	return nil
}

func (t *SOCKS5UDPESPPacketTransport) LocalNetworkAddr() net.Addr {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.assoc == nil || t.assoc.udp == nil {
		return nil
	}
	return t.assoc.udp.LocalAddr()
}

func (t *SOCKS5UDPESPPacketTransport) getAssoc(ctx context.Context) (*socks5UDPAssociation, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, ErrPacketTunnelClosed
	}
	if t.assoc != nil {
		return t.assoc, nil
	}
	assoc, err := newSOCKS5UDPAssociation(ctx, t.Proxy, t.effectiveTimeout())
	if err != nil {
		return nil, err
	}
	if t.closed {
		_ = assoc.Close(ctx)
		return nil, ErrPacketTunnelClosed
	}
	t.assoc = assoc
	return assoc, nil
}

func (t *SOCKS5UDPESPPacketTransport) effectiveTimeout() time.Duration {
	if t != nil && t.Timeout > 0 {
		return t.Timeout
	}
	return 8 * time.Second
}

func newSOCKS5UDPAssociation(ctx context.Context, proxy *ProxyConfig, timeout time.Duration) (*socks5UDPAssociation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	addr := socks5ProxyAddress(proxy)
	if addr == "" {
		return nil, fmt.Errorf("%w: socks5 proxy address is empty", ErrInvalidTunnelConfig)
	}
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	dialer := net.Dialer{Timeout: timeout}
	tcp, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("socks5 udp proxy %s tcp connect failed: %w", socks5ProxyLabel(proxy), err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = tcp.Close()
		}
	}()
	if err := tcp.SetDeadline(socks5Deadline(ctx, timeout)); err != nil {
		return nil, err
	}
	if err := socks5Handshake(tcp, proxy); err != nil {
		return nil, fmt.Errorf("socks5 udp proxy %s handshake failed: %w", socks5ProxyLabel(proxy), err)
	}
	relay, err := socks5UDPAssociate(tcp, addr)
	if err != nil {
		return nil, fmt.Errorf("socks5 udp proxy %s udp associate failed: %w", socks5ProxyLabel(proxy), err)
	}
	udp, err := net.DialUDP("udp", nil, relay)
	if err != nil {
		return nil, fmt.Errorf("socks5 udp proxy %s relay dial failed: %w", socks5ProxyLabel(proxy), err)
	}
	cleanup = false
	return &socks5UDPAssociation{
		proxy:  derefProxy(proxy),
		tcp:    tcp,
		udp:    udp,
		relay:  relay,
		remote: addr,
	}, nil
}

func (a *socks5UDPAssociation) WriteToTarget(ctx context.Context, target string, payload []byte, timeout time.Duration) error {
	if a == nil {
		return ErrInvalidPacketTunnel
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return errSOCKS5UDPClosed
	}
	frame, err := socks5UDPFrame(target, payload)
	if err != nil {
		return err
	}
	if err := a.udp.SetWriteDeadline(socks5Deadline(ctx, timeout)); err != nil {
		return err
	}
	_, err = a.udp.Write(frame)
	return transportNetError(ctx, err)
}

func (a *socks5UDPAssociation) ReadFromTarget(ctx context.Context, target string, timeout time.Duration, size int) ([]byte, error) {
	if a == nil {
		return nil, ErrInvalidPacketTunnel
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, errSOCKS5UDPClosed
	}
	if size <= 0 {
		size = 64 * 1024
	}
	buf := make([]byte, size)
	for {
		if err := contextReady(ctx); err != nil {
			return nil, err
		}
		if err := a.udp.SetReadDeadline(socks5Deadline(ctx, timeout)); err != nil {
			return nil, err
		}
		n, err := a.udp.Read(buf)
		if err != nil {
			return nil, transportNetError(ctx, err)
		}
		_, payload, err := parseSOCKS5UDPFrame(buf[:n])
		if err != nil {
			return nil, err
		}
		return payload, nil
	}
}

func (a *socks5UDPAssociation) Close(ctx context.Context) error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	udp := a.udp
	tcp := a.tcp
	a.udp = nil
	a.tcp = nil
	a.mu.Unlock()
	var err error
	if udp != nil {
		err = transportNetError(ctx, udp.Close())
	}
	if tcp != nil {
		if closeErr := transportNetError(ctx, tcp.Close()); err == nil {
			err = closeErr
		}
	}
	return err
}

func socks5Handshake(conn io.ReadWriter, proxy *ProxyConfig) error {
	methods := []byte{socks5AuthNone}
	if strings.TrimSpace(proxyUsername(proxy)) != "" {
		methods = []byte{socks5AuthUserPassword, socks5AuthNone}
	}
	req := make([]byte, 2+len(methods))
	req[0] = socks5Version
	req[1] = byte(len(methods))
	copy(req[2:], methods)
	if _, err := conn.Write(req); err != nil {
		return err
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return err
	}
	if resp[0] != socks5Version {
		return fmt.Errorf("version mismatch: got 0x%02x", resp[0])
	}
	switch resp[1] {
	case socks5AuthNone:
		return nil
	case socks5AuthUserPassword:
		return socks5UserPassAuth(conn, proxyUsername(proxy), proxyPassword(proxy))
	case socks5AuthNoAcceptable:
		return errors.New("no acceptable auth method")
	default:
		return fmt.Errorf("unsupported auth method 0x%02x", resp[1])
	}
}

func socks5UserPassAuth(conn io.ReadWriter, username, password string) error {
	if strings.TrimSpace(username) == "" {
		return errors.New("username/password auth requested without username")
	}
	if len(username) > 255 || len(password) > 255 {
		return errors.New("username/password is too long")
	}
	req := make([]byte, 0, 3+len(username)+len(password))
	req = append(req, socks5UserPassVersion, byte(len(username)))
	req = append(req, []byte(username)...)
	req = append(req, byte(len(password)))
	req = append(req, []byte(password)...)
	if _, err := conn.Write(req); err != nil {
		return err
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return err
	}
	if resp[0] != socks5UserPassVersion || resp[1] != 0x00 {
		return fmt.Errorf("username/password auth rejected: version=0x%02x status=0x%02x", resp[0], resp[1])
	}
	return nil
}

func socks5UDPAssociate(conn io.ReadWriter, proxyAddr string) (*net.UDPAddr, error) {
	req := []byte{socks5Version, socks5CmdUDPAssociate, 0x00, socks5AtypIPv4, 0, 0, 0, 0, 0, 0}
	if _, err := conn.Write(req); err != nil {
		return nil, err
	}
	respHeader := make([]byte, 4)
	if _, err := io.ReadFull(conn, respHeader); err != nil {
		return nil, err
	}
	if respHeader[0] != socks5Version {
		return nil, fmt.Errorf("version mismatch: got 0x%02x", respHeader[0])
	}
	if respHeader[1] != socks5ReplySuccess {
		return nil, fmt.Errorf("associate rejected: status=0x%02x", respHeader[1])
	}
	host, port, err := socks5ReadAddr(conn, respHeader[3])
	if err != nil {
		return nil, err
	}
	proxyHost, _, _ := net.SplitHostPort(proxyAddr)
	if shouldUseSOCKS5ProxyHostForRelay(host, proxyHost) {
		host = proxyHost
	}
	return net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(int(port))))
}

func shouldUseSOCKS5ProxyHostForRelay(relayHost, proxyHost string) bool {
	relayHost = strings.Trim(strings.TrimSpace(relayHost), "[]")
	proxyHost = strings.Trim(strings.TrimSpace(proxyHost), "[]")
	if proxyHost == "" {
		return false
	}
	if relayHost == "" || relayHost == "0.0.0.0" || relayHost == "::" {
		return true
	}
	relayIP := net.ParseIP(relayHost)
	if relayIP == nil || !relayIP.IsLoopback() {
		return false
	}
	proxyIP := net.ParseIP(proxyHost)
	return proxyIP == nil || !proxyIP.IsLoopback()
}

func socks5UDPFrame(target string, payload []byte) ([]byte, error) {
	host, port, err := splitHostPortRequired(target)
	if err != nil {
		return nil, err
	}
	out := []byte{0, 0, 0}
	out, err = appendSOCKS5Addr(out, host, port)
	if err != nil {
		return nil, err
	}
	return append(out, payload...), nil
}

func parseSOCKS5UDPFrame(frame []byte) (string, []byte, error) {
	if len(frame) < 4 || frame[0] != 0 || frame[1] != 0 {
		return "", nil, errors.New("invalid socks5 udp frame")
	}
	if frame[2] != 0 {
		return "", nil, errors.New("fragmented socks5 udp frame is unsupported")
	}
	host, port, off, err := parseSOCKS5Addr(frame, 3)
	if err != nil {
		return "", nil, err
	}
	return net.JoinHostPort(host, strconv.Itoa(int(port))), append([]byte(nil), frame[off:]...), nil
}

func appendSOCKS5Addr(out []byte, host string, port uint16) ([]byte, error) {
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			out = append(out, socks5AtypIPv4)
			out = append(out, v4...)
		} else {
			v6 := ip.To16()
			if v6 == nil {
				return nil, fmt.Errorf("invalid IP address %q", host)
			}
			out = append(out, socks5AtypIPv6)
			out = append(out, v6...)
		}
	} else {
		if len(host) > 255 {
			return nil, fmt.Errorf("domain is too long: %q", host)
		}
		out = append(out, socks5AtypDomain, byte(len(host)))
		out = append(out, []byte(host)...)
	}
	var p [2]byte
	binary.BigEndian.PutUint16(p[:], port)
	return append(out, p[:]...), nil
}

func socks5ReadAddr(r io.Reader, atyp byte) (string, uint16, error) {
	switch atyp {
	case socks5AtypIPv4:
		buf := make([]byte, 6)
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", 0, err
		}
		return net.IP(buf[:4]).String(), binary.BigEndian.Uint16(buf[4:6]), nil
	case socks5AtypDomain:
		var l [1]byte
		if _, err := io.ReadFull(r, l[:]); err != nil {
			return "", 0, err
		}
		buf := make([]byte, int(l[0])+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", 0, err
		}
		return string(buf[:len(buf)-2]), binary.BigEndian.Uint16(buf[len(buf)-2:]), nil
	case socks5AtypIPv6:
		buf := make([]byte, 18)
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", 0, err
		}
		return net.IP(buf[:16]).String(), binary.BigEndian.Uint16(buf[16:18]), nil
	default:
		return "", 0, fmt.Errorf("unsupported address type 0x%02x", atyp)
	}
}

func parseSOCKS5Addr(frame []byte, off int) (string, uint16, int, error) {
	if off >= len(frame) {
		return "", 0, 0, errors.New("missing socks5 address type")
	}
	atyp := frame[off]
	off++
	var host string
	switch atyp {
	case socks5AtypIPv4:
		if len(frame) < off+4+2 {
			return "", 0, 0, errors.New("short IPv4 socks5 address")
		}
		host = net.IP(frame[off : off+4]).String()
		off += 4
	case socks5AtypDomain:
		if len(frame) < off+1 {
			return "", 0, 0, errors.New("short domain socks5 address")
		}
		l := int(frame[off])
		off++
		if len(frame) < off+l+2 {
			return "", 0, 0, errors.New("short domain socks5 address")
		}
		host = string(frame[off : off+l])
		off += l
	case socks5AtypIPv6:
		if len(frame) < off+16+2 {
			return "", 0, 0, errors.New("short IPv6 socks5 address")
		}
		host = net.IP(frame[off : off+16]).String()
		off += 16
	default:
		return "", 0, 0, fmt.Errorf("unsupported address type 0x%02x", atyp)
	}
	port := binary.BigEndian.Uint16(frame[off : off+2])
	return host, port, off + 2, nil
}

func splitHostPortRequired(addr string) (string, uint16, error) {
	host, portText, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return "", 0, err
	}
	return strings.Trim(host, "[]"), uint16(port), nil
}

func socks5ProxyAddress(proxy *ProxyConfig) string {
	if proxy == nil || !proxy.Enabled {
		return ""
	}
	for _, raw := range []string{proxy.Addr, proxy.Address, proxy.URL} {
		if addr := normalizeSOCKS5ProxyAddress(raw); addr != "" {
			return addr
		}
	}
	return ""
}

func normalizeSOCKS5ProxyAddress(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" {
		if !strings.EqualFold(u.Scheme, "socks5") && !strings.EqualFold(u.Scheme, "socks") {
			return ""
		}
		return u.Host
	}
	return raw
}

func socks5ProxyLabel(proxy *ProxyConfig) string {
	if proxy == nil {
		return "<nil>"
	}
	id := strings.TrimSpace(proxy.ID)
	addr := socks5ProxyAddress(proxy)
	if id == "" {
		return addr
	}
	return id + "(" + addr + ")"
}

func proxyUsername(proxy *ProxyConfig) string {
	if proxy == nil {
		return ""
	}
	return strings.TrimSpace(proxy.Username)
}

func proxyPassword(proxy *ProxyConfig) string {
	if proxy == nil {
		return ""
	}
	return proxy.Password
}

func derefProxy(proxy *ProxyConfig) ProxyConfig {
	if proxy == nil {
		return ProxyConfig{}
	}
	return *proxy
}

func socks5Deadline(ctx context.Context, timeout time.Duration) time.Time {
	deadline := time.Time{}
	if ctx != nil {
		if d, ok := ctx.Deadline(); ok {
			deadline = d
		}
	}
	if timeout > 0 {
		t := time.Now().Add(timeout)
		if deadline.IsZero() || t.Before(deadline) {
			deadline = t
		}
	}
	return deadline
}
