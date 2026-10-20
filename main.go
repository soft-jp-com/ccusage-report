// ccusage-report は Claude Code のセッションログ（~/.claude/projects 配下の JSONL）を集計し、
// 期間合計・日次の使用量とプラン判定を Markdown で出力する。ccusage と同じ方法で集計するが、Go だけで動く。
//
// 使い方:
//
//	ccusage-report.exe                                  # 全期間を集計して実行ファイルと同じフォルダに出力
//	ccusage-report.exe -since 20260920 -until 20261020  # 期間指定
//	ccusage-report.exe -out -                           # 標準出力に出す
package main

import (
	"flag"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"time"
	_ "time/tzdata" // -timezone を Windows でも使えるようにする
)

// usage はトークン数とコストの共通部分。
type usage struct {
	InputTokens         int64
	OutputTokens        int64
	CacheCreationTokens int64
	CacheReadTokens     int64
	TotalTokens         int64
	TotalCost           float64
}

func (u *usage) addMsg(m message) {
	u.InputTokens += m.Input
	u.OutputTokens += m.Output
	u.CacheCreationTokens += m.CacheCreation
	u.CacheReadTokens += m.CacheRead
	u.TotalTokens += m.Input + m.Output + m.CacheCreation + m.CacheRead
	u.TotalCost += m.Cost
}

func (u *usage) add(o usage) {
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	u.CacheCreationTokens += o.CacheCreationTokens
	u.CacheReadTokens += o.CacheReadTokens
	u.TotalTokens += o.TotalTokens
	u.TotalCost += o.TotalCost
}

// entry は1期間（日または月）の集計。
type entry struct {
	Key string
	usage
	Models map[string]*usage
}

// loc は日付の区切りと表示に使うタイムゾーン。
var loc = time.Local

func main() {
	ja = osJapanese()
	since := flag.String("since", "", tr("集計開始日 (YYYYMMDD または YYYY-MM-DD)", "start date (YYYYMMDD or YYYY-MM-DD)"))
	until := flag.String("until", "", tr("集計終了日 (YYYYMMDD または YYYY-MM-DD、当日を含む)", "end date, inclusive (YYYYMMDD or YYYY-MM-DD)"))
	out := flag.String("out", "", tr("出力先ファイル (省略時: 実行ファイルと同じフォルダの claude_usage_<ユーザーID>_YYYYMMDD.md、- で標準出力)",
		"output file (default: claude_usage_<user>_YYYYMMDD.md next to the executable; - for stdout)"))
	offline := flag.Bool("offline", false, tr("料金表をネットから取得せず、キャッシュか組み込みの料金表を使う", "do not download the price table; use the cached or built-in one"))
	tz := flag.String("timezone", "", tr("日付の区切りに使うタイムゾーン (例: Asia/Tokyo、省略時は PC の設定)", "time zone for grouping by date (e.g. America/New_York; default: system setting)"))
	max5Limit := flag.Float64("max5-limit", 155, tr("Max5 プランの5時間枠上限の目安 (API 換算 USD)。Pro はこの 1/5、Max20 は 4 倍として判定する",
		"estimated Max5 5-hour limit (API-equivalent USD); Pro is 1/5 of it and Max20 is 4x"))
	near := flag.Float64("near", 0.7, tr("プランの上限のこの割合に達したら「限界近く」とみなす (0.7 = 70%)", "fraction of a plan's limit treated as \"near the limit\" (0.7 = 70%)"))
	freq := flag.Float64("freq", 1, tr("「限界近く」が週あたりこの回数以上なら上位プランを推奨する", "recommend a higher plan when \"near the limit\" happens this many times per week or more"))
	noPlan := flag.Bool("no-plan", false, tr("プラン判定の節を出力しない", "omit the plan recommendation section"))
	lang := flag.String("lang", "", tr("レポートの言語 (ja / en、省略時は OS の表示言語)", "report language (ja / en; default: OS display language)"))
	flag.Parse()

	switch strings.ToLower(*lang) {
	case "ja":
		ja = true
	case "en":
		ja = false
	case "":
	default:
		fatal(fmt.Errorf(tr("-lang は ja か en を指定してください: %s", "-lang must be ja or en: %s"), *lang))
	}
	if *tz != "" {
		l, err := time.LoadLocation(*tz)
		if err != nil {
			fatal(fmt.Errorf(tr("タイムゾーンが不正です: %w", "invalid time zone: %w"), err))
		}
		loc = l
	}
	from, to, err := dateRange(*since, *until)
	if err != nil {
		fatal(err)
	}

	pr := loadPricing(*offline)
	msgs, hits, err := loadLogs(pr, from, to)
	if err != nil {
		fatal(err)
	}

	daily := aggregate(msgs, "2006-01-02")

	now := time.Now().In(loc)
	var planRes *planResult
	if !*noPlan {
		cur, label := readPlanTier()
		r := analyzePlan(buildBlocks(msgs), hits, cur, label, *max5Limit, *near, *freq)
		planRes = &r
	}
	md := render(now, *since, *until, pr.source, daily, aggregate(msgs, "期間合計"), planRes)
	if planRes != nil {
		md += renderPlan(*planRes)
	}

	path := *out
	if path == "" {
		path = filepath.Join(exeDir(), "claude_usage_"+loginUser()+"_"+now.Format("20060102")+".md")
	}
	if path == "-" {
		fmt.Print(md)
		return
	}
	if err := os.WriteFile(path, []byte(md), 0o644); err != nil {
		fatal(err)
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	fmt.Println(tr("出力しました:", "Wrote:"), path)
}

// exeDir は実行ファイルのあるフォルダ。go run で実行した場合（一時フォルダ go-build… に置かれる）や
// 取得できない場合はカレントフォルダ。
func exeDir() string {
	if p, err := os.Executable(); err == nil {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
		if dir := filepath.Dir(p); !strings.Contains(dir, "go-build") {
			return dir
		}
	}
	return "."
}

// dateRange は -since / -until を範囲 [from, to) に変換する。
func dateRange(since, until string) (time.Time, time.Time, error) {
	from := time.Time{}
	to := time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, v := range []struct {
		s   string
		dst *time.Time
		add int
	}{{since, &from, 0}, {until, &to, 1}} {
		if v.s == "" {
			continue
		}
		t, err := time.ParseInLocation("20060102", strings.ReplaceAll(v.s, "-", ""), loc)
		if err != nil {
			return from, to, fmt.Errorf(tr("日付の形式が不正です（YYYYMMDD）: %s", "invalid date (expected YYYYMMDD): %s"), v.s)
		}
		*v.dst = t.AddDate(0, 0, v.add)
	}
	return from, to, nil
}

// aggregate は応答を layout（日付または月）ごとにまとめる。
func aggregate(msgs []message, layout string) []entry {
	idx := map[string]int{}
	var es []entry
	for _, m := range msgs {
		k := m.Time.In(loc).Format(layout)
		i, ok := idx[k]
		if !ok {
			i = len(es)
			idx[k] = i
			es = append(es, entry{Key: k, Models: map[string]*usage{}})
		}
		es[i].usage.addMsg(m)
		mu := es[i].Models[m.Model]
		if mu == nil {
			mu = &usage{}
			es[i].Models[m.Model] = mu
		}
		mu.addMsg(m)
	}
	sort.Slice(es, func(i, j int) bool { return es[i].Key < es[j].Key })
	return es
}

// loginUser は現在ログインしているユーザーの ID を返す（Windows の "DOMAIN\user" はドメイン部分を除く）。
func loginUser() string {
	name := ""
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	if name == "" {
		name = os.Getenv("USERNAME")
	}
	if name == "" {
		name = os.Getenv("USER")
	}
	if i := strings.LastIndexAny(name, `\/`); i >= 0 {
		name = name[i+1:]
	}
	// ファイル名に使えない文字を置き換える
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?* `, r) {
			return '_'
		}
		return r
	}, name)
	if name == "" {
		name = "unknown"
	}
	return name
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, tr("エラー:", "Error:"), err)
	os.Exit(1)
}

// render は使用量の節を出力する。total は期間全体を1件にまとめた集計（使用記録が無ければ空）。
// plan が nil でなければ期間合計の表に現在のプランと推奨プランを載せる。
func render(now time.Time, since, until, priceSource string, daily, total []entry, plan *planResult) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }

	b.WriteString(tr("# Claude Code 使用量レポート\n\n", "# Claude Code Usage Report\n\n"))
	w(tr("- 作成日時: %s\n", "- Generated: %s\n"), now.Format("2006-01-02 15:04"))
	w(tr("- ユーザー: %s\n", "- User: %s\n"), loginUser())
	period := tr("全期間", "all time")
	if since != "" || until != "" {
		period = fmt.Sprintf(tr("%s 〜 %s", "%s - %s"), orDash(since), orDash(until))
	}
	w(tr("- 集計期間: %s\n", "- Period: %s\n"), period)
	b.WriteString(tr("- データ元: ~/.claude/projects/ 配下のセッションログ（サブエージェント分を含む）\n",
		"- Source: session logs under ~/.claude/projects/ (including subagents)\n"))
	w(tr("- 料金表: %s\n", "- Price table: %s\n"), priceSource)
	b.WriteString(tr("- コストは API 料金で換算した参考値 (USD)。サブスクリプション利用時の実際の請求額ではない\n\n",
		"- Costs are API-equivalent estimates (USD), not what a subscription actually bills\n\n"))

	if len(daily) == 0 {
		b.WriteString(tr("対象期間の使用記録はありません。\n\n", "No usage recorded in this period.\n\n"))
		return b.String()
	}

	t := total[0]
	b.WriteString(tr("## 期間合計\n\n", "## Period Total\n\n"))
	b.WriteString(tr("| 項目 | 値 |\n|---|---:|\n", "| Item | Value |\n|---|---:|\n"))
	w(tr("| 利用日数 | %d 日 (%s 〜 %s) |\n", "| Active days | %d (%s - %s) |\n"), len(daily), daily[0].Key, daily[len(daily)-1].Key)
	w(tr("| 合計トークン | %s |\n", "| Total tokens | %s |\n"), num(t.TotalTokens))
	w(tr("| 入力 / 出力 | %s / %s |\n", "| Input / output | %s / %s |\n"), num(t.InputTokens), num(t.OutputTokens))
	w(tr("| キャッシュ作成 / 読込 | %s / %s |\n", "| Cache write / read | %s / %s |\n"), num(t.CacheCreationTokens), num(t.CacheReadTokens))
	w(tr("| API 換算コスト | %s |\n", "| API-equivalent cost | %s |\n"), usd(t.TotalCost))
	w(tr("| 1 日あたり平均コスト | %s |\n", "| Average cost per day | %s |\n"), usd(t.TotalCost/float64(len(daily))))
	if plan != nil {
		w(tr("| 現在のプラン | %s |\n", "| Current plan | %s |\n"), plan.CurrentLabel)
		if v := plan.verdict(); v != "" {
			w(tr("| 推奨プラン | **%s** |\n", "| Recommended plan | **%s** |\n"), v)
		}
	}
	w("\n")

	b.WriteString(tr("### 期間合計・モデル別\n\n", "### Period Total by Model\n\n"))
	modelTable(&b, "", total)

	b.WriteString(tr("\n## 日次\n\n", "\n## Daily\n\n"))
	usageTable(&b, tr("日付", "Date"), daily, weekday)
	b.WriteString(tr("\n### 日次・モデル別\n\n", "\n### Daily by Model\n\n"))
	modelTable(&b, tr("日付", "Date"), daily)
	w("\n")
	return b.String()
}

func (e entry) modelNames() []string {
	var ns []string
	for n := range e.Models {
		ns = append(ns, n)
	}
	sort.Slice(ns, func(i, j int) bool { return e.Models[ns[i]].TotalCost > e.Models[ns[j]].TotalCost })
	return ns
}

func usageTable(b *strings.Builder, label string, es []entry, suffix func(string) string) {
	fmt.Fprintf(b, "| %s | %s | %s |\n", label, tokenHeaders(), tr("モデル", "Models"))
	b.WriteString("|---|---:|---:|---:|---:|---:|---:|---|\n")
	var total usage
	for _, e := range es {
		k := e.Key
		if suffix != nil {
			k += suffix(e.Key)
		}
		names := e.modelNames()
		sort.Strings(names)
		fmt.Fprintf(b, "| %s | %s | %s | %s | %s | %s | %s | %s |\n", k,
			num(e.InputTokens), num(e.OutputTokens), num(e.CacheCreationTokens), num(e.CacheReadTokens),
			num(e.TotalTokens), usd(e.TotalCost), strings.Join(names, ", "))
		total.add(e.usage)
	}
	fmt.Fprintf(b, "| **"+tr("合計", "Total")+"** | **%s** | **%s** | **%s** | **%s** | **%s** | **%s** | |\n",
		num(total.InputTokens), num(total.OutputTokens), num(total.CacheCreationTokens), num(total.CacheReadTokens),
		num(total.TotalTokens), usd(total.TotalCost))
}

// modelTable はモデル別の表を出力する。label が空なら期間の列を省く。
func modelTable(b *strings.Builder, label string, es []entry) {
	if label != "" {
		fmt.Fprintf(b, "| %s ", label)
	}
	fmt.Fprintf(b, "| %s | %s |\n", tr("モデル", "Model"), tokenHeaders())
	if label != "" {
		b.WriteString("|---")
	}
	b.WriteString("|---|---:|---:|---:|---:|---:|---:|\n")
	for _, e := range es {
		for _, n := range e.modelNames() {
			u := e.Models[n]
			if label != "" {
				fmt.Fprintf(b, "| %s ", e.Key)
			}
			fmt.Fprintf(b, "| %s | %s | %s | %s | %s | %s | %s |\n", n,
				num(u.InputTokens), num(u.OutputTokens), num(u.CacheCreationTokens), num(u.CacheReadTokens),
				num(u.TotalTokens), usd(u.TotalCost))
		}
	}
}

func weekday(date string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return ""
	}
	if !ja {
		return " (" + t.Weekday().String()[:3] + ")"
	}
	return " (" + []string{"日", "月", "火", "水", "木", "金", "土"}[t.Weekday()] + ")"
}

// tokenHeaders はトークン数とコストの列見出し。
func tokenHeaders() string {
	return tr("入力 | 出力 | キャッシュ作成 | キャッシュ読込 | 合計トークン | コスト (USD)",
		"Input | Output | Cache write | Cache read | Total tokens | Cost (USD)")
}

func uniq(ss []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func num(n int64) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func usd(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	i := strings.IndexByte(s, '.')
	var n int64
	fmt.Sscan(s[:i], &n)
	return "$" + num(n) + s[i:]
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
