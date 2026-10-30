//go:build js && wasm

// All computation, compiled to WebAssembly (TinyGo). Called from app.js as
// findCycles(times, closes, sortedThresholds) -> {average, cycles: [{peak, low, recovery, drop, days, band}], boxes: [{stats, outliers}]}
// powSolve(challenge, bits, from, tries) -> X-PoW header value, or null if none of these nonces solves it
package main

import (
	"encoding/base64"
	"sort"
	"syscall/js"

	"stockcycles/puzzle"
)

func float64s(v js.Value) []float64 {
	s := make([]float64, v.Length())
	for i := range s {
		s[i] = v.Index(i).Float()
	}
	return s
}

// quantile with linear interpolation, like numpy and matplotlib.
func quantile(sorted []float64, p float64) float64 {
	h := float64(len(sorted)-1) * p
	i := int(h)
	if i+1 >= len(sorted) {
		return sorted[i]
	}
	return sorted[i] + (sorted[i+1]-sorted[i])*(h-float64(i))
}

// boxStats returns matplotlib's boxplot numbers: whiskers at the furthest points within 1.5 IQR.
func boxStats(values []float64) map[string]any {
	if len(values) == 0 {
		return map[string]any{"stats": "-", "outliers": []any{}}
	}
	sort.Float64s(values)
	q1, median, q3 := quantile(values, 0.25), quantile(values, 0.5), quantile(values, 0.75)
	whiskerLow, whiskerHigh, outliers := q3, q1, []any{}
	for _, v := range values {
		if v < q1-1.5*(q3-q1) || v > q3+1.5*(q3-q1) {
			outliers = append(outliers, v)
		} else {
			whiskerLow, whiskerHigh = min(whiskerLow, v), max(whiskerHigh, v)
		}
	}
	return map[string]any{"stats": []any{whiskerLow, q1, median, q3, whiskerHigh}, "outliers": outliers}
}

func findCycles(_ js.Value, args []js.Value) any {
	times, closes, thresholds := float64s(args[0]), float64s(args[1]), float64s(args[2])
	n, sum, cycles, stack := len(closes), 0.0, []any{}, []int{}
	weeksByBand := make([][]float64, len(thresholds))
	isLocalMin := make([]bool, n)
	for i := range closes {
		sum += closes[i]
		isLocalMin[i] = i > 0 && i < n-1 && closes[i] < closes[i-1] && closes[i] < closes[i+1]
	}
	for recovery := 1; recovery < n; recovery++ { // 1:1 port of the stack logic in stock.py
		if recovery+1 < n && closes[recovery+1] > closes[recovery] {
			continue
		}
		peak := -1
		for len(stack) > 0 && closes[recovery] >= closes[stack[len(stack)-1]] {
			peak, stack = stack[len(stack)-1], stack[:len(stack)-1]
		}
		if peak >= 0 {
			low := -1
			for i := peak + 1; i < recovery; i++ {
				if isLocalMin[i] && (low < 0 || closes[i] < closes[low]) {
					low = i
				}
			}
			if low >= 0 {
				drop := (closes[peak] - closes[low]) * 100 / closes[peak]
				days, band := int((times[recovery]-times[peak])/864e5), -1
				for k := range thresholds { // highest threshold <= drop; -1 = below all (grey in stock.py)
					if drop >= thresholds[k] {
						band = k
					}
				}
				if band >= 0 {
					weeksByBand[band] = append(weeksByBand[band], float64(days)/7)
				}
				cycles = append(cycles, map[string]any{"peak": peak, "low": low, "recovery": recovery,
					"drop": drop, "days": days, "band": band})
			}
		}
		stack = append(stack, recovery)
	}
	boxes := make([]any, len(thresholds))
	for band, weeks := range weeksByBand {
		boxes[band] = boxStats(weeks)
	}
	return map[string]any{"average": sum / float64(max(n, 1)), "cycles": cycles, "boxes": boxes}
}

func powSolve(_ js.Value, args []js.Value) any {
	b64 := base64.RawURLEncoding
	challenge, err := b64.DecodeString(args[0].String())
	if err != nil {
		return nil
	}
	nonce, ok := puzzle.Solve(challenge, args[1].Int(), uint64(args[2].Float()), uint64(args[3].Float()))
	if !ok {
		return nil
	}
	return args[0].String() + "." + b64.EncodeToString(nonce)
}

func main() {
	js.Global().Set("findCycles", js.FuncOf(findCycles))
	js.Global().Set("powSolve", js.FuncOf(powSolve))
	select {} // keep the runtime alive for calls from JavaScript
}
