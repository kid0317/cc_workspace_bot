package session

import (
	"testing"
	"time"
	"unicode/utf8"
)

// ── SplitSegments ─────────────────────────────────────────────────────────────

func TestSplitSegments(t *testing.T) {
	def := DefaultSegmentOptions()

	tests := []struct {
		name string
		text string
		opts SegmentOptions
		want []string // nil means expect empty slice
	}{
		{
			name: "Delimiter_基础三段",
			text: "嘿你回来啦[[SEND]]今天怎么样啊[[SEND]]我刚才在想你说的那件事",
			opts: def,
			want: []string{"嘿你回来啦", "今天怎么样啊", "我刚才在想你说的那件事"},
		},
		{
			name: "Delimiter_首尾含SEND过滤空段",
			text: "[[SEND]]hello[[SEND]]world[[SEND]]",
			opts: def,
			want: []string{"hello", "world"},
		},
		{
			name: "Delimiter_仅含SEND返回空",
			text: "[[SEND]][[SEND]]",
			opts: def,
			want: nil,
		},
		{
			name: "Delimiter_段内换行保留",
			text: "第一段\n内部换行[[SEND]]第二段",
			opts: def,
			want: []string{"第一段\n内部换行", "第二段"},
		},
		{
			name: "Fallback_双换行段落",
			text: "第一段内容\n\n第二段内容\n\n第三段",
			opts: SegmentOptions{
				Delimiter:           "[[SEND]]",
				MaxRunes:            80,
				MinRunes:            2,
				MaxFallbackSegments: 5,
				BaseDelay:           400 * time.Millisecond,
				PerReadRune:         35 * time.Millisecond,
				PerTypeRune:         80 * time.Millisecond,
				MinDelay:            600 * time.Millisecond,
				MaxDelay:            2000 * time.Millisecond,
				FirstMinDelay:       300 * time.Millisecond,
				FirstMaxDelay:       1500 * time.Millisecond,
				JitterFraction:      0,
			},
			want: []string{"第一段内容", "第二段内容", "第三段"},
		},
		{
			name: "Fallback_中文长句按标点切",
			// 3 sentences with Chinese punctuation, total > MaxRunes=20
			// "你好啊。今天天气真不错！我们去公园走走吧？" = 23 runes > 20
			text: "你好啊。今天天气真不错！我们去公园走走吧？",
			opts: SegmentOptions{
				Delimiter:           "[[SEND]]",
				MaxRunes:            20,
				MinRunes:            2,
				MaxFallbackSegments: 5,
				BaseDelay:           400 * time.Millisecond,
				PerReadRune:         35 * time.Millisecond,
				PerTypeRune:         80 * time.Millisecond,
				MinDelay:            600 * time.Millisecond,
				MaxDelay:            2000 * time.Millisecond,
				FirstMinDelay:       300 * time.Millisecond,
				FirstMaxDelay:       1500 * time.Millisecond,
				JitterFraction:      0,
			},
			want: []string{"你好啊。", "今天天气真不错！", "我们去公园走走吧？"},
		},
		{
			name: "Fallback_英文长句按句点空格切",
			// "Hello world. How are you. I am fine." = 36 runes > MaxRunes=30
			text: "Hello world. How are you. I am fine.",
			opts: SegmentOptions{
				Delimiter:           "[[SEND]]",
				MaxRunes:            30,
				MinRunes:            2,
				MaxFallbackSegments: 5,
				BaseDelay:           400 * time.Millisecond,
				PerReadRune:         35 * time.Millisecond,
				PerTypeRune:         80 * time.Millisecond,
				MinDelay:            600 * time.Millisecond,
				MaxDelay:            2000 * time.Millisecond,
				FirstMinDelay:       300 * time.Millisecond,
				FirstMaxDelay:       1500 * time.Millisecond,
				JitterFraction:      0,
			},
			want: []string{"Hello world.", "How are you.", "I am fine."},
		},
		{
			name: "Fallback_超过MaxFallbackSegments退回单段",
			// text with many paragraphs that would exceed MaxFallbackSegments=2
			text: "段落一\n\n段落二\n\n段落三\n\n段落四",
			opts: SegmentOptions{
				Delimiter:           "[[SEND]]",
				MaxRunes:            80,
				MinRunes:            2,
				MaxFallbackSegments: 2,
				BaseDelay:           400 * time.Millisecond,
				PerReadRune:         35 * time.Millisecond,
				PerTypeRune:         80 * time.Millisecond,
				MinDelay:            600 * time.Millisecond,
				MaxDelay:            2000 * time.Millisecond,
				FirstMinDelay:       300 * time.Millisecond,
				FirstMaxDelay:       1500 * time.Millisecond,
				JitterFraction:      0,
			},
			want: []string{"段落一\n\n段落二\n\n段落三\n\n段落四"},
		},
		{
			name: "Merge_短段合并",
			// segments "a", "b", "longer segment" — "a" and "b" should merge
			text: "a[[SEND]]b[[SEND]]longer segment here",
			opts: SegmentOptions{
				Delimiter:           "[[SEND]]",
				MaxRunes:            80,
				MinRunes:            3, // "a" and "b" are shorter than 3 runes
				MaxFallbackSegments: 3,
				BaseDelay:           400 * time.Millisecond,
				PerReadRune:         35 * time.Millisecond,
				PerTypeRune:         80 * time.Millisecond,
				MinDelay:            600 * time.Millisecond,
				MaxDelay:            2000 * time.Millisecond,
				FirstMinDelay:       300 * time.Millisecond,
				FirstMaxDelay:       1500 * time.Millisecond,
				JitterFraction:      0,
			},
			want: []string{"a b", "longer segment here"},
		},
		{
			name: "MaxRunes_硬切",
			// Single segment exceeding MaxRunes should be hard-split
			text: "一二三四五六七八九十一二三四五",
			opts: SegmentOptions{
				Delimiter:           "[[SEND]]",
				MaxRunes:            5,
				MinRunes:            2,
				MaxFallbackSegments: 10,
				BaseDelay:           400 * time.Millisecond,
				PerReadRune:         35 * time.Millisecond,
				PerTypeRune:         80 * time.Millisecond,
				MinDelay:            600 * time.Millisecond,
				MaxDelay:            2000 * time.Millisecond,
				FirstMinDelay:       300 * time.Millisecond,
				FirstMaxDelay:       1500 * time.Millisecond,
				JitterFraction:      0,
			},
			// 15 chars / 5 per segment = 3 segments
			want: []string{"一二三四五", "六七八九十", "一二三四五"},
		},
		{
			name: "Empty_空输入",
			text: "",
			opts: def,
			want: nil,
		},
		{
			name: "Empty_全空白",
			text: "   \n  \t  ",
			opts: def,
			want: nil,
		},
		{
			name: "RuneCount_emoji混排计数正确",
			// "A😀B" = 3 runes, "C😎D" = 3 runes
			// With MaxRunes=3 and delimiter, should not be further split
			text: "A😀B[[SEND]]C😎D",
			opts: SegmentOptions{
				Delimiter:           "[[SEND]]",
				MaxRunes:            3,
				MinRunes:            1,
				MaxFallbackSegments: 3,
				BaseDelay:           400 * time.Millisecond,
				PerReadRune:         35 * time.Millisecond,
				PerTypeRune:         80 * time.Millisecond,
				MinDelay:            600 * time.Millisecond,
				MaxDelay:            2000 * time.Millisecond,
				FirstMinDelay:       300 * time.Millisecond,
				FirstMaxDelay:       1500 * time.Millisecond,
				JitterFraction:      0,
			},
			want: []string{"A😀B", "C😎D"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SplitSegments(tt.text, tt.opts)
			if len(got) != len(tt.want) {
				t.Fatalf("SplitSegments(%q) = %v (len %d), want %v (len %d)",
					tt.text, got, len(got), tt.want, len(tt.want))
			}
			for i, seg := range got {
				if seg != tt.want[i] {
					t.Errorf("segment[%d] = %q, want %q", i, seg, tt.want[i])
				}
				// Verify rune-count correctness for emoji test
				if tt.name == "RuneCount_emoji混排计数正确" {
					rc := utf8.RuneCountInString(seg)
					if rc > tt.opts.MaxRunes {
						t.Errorf("segment[%d] rune count %d exceeds MaxRunes %d", i, rc, tt.opts.MaxRunes)
					}
				}
			}
		})
	}
}

// ── Additional edge-case tests for coverage ───────────────────────────────────

func TestSplitSegments_Extra(t *testing.T) {
	tests := []struct {
		name string
		text string
		opts SegmentOptions
		want []string
	}{
		{
			name: "HardSplit_在标点处切",
			// "一二三，四五六七八" — comma at index 3 is a cut point for MaxRunes=5
			// runes: 一二三，四五六七八 (9 runes), MaxRunes=5
			// hardSplit looks for punct in [2..4]: runes[3]='，' → cutAt=4
			text: "一二三，四五六七八",
			opts: SegmentOptions{
				Delimiter:           "[[SEND]]",
				MaxRunes:            5,
				MinRunes:            1,
				MaxFallbackSegments: 10,
				BaseDelay:           400 * time.Millisecond,
				PerReadRune:         35 * time.Millisecond,
				PerTypeRune:         80 * time.Millisecond,
				MinDelay:            600 * time.Millisecond,
				MaxDelay:            2000 * time.Millisecond,
				FirstMinDelay:       300 * time.Millisecond,
				FirstMaxDelay:       1500 * time.Millisecond,
				JitterFraction:      0,
			},
			want: []string{"一二三，", "四五六七八"},
		},
		{
			name: "GreedyMerge_两段都太短无法合并超MaxRunes",
			// "ab" and "cd" each 2 runes < MinRunes=3, merged "ab cd" = 5 runes > MaxRunes=4
			// → flush "ab", then "cd" also pending, at end flush "cd"
			text: "ab[[SEND]]cd[[SEND]]efghijk",
			opts: SegmentOptions{
				Delimiter:           "[[SEND]]",
				MaxRunes:            4,
				MinRunes:            3,
				MaxFallbackSegments: 10,
				BaseDelay:           400 * time.Millisecond,
				PerReadRune:         35 * time.Millisecond,
				PerTypeRune:         80 * time.Millisecond,
				MinDelay:            600 * time.Millisecond,
				MaxDelay:            2000 * time.Millisecond,
				FirstMinDelay:       300 * time.Millisecond,
				FirstMaxDelay:       1500 * time.Millisecond,
				JitterFraction:      0,
			},
			// "ab" and "cd" can't merge (5 > 4), hardSplit "efghijk" at maxRunes=4
			want: []string{"ab", "cd", "efgh", "ijk"},
		},
		{
			name: "GreedyMerge_连续短段累积合并",
			// Three very short segments that together exceed minRunes only after 3 merges
			// "x"(1), "y"(1) → merge "x y"(3 >= 2), then "z"(1) short → new pending
			// At end: pending "z" flushed
			text: "x[[SEND]]y[[SEND]]z",
			opts: SegmentOptions{
				Delimiter:           "[[SEND]]",
				MaxRunes:            10,
				MinRunes:            2,
				MaxFallbackSegments: 10,
				BaseDelay:           400 * time.Millisecond,
				PerReadRune:         35 * time.Millisecond,
				PerTypeRune:         80 * time.Millisecond,
				MinDelay:            600 * time.Millisecond,
				MaxDelay:            2000 * time.Millisecond,
				FirstMinDelay:       300 * time.Millisecond,
				FirstMaxDelay:       1500 * time.Millisecond,
				JitterFraction:      0,
			},
			want: []string{"x y", "z"},
		},
		{
			name: "Fallback_单换行触发splitBySentence内换行分支",
			// Text with single newline > MaxRunes to trigger splitBySentence with \n handling
			text: "第一句话\n第二句话",
			opts: SegmentOptions{
				Delimiter:           "[[SEND]]",
				MaxRunes:            5,
				MinRunes:            1,
				MaxFallbackSegments: 10,
				BaseDelay:           400 * time.Millisecond,
				PerReadRune:         35 * time.Millisecond,
				PerTypeRune:         80 * time.Millisecond,
				MinDelay:            600 * time.Millisecond,
				MaxDelay:            2000 * time.Millisecond,
				FirstMinDelay:       300 * time.Millisecond,
				FirstMaxDelay:       1500 * time.Millisecond,
				JitterFraction:      0,
			},
			// "第一句话\n第二句话" = 9 runes > 5, no paragraph split, triggers sentence
			// splitBySentence: hits \n case → "第一句话" flushed, "第二句话" remainder
			want: []string{"第一句话", "第二句话"},
		},
		{
			name: "Fallback_句点在字符串末尾",
			// Period at end of string without trailing space (≤ MaxRunes so no hard split)
			text: "Hi.",
			opts: SegmentOptions{
				Delimiter:           "[[SEND]]",
				MaxRunes:            10,
				MinRunes:            1,
				MaxFallbackSegments: 10,
				BaseDelay:           400 * time.Millisecond,
				PerReadRune:         35 * time.Millisecond,
				PerTypeRune:         80 * time.Millisecond,
				MinDelay:            600 * time.Millisecond,
				MaxDelay:            2000 * time.Millisecond,
				FirstMinDelay:       300 * time.Millisecond,
				FirstMaxDelay:       1500 * time.Millisecond,
				JitterFraction:      0,
			},
			// 3 runes <= MaxRunes=10, fallback stays as single segment
			want: []string{"Hi."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SplitSegments(tt.text, tt.opts)
			if len(got) != len(tt.want) {
				t.Fatalf("SplitSegments(%q) = %v (len %d), want %v (len %d)",
					tt.text, got, len(got), tt.want, len(tt.want))
			}
			for i, seg := range got {
				if seg != tt.want[i] {
					t.Errorf("segment[%d] = %q, want %q", i, seg, tt.want[i])
				}
			}
		})
	}
}

// ── TypingDelay ───────────────────────────────────────────────────────────────

func TestTypingDelay(t *testing.T) {
	def := DefaultSegmentOptions()
	zeroJitter := func() float64 { return 0.5 } // midpoint → jitter = 0

	tests := []struct {
		name       string
		prev       string
		next       string
		isFirst    bool
		opts       SegmentOptions
		randSource func() float64
		checkFn    func(t *testing.T, d time.Duration)
	}{
		{
			name:       "FirstSegment_范围在FirstMin-FirstMax",
			prev:       "ignored",
			next:       "hello",
			isFirst:    true,
			opts:       def,
			randSource: zeroJitter,
			checkFn: func(t *testing.T, d time.Duration) {
				if d < def.FirstMinDelay || d > def.FirstMaxDelay {
					t.Errorf("first segment delay %v not in [%v, %v]", d, def.FirstMinDelay, def.FirstMaxDelay)
				}
			},
		},
		{
			name:       "NonFirst_范围在Min-Max",
			prev:       "hi",
			next:       "there",
			isFirst:    false,
			opts:       def,
			randSource: zeroJitter,
			checkFn: func(t *testing.T, d time.Duration) {
				if d < def.MinDelay || d > def.MaxDelay {
					t.Errorf("non-first delay %v not in [%v, %v]", d, def.MinDelay, def.MaxDelay)
				}
			},
		},
		{
			name:    "LongPrev_被MaxDelay截断",
			prev:    "这是一段很长很长的前一条消息，用来测试MaxDelay截断效果，字数超过上限阈值的情况下应该被截断到MaxDelay的值",
			next:    "这也是一段很长很长的下一条消息，同样超过上限阈值",
			isFirst: false,
			opts:    def,
			checkFn: func(t *testing.T, d time.Duration) {
				if d != def.MaxDelay {
					t.Errorf("long prev/next delay = %v, want %v (MaxDelay)", d, def.MaxDelay)
				}
			},
		},
		{
			name:    "JitterZero_确定性值",
			prev:    "prev",
			next:    "next",
			isFirst: false,
			opts: SegmentOptions{
				Delimiter:      "[[SEND]]",
				MaxRunes:       80,
				MinRunes:       2,
				BaseDelay:      400 * time.Millisecond,
				PerReadRune:    35 * time.Millisecond,
				PerTypeRune:    80 * time.Millisecond,
				MinDelay:       600 * time.Millisecond,
				MaxDelay:       2000 * time.Millisecond,
				FirstMinDelay:  300 * time.Millisecond,
				FirstMaxDelay:  1500 * time.Millisecond,
				JitterFraction: 0, // zero jitter
			},
			randSource: func() float64 { return 0.5 },
			checkFn: func(t *testing.T, d time.Duration) {
				// prev="prev" 4 runes, next="next" 4 runes
				// delay = 400ms + 4*35ms + 4*80ms = 400 + 140 + 320 = 860ms
				// jitter=0, so no variation
				// clamp [600ms, 2000ms] → 860ms
				want := 400*time.Millisecond + 4*35*time.Millisecond + 4*80*time.Millisecond
				if d != want {
					t.Errorf("deterministic delay = %v, want %v", d, want)
				}
			},
		},
		{
			name:    "ZeroValueOpts_不返回零",
			prev:    "prev",
			next:    "next",
			isFirst: false,
			opts:    SegmentOptions{}, // zero-value
			checkFn: func(t *testing.T, d time.Duration) {
				if d <= 0 {
					t.Errorf("zero-value opts should not return zero delay, got %v", d)
				}
				// Should use DefaultSegmentOptions MaxDelay as upper bound
				maxDelay := DefaultSegmentOptions().MaxDelay
				if d > maxDelay {
					t.Errorf("delay %v exceeds DefaultSegmentOptions().MaxDelay %v", d, maxDelay)
				}
			},
		},
		{
			name:    "RandSourceInjection_可复现",
			prev:    "hello",
			next:    "world",
			isFirst: false,
			opts: SegmentOptions{
				Delimiter:      "[[SEND]]",
				MaxRunes:       80,
				MinRunes:       2,
				BaseDelay:      400 * time.Millisecond,
				PerReadRune:    35 * time.Millisecond,
				PerTypeRune:    80 * time.Millisecond,
				MinDelay:       600 * time.Millisecond,
				MaxDelay:       2000 * time.Millisecond,
				FirstMinDelay:  300 * time.Millisecond,
				FirstMaxDelay:  1500 * time.Millisecond,
				JitterFraction: 0.2,
			},
			randSource: func() float64 { return 0.75 }, // fixed value
			checkFn: func(t *testing.T, d time.Duration) {
				// Call again with same randSource to verify reproducibility
				opts := SegmentOptions{
					Delimiter:      "[[SEND]]",
					MaxRunes:       80,
					MinRunes:       2,
					BaseDelay:      400 * time.Millisecond,
					PerReadRune:    35 * time.Millisecond,
					PerTypeRune:    80 * time.Millisecond,
					MinDelay:       600 * time.Millisecond,
					MaxDelay:       2000 * time.Millisecond,
					FirstMinDelay:  300 * time.Millisecond,
					FirstMaxDelay:  1500 * time.Millisecond,
					JitterFraction: 0.2,
				}
				d2 := TypingDelay("hello", "world", false, opts, func() float64 { return 0.75 })
				if d != d2 {
					t.Errorf("reproducible rand: first=%v second=%v, want same", d, d2)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := TypingDelay(tt.prev, tt.next, tt.isFirst, tt.opts, tt.randSource)
			tt.checkFn(t, d)
		})
	}
}
