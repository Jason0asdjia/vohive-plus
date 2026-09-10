//go:build live

package swu

import (
	"context"
	"encoding/binary"
	"net"
	"os"
	"testing"
	"time"

	"github.com/iniwex5/vowifi-go/engine/swu/ikev2"
)

func TestLiveSOCKS5UDPProxyDNS(t *testing.T) {
	proxyAddr := os.Getenv("VOHIVE_LIVE_SOCKS5_ADDR")
	if proxyAddr == "" {
		t.Skip("set VOHIVE_LIVE_SOCKS5_ADDR to a SOCKS5 address or direct")
	}
	target := os.Getenv("VOHIVE_LIVE_UDP_TARGET")
	if target == "" {
		target = "1.1.1.1:53"
	}
	dnsName := os.Getenv("VOHIVE_LIVE_DNS_NAME")
	if dnsName == "" {
		dnsName = "vohive-plus.example"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	assoc, err := newSOCKS5UDPAssociation(ctx, &ProxyConfig{
		ID:      "live",
		Addr:    proxyAddr,
		Enabled: true,
	}, 6*time.Second)
	if err != nil {
		t.Fatalf("newSOCKS5UDPAssociation() error = %v", err)
	}
	defer assoc.Close(ctx)
	t.Logf("proxy=%s relay=%s local=%s target=%s", proxyAddr, assoc.relay, assoc.udp.LocalAddr(), target)

	query := dnsAQuery(dnsName)
	if err := assoc.WriteToTarget(ctx, target, query, 6*time.Second); err != nil {
		t.Fatalf("WriteToTarget() error = %v", err)
	}
	resp, err := assoc.ReadFromTarget(ctx, target, 6*time.Second, 1500)
	if err != nil {
		t.Fatalf("ReadFromTarget() error = %v", err)
	}
	if len(resp) < 12 {
		t.Fatalf("short DNS response: %x", resp)
	}
	if binary.BigEndian.Uint16(resp[0:2]) != binary.BigEndian.Uint16(query[0:2]) {
		t.Fatalf("DNS transaction mismatch: query=%x response=%x", query[:2], resp[:2])
	}
	if resp[2]&0x80 == 0 {
		t.Fatalf("DNS response bit not set: flags=%x", resp[2:4])
	}
	t.Logf("received %d bytes from SOCKS5 UDP target", len(resp))
	if answers := dnsAAnswers(resp); len(answers) > 0 {
		t.Logf("A answers for %s: %v", dnsName, answers)
	}
}

func TestLiveSOCKS5UDPIKEInitToEPDG(t *testing.T) {
	proxyAddr := os.Getenv("VOHIVE_LIVE_SOCKS5_ADDR")
	if proxyAddr == "" {
		t.Skip("set VOHIVE_LIVE_SOCKS5_ADDR to run live SOCKS5 UDP test")
	}
	target := os.Getenv("VOHIVE_LIVE_IKE_TARGET")
	if target == "" {
		t.Skip("set VOHIVE_LIVE_IKE_TARGET to an ePDG host:port")
	}
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		t.Fatalf("invalid VOHIVE_LIVE_IKE_TARGET %q: %v", target, err)
	}
	remoteIP := net.ParseIP(host)
	if remoteIP == nil {
		addrs, err := net.LookupIP(host)
		if err != nil || len(addrs) == 0 {
			t.Fatalf("resolve %s: addrs=%v err=%v", host, addrs, err)
		}
		remoteIP = addrs[0]
	}
	remotePort, err := parsePort(port)
	if err != nil {
		t.Fatalf("invalid target port %q: %v", port, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	useNonESPMarker := os.Getenv("VOHIVE_LIVE_IKE_NON_ESP_MARKER") == "1"
	if os.Getenv("VOHIVE_LIVE_IKE_NON_ESP_MARKER") == "" {
		useNonESPMarker = remotePort == 4500
	}
	localPort := remotePort
	if raw := os.Getenv("VOHIVE_LIVE_IKE_LOCAL_PORT"); raw != "" {
		parsed, err := parsePort(raw)
		if err != nil {
			t.Fatalf("invalid VOHIVE_LIVE_IKE_LOCAL_PORT %q: %v", raw, err)
		}
		localPort = parsed
	}
	var transport ikev2.InitTransport
	if proxyAddr == "direct" {
		transport = ikev2.UDPTransport{
			RemoteAddr:      target,
			Timeout:         10 * time.Second,
			UseNonESPMarker: useNonESPMarker,
		}
	} else {
		transport = &socks5UDPIKETransport{
			Proxy: &ProxyConfig{
				ID:      "live",
				Addr:    proxyAddr,
				Enabled: true,
			},
			RemoteAddr:      target,
			Timeout:         10 * time.Second,
			UseNonESPMarker: useNonESPMarker,
		}
	}
	t.Logf("IKE_SA_INIT target=%s local_port=%d remote_port=%d non_esp_marker=%t", target, localPort, remotePort, useNonESPMarker)
	localIP := net.ParseIP("192.0.2.10")
	natDetection := os.Getenv("VOHIVE_LIVE_IKE_NAT_DETECTION")
	if natDetection == "0" {
		localIP = nil
		remoteIP = nil
		localPort = 0
		remotePort = 0
	}
	t.Logf("IKE_SA_INIT nat_detection=%t", natDetection != "0")
	res, err := ikev2.RunIKE_SA_INIT(ctx, ikev2.InitConfig{
		Transport:  transport,
		SA:         liveIKEProposal(t),
		LocalIP:    localIP,
		LocalPort:  localPort,
		RemoteIP:   remoteIP,
		RemotePort: remotePort,
	})
	if err != nil {
		t.Fatalf("IKE_SA_INIT via SOCKS5 UDP to %s failed: %v", target, err)
	}
	t.Logf("IKE_SA_INIT response from %s: responder_spi=%016x nat_detected=%t mobike=%t", target, res.ResponderSPI, res.NATDetected, res.MOBIKESupported)
}

func liveIKEProposal(t *testing.T) ikev2.SecurityAssociation {
	t.Helper()
	if os.Getenv("VOHIVE_LIVE_IKE_SUITE") == "aes256-sha256-prfsha512-modp2048" {
		return liveIKEProposalForGroup(ikev2.DHGroup2048BitMODP)
	}
	switch os.Getenv("VOHIVE_LIVE_IKE_DH_GROUP") {
	case "", "curve25519":
		return ikev2.SecurityAssociation{}
	case "modp1024":
		return liveIKEProposalForGroup(ikev2.DHGroup1024BitMODP)
	case "modp1536":
		return liveIKEProposalForGroup(ikev2.DHGroup1536BitMODP)
	case "ecp256":
		return liveIKEProposalForGroup(ikev2.DHGroup256BitECP)
	case "ecp384":
		return liveIKEProposalForGroup(ikev2.DHGroup384BitECP)
	case "ecp521":
		return liveIKEProposalForGroup(ikev2.DHGroup521BitECP)
	case "modp2048":
		return liveIKEProposalForGroup(ikev2.DHGroup2048BitMODP)
	default:
		t.Fatalf("unsupported VOHIVE_LIVE_IKE_DH_GROUP %q", os.Getenv("VOHIVE_LIVE_IKE_DH_GROUP"))
		return ikev2.SecurityAssociation{}
	}
}

func TestLiveIKEProposalExactAES256Suite(t *testing.T) {
	t.Setenv("VOHIVE_LIVE_IKE_SUITE", "aes256-sha256-prfsha512-modp2048")
	t.Setenv("VOHIVE_LIVE_IKE_DH_GROUP", "")
	sa := liveIKEProposal(t)
	if len(sa.Proposals) != 1 {
		t.Fatalf("proposal count=%d, want one exact suite", len(sa.Proposals))
	}
	transforms := sa.Proposals[0].Transforms
	if len(transforms) != 4 {
		t.Fatalf("transforms=%+v, want ENCR/PRF/INTEG/DH", transforms)
	}
	if transforms[0].Type != ikev2.TransformENCR || transforms[0].ID != ikev2.ENCR_AES_CBC || len(transforms[0].Attributes) != 1 || binary.BigEndian.Uint16(transforms[0].Attributes[0].Value) != 256 {
		t.Fatalf("encryption=%+v, want AES-CBC-256", transforms[0])
	}
	if transforms[1].ID != ikev2.PRF_HMAC_SHA2_512 || transforms[2].ID != ikev2.INTEG_HMAC_SHA2_256_128 || transforms[3].ID != ikev2.DHGroup2048BitMODP {
		t.Fatalf("transforms=%+v, want PRF512/SHA256/MODP2048", transforms)
	}
}

func liveIKEProposalForGroup(group uint16) ikev2.SecurityAssociation {
	prf := ikev2.PRF_HMAC_SHA2_256
	integ := ikev2.INTEG_HMAC_SHA2_256_128
	keyBits := uint16(128)
	switch os.Getenv("VOHIVE_LIVE_IKE_SUITE") {
	case "", "sha256":
	case "aes256-sha256-prfsha512-modp2048":
		keyBits = 256
		prf = ikev2.PRF_HMAC_SHA2_512
	case "sha1":
		prf = ikev2.PRF_HMAC_SHA1
		integ = ikev2.INTEG_HMAC_SHA1_96
	default:
		panic("unsupported VOHIVE_LIVE_IKE_SUITE")
	}
	return ikev2.SecurityAssociation{Proposals: []ikev2.Proposal{{
		Number:     1,
		ProtocolID: ikev2.ProtocolIKE,
		Transforms: []ikev2.Transform{
			{Type: ikev2.TransformENCR, ID: ikev2.ENCR_AES_CBC, Attributes: []ikev2.TransformAttribute{ikev2.KeyLengthAttribute(keyBits)}},
			{Type: ikev2.TransformPRF, ID: prf},
			{Type: ikev2.TransformINTEG, ID: integ},
			{Type: ikev2.TransformDHRGroup, ID: group},
		},
	}}}
}

func dnsAQuery(name string) []byte {
	msg := []byte{0x45, 0x23, 0x01, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	for _, part := range splitDNSName(name) {
		msg = append(msg, byte(len(part)))
		msg = append(msg, []byte(part)...)
	}
	msg = append(msg, 0x00, 0x00, 0x01, 0x00, 0x01)
	return msg
}

func splitDNSName(name string) []string {
	var parts []string
	for _, part := range splitNonEmpty(name, '.') {
		if len(part) > 63 {
			return nil
		}
		parts = append(parts, part)
	}
	return parts
}

func splitNonEmpty(s string, sep rune) []string {
	var out []string
	start := 0
	for i, r := range s {
		if r != sep {
			continue
		}
		if i > start {
			out = append(out, s[start:i])
		}
		start = i + len(string(r))
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func parsePort(s string) (uint16, error) {
	var n uint32
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, os.ErrInvalid
		}
		n = n*10 + uint32(r-'0')
		if n > 65535 {
			return 0, os.ErrInvalid
		}
	}
	if n == 0 {
		return 0, os.ErrInvalid
	}
	return uint16(n), nil
}

func dnsAAnswers(msg []byte) []string {
	if len(msg) < 12 {
		return nil
	}
	qd := int(binary.BigEndian.Uint16(msg[4:6]))
	an := int(binary.BigEndian.Uint16(msg[6:8]))
	off := 12
	for i := 0; i < qd; i++ {
		next, ok := skipDNSName(msg, off)
		if !ok || len(msg) < next+4 {
			return nil
		}
		off = next + 4
	}
	var out []string
	for i := 0; i < an; i++ {
		next, ok := skipDNSName(msg, off)
		if !ok || len(msg) < next+10 {
			return out
		}
		typ := binary.BigEndian.Uint16(msg[next : next+2])
		class := binary.BigEndian.Uint16(msg[next+2 : next+4])
		rdlen := int(binary.BigEndian.Uint16(msg[next+8 : next+10]))
		rdata := next + 10
		if len(msg) < rdata+rdlen {
			return out
		}
		if typ == 1 && class == 1 && rdlen == net.IPv4len {
			out = append(out, net.IP(msg[rdata:rdata+rdlen]).String())
		}
		off = rdata + rdlen
	}
	return out
}

func skipDNSName(msg []byte, off int) (int, bool) {
	for jumps := 0; ; jumps++ {
		if off >= len(msg) || jumps > 16 {
			return 0, false
		}
		l := int(msg[off])
		if l&0xc0 == 0xc0 {
			if off+1 >= len(msg) {
				return 0, false
			}
			return off + 2, true
		}
		off++
		if l == 0 {
			return off, true
		}
		if l&0xc0 != 0 || off+l > len(msg) {
			return 0, false
		}
		off += l
	}
}
