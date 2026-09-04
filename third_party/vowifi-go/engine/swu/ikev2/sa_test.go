package ikev2

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"
)

func TestDefaultIKEProposalMarshalParse(t *testing.T) {
	sa := DefaultIKEProposal()
	body, err := sa.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary() error = %v", err)
	}
	parsed, err := ParseSecurityAssociation(body)
	if err != nil {
		t.Fatalf("ParseSecurityAssociation() error = %v", err)
	}
	if len(parsed.Proposals) < 2 || len(parsed.Proposals[0].Transforms) != 4 {
		t.Fatalf("parsed=%+v", parsed)
	}
	encr := parsed.Proposals[0].Transforms[0]
	if encr.Type != TransformENCR || encr.ID != ENCR_AES_CBC || len(encr.Attributes) != 1 {
		t.Fatalf("ENCR transform=%+v", encr)
	}
	if encr.Attributes[0].Type != AttributeKeyLength || hex.EncodeToString(encr.Attributes[0].Value) != "0080" {
		t.Fatalf("ENCR attrs=%+v", encr.Attributes)
	}
}

func TestDefaultIKEProposalOffersModernAndLegacyMODPFallbacks(t *testing.T) {
	sa := DefaultIKEProposal()
	if !hasIKEProposal(sa, PRF_HMAC_SHA2_256, INTEG_HMAC_SHA2_256_128, DHGroup2048BitMODP) {
		t.Fatalf("DefaultIKEProposal() does not offer SHA2/MODP2048: %+v", sa)
	}
	if !hasIKEProposal(sa, PRF_HMAC_SHA1, INTEG_HMAC_SHA1_96, DHGroup1024BitMODP) {
		t.Fatalf("DefaultIKEProposal() does not offer SHA1/MODP1024 fallback: %+v", sa)
	}
}

func TestDefaultESPProposalIncludesSPI(t *testing.T) {
	body, err := DefaultESPProposal([]byte{0xaa, 0xbb, 0xcc, 0xdd}).MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary() error = %v", err)
	}
	parsed, err := ParseSecurityAssociation(body)
	if err != nil {
		t.Fatalf("ParseSecurityAssociation() error = %v", err)
	}
	p := parsed.Proposals[0]
	if p.ProtocolID != ProtocolESP || hex.EncodeToString(p.SPI) != "aabbccdd" || len(p.Transforms) != 3 {
		t.Fatalf("proposal=%+v", p)
	}
}

func TestDefaultESPProposalOffersSupportedCBCFallbacks(t *testing.T) {
	sa := DefaultESPProposal([]byte{0xaa, 0xbb, 0xcc, 0xdd})
	if !hasESPProposal(sa, ENCR_AES_CBC, 128, INTEG_HMAC_SHA2_256_128) {
		t.Fatalf("DefaultESPProposal() does not offer AES-CBC-128/SHA256: %+v", sa)
	}
	if !hasESPProposal(sa, ENCR_AES_CBC, 128, INTEG_HMAC_SHA1_96) {
		t.Fatalf("DefaultESPProposal() does not offer AES-CBC-128/SHA1 fallback: %+v", sa)
	}
	if hasESPProposal(sa, ENCR_AES_GCM_16, 128, 0) || hasESPProposal(sa, ENCR_AES_GCM_16, 256, 0) {
		t.Fatalf("DefaultESPProposal() must not offer AES-GCM before ESP data plane supports AEAD: %+v", sa)
	}
}

func TestSecurityAssociationRejectsBadTransformCount(t *testing.T) {
	body := mustHex("0000002c010100050300000c0100000c800e00800300000802000005030000080300000c000000080400001f")
	_, err := ParseSecurityAssociation(body)
	if !errors.Is(err, ErrInvalidSA) {
		t.Fatalf("ParseSecurityAssociation() err=%v, want ErrInvalidSA", err)
	}
}

func hasIKEProposal(sa SecurityAssociation, prfID, integID, dhID uint16) bool {
	for _, p := range sa.Proposals {
		if p.ProtocolID != ProtocolIKE {
			continue
		}
		var hasPRF, hasInteg, hasDH bool
		for _, tr := range p.Transforms {
			switch tr.Type {
			case TransformPRF:
				hasPRF = hasPRF || tr.ID == prfID
			case TransformINTEG:
				hasInteg = hasInteg || tr.ID == integID
			case TransformDHRGroup:
				hasDH = hasDH || tr.ID == dhID
			}
		}
		if hasPRF && hasInteg && hasDH {
			return true
		}
	}
	return false
}

func hasESPProposal(sa SecurityAssociation, encrID uint16, keyBits uint16, integID uint16) bool {
	for _, p := range sa.Proposals {
		if p.ProtocolID != ProtocolESP {
			continue
		}
		var hasENCR, hasInteg, hasESN bool
		for _, tr := range p.Transforms {
			switch tr.Type {
			case TransformENCR:
				hasENCR = tr.ID == encrID && transformKeyLength(tr) == keyBits
			case TransformINTEG:
				hasInteg = integID != 0 && tr.ID == integID
			case TransformESN:
				hasESN = tr.ID == ESNNo
			}
		}
		if hasENCR && hasESN && (integID == 0 || hasInteg) {
			return true
		}
	}
	return false
}

func allESPProposalsUseSPI(sa SecurityAssociation, spi []byte) bool {
	if len(sa.Proposals) == 0 {
		return false
	}
	for _, p := range sa.Proposals {
		if p.ProtocolID != ProtocolESP || !bytes.Equal(p.SPI, spi) {
			return false
		}
	}
	return true
}
