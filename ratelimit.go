package lobby

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// limiter is a fixed-window counter per key. It's deliberately simple: the
// goal is to make brute-forcing room codes or spamming room creation
// impractical, not to shape traffic precisely.
type limiter struct {
	limit  int
	window time.Duration

	mu        sync.Mutex
	hits      map[string]*window
	lastSweep time.Time
}

type window struct {
	start time.Time
	n     int
}

func newLimiter(limit int, per time.Duration) *limiter {
	return &limiter{limit: limit, window: per, hits: make(map[string]*window)}
}

// allow records a hit for key and reports whether it is within the limit,
// plus how long until the current window resets.
func (l *limiter) allow(key string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastSweep) > l.window {
		for k, w := range l.hits {
			if now.Sub(w.start) >= l.window {
				delete(l.hits, k)
			}
		}
		l.lastSweep = now
	}

	w := l.hits[key]
	if w == nil || now.Sub(w.start) >= l.window {
		w = &window{start: now}
		l.hits[key] = w
	}
	w.n++
	return w.n <= l.limit, w.start.Add(l.window).Sub(now)
}

// allow applies the rate limit to an HTTP request, writing a 429 if exceeded.
func (m *Manager) allow(w http.ResponseWriter, r *http.Request) bool {
	if m.limiter == nil {
		return true
	}
	ok, reset := m.limiter.allow(m.cfg.ClientIP(r), time.Now())
	if !ok {
		m.stats.rateLimited.Add(1)
		w.Header().Set("Retry-After", strconv.Itoa(int(reset.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, message(ErrRateLimited))
	}
	return ok
}

// tokenBucket allows rate events per second with bursts up to burst. It is
// used by one goroutine (a connection's read loop), so it has no lock.
type tokenBucket struct {
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

func newTokenBucket(rate float64, burst int, now time.Time) *tokenBucket {
	return &tokenBucket{rate: rate, burst: float64(burst), tokens: float64(burst), last: now}
}

func (b *tokenBucket) take(now time.Time) bool {
	b.tokens = min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// HeaderClientIP returns a Config.ClientIP that reads the client address
// from a header set by your reverse proxy, such as "X-Real-IP" or
// "X-Forwarded-For". For comma-separated lists it takes the last entry,
// the one appended by the proxy nearest to this server, since earlier
// entries are client-supplied and spoofable. Falls back to RemoteAddr.
//
// Only use this behind a proxy that sets the header: otherwise clients can
// pick their own rate-limit key.
func HeaderClientIP(header string) func(*http.Request) string {
	return func(r *http.Request) string {
		v := r.Header.Get(header)
		if i := strings.LastIndexByte(v, ','); i >= 0 {
			v = v[i+1:]
		}
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
		return remoteIP(r)
	}
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
