package pinger

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"sync/atomic"
	"time"

	probing "github.com/prometheus-community/pro-bing"
)

// StatsUpdate is one snapshot of a target's ping state, sent on the
// shared updates channel after each send/receive/error event. The UI
// keeps only the latest update per TargetID, so each value is a full
// replacement rather than an increment.
type StatsUpdate struct {
	TargetID string
	Sent     int64
	Recv     int64
	RTT      time.Duration // most recent successful sample; zero if N/A
	MinRTT   time.Duration // smallest RTT observed; zero until first reply
	AvgRTT   time.Duration // running mean over recv replies; zero until first reply
	MaxRTT   time.Duration // largest RTT observed; zero until first reply
	Jitter   time.Duration // RFC 3550 smoothed inter-packet jitter; zero until 2nd reply
	LastErr  error         // sticky last error for display; nil on success
	Dropped  bool          // pinger has stopped; UI should remove this target
}

// Pinger runs a continuous ICMP echo loop against a single target and
// publishes StatsUpdates on Updates. Construct one per target.
type Pinger struct {
	ID       string
	Host     string
	Mode     Mode
	Interval time.Duration
	Size     int
	Drop     int // >0: drop target after this many sends with zero recvs
	Updates  chan<- StatsUpdate
}

// Run blocks until ctx is cancelled, sending one echo per Interval and
// emitting a StatsUpdate after each send, receive, or recv error. A
// resolver/socket setup failure is emitted as a StatsUpdate with
// LastErr set and then returned, so the UI shows the row in an errored
// state rather than silently absent.
func (p *Pinger) Run(ctx context.Context) error {
	pp, err := probing.NewPinger(p.Host)
	if err != nil {
		p.emit(ctx, StatsUpdate{TargetID: p.ID, LastErr: fmt.Errorf("resolve: %w", err)})
		return err
	}
	pp.Interval = p.Interval
	// pro-bing always builds a time.NewTicker from Timeout and panics on
	// zero. We never want it to fire — ctx-cancel triggers Stop() — so
	// set it to the maximum representable duration.
	pp.Timeout = time.Duration(math.MaxInt64)
	pp.Size = p.Size
	pp.RecordRtts = false
	pp.SetPrivileged(p.Mode == ModePrivileged)
	// pro-bing's default logger writes to stderr on every failed send,
	// which scribbles over the alt screen. Errors reach the UI through
	// OnSendError/OnRecvError instead.
	pp.SetLogger(probing.NoopLogger{})

	// pCtx lets the pinger stop itself (on drop) without waiting for
	// the parent ctx, while still inheriting cancellation from it.
	pCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var sent, recv atomic.Int64
	// prevRTT/jitter/min/sum/max are written only from OnRecv (pro-bing's
	// recv loop is single-goroutine) but read from OnSend's snapshot, so
	// atomics are still the right choice.
	var prevRTT, jitter atomic.Int64
	var minRTT, sumRTT, maxRTT atomic.Int64
	minRTT.Store(math.MaxInt64) // sentinel so the first reply always becomes the min
	snapshot := func(rtt time.Duration, lastErr error) StatsUpdate {
		min := time.Duration(minRTT.Load())
		if min == time.Duration(math.MaxInt64) {
			min = 0 // no replies yet; UI renders as "—"
		}
		var avg time.Duration
		if n := recv.Load(); n > 0 {
			avg = time.Duration(sumRTT.Load() / n)
		}
		return StatsUpdate{
			TargetID: p.ID,
			Sent:     sent.Load(),
			Recv:     recv.Load(),
			RTT:      rtt,
			MinRTT:   min,
			AvgRTT:   avg,
			MaxRTT:   time.Duration(maxRTT.Load()),
			Jitter:   time.Duration(jitter.Load()),
			LastErr:  lastErr,
		}
	}

	pp.OnSend = func(*probing.Packet) {
		n := sent.Add(1)
		if p.Drop > 0 && n >= int64(p.Drop) && recv.Load() == 0 {
			u := snapshot(0, nil)
			u.Dropped = true
			p.emit(pCtx, u)
			cancel()
			return
		}
		// Emit with sent-1 so loss% reflects the settled outcome of
		// prior packets, not the just-sent (in-flight) one. Otherwise
		// the display flickers between 0% and (1/N)*100% on every
		// send tick as sent leads recv by one packet.
		u := snapshot(0, nil)
		u.Sent = n - 1
		p.emit(pCtx, u)
	}
	pp.OnRecv = func(pkt *probing.Packet) {
		recv.Add(1)
		rtt := int64(pkt.Rtt)
		sumRTT.Add(rtt)
		if cur := minRTT.Load(); rtt < cur {
			minRTT.Store(rtt)
		}
		if cur := maxRTT.Load(); rtt > cur {
			maxRTT.Store(rtt)
		}
		// RFC 3550 smoothed jitter: J = J + (|D| - J)/16. Skip the
		// first reply (prev == 0), where the delta is undefined.
		prev := time.Duration(prevRTT.Swap(rtt))
		if prev > 0 {
			d := pkt.Rtt - prev
			if d < 0 {
				d = -d
			}
			j := time.Duration(jitter.Load())
			jitter.Store(int64(j + (d-j)/16))
		}
		p.emit(pCtx, snapshot(pkt.Rtt, nil))
	}
	pp.OnRecvError = func(err error) {
		// pro-bing's recv loop fires OnRecvError on every read
		// deadline tick as part of its normal poll cycle. Those
		// aren't real failures; ignore them so the UI doesn't
		// flicker between the latest RTT and "err".
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return
		}
		p.emit(pCtx, snapshot(0, err))
	}
	pp.OnSendError = func(_ *probing.Packet, err error) {
		p.emit(pCtx, snapshot(0, err))
	}

	// pro-bing.Run() blocks until Stop is called. Translate ctx cancel
	// into Stop, then let Run return naturally.
	go func() {
		<-pCtx.Done()
		pp.Stop()
	}()
	return pp.Run()
}

// emit sends an update with bounded blocking. A full channel only
// stalls until ctx is cancelled; missing one update is fine since
// every update is a full snapshot.
func (p *Pinger) emit(ctx context.Context, u StatsUpdate) {
	select {
	case p.Updates <- u:
	case <-ctx.Done():
	}
}
