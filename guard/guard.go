// Package guard admits a request only with a solved, unused proof-of-work challenge,
// and raises the difficulty while more than Config.PerMinute requests get through.
//
//  1. client → GET challenge   server → {"challenge": b64, "bits": k}   (stateless)
//  2. client finds n: SHA-256(challenge ‖ n) has ≥ k leading zero bits
//  3. client → request with header X-PoW: <challenge>.<n>   server → handler, or 429
//
// Challenge: issued uint64be (8) | bits (1) | salt (16) | HMAC-SHA256 of the preceding (32).
// It is not bound to the client or endpoint: every request costs the same work, and each proof is single-use.
package guard

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"stockcycles/puzzle"
)

const (
	bodyLen = 8 + 1 + 16
	tokLen  = bodyLen + sha256.Size
	hardMax = 255 // bits is one byte
)

type Config struct {
	Key       []byte        // HMAC key, ≥ 32 bytes from crypto/rand
	PerMinute float64       // admitted requests per minute to sustain
	TTL       time.Duration // challenge lifetime
	Tau       time.Duration // how fast the difficulty follows the load
	MinBits   float64       // floor and starting difficulty
}

type Guard struct {
	cfg  Config
	now  func() time.Time
	mu   sync.Mutex
	cnt  [60]int64 // admitted requests per second, ring buffer
	sec  [60]int64 // unix second each slot belongs to
	last int64
	bits float64          // always in [MinBits, hardMax]
	seen map[string]int64 // spent tokens -> expiry
}

func New(c Config) *Guard {
	if len(c.Key) < 32 {
		panic("guard: key must be ≥ 32 bytes")
	}
	return &Guard{cfg: c, now: time.Now, bits: c.MinBits, seen: map[string]int64{}}
}

func (g *Guard) Bits() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return int(math.Round(g.bits))
}

func (g *Guard) countLocked(now, w int64) (n int64) {
	for i := range g.cnt {
		if now-g.sec[i] < w {
			n += g.cnt[i]
		}
	}
	return
}

// The controller looks at the last few seconds only, so it does not keep raising difficulty on stale counts.
const ctrlWindow = 5

// stepLocked retargets at most once per second, lazily on arrival. Only solved requests are counted,
// so the measured rate is H/2^bits for attacker hash rate H; the exact correction is
// bits += log2(rate/target), applied at speed Δt/τ. Rising is unclamped; falling ≤ 0.15 bit/s.
func (g *Guard) stepLocked(now int64) int {
	if now > g.last {
		dt := math.Min(float64(now-g.last), 60)
		g.last = now
		rate := float64(g.countLocked(now, ctrlWindow)) * 60 / ctrlWindow
		r := math.Max(0.25, rate/g.cfg.PerMinute)
		delta := math.Max(math.Log2(r)*dt/g.cfg.Tau.Seconds(), -0.15*dt)
		g.bits = math.Max(g.cfg.MinBits, math.Min(hardMax, g.bits+delta))
		for k, exp := range g.seen {
			if exp < now {
				delete(g.seen, k)
			}
		}
	}
	return int(math.Round(g.bits))
}

// ChallengeHandler must not be wrapped in Middleware.
func (g *Guard) ChallengeHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := g.now().Unix()
		g.mu.Lock()
		k := g.stepLocked(now)
		g.mu.Unlock()

		tok := make([]byte, bodyLen, tokLen)
		binary.BigEndian.PutUint64(tok, uint64(now))
		tok[8] = byte(k)
		rand.Read(tok[9:])
		tok = append(tok, g.mac(tok)...)

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(struct {
			Challenge string `json:"challenge"`
			Bits      int    `json:"bits"`
		}{base64.RawURLEncoding.EncodeToString(tok), k})
	})
}

func (g *Guard) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !g.admit(r) {
			http.Error(w, "proof of work required", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (g *Guard) admit(r *http.Request) bool {
	tok, nonce, ok := parseHeader(r.Header.Get("X-PoW"))
	if !ok {
		return false
	}
	now := g.now().Unix()
	g.mu.Lock()
	cur := g.stepLocked(now)
	g.mu.Unlock()
	if !g.validToken(tok, now, cur) || !puzzle.Valid(tok, nonce, int(tok[8])) {
		return false
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	key := string(tok)
	if _, spent := g.seen[key]; spent {
		return false
	}
	g.seen[key] = int64(binary.BigEndian.Uint64(tok)) + int64(g.cfg.TTL/time.Second)
	i := now % 60
	if g.sec[i] != now {
		g.sec[i], g.cnt[i] = now, 0
	}
	g.cnt[i]++
	return true
}

func parseHeader(h string) (tok, nonce []byte, ok bool) {
	a, b, found := strings.Cut(h, ".")
	if !found {
		return nil, nil, false
	}
	tok, err1 := base64.RawURLEncoding.DecodeString(a)
	nonce, err2 := base64.RawURLEncoding.DecodeString(b)
	if err1 != nil || err2 != nil || len(tok) != tokLen || len(nonce) != puzzle.NonceLen {
		return nil, nil, false
	}
	return tok, nonce, true
}

func (g *Guard) validToken(tok []byte, now int64, cur int) bool {
	if !hmac.Equal(tok[bodyLen:], g.mac(tok[:bodyLen])) {
		return false
	}
	issued, k := int64(binary.BigEndian.Uint64(tok)), int(tok[8])
	switch {
	case issued > now, now-issued > int64(g.cfg.TTL/time.Second):
		return false
	case k < int(g.cfg.MinBits), k+2 < cur: // cheap token from before the difficulty rose
		return false
	}
	return true
}

func (g *Guard) mac(b []byte) []byte {
	m := hmac.New(sha256.New, g.cfg.Key)
	m.Write(b)
	return m.Sum(nil)
}
