package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// plan はサブスクリプションプランと5時間枠上限の目安 (API 換算 USD)。
type plan struct {
	Name  string
	Limit float64
}

// plans は Max5 の上限を基準に Pro = 1/5、Max20 = 4 倍 (Pro の 20 倍) として組み立てる。
func plans(max5Limit float64) []plan {
	return []plan{
		{"Pro", max5Limit / 5},
		{"Max5", max5Limit},
		{"Max20", max5Limit * 4},
	}
}

// readPlanTier は認証情報ファイルからプラン種別だけを読み取る（トークン等の他の値は保持しない）。
// 戻り値は plans() のインデックス。判別できなければ -1。
func readPlanTier() (int, string) {
	unknown := tr("不明", "unknown")
	raw, err := os.ReadFile(filepath.Join(claudeDir(), ".credentials.json"))
	if err != nil {
		return -1, unknown + tr("（認証情報ファイルを読めません）", " (cannot read the credentials file)")
	}
	var v struct {
		ClaudeAiOauth struct {
			SubscriptionType string `json:"subscriptionType"`
			RateLimitTier    string `json:"rateLimitTier"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return -1, unknown
	}
	sub, tier := v.ClaudeAiOauth.SubscriptionType, v.ClaudeAiOauth.RateLimitTier
	label := fmt.Sprintf("%s (%s)", orDash(sub), orDash(tier))
	switch {
	case strings.Contains(tier, "max_20x"):
		return 2, "Max20 — " + label
	case strings.Contains(tier, "max_5x"):
		return 1, "Max5 — " + label
	case sub == "pro":
		return 0, "Pro — " + label
	}
	return -1, unknown + " — " + label
}

type planResult struct {
	Plans        []plan
	Current      int
	CurrentLabel string
	Blocks       []block
	Median, P90  float64
	Max          float64
	Max7d        float64
	Weeks        float64
	Hits         []limitHit
	Stats        []planStat // Plans と同じ並び
	Near, Freq   float64
	Recommend    int
	Reasons      []string
}

// planStat は1つのプランに対する5時間ブロックの到達状況。
type planStat struct {
	Near        int // 上限の near 割以上に達したブロック数
	Over        int // 上限を超えたブロック数
	NearPerWeek float64
	Frequent    bool // NearPerWeek が freq 以上
}

// analyzePlan は5時間ブロックの使用量からプランを判定する。
// near は「限界近く」とみなす上限比 (0.7 なら 70%)、freq は「頻繁」とみなす週あたり回数。
func analyzePlan(bs []block, hits []limitHit, current int, currentLabel string, max5Limit, near, freq float64) planResult {
	r := planResult{Plans: plans(max5Limit), Current: current, CurrentLabel: currentLabel, Blocks: bs, Hits: hits, Near: near, Freq: freq, Recommend: -1}
	if len(bs) == 0 {
		return r
	}
	costs := make([]float64, len(bs))
	for i, b := range bs {
		costs[i] = b.Cost
	}
	sort.Float64s(costs)
	r.Median = percentile(costs, 0.5)
	r.P90 = percentile(costs, 0.9)
	r.Max = costs[len(costs)-1]

	// 任意の連続7日間に始まったブロックの合計の最大値
	for i := range bs {
		sum := 0.0
		for j := i; j < len(bs) && bs[j].start.Before(bs[i].start.AddDate(0, 0, 7)); j++ {
			sum += bs[j].Cost
		}
		r.Max7d = math.Max(r.Max7d, sum)
	}
	r.Weeks = math.Max(1, bs[len(bs)-1].start.Sub(bs[0].start).Hours()/24/7)

	// 多くの人はリミットに達する前に別の AI に切り替えるため、上限の near 割に達した時点で
	// 「限界近くまで使った」とみなす。各プランについてその回数（週あたり）と上限超えの回数を数える。
	for _, p := range r.Plans {
		var s planStat
		for _, bl := range bs {
			if bl.Cost >= p.Limit*near {
				s.Near++
			}
			if bl.Cost > p.Limit {
				s.Over++
			}
		}
		s.NearPerWeek = float64(s.Near) / r.Weeks
		s.Frequent = s.NearPerWeek >= freq
		r.Stats = append(r.Stats, s)
	}
	var weekHits int
	for _, h := range hits {
		if h.Type != "five_hour" {
			weekHits++
		}
	}
	nearPct := fmt.Sprintf("%.0f%%", near*100)
	// fits: 頻繁には near 割に達せず、上限を一度も超えていない（常にそのプラン内に収まる）
	fits := func(i int) bool { return !r.Stats[i].Frequent && r.Stats[i].Over == 0 }
	lowest := func(from int, ok func(int) bool) int {
		for i := from; i < len(r.Plans); i++ {
			if ok(i) {
				return i
			}
		}
		return len(r.Plans) - 1
	}
	reason := func(jaFmt, enFmt string, a ...any) {
		r.Reasons = append(r.Reasons, fmt.Sprintf(tr(jaFmt, enFmt), a...))
	}

	switch {
	case current < 0:
		r.Recommend = lowest(0, fits)
		reason("現在のプランが不明のため、%s 到達が週 %.0f 回未満かつ上限超えなしの最小プランを推奨",
			"Current plan unknown: recommending the smallest plan that reaches %s less than %.0f times a week and never exceeds its limit", nearPct, freq)
	case r.Stats[current].Frequent || weekHits > 0:
		// 上げる: 現在のプランで限界近くまで使うことが多い
		r.Recommend = lowest(current+1, func(i int) bool { return !r.Stats[i].Frequent })
		if current == len(r.Plans)-1 {
			r.Recommend = current
		}
		if s := r.Stats[current]; s.Frequent {
			reason("現在のプラン (%s) の上限の %s 以上に達したブロックが %d 回（週 %.1f 回）で、頻繁に限界近くまで使っている",
				"%[3]d blocks (%.1[4]f per week) reached %[2]s or more of the current plan (%[1]s) limit: frequently using it close to the limit",
				r.Plans[current].Name, nearPct, s.Near, s.NearPerWeek)
		}
		if weekHits > 0 {
			reason("週枠のリミットに %d 回達している", "Hit the weekly limit %d time(s)", weekHits)
		}
		if current == len(r.Plans)-1 {
			reason("すでに最上位プランのため、利用の平準化や他の AI との併用を検討", "Already on the top plan: consider spreading usage out or combining with other AI tools")
		}
	default:
		// 下げる: 下位プランの範囲に常に収まっている
		r.Recommend = current
		for i := range current {
			if fits(i) {
				r.Recommend = i
				break
			}
		}
		if r.Recommend < current {
			p := r.Plans[r.Recommend]
			reason("すべてのブロックが %s の上限（%s）内に収まり、%s 以上に達したのは週 %.1f 回",
				"Every block fits within the %s limit (%s); reached %s or more only %.1f times per week",
				p.Name, usd(p.Limit), nearPct, r.Stats[r.Recommend].NearPerWeek)
		} else {
			reason("現在のプラン (%s) の %s 以上に達したのは週 %.1f 回（週 %.0f 回未満）",
				"Reached %[2]s or more of the current plan (%[1]s) %.1[3]f times per week (less than %.0[4]f)",
				r.Plans[current].Name, nearPct, r.Stats[current].NearPerWeek, freq)
			if current > 0 {
				p := r.Plans[current-1]
				s := r.Stats[current-1]
				reason("1段下の %s では上限超えが %d 回、%s 以上が週 %.1f 回になり収まらない",
					"One tier down (%s) would exceed the limit %d times and reach %s or more %.1f times per week",
					p.Name, s.Over, nearPct, s.NearPerWeek)
			}
		}
	}
	return r
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[max(int(math.Ceil(p*float64(len(sorted))))-1, 0)]
}

func pct(v, limit float64) string {
	if limit <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", v/limit*100)
}

func hitTypeName(t string) string {
	switch t {
	case "five_hour":
		return tr("5時間枠", "5-hour")
	case "seven_day":
		return tr("週枠", "Weekly")
	case "seven_day_opus":
		return tr("週枠 (Opus)", "Weekly (Opus)")
	case "seven_day_sonnet":
		return tr("週枠 (Sonnet)", "Weekly (Sonnet)")
	}
	return t
}

// verdict は推奨プランと現在のプランとの比較。判定できなければ空。
func (r planResult) verdict() string {
	if r.Recommend < 0 {
		return ""
	}
	v := r.Plans[r.Recommend].Name
	if r.Current >= 0 {
		switch {
		case r.Recommend == r.Current:
			v += tr("（現在のプランのままで適正）", " (current plan is appropriate)")
		case r.Recommend > r.Current:
			v += tr("（上位プランへの変更を検討）", " (consider upgrading)")
		default:
			v += tr("（下位プランでも足りる可能性）", " (a lower plan may be enough)")
		}
	}
	return v
}

func renderPlan(r planResult) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }

	b.WriteString(tr("## プラン判定（目安）\n\n", "## Plan Recommendation (Estimate)\n\n"))
	w(tr("- 現在のプラン: %s\n", "- Current plan: %s\n"), r.CurrentLabel)
	b.WriteString(tr("- 5時間枠上限の目安（API 換算）: ", "- Estimated 5-hour limit (API-equivalent): "))
	for i, p := range r.Plans {
		if i > 0 {
			w(" / ")
		}
		w("%s %s", p.Name, usd(p.Limit))
	}
	w("\n\n")

	if len(r.Blocks) == 0 {
		b.WriteString(tr("対象期間の5時間ブロックがありません。\n\n", "No 5-hour blocks in this period.\n\n"))
		return b.String()
	}

	if v := r.verdict(); v != "" {
		w(tr("**推奨: %s**\n\n", "**Recommendation: %s**\n\n"), v)
		for _, s := range r.Reasons {
			w("- %s\n", s)
		}
		w("\n")
	}

	b.WriteString(tr("### 5時間ブロックの使用量\n\n", "### 5-Hour Block Usage\n\n"))
	b.WriteString(tr("| 項目 | API 換算 |", "| Item | API-equivalent |"))
	for _, p := range r.Plans {
		w(tr(" %s 比 |", " vs %s |"), p.Name)
	}
	w("\n|---|---:|")
	for range r.Plans {
		w("---:|")
	}
	w("\n")
	row := func(label string, v float64) {
		w("| %s | %s |", label, usd(v))
		for _, p := range r.Plans {
			w(" %s |", pct(v, p.Limit))
		}
		w("\n")
	}
	row(tr("中央値", "Median"), r.Median)
	row(tr("上位10%", "Top 10%"), r.P90)
	row(tr("最大", "Max"), r.Max)
	w(tr("| 上限の %.0f%% 以上のブロック（週あたり） | |", "| Blocks at %.0f%%+ of limit (per week) | |"), r.Near*100)
	for _, s := range r.Stats {
		mark := ""
		if s.Frequent {
			mark = " ⚠"
		}
		w(tr(" %d 回（%.1f）%s |", " %d (%.1f)%s |"), s.Near, s.NearPerWeek, mark)
	}
	b.WriteString(tr("\n| 上限超えのブロック | |", "\n| Blocks over limit | |"))
	for _, s := range r.Stats {
		w(" %d / %d |", s.Over, len(r.Blocks))
	}
	w("\n\n")
	w(tr("- 対象: %d ブロック（%s 〜 %s、約 %.1f 週）\n", "- Blocks: %d (%s - %s, about %.1f weeks)\n"), len(r.Blocks),
		r.Blocks[0].start.In(loc).Format("2006-01-02"), r.Blocks[len(r.Blocks)-1].start.In(loc).Format("2006-01-02"), r.Weeks)
	w(tr("- 連続7日間の最大: %s\n\n", "- Max over any 7 consecutive days: %s\n\n"), usd(r.Max7d))

	top := append([]block(nil), r.Blocks...)
	sort.Slice(top, func(i, j int) bool { return top[i].Cost > top[j].Cost })
	if len(top) > 5 {
		top = top[:5]
	}
	w(tr("#### 使用量の多いブロック（上位 %d）\n\n", "#### Heaviest Blocks (Top %d)\n\n"), len(top))
	b.WriteString(tr("| 開始 | API 換算 |", "| Start | API-equivalent |"))
	if r.Current >= 0 {
		w(tr(" 現在のプラン (%s) 比 |", " vs current plan (%s) |"), r.Plans[r.Current].Name)
	}
	b.WriteString(tr(" モデル |\n|---|---:|", " Models |\n|---|---:|"))
	if r.Current >= 0 {
		w("---:|")
	}
	w("---|\n")
	for _, bl := range top {
		w("| %s | %s |", bl.start.In(loc).Format("2006-01-02 15:04"), usd(bl.Cost))
		if r.Current >= 0 {
			w(" %s |", pct(bl.Cost, r.Plans[r.Current].Limit))
		}
		w(" %s |\n", strings.Join(uniq(bl.Models), ", "))
	}
	w("\n")

	b.WriteString(tr("### リミット到達履歴\n\n", "### Limit Hits\n\n"))
	if len(r.Hits) == 0 {
		b.WriteString(tr("対象期間にリミット到達の記録はありません。\n\n", "No limit hits recorded in this period.\n\n"))
	} else {
		b.WriteString(tr("| 日時 | 種類 | リセット | 枠内の API 換算 |\n|---|---|---|---:|\n", "| Time | Type | Resets | API-equivalent in window |\n|---|---|---|---:|\n"))
		var sum float64
		var n int
		for _, h := range r.Hits {
			wc := "-"
			if h.Type == "five_hour" {
				wc = usd(h.WindowCost)
				sum += h.WindowCost
				n++
			}
			w("| %s | %s | %s | %s |\n", h.Time.In(loc).Format("2006-01-02 15:04"), hitTypeName(h.Type), h.ResetsAt.In(loc).Format("2006-01-02 15:04"), wc)
		}
		w("\n")
		if n > 0 && r.Current >= 0 {
			avg := sum / float64(n)
			w(tr("- 枠内の API 換算の平均: %s（%s の目安 %s の %s）\n\n", "- Average API-equivalent in window: %s (%s estimate %s: %s)\n\n"),
				usd(avg), r.Plans[r.Current].Name, usd(r.Plans[r.Current].Limit), pct(avg, r.Plans[r.Current].Limit))
		}
	}
	return b.String()
}
