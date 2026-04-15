package session

import (
	"math/rand"
	"strings"
	"time"
	"unicode/utf8"
)

// SegmentOptions controls split behavior and timing simulation.
type SegmentOptions struct {
	Delimiter           string        // default "[[SEND]]"
	MaxRunes            int           // hard cap per segment, default 80
	MinRunes            int           // merge segments shorter than this, default 2
	MaxFallbackSegments int           // fallback path: if result > N segments, send as one; default 3
	BaseDelay           time.Duration // default 400ms
	PerReadRune         time.Duration // default 35ms (simulated reading speed)
	PerTypeRune         time.Duration // default 80ms (simulated typing speed)
	MinDelay            time.Duration // default 600ms
	MaxDelay            time.Duration // default 2000ms
	FirstMinDelay       time.Duration // default 300ms
	FirstMaxDelay       time.Duration // default 1500ms
	JitterFraction      float64       // ±jitter, default 0.2
}

// DefaultSegmentOptions returns the recommended defaults for companion workspaces.
func DefaultSegmentOptions() SegmentOptions {
	return SegmentOptions{
		Delimiter:           "[[SEND]]",
		MaxRunes:            80,
		MinRunes:            2,
		MaxFallbackSegments: 3,
		BaseDelay:           400 * time.Millisecond,
		PerReadRune:         35 * time.Millisecond,
		PerTypeRune:         80 * time.Millisecond,
		MinDelay:            600 * time.Millisecond,
		MaxDelay:            2000 * time.Millisecond,
		FirstMinDelay:       300 * time.Millisecond,
		FirstMaxDelay:       1500 * time.Millisecond,
		JitterFraction:      0.2,
	}
}

// SplitSegments splits an assistant text into ordered, sendable segments.
//
// Primary path: split by opts.Delimiter, trim each piece.
// Fallback (no delimiter present): split by paragraph (\n{2,}), then
// by sentence (。！？!?. ), then hardSplit by MaxRunes.
// If fallback produces > MaxFallbackSegments, returns the original text
// as a single segment to avoid semantic fragmentation.
//
// Always strips empty results; never returns nil.
func SplitSegments(text string, opts SegmentOptions) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{}
	}

	if strings.Contains(text, opts.Delimiter) {
		return splitByDelimiter(text, opts)
	}
	return splitFallback(text, opts)
}

// splitByDelimiter handles the primary path when delimiter is present.
func splitByDelimiter(text string, opts SegmentOptions) []string {
	raw := strings.Split(text, opts.Delimiter)
	parts := filterEmpty(raw)
	parts = greedyMerge(parts, opts.MinRunes, opts.MaxRunes)
	parts = hardSplitAll(parts, opts.MaxRunes)
	return parts
}

// splitFallback handles the fallback path when no delimiter is present.
func splitFallback(text string, opts SegmentOptions) []string {
	parts := splitByParagraph(text)
	if len(parts) == 1 && utf8.RuneCountInString(text) > opts.MaxRunes {
		parts = splitBySentence(text)
	}
	parts = filterEmpty(parts)
	parts = greedyMerge(parts, opts.MinRunes, opts.MaxRunes)
	parts = hardSplitAll(parts, opts.MaxRunes)
	if len(parts) > opts.MaxFallbackSegments {
		return []string{text}
	}
	return parts
}

// splitByParagraph splits text on two or more consecutive newlines.
func splitByParagraph(text string) []string {
	// Split on \n\n or more
	var parts []string
	remaining := text
	for {
		idx := strings.Index(remaining, "\n\n")
		if idx < 0 {
			parts = append(parts, remaining)
			break
		}
		parts = append(parts, remaining[:idx])
		// Skip all consecutive newlines
		rest := remaining[idx:]
		i := 0
		for i < len(rest) && rest[i] == '\n' {
			i++
		}
		remaining = rest[i:]
	}
	return parts
}

// splitBySentence splits text at sentence-ending punctuation.
// Supported: 。！？!? and `. ` (period followed by space).
func splitBySentence(text string) []string {
	var parts []string
	var current strings.Builder
	runes := []rune(text)

	for i, r := range runes {
		current.WriteRune(r)

		switch r {
		case '。', '！', '？', '!', '?':
			parts = append(parts, current.String())
			current.Reset()
		case '.':
			// English sentence: `. ` (period followed by space or end)
			if i+1 < len(runes) && runes[i+1] == ' ' {
				parts = append(parts, current.String())
				current.Reset()
			} else if i+1 == len(runes) {
				// Period at end of string
				parts = append(parts, current.String())
				current.Reset()
			}
		case '\n':
			// Treat single newline as potential split point during sentence splitting
			if current.Len() > 0 {
				parts = append(parts, strings.TrimRight(current.String(), "\n"))
				current.Reset()
			}
		}
	}

	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

// greedyMerge merges adjacent segments shorter than minRunes into the next segment,
// as long as the merged result does not exceed maxRunes.
func greedyMerge(parts []string, minRunes, maxRunes int) []string {
	if len(parts) == 0 {
		return parts
	}

	result := make([]string, 0, len(parts))
	pending := ""

	for _, p := range parts {
		if pending == "" {
			if utf8.RuneCountInString(p) < minRunes {
				pending = p
			} else {
				result = append(result, p)
			}
			continue
		}

		// We have a pending short segment; try to merge with p
		merged := pending + " " + p
		if utf8.RuneCountInString(merged) <= maxRunes {
			// Accept merge; keep merging if still short
			if utf8.RuneCountInString(merged) < minRunes {
				pending = merged
			} else {
				result = append(result, merged)
				pending = ""
			}
		} else {
			// Cannot merge without exceeding maxRunes; flush pending and start fresh
			result = append(result, pending)
			if utf8.RuneCountInString(p) < minRunes {
				pending = p
			} else {
				result = append(result, p)
				pending = ""
			}
		}
	}

	if pending != "" {
		result = append(result, pending)
	}
	return result
}

// hardSplitAll applies hardSplit to every segment in parts.
func hardSplitAll(parts []string, maxRunes int) []string {
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		result = append(result, hardSplit(p, maxRunes)...)
	}
	return result
}

// hardSplit splits a single segment that exceeds maxRunes.
// Tries to cut at punctuation; falls back to rune boundary.
func hardSplit(text string, maxRunes int) []string {
	if utf8.RuneCountInString(text) <= maxRunes {
		return []string{text}
	}

	var parts []string
	runes := []rune(text)

	for len(runes) > maxRunes {
		// Look for a punctuation cut point within [maxRunes/2 .. maxRunes]
		cutAt := maxRunes
		for i := maxRunes - 1; i >= maxRunes/2; i-- {
			if isPunct(runes[i]) {
				cutAt = i + 1
				break
			}
		}
		parts = append(parts, string(runes[:cutAt]))
		runes = runes[cutAt:]
	}

	if len(runes) > 0 {
		parts = append(parts, string(runes))
	}
	return parts
}

// isPunct reports whether r is a sentence-ending or splitting punctuation.
func isPunct(r rune) bool {
	switch r {
	case '。', '！', '？', '!', '?', '.', ',', '，', '、', ';', '；', ' ':
		return true
	}
	return false
}

// filterEmpty removes empty (after TrimSpace) strings from parts.
func filterEmpty(parts []string) []string {
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// TypingDelay returns the simulated delay before sending the next segment.
// When isFirst=true, prev is ignored; delay uses First*Delay bounds.
// randSource allows deterministic tests; pass nil to use math/rand.
func TypingDelay(prev, next string, isFirst bool, opts SegmentOptions,
	randSource func() float64) time.Duration {

	// zero-value defence: fill missing bounds from defaults
	def := DefaultSegmentOptions()
	if opts.MaxDelay == 0 {
		opts.MaxDelay = def.MaxDelay
	}
	if opts.MinDelay == 0 {
		opts.MinDelay = def.MinDelay
	}
	if opts.FirstMinDelay == 0 {
		opts.FirstMinDelay = def.FirstMinDelay
	}
	if opts.FirstMaxDelay == 0 {
		opts.FirstMaxDelay = def.FirstMaxDelay
	}
	if opts.PerTypeRune == 0 {
		opts.PerTypeRune = def.PerTypeRune
	}
	if opts.BaseDelay == 0 {
		opts.BaseDelay = def.BaseDelay
	}
	if opts.PerReadRune == 0 {
		opts.PerReadRune = def.PerReadRune
	}

	rng := randSource
	if rng == nil {
		rng = rand.Float64
	}

	nextRunes := utf8.RuneCountInString(next)

	if isFirst {
		base := time.Duration(nextRunes) * opts.PerTypeRune
		base = addJitter(base, opts.JitterFraction, rng)
		return clamp(base, opts.FirstMinDelay, opts.FirstMaxDelay)
	}

	prevRunes := utf8.RuneCountInString(prev)
	base := opts.BaseDelay +
		time.Duration(prevRunes)*opts.PerReadRune +
		time.Duration(nextRunes)*opts.PerTypeRune
	base = addJitter(base, opts.JitterFraction, rng)
	return clamp(base, opts.MinDelay, opts.MaxDelay)
}

// addJitter adds ±JitterFraction random variation to d.
// rand returns [0,1); we map to [-JitterFraction, +JitterFraction].
func addJitter(d time.Duration, fraction float64, rng func() float64) time.Duration {
	if fraction == 0 {
		return d
	}
	// rng() in [0,1) → shift to [-1, 1) range
	r := rng()*2 - 1 // [-1, 1)
	delta := time.Duration(float64(d) * fraction * r)
	return d + delta
}

// clamp restricts d to [lo, hi].
func clamp(d, lo, hi time.Duration) time.Duration {
	if d < lo {
		return lo
	}
	if d > hi {
		return hi
	}
	return d
}
