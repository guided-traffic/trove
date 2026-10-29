package config

// closest returns the candidate nearest to name by edit distance, if it is near
// enough to be a likely typo.
func closest(name string, candidates []string) (string, bool) {
	best, bestD := "", 1<<30
	for _, c := range candidates {
		if d := levenshtein(name, c); d < bestD {
			best, bestD = c, d
		}
	}
	limit := len(name) / 3
	if limit < 2 {
		limit = 2
	}
	return best, best != "" && bestD <= limit
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}
