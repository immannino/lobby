package lobby

import "sync/atomic"

// Stats is a point-in-time snapshot of a Manager's activity, for metrics
// and health checks. Counters are cumulative since New; gauges are current.
type Stats struct {
	Rooms      int `json:"rooms"`      // gauge: open rooms
	WebSockets int `json:"websockets"` // gauge: attached websocket connections

	MessagesIn   uint64 `json:"messagesIn"`   // client → server messages accepted
	MessagesOut  uint64 `json:"messagesOut"`  // server → client deliveries (one per recipient)
	SlowDrops    uint64 `json:"slowDrops"`    // clients disconnected for not keeping up
	RateLimited  uint64 `json:"rateLimited"`  // HTTP requests and connections rejected by rate limits
	Panics       uint64 `json:"panics"`       // panics recovered from game hooks
	RoomsCreated uint64 `json:"roomsCreated"` // rooms ever created (not restored)
}

type counters struct {
	webSockets   atomic.Int64
	messagesIn   atomic.Uint64
	messagesOut  atomic.Uint64
	slowDrops    atomic.Uint64
	rateLimited  atomic.Uint64
	panics       atomic.Uint64
	roomsCreated atomic.Uint64
}

// Stats returns current activity counters. It is safe to call from any
// goroutine; publish it with expvar, Prometheus, or a health endpoint:
//
//	expvar.Publish("lobby", expvar.Func(func() any { return m.Stats() }))
func (m *Manager) Stats() Stats {
	return Stats{
		Rooms:        m.Rooms(),
		WebSockets:   int(m.stats.webSockets.Load()),
		MessagesIn:   m.stats.messagesIn.Load(),
		MessagesOut:  m.stats.messagesOut.Load(),
		SlowDrops:    m.stats.slowDrops.Load(),
		RateLimited:  m.stats.rateLimited.Load(),
		Panics:       m.stats.panics.Load(),
		RoomsCreated: m.stats.roomsCreated.Load(),
	}
}
