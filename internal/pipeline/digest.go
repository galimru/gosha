package pipeline

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/galimru/gosha/internal/notify"
	"github.com/galimru/gosha/internal/store"
)

// metaDigestSent is the store key holding the local date of the last digest.
const metaDigestSent = "digest_sent"

// DigestLoop sends the daily digest at telegram.digest_at (local time)
// until ctx ends. A digest missed while Gosha was down is sent late, once,
// on the same day; one already sent isn't sent again after a restart.
func (e *Env) DigestLoop(ctx context.Context) {
	at := e.Global.Telegram.Digest()
	if at == "" {
		return
	}
	for {
		sent, err := e.Store.Meta(ctx, metaDigestSent)
		if err != nil {
			e.Log.Warn("digest", "err", err)
		}
		now := time.Now()
		due, next := digestDue(now, at, sent)
		if due {
			if err := e.sendDigest(ctx, now, at); err != nil {
				e.Log.Warn("digest", "err", e.Redactor.Redact(err.Error()))
			}
			if err := e.Store.SetMeta(ctx, metaDigestSent, now.Format(time.DateOnly)); err != nil {
				e.Log.Warn("digest", "err", err)
				next = now.Add(time.Hour) // don't resend in a tight loop
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
		}
	}
}

// digestDue reports whether today's digest is due at now, given the local
// date it was last sent on, and when to look again.
func digestDue(now time.Time, at, sent string) (due bool, next time.Time) {
	hm, _ := time.Parse("15:04", at)
	fire := time.Date(now.Year(), now.Month(), now.Day(), hm.Hour(), hm.Minute(), 0, 0, now.Location())
	if now.Before(fire) {
		return false, fire
	}
	return sent != now.Format(time.DateOnly), fire.AddDate(0, 0, 1)
}

// sendDigest sends the summary of the local day before now.
func (e *Env) sendDigest(ctx context.Context, now time.Time, at string) error {
	names, err := e.Home.Projects()
	if err != nil {
		return err
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	counts, err := e.Store.Counts(ctx, today.AddDate(0, 0, -1), today)
	if err != nil {
		return err
	}
	ready := map[string]int{}
	e.readyMu.Lock()
	for _, n := range names {
		r, ok := e.ready[n]
		if !ok {
			r = -1
		}
		ready[n] = r
	}
	e.readyMu.Unlock()
	e.Notify.Send(notify.Event{Text: digestText(at, names, counts, ready)})
	return nil
}

// noteReady records how many tickets the project's last poll found Ready,
// for the digest.
func (e *Env) noteReady(name string, n int) {
	e.readyMu.Lock()
	defer e.readyMu.Unlock()
	if e.ready == nil {
		e.ready = map[string]int{}
	}
	e.ready[name] = n
}

// digestText is e.g. "09:00 · yesterday: 4 delivered, 1 blocked · queue: 3
// Ready", with a line per project when there's more than one. A Ready
// count of -1 couldn't be read, and shows as "?".
func digestText(at string, names []string, counts map[string]store.DayCounts, ready map[string]int) string {
	var total store.DayCounts
	queue, unknown := 0, false
	for _, n := range names {
		total.Delivered += counts[n].Delivered
		total.Blocked += counts[n].Blocked
		if ready[n] < 0 {
			unknown = true
		} else {
			queue += ready[n]
		}
	}
	line := func(c store.DayCounts, queue string) string {
		return fmt.Sprintf("%d delivered, %d blocked · queue: %s Ready", c.Delivered, c.Blocked, queue)
	}
	q := fmt.Sprint(queue)
	if unknown {
		q += "+?"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s · yesterday: %s", at, line(total, q))
	if len(names) > 1 {
		for _, n := range names {
			q := "?"
			if ready[n] >= 0 {
				q = fmt.Sprint(ready[n])
			}
			fmt.Fprintf(&b, "\n%s: %s", n, line(counts[n], q))
		}
	}
	return b.String()
}
