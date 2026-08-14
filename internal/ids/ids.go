package ids

import (
	// SHA-1 is here because RFC 4122 defines version 5 UUIDs over it. This is a
	// naming use, not a security one, and no other code in this package uses it.
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

// dnsNamespace is the RFC 4122 DNS namespace UUID, 6ba7b810-9dad-11d1-80b4-00c04fd430c8.
var dnsNamespace = [16]byte{
	0x6b, 0xa7, 0xb8, 0x10, 0x9d, 0xad, 0x11, 0xd1,
	0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8,
}

// namespace is the Servicesim UUID namespace: the version 5 UUID of the
// reserved name "servicesim.c360studio.test" under dnsNamespace. Deriving it
// rather than inventing a literal means the value is reproducible from first
// principles by anyone auditing a golden file, and ids_test.go pins the result
// so it can never drift silently.
var namespace = uuidV5Bytes(dnsNamespace, []byte("servicesim.c360studio.test"))

// lengthPrefixed returns the concatenation of parts with each part preceded by
// its length as eight big-endian bytes. The prefix is what makes the encoding
// injective: without it ("ab", "c") and ("a", "bc") hash identically.
func lengthPrefixed(parts []string) []byte {
	size := 8 * len(parts)
	for _, part := range parts {
		size += len(part)
	}
	buf := make([]byte, 0, size)
	var prefix [8]byte
	for _, part := range parts {
		binary.BigEndian.PutUint64(prefix[:], uint64(len(part)))
		buf = append(buf, prefix[:]...)
		buf = append(buf, part...)
	}
	return buf
}

// Derive returns SHA-256 over the length-prefixed concatenation of parts. Length
// prefixing is not decoration: without it Derive("ab","c") and Derive("a","bc")
// collide, and a scenario named "exa" with route "search" would collide with one
// named "exas" with route "earch".
func Derive(parts ...string) [32]byte {
	return sha256.Sum256(lengthPrefixed(parts))
}

// Hex32 returns the first sixteen derived bytes as thirty-two lowercase hex
// characters. This is the shape Exa documents for requestId — for example
// "b5947044c4b78efa9552a7c89b306d95" — not a readable slug. It is also the body
// of the Perplexity Agent "resp_" and "msg_" identifiers, whose prefixes are
// applied by the provider package.
func Hex32(parts ...string) string {
	digest := Derive(parts...)
	return hex.EncodeToString(digest[:16])
}

// uuidV5Bytes returns the raw sixteen bytes of the RFC 4122 version 5 UUID of
// name within ns: SHA-1 over ns||name, truncated to sixteen bytes, with the
// version and variant bits overwritten as the RFC requires.
func uuidV5Bytes(ns [16]byte, name []byte) [16]byte {
	h := sha1.New()
	h.Write(ns[:])
	h.Write(name)
	sum := h.Sum(nil)

	var out [16]byte
	copy(out[:], sum[:16])
	out[6] = (out[6] & 0x0f) | 0x50 // version 5
	out[8] = (out[8] & 0x3f) | 0x80 // RFC 4122 variant
	return out
}

// UUIDv5 returns an RFC 4122 version 5 UUID derived from parts under the
// Servicesim namespace. This is the shape Tavily documents for request_id and a
// safe default for a Perplexity completion id. crypto/sha1 is used because
// RFC 4122 specifies it for version 5; this is a naming use, not a security one.
func UUIDv5(parts ...string) string {
	u := uuidV5Bytes(namespace, lengthPrefixed(parts))
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

// Float returns a stable value in [lo, hi) derived from parts, for synthesising
// plausible scores without a random source. It returns lo when hi is not
// greater than lo, so a degenerate range yields the range's own value rather
// than a surprise outside it.
func Float(lo, hi float64, parts ...string) float64 {
	if !(hi > lo) {
		return lo
	}
	digest := Derive(parts...)
	// Fifty-three bits is the full float64 mantissa, so the quotient lands in
	// [0, 1) exactly and never rounds up to 1 the way a 64-bit division would.
	unit := float64(binary.BigEndian.Uint64(digest[:8])>>11) / (1 << 53)
	return lo + unit*(hi-lo)
}
