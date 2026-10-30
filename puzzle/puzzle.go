// Package puzzle is the proof-of-work rule, shared by the server and the WASM client:
//
//	valid(c, n, k)  ⇔  SHA-256(c ‖ n) has ≥ k leading zero bits,  n ∈ {0,1}^128
package puzzle

import (
	"crypto/sha256"
	"encoding/binary"
	"math/bits"
)

const NonceLen = 16

func ZeroBits(h [32]byte) int {
	n := 0
	for _, b := range h {
		if b != 0 {
			return n + bits.LeadingZeros8(b)
		}
		n += 8
	}
	return n
}

func Valid(challenge, nonce []byte, k int) bool {
	if len(nonce) != NonceLen {
		return false
	}
	buf := append(append(make([]byte, 0, len(challenge)+NonceLen), challenge...), nonce...)
	return ZeroBits(sha256.Sum256(buf)) >= k
}

// Solve tries the nonces from, from+1, … (tries of them), so a caller can search in slices and stay responsive.
func Solve(challenge []byte, k int, from, tries uint64) (nonce []byte, ok bool) {
	buf := make([]byte, len(challenge)+NonceLen)
	copy(buf, challenge)
	ctr := buf[len(buf)-8:]
	for n := from; n-from < tries; n++ {
		binary.BigEndian.PutUint64(ctr, n)
		if ZeroBits(sha256.Sum256(buf)) >= k {
			return append([]byte(nil), buf[len(challenge):]...), true
		}
	}
	return nil, false
}
