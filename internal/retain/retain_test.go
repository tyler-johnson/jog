package retain

import (
	"testing"
	"time"
)

var now = time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

// synthetic timeline: every 10 minutes for the last 2 days, then hourly
// back to 10 days, then every 6 hours back to 100 days.
func timeline() []time.Time {
	var ts []time.Time
	for m := 0; m < 2*24*6; m++ {
		ts = append(ts, now.Add(-time.Duration(m)*10*time.Minute))
	}
	for h := 2 * 24; h < 10*24; h++ {
		ts = append(ts, now.Add(-time.Duration(h)*time.Hour))
	}
	for h := 10 * 24; h < 100*24; h += 6 {
		ts = append(ts, now.Add(-time.Duration(h)*time.Hour))
	}
	return ts
}

func kept(p Policy, at time.Time, ts []time.Time) []time.Time {
	var out []time.Time
	for i, k := range p.Keep(at, ts) {
		if k {
			out = append(out, ts[i])
		}
	}
	return out
}

func TestTaper(t *testing.T) {
	ts := timeline()
	surv := kept(Default, now, ts)

	counts := map[string]int{}
	for _, s := range surv {
		age := now.Sub(s)
		switch {
		case age <= Default.KeepAll:
			counts["all"]++
		case age <= Default.KeepHourly:
			counts["hourly"]++
		case age <= Default.KeepDaily:
			counts["daily"]++
		default:
			t.Errorf("snapshot older than KeepDaily survived: %v (age %v)", s, age)
		}
	}
	// ≤24h: every 10-minute snapshot survives = 6/hour × 24h + the T-24h one.
	if counts["all"] != 6*24+1 {
		t.Errorf("keep-all tier: got %d, want %d", counts["all"], 6*24+1)
	}
	// 24h–7d: one per hour. Hours 24–48 have 6 candidates each, 48–168 one.
	if counts["hourly"] != 6*24 {
		t.Errorf("hourly tier: got %d, want %d", counts["hourly"], 6*24)
	}
	// 7d–90d: one per epoch-aligned day bucket. The age window's timestamps
	// run May 10 12:00 – Aug 1 12:00 UTC: two partial edge days plus the
	// full days between = 84 buckets, each contributing one survivor.
	if counts["daily"] != 84 {
		t.Errorf("daily tier: got %d, want %d", counts["daily"], 84)
	}

	// Newest-per-bucket: in a dense hour bucket the survivor is its newest.
	bucket := now.Add(-30 * time.Hour).UTC().Truncate(time.Hour)
	var newest, surviving time.Time
	for _, x := range ts {
		if x.UTC().Truncate(time.Hour).Equal(bucket) && x.After(newest) {
			newest = x
		}
	}
	for _, s := range surv {
		if s.UTC().Truncate(time.Hour).Equal(bucket) {
			surviving = s
		}
	}
	if !surviving.Equal(newest) {
		t.Errorf("hour bucket %v: survivor %v is not the newest %v", bucket, surviving, newest)
	}
}

// Idempotence: at the same now, the survivors all survive a second pass —
// trim can rerun without eating its own output.
func TestIdempotentAtFixedNow(t *testing.T) {
	surv := kept(Default, now, timeline())
	for i, k := range Default.Keep(now, surv) {
		if !k {
			t.Fatalf("survivor %v dropped on second pass", surv[i])
		}
	}
}

// No resurrection: as now advances, the kept set only shrinks — a dropped
// snapshot never comes back (it couldn't; trim already deleted it).
func TestNoResurrection(t *testing.T) {
	ts := timeline()
	prev := Default.Keep(now, ts)
	for _, dt := range []time.Duration{10 * time.Minute, time.Hour, 25 * time.Hour, 8 * 24 * time.Hour} {
		next := Default.Keep(now.Add(dt), ts)
		for i := range ts {
			if next[i] && !prev[i] {
				t.Fatalf("snapshot %v resurrected after now+%v", ts[i], dt)
			}
		}
		prev = next
	}
}

// Bucket edges: a snapshot exactly at the KeepAll boundary is kept whole;
// one just past it competes in its hour bucket.
func TestBoundaries(t *testing.T) {
	edge := now.Add(-Default.KeepAll)
	inside := edge.Add(time.Second)
	outside := edge.Add(-time.Second)
	verdict := Default.Keep(now, []time.Time{inside, edge, outside})
	if !verdict[0] || !verdict[1] {
		t.Errorf("snapshots at/inside the KeepAll edge must be kept: %v", verdict)
	}
	// outside is alone in its hour bucket → newest → kept too.
	if !verdict[2] {
		t.Errorf("lone hour-bucket snapshot must survive: %v", verdict)
	}
	// Beyond KeepDaily: always dropped.
	ancient := now.Add(-Default.KeepDaily - time.Hour)
	if v := Default.Keep(now, []time.Time{ancient}); v[0] {
		t.Error("snapshot older than KeepDaily survived")
	}
}
