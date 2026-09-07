package swu

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestIKEPacketTunnelManagerUsesSOCKS5UDPProxyForIKETransport(t *testing.T) {
	proxy := newFakeSOCKS5UDPProxy(t)
	defer proxy.Close()

	manager := NewIKEPacketTunnelManager(IKEPacketTunnelManagerConfig{})
	transport, err := manager.ikeTransport(TunnelConfig{
		Proxy: &ProxyConfig{ID: "uk", Addr: proxy.Addr(), Enabled: true},
	}, IKETransportConfig{
		RemoteAddr:      "203.0.113.9:4500",
		Timeout:         time.Second,
		UseNonESPMarker: true,
	})
	if err != nil {
		t.Fatalf("ikeTransport() error = %v", err)
	}

	proxy.NextResponse([]byte{0, 0, 0, 0, 0x04, 0x05, 0x06})
	resp, err := transport.ExchangeIKE(context.Background(), []byte{0x01, 0x02, 0x03})
	if err != nil {
		t.Fatalf("ExchangeIKE() error = %v", err)
	}
	if !bytes.Equal(resp, []byte{0x04, 0x05, 0x06}) {
		t.Fatalf("response=%x", resp)
	}

	req := proxy.LastUDPRequest()
	if req.Target != "203.0.113.9:4500" {
		t.Fatalf("proxy target=%q", req.Target)
	}
	if !bytes.Equal(req.Payload, []byte{0, 0, 0, 0, 0x01, 0x02, 0x03}) {
		t.Fatalf("proxy payload=%x", req.Payload)
	}
}

func TestIKEPacketTunnelManagerUsesSOCKS5UDPProxyForESPTransport(t *testing.T) {
	proxy := newFakeSOCKS5UDPProxy(t)
	defer proxy.Close()

	manager := NewIKEPacketTunnelManager(IKEPacketTunnelManagerConfig{})
	transport, err := manager.espTransport(TunnelConfig{
		Proxy: &ProxyConfig{ID: "uk", Addr: proxy.Addr(), Enabled: true},
	}, ESPTransportConfig{
		RemoteAddr: "203.0.113.9:4500",
		Timeout:    time.Second,
	})
	if err != nil {
		t.Fatalf("espTransport() error = %v", err)
	}
	readWrite, ok := transport.(ESPPacketReadWriteTransport)
	if !ok {
		t.Fatalf("espTransport() type %T does not support reading ESP packets", transport)
	}

	proxy.NextResponse([]byte{0x87, 0x65, 0x43, 0x21, 0, 0, 0, 2, 0xcc})
	packet := []byte{0x12, 0x34, 0x56, 0x78, 0, 0, 0, 1}
	if err := readWrite.SendESPPacket(context.Background(), packet); err != nil {
		t.Fatalf("SendESPPacket() error = %v", err)
	}
	got, err := readWrite.ReadESPPacket(context.Background())
	if err != nil {
		t.Fatalf("ReadESPPacket() error = %v", err)
	}
	if !bytes.Equal(got, []byte{0x87, 0x65, 0x43, 0x21, 0, 0, 0, 2, 0xcc}) {
		t.Fatalf("ReadESPPacket()=%x", got)
	}

	req := proxy.LastUDPRequest()
	if req.Target != "203.0.113.9:4500" {
		t.Fatalf("proxy target=%q", req.Target)
	}
	if !bytes.Equal(req.Payload, packet) {
		t.Fatalf("proxy payload=%x", req.Payload)
	}
}

type fakeSOCKS5UDPProxy struct {
	t         *testing.T
	tcp       net.Listener
	udp       net.PacketConn
	response  chan []byte
	requests  chan fakeSOCKS5UDPRequest
	closeDone chan struct{}
}

type fakeSOCKS5UDPRequest struct {
	Target  string
	Payload []byte
}

func newFakeSOCKS5UDPProxy(t *testing.T) *fakeSOCKS5UDPProxy {
	t.Helper()
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen(tcp) error = %v", err)
	}
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		_ = tcp.Close()
		t.Fatalf("ListenPacket(udp) error = %v", err)
	}
	p := &fakeSOCKS5UDPProxy{
		t:         t,
		tcp:       tcp,
		udp:       udp,
		response:  make(chan []byte, 4),
		requests:  make(chan fakeSOCKS5UDPRequest, 4),
		closeDone: make(chan struct{}),
	}
	go p.serveTCP()
	go p.serveUDP()
	return p
}

func (p *fakeSOCKS5UDPProxy) Addr() string {
	return p.tcp.Addr().String()
}

func (p *fakeSOCKS5UDPProxy) NextResponse(payload []byte) {
	p.response <- append([]byte(nil), payload...)
}

func (p *fakeSOCKS5UDPProxy) LastUDPRequest() fakeSOCKS5UDPRequest {
	select {
	case req := <-p.requests:
		return req
	case <-time.After(time.Second):
		p.t.Fatal("timed out waiting for SOCKS5 UDP request")
		return fakeSOCKS5UDPRequest{}
	}
}

func (p *fakeSOCKS5UDPProxy) Close() {
	_ = p.tcp.Close()
	_ = p.udp.Close()
	select {
	case <-p.closeDone:
	case <-time.After(time.Second):
	}
}

func (p *fakeSOCKS5UDPProxy) serveTCP() {
	defer close(p.closeDone)
	for {
		conn, err := p.tcp.Accept()
		if err != nil {
			return
		}
		go p.handleTCP(conn)
	}
}

func (p *fakeSOCKS5UDPProxy) handleTCP(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	header := make([]byte, 2)
	if _, err := io.ReadFull(conn, header); err != nil {
		return
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return
	}
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		return
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(conn, req); err != nil {
		return
	}
	if req[0] != 0x05 || req[1] != 0x03 {
		return
	}
	if err := discardSOCKS5Addr(conn, req[3]); err != nil {
		return
	}
	host, port, _ := net.SplitHostPort(p.udp.LocalAddr().String())
	ip := net.ParseIP(host).To4()
	if ip == nil {
		ip = []byte{127, 0, 0, 1}
	}
	portNum := mustPort(port)
	resp := []byte{0x05, 0x00, 0x00, 0x01, ip[0], ip[1], ip[2], ip[3], byte(portNum >> 8), byte(portNum)}
	if _, err := conn.Write(resp); err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, conn)
}

func (p *fakeSOCKS5UDPProxy) serveUDP() {
	buf := make([]byte, 4096)
	for {
		n, addr, err := p.udp.ReadFrom(buf)
		if err != nil {
			return
		}
		target, payload, err := parseFakeSOCKS5UDPFrame(buf[:n])
		if err != nil {
			p.t.Errorf("parse SOCKS5 UDP frame: %v", err)
			return
		}
		p.requests <- fakeSOCKS5UDPRequest{Target: target, Payload: append([]byte(nil), payload...)}
		response := <-p.response
		frame, err := buildFakeSOCKS5UDPFrame(target, response)
		if err != nil {
			p.t.Errorf("build SOCKS5 UDP frame: %v", err)
			return
		}
		if _, err := p.udp.WriteTo(frame, addr); err != nil {
			return
		}
	}
}

func discardSOCKS5Addr(r io.Reader, atyp byte) error {
	switch atyp {
	case 0x01:
		buf := make([]byte, 6)
		_, err := io.ReadFull(r, buf)
		return err
	case 0x03:
		var l [1]byte
		if _, err := io.ReadFull(r, l[:]); err != nil {
			return err
		}
		buf := make([]byte, int(l[0])+2)
		_, err := io.ReadFull(r, buf)
		return err
	case 0x04:
		buf := make([]byte, 18)
		_, err := io.ReadFull(r, buf)
		return err
	default:
		return errors.New("unsupported ATYP")
	}
}

func parseFakeSOCKS5UDPFrame(frame []byte) (string, []byte, error) {
	if len(frame) < 4 || frame[0] != 0 || frame[1] != 0 || frame[2] != 0 {
		return "", nil, errors.New("invalid SOCKS5 UDP header")
	}
	target, off, err := parseFakeSOCKS5Addr(frame, 3)
	if err != nil {
		return "", nil, err
	}
	return target, frame[off:], nil
}

func parseFakeSOCKS5Addr(frame []byte, off int) (string, int, error) {
	if off >= len(frame) {
		return "", 0, errors.New("missing ATYP")
	}
	atyp := frame[off]
	off++
	var host string
	switch atyp {
	case 0x01:
		if len(frame) < off+4+2 {
			return "", 0, errors.New("short IPv4 frame")
		}
		host = net.IP(frame[off : off+4]).String()
		off += 4
	case 0x03:
		if len(frame) < off+1 {
			return "", 0, errors.New("short domain length")
		}
		l := int(frame[off])
		off++
		if len(frame) < off+l+2 {
			return "", 0, errors.New("short domain frame")
		}
		host = string(frame[off : off+l])
		off += l
	case 0x04:
		if len(frame) < off+16+2 {
			return "", 0, errors.New("short IPv6 frame")
		}
		host = net.IP(frame[off : off+16]).String()
		off += 16
	default:
		return "", 0, errors.New("unsupported ATYP")
	}
	port := binary.BigEndian.Uint16(frame[off : off+2])
	off += 2
	return net.JoinHostPort(host, stringPort(port)), off, nil
}

func buildFakeSOCKS5UDPFrame(target string, payload []byte) ([]byte, error) {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return nil, err
	}
	out := []byte{0, 0, 0}
	if ip := net.ParseIP(host).To4(); ip != nil {
		out = append(out, 0x01)
		out = append(out, ip...)
	} else {
		out = append(out, 0x03, byte(len(host)))
		out = append(out, []byte(host)...)
	}
	portNum := mustPort(port)
	out = append(out, byte(portNum>>8), byte(portNum))
	out = append(out, payload...)
	return out, nil
}

func mustPort(port string) uint16 {
	var n uint16
	for _, c := range []byte(port) {
		n = n*10 + uint16(c-'0')
	}
	return n
}

func stringPort(port uint16) string {
	if port == 0 {
		return "0"
	}
	var buf [5]byte
	i := len(buf)
	for port > 0 {
		i--
		buf[i] = byte('0' + port%10)
		port /= 10
	}
	return string(buf[i:])
}
