package model

import "sort"

// The roles auto-archive reads. done is the lane a ticket sits in before it is
// swept; archived is where it goes. Both are role keys from [board.roles], so
// a board that names its lanes differently still works — but one with no
// archived role has nowhere to sweep to, and the sweep stays off.
const (
	RoleDone     = "done"
	RoleArchived = "archived"
)

// secondsPerDay converts the archive threshold, given in days, to the
// lane-time seconds it is compared against.
const secondsPerDay = 86400

// HasRole reports whether role is one of the configured board roles.
func HasRole(roles []RolePair, role string) bool {
	for _, r := range roles {
		if r.Role == role {
			return true
		}
	}
	return false
}

// ArchiveCandidates returns the keys of tickets that have sat in done for at
// least days, sorted so the sweep runs in a stable order.
//
// lane is the read-only time-in-lane batch keyed by ticket key, where seconds
// is the time since the ticket last entered its current lane. A ticket with no
// entry, a nil entry, or seconds that is missing, non-numeric or zero never
// qualifies: zero is what a backend with no lane history reports, and "we do
// not know how long" must not read as "long enough". days <= 0 disables the
// sweep entirely.
func ArchiveCandidates(tickets []Ticket, lane map[string]map[string]any, days float64) []string {
	if days <= 0 {
		return nil
	}
	threshold := days * secondsPerDay
	var out []string
	for _, t := range tickets {
		if getStr(t, "status_role") != RoleDone {
			continue
		}
		key := getStr(t, "key")
		if key == "" {
			continue
		}
		secs, ok := lane[key]["seconds"].(float64)
		if !ok || secs <= 0 || secs < threshold {
			continue
		}
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
