package ikev2

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdh"
	"encoding/hex"
	"errors"
	"net"
	"testing"
	"time"
)

type initFakeTransport struct {
	t            *testing.T
	group        uint16
	responderSPI uint64
	responderKey []byte
	nonceR       []byte
	remoteIP     net.IP
	remotePort   uint16
	localIP      net.IP
	localPort    uint16
	request      Message
}

func (f *initFakeTransport) ExchangeIKE(ctx context.Context, request []byte) ([]byte, error) {
	f.t.Helper()
	req, err := ParseMessage(request)
	if err != nil {
		return nil, err
	}
	f.request = req
	if req.Header.ExchangeType != ExchangeIKE_SA_INIT || req.Header.Flags&FlagInitiator == 0 {
		f.t.Fatalf("request header=%+v", req.Header)
	}
	if len(req.Payloads) < 3 || req.Payloads[0].Type != PayloadSA || req.Payloads[1].Type != PayloadKE || req.Payloads[2].Type != PayloadNonce {
		f.t.Fatalf("request payloads=%+v", req.Payloads)
	}
	group := f.group
	if group == 0 {
		group = DHGroupCurve25519
	}
	curve, err := ecdhCurveForGroup(group)
	if err != nil {
		return nil, err
	}
	privR, err := curve.NewPrivateKey(f.responderKey)
	if err != nil {
		return nil, err
	}
	pubR, err := ikePublicKeyData(group, privR.PublicKey().Bytes())
	if err != nil {
		return nil, err
	}
	payloads := []Payload{
		req.Payloads[0],
		KeyExchangePayload(group, pubR),
		NoncePayload(f.nonceR),
	}
	src, err := NATDetectionNotify(NotifyNATDetectionSourceIP, req.Header.InitiatorSPI, f.responderSPI, f.remoteIP, f.remotePort)
	if err != nil {
		return nil, err
	}
	dst, err := NATDetectionNotify(NotifyNATDetectionDestinationIP, req.Header.InitiatorSPI, f.responderSPI, f.localIP, f.localPort)
	if err != nil {
		return nil, err
	}
	payloads = append(payloads, src, dst, MOBIKESupportedNotify())
	resp := Message{
		Header: Header{
			InitiatorSPI: req.Header.InitiatorSPI,
			ResponderSPI: f.responderSPI,
			ExchangeType: ExchangeIKE_SA_INIT,
			Flags:        FlagResponse,
		},
		Payloads: payloads,
	}
	return resp.MarshalBinary()
}

func TestRunIKESAInitSupportsECP256(t *testing.T) {
	nonceI := bytes.Repeat([]byte{0xa1}, 32)
	nonceR := bytes.Repeat([]byte{0xb2}, 32)
	fake := &initFakeTransport{
		t:            t,
		group:        DHGroup256BitECP,
		responderSPI: 0x1112131415161718,
		responderKey: bytes.Repeat([]byte{0x22}, 32),
		nonceR:       nonceR,
		remoteIP:     net.ParseIP("192.0.2.20"),
		remotePort:   500,
		localIP:      net.ParseIP("192.0.2.10"),
		localPort:    500,
	}
	res, err := RunIKE_SA_INIT(context.Background(), InitConfig{
		Transport:    fake,
		InitiatorSPI: 0x0102030405060708,
		NonceI:       nonceI,
		SA: SecurityAssociation{Proposals: []Proposal{{
			Number:     1,
			ProtocolID: ProtocolIKE,
			Transforms: []Transform{
				{Type: TransformENCR, ID: ENCR_AES_CBC, Attributes: []TransformAttribute{KeyLengthAttribute(128)}},
				{Type: TransformPRF, ID: PRF_HMAC_SHA2_256},
				{Type: TransformINTEG, ID: INTEG_HMAC_SHA2_256_128},
				{Type: TransformDHRGroup, ID: DHGroup256BitECP},
			},
		}}},
		LocalIP:    fake.localIP,
		LocalPort:  fake.localPort,
		RemoteIP:   fake.remoteIP,
		RemotePort: fake.remotePort,
	})
	if err != nil {
		t.Fatalf("RunIKE_SA_INIT() error = %v", err)
	}
	if len(res.PublicKeyI) != 64 || len(res.PublicKeyR) != 64 || len(res.SharedSecret) != 32 {
		t.Fatalf("key lengths pubI=%d pubR=%d shared=%d", len(res.PublicKeyI), len(res.PublicKeyR), len(res.SharedSecret))
	}
	ke, err := ParseKeyExchange(fake.request.Payloads[1].Body)
	if err != nil {
		t.Fatalf("ParseKeyExchange() error = %v", err)
	}
	if ke.DHGroup != DHGroup256BitECP {
		t.Fatalf("request DH group=%d, want %d", ke.DHGroup, DHGroup256BitECP)
	}
}

func TestRunIKESAInitRetriesWithPreferredDHGroupFromInvalidKE(t *testing.T) {
	const preferredGroup = 2

	nonceI := bytes.Repeat([]byte{0xa1}, 32)
	nonceR := bytes.Repeat([]byte{0xb2}, 32)
	transport := &invalidKERetryTransport{
		t:            t,
		preferred:    preferredGroup,
		responderSPI: 0x1112131415161718,
		nonceR:       nonceR,
		remoteIP:     net.ParseIP("192.0.2.20"),
		remotePort:   500,
		localIP:      net.ParseIP("192.0.2.10"),
		localPort:    500,
	}
	res, err := RunIKE_SA_INIT(context.Background(), InitConfig{
		Transport:    transport,
		InitiatorSPI: 0x0102030405060708,
		NonceI:       nonceI,
		LocalIP:      transport.localIP,
		LocalPort:    transport.localPort,
		RemoteIP:     transport.remoteIP,
		RemotePort:   transport.remotePort,
	})
	if err != nil {
		t.Fatalf("RunIKE_SA_INIT() error = %v", err)
	}
	if len(transport.requests) != 2 {
		t.Fatalf("requests=%d, want 2", len(transport.requests))
	}
	firstKE := mustRequestKE(t, transport.requests[0])
	if firstKE.DHGroup == preferredGroup {
		t.Fatalf("first request unexpectedly used preferred group %d", preferredGroup)
	}
	secondKE := mustRequestKE(t, transport.requests[1])
	if secondKE.DHGroup != preferredGroup {
		t.Fatalf("second request DH group=%d, want %d", secondKE.DHGroup, preferredGroup)
	}
	if !hasIKEProposal(res.SelectedSA, PRF_HMAC_SHA1, INTEG_HMAC_SHA1_96, preferredGroup) || res.PRF != crypto.SHA1 {
		t.Fatalf("selected SA=%+v PRF=%v, want MODP1024/SHA1", res.SelectedSA, res.PRF)
	}
}

func TestRunIKESAInitDerivesKeys(t *testing.T) {
	initiatorKey := bytes.Repeat([]byte{0x11}, 32)
	responderKey := bytes.Repeat([]byte{0x22}, 32)
	nonceI := bytes.Repeat([]byte{0xa1}, 32)
	nonceR := bytes.Repeat([]byte{0xb2}, 32)
	fake := &initFakeTransport{
		t:            t,
		responderSPI: 0x1112131415161718,
		responderKey: responderKey,
		nonceR:       nonceR,
		remoteIP:     net.ParseIP("192.0.2.20"),
		remotePort:   500,
		localIP:      net.ParseIP("192.0.2.10"),
		localPort:    500,
	}
	res, err := RunIKE_SA_INIT(context.Background(), InitConfig{
		Transport:        fake,
		InitiatorSPI:     0x0102030405060708,
		NonceI:           nonceI,
		X25519PrivateKey: initiatorKey,
		SA:               curve25519Proposal(),
		LocalIP:          fake.localIP,
		LocalPort:        fake.localPort,
		RemoteIP:         fake.remoteIP,
		RemotePort:       fake.remotePort,
	})
	if err != nil {
		t.Fatalf("RunIKE_SA_INIT() error = %v", err)
	}
	if res.InitiatorSPI != 0x0102030405060708 || res.ResponderSPI != fake.responderSPI {
		t.Fatalf("SPIs=%x/%x", res.InitiatorSPI, res.ResponderSPI)
	}
	if !res.MOBIKESupported || res.NATDetected || res.PRF != crypto.SHA256 {
		t.Fatalf("mobike=%t nat=%t prf=%v", res.MOBIKESupported, res.NATDetected, res.PRF)
	}
	if len(res.SKEYSEED) != crypto.SHA256.Size() || len(res.KeyMaterial) != res.Keys.Profile.RequiredLength() {
		t.Fatalf("key lengths skeyseed=%d material=%d", len(res.SKEYSEED), len(res.KeyMaterial))
	}
	if len(res.Keys.SKAi) != crypto.SHA256.Size() || len(res.Keys.SKEi) != 16 || len(res.Keys.SKPi) != crypto.SHA256.Size() {
		t.Fatalf("split keys=%+v", res.Keys)
	}
	privR, err := ecdh.X25519().NewPrivateKey(responderKey)
	if err != nil {
		t.Fatalf("NewPrivateKey() error = %v", err)
	}
	pubI, err := ecdh.X25519().NewPublicKey(res.PublicKeyI)
	if err != nil {
		t.Fatalf("NewPublicKey() error = %v", err)
	}
	wantShared, err := privR.ECDH(pubI)
	if err != nil {
		t.Fatalf("ECDH() error = %v", err)
	}
	if !bytes.Equal(res.SharedSecret, wantShared) {
		t.Fatalf("shared=%x want %x", res.SharedSecret, wantShared)
	}
	wantSKEYSEED, err := SKEYSEED(crypto.SHA256, nonceI, nonceR, wantShared)
	if err != nil {
		t.Fatalf("SKEYSEED() error = %v", err)
	}
	if !bytes.Equal(res.SKEYSEED, wantSKEYSEED) {
		t.Fatalf("skeyseed=%x want %x", res.SKEYSEED, wantSKEYSEED)
	}
	if got := countPayloadType(fake.request.Payloads, PayloadNotify); got != 3 {
		t.Fatalf("request notify payloads=%d, want NAT-D source/dest + MOBIKE", got)
	}
}

func TestRunIKESAInitRejectsMissingNonce(t *testing.T) {
	transport := InitTransportFunc(func(ctx context.Context, request []byte) ([]byte, error) {
		req, err := ParseMessage(request)
		if err != nil {
			return nil, err
		}
		resp := Message{
			Header: Header{
				InitiatorSPI: req.Header.InitiatorSPI,
				ResponderSPI: 0x1112131415161718,
				ExchangeType: ExchangeIKE_SA_INIT,
				Flags:        FlagResponse,
			},
			Payloads: []Payload{
				req.Payloads[0],
				KeyExchangePayload(DHGroupCurve25519, bytes.Repeat([]byte{0x33}, 32)),
			},
		}
		return resp.MarshalBinary()
	})
	_, err := RunIKE_SA_INIT(context.Background(), InitConfig{
		Transport:        transport,
		InitiatorSPI:     1,
		NonceI:           bytes.Repeat([]byte{0x01}, 32),
		X25519PrivateKey: bytes.Repeat([]byte{0x02}, 32),
		SA:               curve25519Proposal(),
	})
	if !errors.Is(err, ErrInvalidInitResponse) {
		t.Fatalf("RunIKE_SA_INIT() err=%v, want ErrInvalidInitResponse", err)
	}
}

func TestUDPTransportExchangesWithNonESPMarker(t *testing.T) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket() error = %v", err)
	}
	defer conn.Close()
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 1500)
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			done <- err
			return
		}
		if got := hex.EncodeToString(buf[:n]); got != "00000000010203" {
			done <- errors.New("unexpected UDP request " + got)
			return
		}
		_, err = conn.WriteTo([]byte{0, 0, 0, 0, 4, 5, 6}, addr)
		done <- err
	}()
	transport := UDPTransport{
		RemoteAddr:      conn.LocalAddr().String(),
		Timeout:         2 * time.Second,
		UseNonESPMarker: true,
	}
	resp, err := transport.ExchangeIKE(context.Background(), []byte{1, 2, 3})
	if err != nil {
		t.Fatalf("ExchangeIKE() error = %v", err)
	}
	if hex.EncodeToString(resp) != "040506" {
		t.Fatalf("resp=%x", resp)
	}
	if err := <-done; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

type InitTransportFunc func(context.Context, []byte) ([]byte, error)

func (f InitTransportFunc) ExchangeIKE(ctx context.Context, request []byte) ([]byte, error) {
	return f(ctx, request)
}

func countPayloadType(payloads []Payload, payloadType uint8) int {
	count := 0
	for _, p := range payloads {
		if p.Type == payloadType {
			count++
		}
	}
	return count
}

type invalidKERetryTransport struct {
	t            *testing.T
	preferred    uint16
	responderSPI uint64
	nonceR       []byte
	remoteIP     net.IP
	remotePort   uint16
	localIP      net.IP
	localPort    uint16
	requests     []Message
}

func (f *invalidKERetryTransport) ExchangeIKE(ctx context.Context, request []byte) ([]byte, error) {
	f.t.Helper()
	req, err := ParseMessage(request)
	if err != nil {
		return nil, err
	}
	f.requests = append(f.requests, req)
	if len(f.requests) == 1 {
		notify, err := NotifyPayload(Notify{
			NotifyType:       NotifyInvalidKEPayload,
			NotificationData: []byte{byte(f.preferred >> 8), byte(f.preferred)},
		})
		if err != nil {
			return nil, err
		}
		return (Message{
			Header: Header{
				InitiatorSPI: req.Header.InitiatorSPI,
				ExchangeType: ExchangeIKE_SA_INIT,
				Flags:        FlagResponse,
			},
			Payloads: []Payload{notify},
		}).MarshalBinary()
	}
	ke := mustRequestKE(f.t, req)
	if ke.DHGroup != f.preferred {
		f.t.Fatalf("retry request DH group=%d, want %d", ke.DHGroup, f.preferred)
	}
	privR, pubR, err := initKeyExchange(f.preferred, nil, bytes.NewReader(bytes.Repeat([]byte{0x32}, 512)))
	if err != nil {
		return nil, err
	}
	_ = privR
	payloads := []Payload{
		mustSecurityAssociationPayload(f.t, legacySHA1Proposal(f.preferred)),
		KeyExchangePayload(f.preferred, pubR),
		NoncePayload(f.nonceR),
	}
	src, err := NATDetectionNotify(NotifyNATDetectionSourceIP, req.Header.InitiatorSPI, f.responderSPI, f.remoteIP, f.remotePort)
	if err != nil {
		return nil, err
	}
	dst, err := NATDetectionNotify(NotifyNATDetectionDestinationIP, req.Header.InitiatorSPI, f.responderSPI, f.localIP, f.localPort)
	if err != nil {
		return nil, err
	}
	payloads = append(payloads, src, dst)
	return (Message{
		Header: Header{
			InitiatorSPI: req.Header.InitiatorSPI,
			ResponderSPI: f.responderSPI,
			ExchangeType: ExchangeIKE_SA_INIT,
			Flags:        FlagResponse,
		},
		Payloads: payloads,
	}).MarshalBinary()
}

func mustRequestKE(t *testing.T, req Message) KeyExchange {
	t.Helper()
	if len(req.Payloads) < 2 || req.Payloads[1].Type != PayloadKE {
		t.Fatalf("request payloads=%+v", req.Payloads)
	}
	ke, err := ParseKeyExchange(req.Payloads[1].Body)
	if err != nil {
		t.Fatalf("ParseKeyExchange() error = %v", err)
	}
	return ke
}

func mustSecurityAssociationPayload(t *testing.T, sa SecurityAssociation) Payload {
	t.Helper()
	payload, err := SecurityAssociationPayload(sa)
	if err != nil {
		t.Fatalf("SecurityAssociationPayload() error = %v", err)
	}
	return payload
}

func legacySHA1Proposal(group uint16) SecurityAssociation {
	return SecurityAssociation{Proposals: []Proposal{{
		Number:     1,
		ProtocolID: ProtocolIKE,
		Transforms: []Transform{
			{Type: TransformENCR, ID: ENCR_AES_CBC, Attributes: []TransformAttribute{KeyLengthAttribute(128)}},
			{Type: TransformPRF, ID: PRF_HMAC_SHA1},
			{Type: TransformINTEG, ID: INTEG_HMAC_SHA1_96},
			{Type: TransformDHRGroup, ID: group},
		},
	}}}
}

func curve25519Proposal() SecurityAssociation {
	return SecurityAssociation{Proposals: []Proposal{{
		Number:     1,
		ProtocolID: ProtocolIKE,
		Transforms: []Transform{
			{Type: TransformENCR, ID: ENCR_AES_CBC, Attributes: []TransformAttribute{KeyLengthAttribute(128)}},
			{Type: TransformPRF, ID: PRF_HMAC_SHA2_256},
			{Type: TransformINTEG, ID: INTEG_HMAC_SHA2_256_128},
			{Type: TransformDHRGroup, ID: DHGroupCurve25519},
		},
	}}}
}
