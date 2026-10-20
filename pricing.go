package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// pricesURL は ccusage と同じ LiteLLM の料金表。
const pricesURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

// prices_fallback.json は LiteLLM の料金表から Claude のモデルだけを抜き出したもの。
// 料金表を取得できず、キャッシュも無いときに使う。
//
//go:embed prices_fallback.json
var fallbackPrices []byte

// modelPrice は1モデルの単価 (USD / token)。
type modelPrice map[string]float64

type pricing struct {
	models map[string]modelPrice
	source string
	warned map[string]bool
}

// loadPricing は料金表を取得する。オンライン → キャッシュ → 組み込みの順に試す。
func loadPricing(offline bool) *pricing {
	cache := ""
	if dir, err := os.UserCacheDir(); err == nil {
		cache = filepath.Join(dir, "ccusage-report", "prices.json")
	}
	if !offline {
		if raw, err := fetchPrices(); err == nil {
			if p, err := parsePrices(raw); err == nil {
				if cache != "" && os.MkdirAll(filepath.Dir(cache), 0o755) == nil {
					os.WriteFile(cache, raw, 0o644)
				}
				return &pricing{models: p, source: tr("LiteLLM（オンライン取得）", "LiteLLM (downloaded)"), warned: map[string]bool{}}
			}
		} else {
			fmt.Fprintln(os.Stderr, tr("警告: 料金表を取得できませんでした:", "Warning: could not download the price table:"), err)
		}
	}
	if cache != "" {
		if raw, err := os.ReadFile(cache); err == nil {
			if p, err := parsePrices(raw); err == nil {
				st, _ := os.Stat(cache)
				return &pricing{models: p, source: fmt.Sprintf(tr("LiteLLM（キャッシュ %s）", "LiteLLM (cached %s)"), st.ModTime().Format("2006-01-02")), warned: map[string]bool{}}
			}
		}
	}
	p, _ := parsePrices(fallbackPrices)
	return &pricing{models: p, source: tr("組み込みの料金表（2026-10-20 時点の LiteLLM）", "built-in price table (LiteLLM as of 2026-10-20)"), warned: map[string]bool{}}
}

func fetchPrices() ([]byte, error) {
	c := http.Client{Timeout: 20 * time.Second}
	resp, err := c.Get(pricesURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// parsePrices は料金表のうち数値の *cost* 項目だけを取り出す。
func parsePrices(raw []byte) (map[string]modelPrice, error) {
	var all map[string]map[string]any
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, err
	}
	out := map[string]modelPrice{}
	for name, fields := range all {
		mp := modelPrice{}
		for k, v := range fields {
			if f, ok := v.(float64); ok && strings.Contains(k, "cost") {
				mp[k] = f
			}
		}
		if len(mp) > 0 {
			out[name] = mp
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty price table")
	}
	return out, nil
}

func (p *pricing) lookup(model string) modelPrice {
	for _, k := range []string{model, "anthropic/" + model, strings.TrimPrefix(model, "anthropic.")} {
		if mp, ok := p.models[k]; ok {
			return mp
		}
	}
	// 日付付き（例: claude-xxx-20250514）などは前方一致で最も長い名前を使う
	best := ""
	for k := range p.models {
		if strings.HasPrefix(model, k) && len(k) > len(best) {
			best = k
		}
	}
	if best != "" {
		return p.models[best]
	}
	if !p.warned[model] {
		p.warned[model] = true
		fmt.Fprintln(os.Stderr, tr("警告: 料金表に無いモデルはコスト 0 で集計します:", "Warning: model not in the price table, counted as cost 0:"), model)
	}
	return nil
}

var tierRe = regexp.MustCompile(`_above_(\d+)k_tokens$`)

// tier は「プロンプトが N トークンを超える場合」の段階料金の閾値のうち、該当する最大のものを返す。
func (mp modelPrice) tier(prompt int64) string {
	var ths []int
	for k := range mp {
		if m := tierRe.FindStringSubmatch(k); m != nil && strings.HasPrefix(k, "input_cost_per_token") {
			n, _ := strconv.Atoi(m[1])
			ths = append(ths, n)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(ths)))
	for _, n := range ths {
		if prompt > int64(n)*1000 {
			return fmt.Sprintf("_above_%dk_tokens", n)
		}
	}
	return ""
}

func (mp modelPrice) get(key, suffix string) float64 {
	if suffix != "" {
		if v, ok := mp[key+suffix]; ok {
			return v
		}
	}
	return mp[key]
}

// cost は1メッセージの API 換算コスト。1時間キャッシュの書き込みは専用単価があれば使う。
func (p *pricing) cost(model string, u msgUsage) float64 {
	mp := p.lookup(model)
	if mp == nil {
		return 0
	}
	sfx := mp.tier(u.Input + u.CacheCreation + u.CacheRead)
	cw5m, cw1h := u.CacheCreation-u.CacheCreation1h, u.CacheCreation1h
	price1h := mp.get("cache_creation_input_token_cost", sfx)
	if v, ok := mp["cache_creation_input_token_cost_above_1hr"+sfx]; ok {
		price1h = v
	} else if v, ok := mp["cache_creation_input_token_cost_above_1hr"]; ok && sfx == "" {
		price1h = v
	}
	return float64(u.Input)*mp.get("input_cost_per_token", sfx) +
		float64(u.Output)*mp.get("output_cost_per_token", sfx) +
		float64(cw5m)*mp.get("cache_creation_input_token_cost", sfx) +
		float64(cw1h)*price1h +
		float64(u.CacheRead)*mp.get("cache_read_input_token_cost", sfx)
}
