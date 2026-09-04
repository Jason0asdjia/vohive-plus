package ikev2

import (
	"context"
	"crypto"
	"crypto/ecdh"
	crand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"time"
)

const (
	DefaultNonceLength          = 32
	DefaultIKEKeyMaterialLength = 192
)

var (
	ErrInvalidInitConfig   = errors.New("invalid ikev2 init config")
	ErrInvalidInitResponse = errors.New("invalid ikev2 init response")
)

type InitTransport interface {
	ExchangeIKE(context.Context, []byte) ([]byte, error)
}

type UDPTransport struct {
	RemoteAddr      string
	LocalAddr       string
	Timeout         time.Duration
	UseNonESPMarker bool
	ReadBufferSize  int
}

func (t UDPTransport) ExchangeIKE(ctx context.Context, request []byte) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	remote := strings.TrimSpace(t.RemoteAddr)
	if remote == "" {
		return nil, fmt.Errorf("%w: remote address is empty", ErrInvalidInitConfig)
	}
	dialer := net.Dialer{}
	if strings.TrimSpace(t.LocalAddr) != "" {
		addr, err := net.ResolveUDPAddr("udp", t.LocalAddr)
		if err != nil {
			return nil, err
		}
		dialer.LocalAddr = addr
	}
	conn, err := dialer.DialContext(ctx, "udp", remote)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if timeout := t.Timeout; timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}
	wire := request
	if t.UseNonESPMarker {
		wire = append([]byte{0, 0, 0, 0}, request...)
	}
	if _, err := conn.Write(wire); err != nil {
		return nil, err
	}
	size := t.ReadBufferSize
	if size <= 0 {
		size = 64 * 1024
	}
	buf := make([]byte, size)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	resp := append([]byte(nil), buf[:n]...)
	if len(resp) >= 4 && resp[0] == 0 && resp[1] == 0 && resp[2] == 0 && resp[3] == 0 {
		resp = resp[4:]
	}
	return resp, nil
}

type InitConfig struct {
	Transport         InitTransport
	Random            io.Reader
	SA                SecurityAssociation
	InitiatorSPI      uint64
	NonceI            []byte
	X25519PrivateKey  []byte
	LocalIP           net.IP
	LocalPort         uint16
	RemoteIP          net.IP
	RemotePort        uint16
	KeyMaterialLength int
}

type InitResult struct {
	RequestBytes    []byte
	ResponseBytes   []byte
	Request         Message
	Response        Message
	SelectedSA      SecurityAssociation
	InitiatorSPI    uint64
	ResponderSPI    uint64
	NonceI          []byte
	NonceR          []byte
	PublicKeyI      []byte
	PublicKeyR      []byte
	SharedSecret    []byte
	PRF             crypto.Hash
	SKEYSEED        []byte
	KeyMaterial     []byte
	Keys            IKEKeys
	MOBIKESupported bool
	NATDetected     bool
}

func RunIKE_SA_INIT(ctx context.Context, cfg InitConfig) (InitResult, error) {
	if cfg.Transport == nil {
		return InitResult{}, fmt.Errorf("%w: transport is nil", ErrInvalidInitConfig)
	}
	random := cfg.Random
	if random == nil {
		random = crand.Reader
	}
	spiI := cfg.InitiatorSPI
	var err error
	if spiI == 0 {
		spiI, err = randomSPI(random)
		if err != nil {
			return InitResult{}, err
		}
	}
	nonceI := append([]byte(nil), cfg.NonceI...)
	if len(nonceI) == 0 {
		nonceI, err = randomBytes(random, DefaultNonceLength)
		if err != nil {
			return InitResult{}, err
		}
	}
	sa := cfg.SA
	if len(sa.Proposals) == 0 {
		sa = DefaultIKEProposal()
	}
	dhGroup := selectedDHGroup(sa)
	priv, pubI, err := initKeyExchange(dhGroup, cfg.X25519PrivateKey, random)
	if err != nil {
		return InitResult{}, err
	}
	saPayload, err := SecurityAssociationPayload(sa)
	if err != nil {
		return InitResult{}, err
	}
	payloads := []Payload{
		saPayload,
		KeyExchangePayload(dhGroup, pubI),
		NoncePayload(nonceI),
	}
	payloads = append(payloads, initNATPayloads(cfg, spiI, 0)...)
	payloads = append(payloads, MOBIKESupportedNotify())
	req := Message{
		Header: Header{
			InitiatorSPI: spiI,
			ExchangeType: ExchangeIKE_SA_INIT,
			Flags:        FlagInitiator,
		},
		Payloads: payloads,
	}
	reqBytes, err := req.MarshalBinary()
	if err != nil {
		return InitResult{}, err
	}
	respBytes, err := cfg.Transport.ExchangeIKE(ctx, reqBytes)
	if err != nil {
		return InitResult{}, err
	}
	resp, err := ParseMessage(respBytes)
	if err != nil {
		return InitResult{}, err
	}
	parsed, err := parseInitResponse(resp, spiI)
	if err != nil {
		return InitResult{}, err
	}
	if parsed.keyExchange.DHGroup != dhGroup {
		return InitResult{}, fmt.Errorf("%w: responder DH group %d != requested %d", ErrInvalidInitResponse, parsed.keyExchange.DHGroup, dhGroup)
	}
	shared, err := priv.sharedSecret(parsed.keyExchange.KeyData)
	if err != nil {
		return InitResult{}, fmt.Errorf("%w: ECDH: %w", ErrInvalidInitResponse, err)
	}
	profile, err := KeyMaterialProfileFromSA(parsed.sa)
	if err != nil {
		return InitResult{}, err
	}
	prfHash := profile.PRF
	skeyseed, err := SKEYSEED(prfHash, nonceI, parsed.nonceR, shared)
	if err != nil {
		return InitResult{}, err
	}
	keyMaterialLength := cfg.KeyMaterialLength
	if keyMaterialLength <= 0 {
		keyMaterialLength = profile.RequiredLength()
	}
	keyMaterial, err := DeriveIKESAKeyMaterial(prfHash, skeyseed, nonceI, parsed.nonceR, spiI, resp.Header.ResponderSPI, keyMaterialLength)
	if err != nil {
		return InitResult{}, err
	}
	var keys IKEKeys
	if len(keyMaterial) >= profile.RequiredLength() {
		keys, err = SplitIKEKeys(profile, keyMaterial)
		if err != nil {
			return InitResult{}, err
		}
	}
	return InitResult{
		RequestBytes:    append([]byte(nil), reqBytes...),
		ResponseBytes:   append([]byte(nil), respBytes...),
		Request:         req,
		Response:        resp,
		SelectedSA:      parsed.sa,
		InitiatorSPI:    spiI,
		ResponderSPI:    resp.Header.ResponderSPI,
		NonceI:          nonceI,
		NonceR:          parsed.nonceR,
		PublicKeyI:      pubI,
		PublicKeyR:      parsed.keyExchange.KeyData,
		SharedSecret:    shared,
		PRF:             prfHash,
		SKEYSEED:        skeyseed,
		KeyMaterial:     keyMaterial,
		Keys:            keys,
		MOBIKESupported: parsed.mobikeSupported,
		NATDetected:     detectNAT(parsed.notifies, spiI, resp.Header.ResponderSPI, cfg),
	}, nil
}

type parsedInitResponse struct {
	sa              SecurityAssociation
	keyExchange     KeyExchange
	nonceR          []byte
	notifies        []Notify
	mobikeSupported bool
}

func parseInitResponse(resp Message, spiI uint64) (parsedInitResponse, error) {
	h := resp.Header
	if h.InitiatorSPI != spiI {
		return parsedInitResponse{}, fmt.Errorf("%w: initiator SPI mismatch", ErrInvalidInitResponse)
	}
	if h.ResponderSPI == 0 {
		return parsedInitResponse{}, fmt.Errorf("%w: responder SPI is zero", ErrInvalidInitResponse)
	}
	if h.ExchangeType != ExchangeIKE_SA_INIT || h.MessageID != 0 || h.Flags&FlagResponse == 0 {
		return parsedInitResponse{}, fmt.Errorf("%w: unexpected header", ErrInvalidInitResponse)
	}
	var out parsedInitResponse
	for _, p := range resp.Payloads {
		switch p.Type {
		case PayloadSA:
			sa, err := ParseSecurityAssociation(p.Body)
			if err != nil {
				return parsedInitResponse{}, err
			}
			out.sa = sa
		case PayloadKE:
			ke, err := ParseKeyExchange(p.Body)
			if err != nil {
				return parsedInitResponse{}, err
			}
			out.keyExchange = ke
		case PayloadNonce:
			out.nonceR = append([]byte(nil), p.Body...)
		case PayloadNotify:
			n, err := ParseNotify(p.Body)
			if err != nil {
				return parsedInitResponse{}, err
			}
			out.notifies = append(out.notifies, n)
			if n.NotifyType == NotifyMOBIKESupported {
				out.mobikeSupported = true
			}
		}
	}
	if len(out.sa.Proposals) == 0 {
		return parsedInitResponse{}, fmt.Errorf("%w: missing SA", ErrInvalidInitResponse)
	}
	if len(out.keyExchange.KeyData) == 0 {
		return parsedInitResponse{}, fmt.Errorf("%w: missing KE", ErrInvalidInitResponse)
	}
	if len(out.nonceR) == 0 {
		return parsedInitResponse{}, fmt.Errorf("%w: missing nonce", ErrInvalidInitResponse)
	}
	return out, nil
}

func initNATPayloads(cfg InitConfig, spiI, spiR uint64) []Payload {
	if cfg.LocalIP == nil || cfg.RemoteIP == nil || cfg.LocalPort == 0 || cfg.RemotePort == 0 {
		return nil
	}
	src, err := NATDetectionNotify(NotifyNATDetectionSourceIP, spiI, spiR, cfg.LocalIP, cfg.LocalPort)
	if err != nil {
		return nil
	}
	dst, err := NATDetectionNotify(NotifyNATDetectionDestinationIP, spiI, spiR, cfg.RemoteIP, cfg.RemotePort)
	if err != nil {
		return nil
	}
	return []Payload{src, dst}
}

func detectNAT(notifies []Notify, spiI, spiR uint64, cfg InitConfig) bool {
	if cfg.LocalIP == nil || cfg.RemoteIP == nil || cfg.LocalPort == 0 || cfg.RemotePort == 0 {
		return false
	}
	sourceHash, sourceErr := NATDetectionHash(spiI, spiR, cfg.RemoteIP, cfg.RemotePort)
	destHash, destErr := NATDetectionHash(spiI, spiR, cfg.LocalIP, cfg.LocalPort)
	if sourceErr != nil || destErr != nil {
		return false
	}
	for _, n := range notifies {
		switch n.NotifyType {
		case NotifyNATDetectionSourceIP:
			if !bytesEqual(n.NotificationData, sourceHash) {
				return true
			}
		case NotifyNATDetectionDestinationIP:
			if !bytesEqual(n.NotificationData, destHash) {
				return true
			}
		}
	}
	return false
}

func selectedPRFHash(sa SecurityAssociation) crypto.Hash {
	for _, p := range sa.Proposals {
		for _, tr := range p.Transforms {
			if tr.Type == TransformPRF {
				if h, err := PRFHashForTransform(tr.ID); err == nil {
					return h
				}
			}
		}
	}
	return crypto.SHA256
}

func selectedDHGroup(sa SecurityAssociation) uint16 {
	for _, p := range sa.Proposals {
		for _, tr := range p.Transforms {
			if tr.Type == TransformDHRGroup && tr.ID != 0 {
				return tr.ID
			}
		}
	}
	return DHGroupCurve25519
}

type ikeInitPrivateKey interface {
	sharedSecret(peerKeyData []byte) ([]byte, error)
}

type ecdhInitPrivateKey struct {
	group uint16
	priv  *ecdh.PrivateKey
}

func (k ecdhInitPrivateKey) sharedSecret(peerKeyData []byte) ([]byte, error) {
	peer, err := ikePublicKey(k.group, peerKeyData)
	if err != nil {
		return nil, err
	}
	return k.priv.ECDH(peer)
}

type modpInitPrivateKey struct {
	x    *big.Int
	p    *big.Int
	size int
}

func (k modpInitPrivateKey) sharedSecret(peerKeyData []byte) ([]byte, error) {
	if len(peerKeyData) == 0 || len(peerKeyData) > k.size {
		return nil, fmt.Errorf("%w: MODP peer key length %d", ErrInvalidInitResponse, len(peerKeyData))
	}
	y := new(big.Int).SetBytes(peerKeyData)
	if y.Sign() <= 0 || y.Cmp(k.p) >= 0 {
		return nil, fmt.Errorf("%w: MODP peer key is out of range", ErrInvalidInitResponse)
	}
	secret := new(big.Int).Exp(y, k.x, k.p)
	return fixedLengthBigInt(secret, k.size), nil
}

func initKeyExchange(group uint16, rawX25519 []byte, random io.Reader) (ikeInitPrivateKey, []byte, error) {
	if group == DHGroup2048BitMODP {
		if len(rawX25519) > 0 {
			return nil, nil, fmt.Errorf("%w: raw private key is only supported for Curve25519", ErrInvalidInitConfig)
		}
		p := modp2048Prime()
		size := (p.BitLen() + 7) / 8
		max := new(big.Int).Sub(p, big.NewInt(3))
		x, err := crand.Int(random, max)
		if err != nil {
			return nil, nil, err
		}
		x.Add(x, big.NewInt(2))
		pub := new(big.Int).Exp(big.NewInt(2), x, p)
		return modpInitPrivateKey{x: x, p: p, size: size}, fixedLengthBigInt(pub, size), nil
	}
	curve, err := ecdhCurveForGroup(group)
	if err != nil {
		return nil, nil, err
	}
	var priv *ecdh.PrivateKey
	if len(rawX25519) > 0 {
		if group != DHGroupCurve25519 {
			return nil, nil, fmt.Errorf("%w: raw private key is only supported for Curve25519", ErrInvalidInitConfig)
		}
		priv, err = curve.NewPrivateKey(append([]byte(nil), rawX25519...))
	} else {
		priv, err = curve.GenerateKey(random)
	}
	if err != nil {
		return nil, nil, err
	}
	pub, err := ikePublicKeyData(group, priv.PublicKey().Bytes())
	if err != nil {
		return nil, nil, err
	}
	return ecdhInitPrivateKey{group: group, priv: priv}, pub, nil
}

func ikePublicKey(group uint16, keyData []byte) (*ecdh.PublicKey, error) {
	curve, err := ecdhCurveForGroup(group)
	if err != nil {
		return nil, err
	}
	switch group {
	case DHGroupCurve25519:
		return curve.NewPublicKey(append([]byte(nil), keyData...))
	case DHGroup256BitECP, DHGroup384BitECP, DHGroup521BitECP:
		wantLen := ecpKeyDataLength(group)
		if len(keyData) != wantLen {
			return nil, fmt.Errorf("%w: ECP key data length %d, want %d", ErrInvalidInitResponse, len(keyData), wantLen)
		}
		raw := make([]byte, 0, wantLen+1)
		raw = append(raw, 0x04)
		raw = append(raw, keyData...)
		return curve.NewPublicKey(raw)
	default:
		return nil, fmt.Errorf("%w: unsupported DH group %d", ErrInvalidInitConfig, group)
	}
}

func ikePublicKeyData(group uint16, public []byte) ([]byte, error) {
	switch group {
	case DHGroupCurve25519:
		if len(public) != 32 {
			return nil, fmt.Errorf("%w: Curve25519 public key length %d", ErrInvalidInitConfig, len(public))
		}
		return append([]byte(nil), public...), nil
	case DHGroup256BitECP, DHGroup384BitECP, DHGroup521BitECP:
		wantLen := ecpKeyDataLength(group)
		if len(public) != wantLen+1 || public[0] != 0x04 {
			return nil, fmt.Errorf("%w: ECP public key length %d, want %d", ErrInvalidInitConfig, len(public), wantLen+1)
		}
		return append([]byte(nil), public[1:]...), nil
	default:
		return nil, fmt.Errorf("%w: unsupported DH group %d", ErrInvalidInitConfig, group)
	}
}

func ecdhCurveForGroup(group uint16) (ecdh.Curve, error) {
	switch group {
	case DHGroupCurve25519:
		return ecdh.X25519(), nil
	case DHGroup256BitECP:
		return ecdh.P256(), nil
	case DHGroup384BitECP:
		return ecdh.P384(), nil
	case DHGroup521BitECP:
		return ecdh.P521(), nil
	default:
		return nil, fmt.Errorf("%w: unsupported DH group %d", ErrInvalidInitConfig, group)
	}
}

func ecpKeyDataLength(group uint16) int {
	switch group {
	case DHGroup256BitECP:
		return 64
	case DHGroup384BitECP:
		return 96
	case DHGroup521BitECP:
		return 132
	default:
		return 0
	}
}

func modp2048Prime() *big.Int {
	p, _ := new(big.Int).SetString(
		"FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD1"+
			"29024E088A67CC74020BBEA63B139B22514A08798E3404DD"+
			"EF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245"+
			"E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7E"+
			"DEE386BFB5A899FA5AE9F24117C4B1FE649286651ECE45B3"+
			"DC2007CB8A163BF0598DA48361C55D39A69163FA8FD24CF5"+
			"F83655D23DCA3AD961C62F356208552BB9ED529077096966"+
			"D670C354E4ABC9804F1746C08CA18217C32905E462E36CE3"+
			"BE39E772C180E86039B2783A2EC07A28FB5C55DF06F4C52C"+
			"9DE2BCBF6955817183995497CEA956AE515D2261898FA0510"+
			"15728E5A8AACAA68FFFFFFFFFFFFFFFF",
		16,
	)
	return p
}

func fixedLengthBigInt(n *big.Int, size int) []byte {
	out := make([]byte, size)
	b := n.Bytes()
	if len(b) > size {
		b = b[len(b)-size:]
	}
	copy(out[size-len(b):], b)
	return out
}

func randomSPI(random io.Reader) (uint64, error) {
	for {
		b, err := randomBytes(random, 8)
		if err != nil {
			return 0, err
		}
		spi := binary.BigEndian.Uint64(b)
		if spi != 0 {
			return spi, nil
		}
	}
}

func randomBytes(random io.Reader, n int) ([]byte, error) {
	if n <= 0 {
		return nil, fmt.Errorf("%w: invalid random length %d", ErrInvalidInitConfig, n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(random, b); err != nil {
		return nil, err
	}
	return b, nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
