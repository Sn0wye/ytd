package downloader

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseItems turns a spec like "1-10,15,20-" into sorted, unique 1-based
// positions within a playlist of n entries. An empty spec selects all.
// Ranges past the end are clipped; a spec that selects nothing is an error.
func ParseItems(spec string, n int) ([]int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		all := make([]int, n)
		for i := range all {
			all[i] = i + 1
		}
		return all, nil
	}

	picked := make([]bool, n+1)
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lo, hi := part, part
		if i := strings.Index(part, "-"); i >= 0 {
			lo, hi = strings.TrimSpace(part[:i]), strings.TrimSpace(part[i+1:])
		}
		from, to := 1, n
		var err error
		if lo != "" {
			if from, err = strconv.Atoi(lo); err != nil || from < 1 {
				return nil, fmt.Errorf("invalid --items %q: %q is not a position (positions start at 1)", spec, lo)
			}
		}
		if hi != "" {
			if to, err = strconv.Atoi(hi); err != nil || to < 1 {
				return nil, fmt.Errorf("invalid --items %q: %q is not a position (positions start at 1)", spec, hi)
			}
		}
		if from > to {
			return nil, fmt.Errorf("invalid --items %q: range %q goes backwards", spec, part)
		}
		for i := from; i <= min(to, n); i++ {
			picked[i] = true
		}
	}

	var out []int
	for i := 1; i <= n; i++ {
		if picked[i] {
			out = append(out, i)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--items %q selects nothing: the playlist has %d videos", spec, n)
	}
	return out, nil
}
