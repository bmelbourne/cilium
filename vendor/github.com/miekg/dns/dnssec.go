package dns

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	_ "crypto/sha1"   // need its init function
	_ "crypto/sha256" // need its init function
	_ "crypto/sha512" // need its init function
	"encoding/asn1"
	"encoding/binary"
	"encoding/hex"
	"math/big"
	"sort"
	"strings"
	"time"
)

// DNSSEC encryption algorithm codes.
const (
	_ uint8 = iota
	RSAMD5
	DH
	DSA
	_ // Skip 4, RFC 6725, section 2.1
	RSASHA1
	DSANSEC3SHA1
	RSASHA1NSEC3SHA1
	RSASHA256
	_ // Skip 9, RFC 6725, section 2.1
	RSASHA512
	_ // Skip 11, RFC 6725, section 2.1
	ECCGOST
	ECDSAP256SHA256
	ECDSAP384SHA384
	ED25519
	ED448
	INDIRECT   uint8 = 252
	PRIVATEDNS uint8 = 253 // Private (experimental keys)
	PRIVATEOID uint8 = 254
)

// AlgorithmToString is a map of algorithm IDs to algorithm names.
var AlgorithmToString = map[uint8]string{
	RSAMD5:           "RSAMD5",
	DH:               "DH",
	DSA:              "DSA",
	RSASHA1:          "RSASHA1",
	DSANSEC3SHA1:     "DSA-NSEC3-SHA1",
	RSASHA1NSEC3SHA1: "RSASHA1-NSEC3-SHA1",
	RSASHA256:        "RSASHA256",
	RSASHA512:        "RSASHA512",
	ECCGOST:          "ECC-GOST",
	ECDSAP256SHA256:  "ECDSAP256SHA256",
	ECDSAP384SHA384:  "ECDSAP384SHA384",
	ED25519:          "ED25519",
	ED448:            "ED448",
	INDIRECT:         "INDIRECT",
	PRIVATEDNS:       "PRIVATEDNS",
	PRIVATEOID:       "PRIVATEOID",
}

// AlgorithmToHash is a map of algorithm crypto hash IDs to crypto.Hash's.
// For newer algorithm that do their own hashing (i.e. ED25519) the returned value
// is 0, implying no (external) hashing should occur. The non-exported identityHash is then
// used.
var AlgorithmToHash = map[uint8]crypto.Hash{
	RSAMD5:           crypto.MD5, // Deprecated in RFC 6725
	DSA:              crypto.SHA1,
	RSASHA1:          crypto.SHA1,
	RSASHA1NSEC3SHA1: crypto.SHA1,
	RSASHA256:        crypto.SHA256,
	ECDSAP256SHA256:  crypto.SHA256,
	ECDSAP384SHA384:  crypto.SHA384,
	RSASHA512:        crypto.SHA512,
	ED25519:          0,
}

// DNSSEC hashing algorithm codes.
const (
	_      uint8 = iota
	SHA1         // RFC 4034
	SHA256       // RFC 4509
	GOST94       // RFC 5933
	SHA384       // Experimental
	SHA512       // Experimental
)

// HashToString is a map of hash IDs to names.
var HashToString = map[uint8]string{
	SHA1:   "SHA1",
	SHA256: "SHA256",
	GOST94: "GOST94",
	SHA384: "SHA384",
	SHA512: "SHA512",
}

// DNSKEY flag values.
const (
	SEP    = 1
	REVOKE = 1 << 7
	ZONE   = 1 << 8
)

// The RRSIG needs to be converted to wireformat with some of the rdata (the signature) missing.
type rrsigWireFmt struct {
	TypeCovered uint16
	Algorithm   uint8
	Labels      uint8
	OrigTtl     uint32
	Expiration  uint32
	Inception   uint32
	KeyTag      uint16
	SignerName  string `dns:"domain-name"`
	/* No Signature */
}

// Used for converting DNSKEY's rdata to wirefmt.
type dnskeyWireFmt struct {
	Flags     uint16
	Protocol  uint8
	Algorithm uint8
	PublicKey string `dns:"base64"`
	/* Nothing is left out */
}

// KeyTag calculates the keytag (or key-id) of the DNSKEY.
func (k *DNSKEY) KeyTag() uint16 {
	if k == nil {
		return 0
	}
	var keytag int
	switch k.Algorithm {
	case RSAMD5:
		// This algorithm has been deprecated, but keep this key-tag calculation.
		// Look at the bottom two bytes of the modules, which the last item in the pubkey.
		// See https://www.rfc-editor.org/errata/eid193 .
		modulus, _ := fromBase64([]byte(k.PublicKey))
		if len(modulus) > 1 {
			x := binary.BigEndian.Uint16(modulus[len(modulus)-3:])
			keytag = int(x)
		}
	default:
		keywire := new(dnskeyWireFmt)
		keywire.Flags = k.Flags
		keywire.Protocol = k.Protocol
		keywire.Algorithm = k.Algorithm
		keywire.PublicKey = k.PublicKey
		wire := make([]byte, DefaultMsgSize)
		n, err := packKeyWire(keywire, wire)
		if err != nil {
			return 0
		}
		wire = wire[:n]
		for i, v := range wire {
			if i&1 != 0 {
				keytag += int(v) // must be larger than uint32
			} else {
				keytag += int(v) << 8
			}
		}
		keytag += keytag >> 16 & 0xFFFF
		keytag &= 0xFFFF
	}
	return uint16(keytag)
}

// ToDS converts a DNSKEY record to a DS record.
func (k *DNSKEY) ToDS(h uint8) *DS {
	if k == nil {
		return nil
	}
	ds := new(DS)
	ds.Hdr.Name = k.Hdr.Name
	ds.Hdr.Class = k.Hdr.Class
	ds.Hdr.Rrtype = TypeDS
	ds.Hdr.Ttl = k.Hdr.Ttl
	ds.Algorithm = k.Algorithm
	ds.DigestType = h
	ds.KeyTag = k.KeyTag()

	keywire := new(dnskeyWireFmt)
	keywire.Flags = k.Flags
	keywire.Protocol = k.Protocol
	keywire.Algorithm = k.Algorithm
	keywire.PublicKey = k.PublicKey
	wire := make([]byte, DefaultMsgSize)
	n, err := packKeyWire(keywire, wire)
	if err != nil {
		return nil
	}
	wire = wire[:n]

	owner := make([]byte, 255)
	off, err1 := PackDomainName(CanonicalName(k.Hdr.Name), owner, 0, nil, false)
	if err1 != nil {
		return nil
	}
	owner = owner[:off]
	// RFC4034:
	// digest = digest_algorithm( DNSKEY owner name | DNSKEY RDATA);
	// "|" denotes concatenation
	// DNSKEY RDATA = Flags | Protocol | Algorithm | Public Key.

	var hash crypto.Hash
	switch h {
	case SHA1:
		hash = crypto.SHA1
	case SHA256:
		hash = crypto.SHA256
	case SHA384:
		hash = crypto.SHA384
	case SHA512:
		hash = crypto.SHA512
	default:
		return nil
	}

	s := hash.New()
	s.Write(owner)
	s.Write(wire)
	ds.Digest = hex.EncodeToString(s.Sum(nil))
	return ds
}

// ToCDNSKEY converts a DNSKEY record to a CDNSKEY record.
func (k *DNSKEY) ToCDNSKEY() *CDNSKEY {
	c := &CDNSKEY{DNSKEY: *k}
	c.Hdr = k.Hdr
	c.Hdr.Rrtype = TypeCDNSKEY
	return c
}

// ToCDS converts a DS record to a CDS record.
func (d *DS) ToCDS() *CDS {
	c := &CDS{DS: *d}
	c.Hdr = d.Hdr
	c.Hdr.Rrtype = TypeCDS
	return c
}

// Sign signs an RRSet. The signature needs to be filled in with the values:
// Inception, Expiration, KeyTag, SignerName and Algorithm.  The rest is copied
// from the RRset. Sign returns a non-nill error when the signing went OK.
// There is no check if RRSet is a proper (RFC 2181) RRSet.  If OrigTTL is non
// zero, it is used as-is, otherwise the TTL of the RRset is used as the
// OrigTTL.
func (rr *RRSIG) Sign(k crypto.Signer, rrset []RR) error {
	h0 := rrset[0].Header()
	rr.Hdr.Rrtype = TypeRRSIG
	rr.Hdr.Name = h0.Name
	rr.Hdr.Class = h0.Class
	if rr.OrigTtl == 0 { // If set don't override
		rr.OrigTtl = h0.Ttl
	}
	rr.TypeCovered = h0.Rrtype
	rr.Labels = uint8(CountLabel(h0.Name))

	if strings.HasPrefix(h0.Name, "*") {
		rr.Labels-- // wildcard, remove from label count
	}

	return rr.signAsIs(k, rrset)
}

func (rr *RRSIG) signAsIs(k crypto.Signer, rrset []RR) error {
	if k == nil {
		return ErrPrivKey
	}
	// s.Inception and s.Expiration may be 0 (rollover etc.), the rest must be set
	if rr.KeyTag == 0 || len(rr.SignerName) == 0 || rr.Algorithm == 0 {
		return ErrKey
	}

	sigwire := new(rrsigWireFmt)
	sigwire.TypeCovered = rr.TypeCovered
	sigwire.Algorithm = rr.Algorithm
	sigwire.Labels = rr.Labels
	sigwire.OrigTtl = rr.OrigTtl
	sigwire.Expiration = rr.Expiration
	sigwire.Inception = rr.Inception
	sigwire.KeyTag = rr.KeyTag
	// For signing, lowercase this name
	sigwire.SignerName = CanonicalName(rr.SignerName)

	// Create the desired binary blob
	signdata := make([]byte, DefaultMsgSize)
	n, err := packSigWire(sigwire, signdata)
	if err != nil {
		return err
	}
	signdata = signdata[:n]
	wire, err := rawSignatureData(rrset, rr)
	if err != nil {
		return err
	}

	h, cryptohash, err := hashFromAlgorithm(rr.Algorithm)
	if err != nil {
		return err
	}

	switch rr.Algorithm {
	case RSAMD5, DSA, DSANSEC3SHA1:
		// See RFC 6944.
		return ErrAlg
	default:
		h.Write(signdata)
		h.Write(wire)

		signature, err := sign(k, h.Sum(nil), cryptohash, rr.Algorithm)
		if err != nil {
			return err
		}

		rr.Signature = toBase64(signature)
		return nil
	}
}

func sign(k crypto.Signer, hashed []byte, hash crypto.Hash, alg uint8) ([]byte, error) {
	signature, err := k.Sign(rand.Reader, hashed, hash)
	if err != nil {
		return nil, err
	}

	switch alg {
	case RSASHA1, RSASHA1NSEC3SHA1, RSASHA256, RSASHA512, ED25519:
		return signature, nil
	case ECDSAP256SHA256, ECDSAP384SHA384:
		ecdsaSignature := &struct {
			R, S *big.Int
		}{}
		if _, err := asn1.Unmarshal(signature, ecdsaSignature); err != nil {
			return nil, err
		}

		var intlen int
		switch alg {
		case ECDSAP256SHA256:
			intlen = 32
		case ECDSAP384SHA384:
			intlen = 48
		}

		signature := intToBytes(ecdsaSignature.R, intlen)
		signature = append(signature, intToBytes(ecdsaSignature.S, intlen)...)
		return signature, nil
	default:
		return nil, ErrAlg
	}
}

// Verify validates an RRSet with the signature and key. This is only the
// cryptographic test, the signature validity period must be checked separately.
// This function copies the rdata of some RRs (to lowercase domain names) for the validation to work.
// It also checks that the Zone Key bit (RFC 4034 2.1.1) is set on the DNSKEY
// and that the Protocol field is set to 3 (RFC 4034 2.1.2).
func (rr *RRSIG) Verify(k *DNSKEY, rrset []RR) error {
	// First the easy checks
	if !IsRRset(rrset) {
		return ErrRRset
	}
	if rr.KeyTag != k.KeyTag() {
		return ErrKey
	}
	if rr.Hdr.Class != k.Hdr.Class {
		return ErrKey
	}
	if rr.Algorithm != k.Algorithm {
		return ErrKey
	}

	signerName := CanonicalName(rr.SignerName)
	if !equal(signerName, k.Hdr.Name) {
		return ErrKey
	}

	if k.Protocol != 3 {
		return ErrKey
	}
	// RFC 4034 2.1.1 If bit 7 has value 0, then the DNSKEY record holds some
	// other type of DNS public key and MUST NOT be used to verify RRSIGs that
	// cover RRsets.
	if k.Flags&ZONE == 0 {
		return ErrKey
	}

	// IsRRset checked that we have at least one RR and that the RRs in
	// the set have consistent type, class, and name. Also check that type,
	// class and name matches the RRSIG record.
	// Also checks RFC 4035 5.3.1 the number of labels in the RRset owner
	// name MUST be greater than or equal to the value in the RRSIG RR's Labels field.
	// RFC 4035 5.3.1 Signer's Name MUST be the name of the zone that [contains the RRset].
	// Since we don't have SOA info, checking suffix may be the best we can do...?
	if h0 := rrset[0].Header(); h0.Class != rr.Hdr.Class ||
		h0.Rrtype != rr.TypeCovered ||
		uint8(CountLabel(h0.Name)) < rr.Labels ||
		!equal(h0.Name, rr.Hdr.Name) ||
		!strings.HasSuffix(CanonicalName(h0.Name), signerName) {

		return ErrRRset
	}

	// RFC 4035 5.3.2.  Reconstructing the Signed Data
	// Copy the sig, except the rrsig data
	sigwire := new(rrsigWireFmt)
	sigwire.TypeCovered = rr.TypeCovered
	sigwire.Algorithm = rr.Algorithm
	sigwire.Labels = rr.Labels
	sigwire.OrigTtl = rr.OrigTtl
	sigwire.Expiration = rr.Expiration
	sigwire.Inception = rr.Inception
	sigwire.KeyTag = rr.KeyTag
	sigwire.SignerName = signerName
	// Create the desired binary blob
	signeddata := make([]byte, DefaultMsgSize)
	n, err := packSigWire(sigwire, signeddata)
	if err != nil {
		return err
	}
	signeddata = signeddata[:n]
	wire, err := rawSignatureData(rrset, rr)
	if err != nil {
		return err
	}

	sigbuf := rr.sigBuf() // Get the binary signature data
	// TODO(miek)
	// remove the domain name and assume its ours?
	// if rr.Algorithm == PRIVATEDNS { // PRIVATEOID
	// }

	h, cryptohash, err := hashFromAlgorithm(rr.Algorithm)
	if err != nil {
		return err
	}

	switch rr.Algorithm {
	case RSASHA1, RSASHA1NSEC3SHA1, RSASHA256, RSASHA512:
		// TODO(mg): this can be done quicker, ie. cache the pubkey data somewhere??
		pubkey := k.publicKeyRSA() // Get the key
		if pubkey == nil {
			return ErrKey
		}

		h.Write(signeddata)
		h.Write(wire)
		return rsa.VerifyPKCS1v15(pubkey, cryptohash, h.Sum(nil), sigbuf)

	case ECDSAP256SHA256, ECDSAP384SHA384:
		pubkey := k.publicKeyECDSA()
		if pubkey == nil {
			return ErrKey
		}

		// Split sigbuf into the r and s coordinates
		r := new(big.Int).SetBytes(sigbuf[:len(sigbuf)/2])
		s := new(big.Int).SetBytes(sigbuf[len(sigbuf)/2:])

		h.Write(signeddata)
		h.Write(wire)
		if ecdsa.Verify(pubkey, h.Sum(nil), r, s) {
			return nil
		}
		return ErrSig

	case ED25519:
		pubkey := k.publicKeyED25519()
		if pubkey == nil {
			return ErrKey
		}

		if ed25519.Verify(pubkey, append(signeddata, wire...), sigbuf) {
			return nil
		}
		return ErrSig

	default:
		return ErrAlg
	}
}

// ValidityPeriod uses RFC1982 serial arithmetic to calculate
// if a signature period is valid. If t is the zero time, the
// current time is taken other t is. Returns true if the signature
// is valid at the given time, otherwise returns false.
func (rr *RRSIG) ValidityPeriod(t time.Time) bool {
	var utc int64
	if t.IsZero() {
		utc = time.Now().UTC().Unix()
	} else {
		utc = t.UTC().Unix()
	}
	modi := (int64(rr.Inception) - utc) / year68
	mode := (int64(rr.Expiration) - utc) / year68
	ti := int64(rr.Inception) + modi*year68
	te := int64(rr.Expiration) + mode*year68
	return ti <= utc && utc <= te
}

// Return the signatures base64 encoding sigdata as a byte slice.
func (rr *RRSIG) sigBuf() []byte {
	sigbuf, err := fromBase64([]byte(rr.Signature))
	if err != nil {
		return nil
	}
	return sigbuf
}

// publicKeyRSA returns the RSA public key from a DNSKEY record.
func (k *DNSKEY) publicKeyRSA() *rsa.PublicKey {
	keybuf, err := fromBase64([]byte(k.PublicKey))
	if err != nil {
		return nil
	}

	if len(keybuf) < 1+1+64 {
		// Exponent must be at least 1 byte and modulus at least 64
		return nil
	}

	// RFC 2537/3110, section 2. RSA Public KEY Resource Records
	// Length is in the 0th byte, unless its zero, then it
	// it in bytes 1 and 2 and its a 16 bit number
	explen := uint16(keybuf[0])
	keyoff := 1
	if explen == 0 {
		explen = uint16(keybuf[1])<<8 | uint16(keybuf[2])
		keyoff = 3
	}

	if explen > 4 || explen == 0 || keybuf[keyoff] == 0 {
		// Exponent larger than supported by the crypto package,
		// empty, or contains prohibited leading zero.
		return nil
	}

	modoff := keyoff + int(explen)
	modlen := len(keybuf) - modoff
	if modlen < 64 || modlen > 512 || keybuf[modoff] == 0 {
		// Modulus is too small, large, or contains prohibited leading zero.
		return nil
	}

	pubkey := new(rsa.PublicKey)

	var expo uint64
	// The exponent of length explen is between keyoff and modoff.
	for _, v := range keybuf[keyoff:modoff] {
		expo <<= 8
		expo |= uint64(v)
	}
	if expo > 1<<31-1 {
		// Larger exponent than supported by the crypto package.
		return nil
	}

	pubkey.E = int(expo)
	pubkey.N = new(big.Int).SetBytes(keybuf[modoff:])
	return pubkey
}

// publicKeyECDSA returns the Curve public key from the DNSKEY record.
func (k *DNSKEY) publicKeyECDSA() *ecdsa.PublicKey {
	keybuf, err := fromBase64([]byte(k.PublicKey))
	if err != nil {
		return nil
	}
	pubkey := new(ecdsa.PublicKey)
	switch k.Algorithm {
	case ECDSAP256SHA256:
		pubkey.Curve = elliptic.P256()
		if len(keybuf) != 64 {
			// wrongly encoded key
			return nil
		}
	case ECDSAP384SHA384:
		pubkey.Curve = elliptic.P384()
		if len(keybuf) != 96 {
			// Wrongly encoded key
			return nil
		}
	}
	pubkey.X = new(big.Int).SetBytes(keybuf[:len(keybuf)/2])
	pubkey.Y = new(big.Int).SetBytes(keybuf[len(keybuf)/2:])
	return pubkey
}

func (k *DNSKEY) publicKeyED25519() ed25519.PublicKey {
	keybuf, err := fromBase64([]byte(k.PublicKey))
	if err != nil {
		return nil
	}
	if len(keybuf) != ed25519.PublicKeySize {
		return nil
	}
	return keybuf
}

type wireSlice [][]byte

func (p wireSlice) Len() int      { return len(p) }
func (p wireSlice) Swap(i, j int) { p[i], p[j] = p[j], p[i] }
func (p wireSlice) Less(i, j int) bool {
	_, ioff, _ := UnpackDomainName(p[i], 0)
	_, joff, _ := UnpackDomainName(p[j], 0)
	return bytes.Compare(p[i][ioff+10:], p[j][joff+10:]) < 0
}

// Return the raw signature data.
func rawSignatureData(rrset []RR, s *RRSIG) (buf []byte, err error) {
	wires := make(wireSlice, len(rrset))
	for i, r := range rrset {
		r1 := r.copy()
		h := r1.Header()
		h.Ttl = s.OrigTtl
		labels := SplitDomainName(h.Name)
		// 6.2. Canonical RR Form. (4) - wildcards
		if len(labels) > int(s.Labels) {
			// Wildcard
			h.Name = "*." + strings.Join(labels[len(labels)-int(s.Labels):], ".") + "."
		}
		// RFC 4034: 6.2.  Canonical RR Form. (2) - domain name to lowercase
		h.Name = CanonicalName(h.Name)
		// 6.2. Canonical RR Form. (3) - domain rdata to lowercase.
		//   NS, MD, MF, CNAME, SOA, MB, MG, MR, PTR,
		//   HINFO, MINFO, MX, RP, AFSDB, RT, SIG, PX, NXT, NAPTR, KX,
		//   SRV, DNAME, A6
		//
		// RFC 6840 - Clarifications and Implementation Notes for DNS Security (DNSSEC):
		//	Section 6.2 of [RFC4034] also erroneously lists HINFO as a record
		//	that needs conversion to lowercase, and twice at that.  Since HINFO
		//	records contain no domain names, they are not subject to case
		//	conversion.
		switch x := r1.(type) {
		case *NS:
			x.Ns = CanonicalName(x.Ns)
		case *MD:
			x.Md = CanonicalName(x.Md)
		case *MF:
			x.Mf = CanonicalName(x.Mf)
		case *CNAME:
			x.Target = CanonicalName(x.Target)
		case *SOA:
			x.Ns = CanonicalName(x.Ns)
			x.Mbox = CanonicalName(x.Mbox)
		case *MB:
			x.Mb = CanonicalName(x.Mb)
		case *MG:
			x.Mg = CanonicalName(x.Mg)
		case *MR:
			x.Mr = CanonicalName(x.Mr)
		case *PTR:
			x.Ptr = CanonicalName(x.Ptr)
		case *MINFO:
			x.Rmail = CanonicalName(x.Rmail)
			x.Email = CanonicalName(x.Email)
		case *MX:
			x.Mx = CanonicalName(x.Mx)
		case *RP:
			x.Mbox = CanonicalName(x.Mbox)
			x.Txt = CanonicalName(x.Txt)
		case *AFSDB:
			x.Hostname = CanonicalName(x.Hostname)
		case *RT:
			x.Host = CanonicalName(x.Host)
		case *SIG:
			x.SignerName = CanonicalName(x.SignerName)
		case *PX:
			x.Map822 = CanonicalName(x.Map822)
			x.Mapx400 = CanonicalName(x.Mapx400)
		case *NAPTR:
			x.Replacement = CanonicalName(x.Replacement)
		case *KX:
			x.Exchanger = CanonicalName(x.Exchanger)
		case *SRV:
			x.Target = CanonicalName(x.Target)
		case *DNAME:
			x.Target = CanonicalName(x.Target)
		}
		// 6.2. Canonical RR Form. (5) - origTTL
		wire := make([]byte, Len(r1)+1) // +1 to be safe(r)
		off, err1 := PackRR(r1, wire, 0, nil, false)
		if err1 != nil {
			return nil, err1
		}
		wire = wire[:off]
		wires[i] = wire
	}
	sort.Sort(wires)
	for i, wire := range wires {
		if i > 0 && bytes.Equal(wire, wires[i-1]) {
			continue
		}
		buf = append(buf, wire...)
	}
	return buf, nil
}

func packSigWire(sw *rrsigWireFmt, msg []byte) (int, error) {
	// copied from zmsg.go RRSIG packing
	off, err := packUint16(sw.TypeCovered, msg, 0)
	if err != nil {
		return off, err
	}
	off, err = packUint8(sw.Algorithm, msg, off)
	if err != nil {
		return off, err
	}
	off, err = packUint8(sw.Labels, msg, off)
	if err != nil {
		return off, err
	}
	off, err = packUint32(sw.OrigTtl, msg, off)
	if err != nil {
		return off, err
	}
	off, err = packUint32(sw.Expiration, msg, off)
	if err != nil {
		return off, err
	}
	off, err = packUint32(sw.Inception, msg, off)
	if err != nil {
		return off, err
	}
	off, err = packUint16(sw.KeyTag, msg, off)
	if err != nil {
		return off, err
	}
	off, err = PackDomainName(sw.SignerName, msg, off, nil, false)
	if err != nil {
		return off, err
	}
	return off, nil
}

func packKeyWire(dw *dnskeyWireFmt, msg []byte) (int, error) {
	// copied from zmsg.go DNSKEY packing
	off, err := packUint16(dw.Flags, msg, 0)
	if err != nil {
		return off, err
	}
	off, err = packUint8(dw.Protocol, msg, off)
	if err != nil {
		return off, err
	}
	off, err = packUint8(dw.Algorithm, msg, off)
	if err != nil {
		return off, err
	}
	off, err = packStringBase64(dw.PublicKey, msg, off)
	if err != nil {
		return off, err
	}
	return off, nil
}
