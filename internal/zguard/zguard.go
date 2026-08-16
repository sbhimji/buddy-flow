// Package zguard is the single gate every z-score in the system passes
// through (mini-spec 2.2, F13): σ_used = max(σ_measured, floor). It is
// dependency-free by design — future metrics with no volume profile (bond
// velocity z, DeltaRatio z) must not drag one in. The floor is a required
// argument, so "forgot the guard" is not an expressible program.
package zguard

import "math"

// Z returns (value − center) / max(sigma, floor).
//
// ok=false is the defined null — the caller renders a gap, never an
// exception (D5):
//   - any input is NaN or ±Inf;
//   - max(sigma, floor) is not a positive number. σ is nonnegative by
//     construction (MAD, floors from medians of MADs); a negative input is
//     out of contract and treated as null rather than sign-flipping z.
//
// σ=0 with floor>0 is NOT null — producing a finite z there is the guard's
// entire purpose. Never panics; never returns NaN or ±Inf.
func Z(value, center, sigma, floor float64) (z float64, ok bool) {
	for _, x := range [...]float64{value, center, sigma, floor} {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, false
		}
	}
	used := math.Max(sigma, floor)
	if used <= 0 {
		return 0, false
	}
	z = (value - center) / used
	// Finite inputs can still overflow the subtraction (±~1e308 apart);
	// no Inf escapes.
	if math.IsInf(z, 0) {
		return 0, false
	}
	return z, true
}
