package app

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Patterns in who plays when: an alert when every tracked player sharing a tag (a clan, the
// neighbours) has gone offline, and "likely teams": players who keep joining or leaving the
// server within seconds of each other.

// tagOnline counts, per tag shared by 2+ tracked players, how many are online. Caller holds a.mu.
func (t *Tracker) tagOnline(tracked map[string]*TrackedPlayer) (online map[string]int, members map[string][]string, label map[string]string) {
	online, members, label = map[string]int{}, map[string][]string{}, map[string]string{}
	for key, tp := range tracked {
		tag := strings.ToLower(strings.TrimSpace(tp.Tag))
		if tag == "" {
			continue
		}
		if l := label[tag]; l == "" || (l == strings.ToLower(l) && tp.Tag != strings.ToLower(tp.Tag)) {
			label[tag] = tp.Tag // prefer the capitalised spelling
		}
		members[tag] = append(members[tag], tp.Name)
		if t.online[key] != nil {
			online[tag]++
		}
	}
	for tag, m := range members {
		if len(m) < 2 {
			delete(members, tag)
			delete(online, tag)
		}
	}
	return
}

// checkTagsOfflineLocked alerts when the last online member of a tag leaves.
func (t *Tracker) checkTagsOfflineLocked(before map[string]int, tracked map[string]*TrackedPlayer) {
	after, members, label := t.tagOnline(tracked)
	for tag, n := range before {
		if n > 0 && after[tag] == 0 && len(members[tag]) >= 2 {
			names := append([]string{}, members[tag]...)
			sort.Strings(names)
			t.a.alertLocked("clan_offline", fmt.Sprintf("Everyone tagged [%s] is offline", label[tag]),
				fmt.Sprintf("All %d players tagged [%s] are now offline (%s).", len(names), label[tag], strings.Join(names, ", ")))
		}
	}
}

// LikelyGroup is a set of players who keep joining or leaving together.
type LikelyGroup struct {
	Members  []GroupMember `json:"members"`
	Together int           `json:"together"` // joins/leaves within a minute of each other
}

type GroupMember struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Online  bool   `json:"online"`
	Tracked bool   `json:"tracked"`
}

const coWindow = 45 // seconds: "at the same time"

// likelyGroupsLocked finds groups from the join/leave log. Caller holds a.mu.
func (t *Tracker) likelyGroupsLocked(trackedKeys map[string]bool) []LikelyGroup {
	if time.Since(t.groupsAt) < 30*time.Second && t.groups != nil {
		return t.markGroups(t.groups, trackedKeys)
	}
	type ev struct {
		at int64
		id string
	}
	var joins, leaves []ev
	count := map[string]int{}
	names := map[string]string{}
	for _, a := range t.activity {
		names[a.ID] = a.Name
		count[a.ID]++
		if a.Joined {
			joins = append(joins, ev{a.Time, a.ID})
		} else {
			leaves = append(leaves, ev{a.Time, a.ID})
		}
	}
	pair := map[[2]string]int{}
	scan := func(es []ev) {
		for i := range es {
			seen := map[string]bool{es[i].id: true}
			for j := i + 1; j < len(es) && es[j].at-es[i].at <= coWindow; j++ {
				b := es[j].id
				if seen[b] {
					continue
				}
				seen[b] = true
				k := [2]string{es[i].id, b}
				if k[0] > k[1] {
					k[0], k[1] = k[1], k[0]
				}
				pair[k]++
			}
		}
	}
	scan(joins)
	scan(leaves)
	// union pairs that moved together at least 3 times, and most times either of them moved
	parent := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		if parent[x] == "" || parent[x] == x {
			parent[x] = x
			return x
		}
		parent[x] = find(parent[x])
		return parent[x]
	}
	score := map[string]int{}
	for k, n := range pair {
		lo := min(count[k[0]], count[k[1]])
		if n >= 3 && float64(n) >= 0.6*float64(lo) {
			ra, rb := find(k[0]), find(k[1])
			if ra != rb {
				parent[ra] = rb
			}
			score[k[0]] += n
			score[k[1]] += n
		}
	}
	byRoot := map[string][]string{}
	for id := range score {
		r := find(id)
		byRoot[r] = append(byRoot[r], id)
	}
	var out []LikelyGroup
	for _, ids := range byRoot {
		if len(ids) < 2 {
			continue
		}
		sort.Strings(ids)
		g := LikelyGroup{}
		for i, a := range ids {
			g.Members = append(g.Members, GroupMember{ID: a, Name: names[a]})
			for _, b := range ids[i+1:] {
				k := [2]string{a, b}
				if k[0] > k[1] {
					k[0], k[1] = k[1], k[0]
				}
				g.Together = max(g.Together, pair[k])
			}
		}
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Members) != len(out[j].Members) {
			return len(out[i].Members) > len(out[j].Members)
		}
		return out[i].Together > out[j].Together
	})
	if len(out) > 20 {
		out = out[:20]
	}
	t.groups, t.groupsAt = out, time.Now()
	return t.markGroups(out, trackedKeys)
}

func (t *Tracker) markGroups(gs []LikelyGroup, trackedKeys map[string]bool) []LikelyGroup {
	out := make([]LikelyGroup, len(gs))
	for i, g := range gs {
		out[i] = LikelyGroup{Together: g.Together}
		for _, m := range g.Members {
			m.Online = t.online[m.ID] != nil
			if p := t.online[m.ID]; p != nil {
				m.Name = p.Name
			}
			m.Tracked = trackedKeys[m.ID]
			out[i].Members = append(out[i].Members, m)
		}
	}
	return out
}
