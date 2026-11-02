package app

import (
	"fmt"
	"sort"
	"time"
	_ "time/tzdata" // Europe/London for the forced-wipe time on Windows, which has no zone files
)

// Server stats: population every 10 minutes (kept 14 days), the busiest and quietest hours,
// and the next forced wipe.

type PopPoint struct {
	T int64 `json:"t"`
	P int32 `json:"p"` // players
	M int32 `json:"m"` // max players
	Q int32 `json:"q"` // queued
}

// recordPopLocked adds a population sample at most every 10 minutes. Caller holds a.mu.
func (a *App) recordPopLocked(now time.Time) {
	if a.srv == nil || a.info == nil || a.info.MaxPlayers == 0 {
		return
	}
	pl := a.srv.PopLog
	if n := len(pl); n > 0 && now.Unix()-pl[n-1].T < 600 {
		return
	}
	a.srv.PopLog = append(pl, PopPoint{now.Unix(), int32(a.info.Players), int32(a.info.MaxPlayers), int32(a.info.QueuedPlayers)})
	cut := now.Add(-14 * 24 * time.Hour).Unix()
	i := 0
	for i < len(a.srv.PopLog) && a.srv.PopLog[i].T < cut {
		i++
	}
	a.srv.PopLog = a.srv.PopLog[i:]
	a.saveLocked()
}

// NextForcedWipe is the first Thursday of the month at 19:00 UK time, when Facepunch's
// monthly update wipes every server.
func NextForcedWipe(now time.Time) time.Time {
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		loc = time.UTC
	}
	n := now.In(loc)
	for k := 0; k < 3; k++ {
		first := time.Date(n.Year(), n.Month()+time.Month(k), 1, 19, 0, 0, 0, loc)
		for first.Weekday() != time.Thursday {
			first = first.AddDate(0, 0, 1)
		}
		if first.After(now) {
			return first
		}
	}
	return time.Time{}
}

type HourStat struct {
	Hour int     `json:"hour"`
	Avg  float64 `json:"avg"`
	N    int     `json:"n"`
}

type StatsView struct {
	Points     []PopPoint `json:"points"`
	Hours      []HourStat `json:"hours"`  // average players by hour of day (your PC's time)
	Quiet      []int      `json:"quiet"`  // the 3 quietest hours
	Busy       []int      `json:"busy"`   // the 3 busiest hours
	Peak       PopPoint   `json:"peak"`   // the highest sample
	Forced     int64      `json:"forced"` // next forced wipe (unix)
	ForcedIn   string     `json:"forcedIn"`
	WipedAt    int64      `json:"wipedAt"` // this server's last wipe
	WipeAgo    string     `json:"wipeAgo"`
	DaysOfData float64    `json:"daysOfData"`
}

func (a *App) Stats() StatsView {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	v := StatsView{Points: []PopPoint{}, Hours: []HourStat{}}
	f := NextForcedWipe(now)
	v.Forced, v.ForcedIn = f.Unix(), fmtDur(time.Until(f))
	if a.srv == nil {
		return v
	}
	if a.info != nil && a.info.WipeTime > 0 {
		v.WipedAt = int64(a.info.WipeTime)
		v.WipeAgo = fmtDur(time.Since(time.Unix(v.WipedAt, 0)))
	}
	v.Points = append(v.Points, a.srv.PopLog...)
	var sum [24]float64
	var cnt [24]int
	for _, p := range v.Points {
		h := time.Unix(p.T, 0).Hour()
		sum[h] += float64(p.P)
		cnt[h]++
		if p.P > v.Peak.P {
			v.Peak = p
		}
	}
	var have []HourStat
	for h := 0; h < 24; h++ {
		hs := HourStat{Hour: h, N: cnt[h]}
		if cnt[h] > 0 {
			hs.Avg = sum[h] / float64(cnt[h])
			have = append(have, hs)
		}
		v.Hours = append(v.Hours, hs)
	}
	if len(v.Points) > 1 {
		v.DaysOfData = float64(v.Points[len(v.Points)-1].T-v.Points[0].T) / 86400
	}
	if len(have) >= 6 {
		sort.Slice(have, func(i, j int) bool { return have[i].Avg < have[j].Avg })
		for i := 0; i < 3; i++ {
			v.Quiet = append(v.Quiet, have[i].Hour)
			v.Busy = append(v.Busy, have[len(have)-1-i].Hour)
		}
	}
	return v
}

// wipeLineLocked is the extra text for !wipe.
func forcedWipeLine(now time.Time) string {
	f := NextForcedWipe(now)
	if f.IsZero() {
		return ""
	}
	return fmt.Sprintf(" Next forced wipe: %s UK time (in %s).", f.Format("Thu 2 Jan 15:04"), fmtDur(f.Sub(now)))
}
