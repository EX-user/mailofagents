package worker

import "testing"

// ctx 特殊专项（boss 令 2026-09-08） fixtures: 全部来自真 CLI×真 key 实弹
// 采集（REALRUN_RESEARCH.md），非人造样张——语义对表就是本专项的产出。
func TestUsageContextPerCLI(t *testing.T) {
	cases := []struct {
		name string
		cli  string
		u    map[string]any
		want int64
	}{
		{
			// codex responses wire 实测: input_tokens 已含 cached —
			// 相加即双计（6M 异常的病根）。修正=单值。
			name: "codex responses includes cached",
			cli:  "codex",
			u: map[string]any{
				"input_tokens":             float64(8890),
				"cached_input_tokens":      float64(8064),
				"cache_write_input_tokens": float64(0),
				"output_tokens":            float64(14),
				"reasoning_output_tokens":  float64(12),
			},
			want: 8890,
		},
		{
			// codex chat wire（旧版）: prompt_tokens 同样含 cached。
			name: "codex chat wire prompt_tokens",
			cli:  "codex",
			u: map[string]any{
				"prompt_tokens":        float64(500),
				"cached_prompt_tokens": float64(400),
			},
			want: 500,
		},
		{
			// opencode 实测: input 与 cache 不重叠（5507+1920+2=7429=total）。
			name: "opencode disjoint input and cache",
			cli:  "opencode",
			u: map[string]any{
				"total":     float64(7429),
				"input":     float64(5507),
				"output":    float64(2),
				"reasoning": float64(0),
				"cache":     map[string]any{"write": float64(0), "read": float64(1920)},
			},
			want: 7427, // input + cacheRead（output 不计；total 含故不取）
		},
		{
			// claude 实测: anthropic 口径 input 与 cache 并列不重叠。
			name: "claude anthropic disjoint",
			cli:  "claude",
			u: map[string]any{
				"input_tokens":                float64(19868),
				"cache_creation_input_tokens": float64(0),
				"cache_read_input_tokens":     float64(0),
				"output_tokens":               float64(2),
			},
			want: 19868,
		},
		{
			// anthropic 家族有缓存命中的形态: input + read + creation 相加。
			name: "anthropic with cache hit",
			cli:  "pi",
			u: map[string]any{
				"input":      float64(1200),
				"cacheRead":  float64(3400),
				"cacheWrite": float64(300),
				"output":     float64(50),
			},
			want: 4900,
		},
	}
	for _, c := range cases {
		if got := usageContext(c.u, c.cli); got != c.want {
			t.Errorf("%s: usageContext = %d, want %d", c.name, got, c.want)
		}
	}
}
