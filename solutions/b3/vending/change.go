package vending

// makeChange returns a set of coins from pool (denomination -> available
// count) that sums to exactly amount, using as few coins as possible, or
// false if no exact combination exists.
//
// Greedy (largest coin first) is WRONG with finite stock: with one quarter and
// three dimes, 30 cents is 3 dimes, but greedy takes the quarter and is stuck
// needing 5. This is a bounded coin-change DP: best[a] is the fewest coins
// that make a using the denominations processed so far. Cost O(D * A * K) for
// D denominations, amount A (after dividing by the gcd) and per-coin counts K.
// denoms must be ascending.
func makeChange(amount int, denoms []int, pool map[int]int) (map[int]int, bool) {
	if amount == 0 {
		return map[int]int{}, true
	}
	g := 0
	for _, d := range denoms {
		g = gcd(g, d)
	}
	if amount%g != 0 {
		return nil, false
	}
	units := amount / g
	const inf = int(^uint(0) >> 1)
	prev := make([]int, units+1)
	for i := range prev {
		prev[i] = inf
	}
	prev[0] = 0
	choice := make([][]int, len(denoms))
	for i, d := range denoms {
		u := d / g
		cur := make([]int, units+1)
		choice[i] = make([]int, units+1)
		for a := 0; a <= units; a++ {
			cur[a] = inf
			for k := 0; k <= pool[d] && k*u <= a; k++ {
				if p := prev[a-k*u]; p != inf && p+k < cur[a] {
					cur[a], choice[i][a] = p+k, k
				}
			}
		}
		prev = cur
	}
	if prev[units] == inf {
		return nil, false
	}
	out := map[int]int{}
	a := units
	for i := len(denoms) - 1; i >= 0; i-- {
		if k := choice[i][a]; k > 0 {
			out[denoms[i]] = k
			a -= k * (denoms[i] / g)
		}
	}
	return out, true
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}
