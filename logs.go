package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// msgUsage は1メッセージのトークン数。
type msgUsage struct {
	Input, Output, CacheCreation, CacheCreation1h, CacheRead int64
}

// message はセッションログの assistant 応答1件。
type message struct {
	Time  time.Time
	Model string
	msgUsage
	Cost float64
}

// limitHit はセッションログに残ったリミット到達（429 rate_limit）の記録。
type limitHit struct {
	Time       time.Time
	Type       string
	ResetsAt   time.Time
	WindowCost float64 // 実際の枠（リセット時刻の5時間前〜到達時刻）の API 換算コスト。5時間枠のみ
}

// logRecord は JSONL 1行のうち集計に使う項目だけ。
type logRecord struct {
	Timestamp string   `json:"timestamp"`
	RequestID string   `json:"requestId"`
	CostUSD   *float64 `json:"costUSD"`
	Error     string   `json:"error"`
	Message   *struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreation            *struct {
				Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
		} `json:"usage"`
	} `json:"message"`
	QuotaLimits *struct {
		Status        string `json:"status"`
		RateLimitType string `json:"rateLimitType"`
		ResetsAt      int64  `json:"resetsAt"`
	} `json:"quotaLimits"`
}

// projectDirs は Claude Code のセッションログの場所。CLAUDE_CONFIG_DIR はカンマ区切りで複数指定できる。
func projectDirs() []string {
	var roots []string
	if env := os.Getenv("CLAUDE_CONFIG_DIR"); env != "" {
		for _, d := range strings.Split(env, ",") {
			if d = strings.TrimSpace(d); d != "" {
				roots = append(roots, d)
			}
		}
	} else {
		home, _ := os.UserHomeDir()
		roots = []string{filepath.Join(home, ".config", "claude"), filepath.Join(home, ".claude")}
	}
	var dirs []string
	for _, r := range roots {
		d := filepath.Join(r, "projects")
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

func claudeDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return strings.TrimSpace(strings.Split(d, ",")[0])
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// loadLogs はセッションログ（サブエージェント分を含む）を読み、期間内の応答とリミット到達を返す。
// 同じ応答はストリーミングの都合で複数行に記録されるため、message.id + requestId で重複を除く。
// 途中の行は出力トークン数が途中までの値なので、出力トークン数が最大の行（最終値）を採用する。
func loadLogs(pr *pricing, from, to time.Time) ([]message, []limitHit, error) {
	dirs := projectDirs()
	if len(dirs) == 0 {
		return nil, nil, errors.New(tr("Claude Code のセッションログ（~/.claude/projects）が見つかりません", "Claude Code session logs (~/.claude/projects) not found"))
	}
	seen := map[string]int{} // 重複判定キー → msgs のインデックス
	hitFirst := map[string]limitHit{}
	var msgs []message
	for _, dir := range dirs {
		filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
				return nil
			}
			f, err := os.Open(path)
			if err != nil {
				return nil
			}
			defer f.Close()
			r := bufio.NewReaderSize(f, 1<<20)
			for {
				line, rerr := r.ReadBytes('\n')
				parseLine(line, pr, from, to, seen, hitFirst, &msgs)
				if rerr != nil {
					if rerr != io.EOF {
						fmt.Fprintln(os.Stderr, tr("警告: 読み込みエラー:", "Warning: read error:"), path, rerr)
					}
					break
				}
			}
			return nil
		})
	}
	sort.Slice(msgs, func(i, j int) bool { return msgs[i].Time.Before(msgs[j].Time) })

	var hits []limitHit
	for _, h := range hitFirst {
		if h.Type == "five_hour" {
			start := h.ResetsAt.Add(-5 * time.Hour)
			for _, m := range msgs {
				if !m.Time.Before(start) && !m.Time.After(h.Time) {
					h.WindowCost += m.Cost
				}
			}
		}
		hits = append(hits, h)
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Time.Before(hits[j].Time) })
	return msgs, hits, nil
}

func parseLine(line []byte, pr *pricing, from, to time.Time, seen map[string]int, hitFirst map[string]limitHit, msgs *[]message) {
	isUsage := bytes.Contains(line, []byte(`"usage"`))
	isHit := bytes.Contains(line, []byte(`"quotaLimits"`))
	if !isUsage && !isHit {
		return
	}
	var rec logRecord
	if json.Unmarshal(line, &rec) != nil {
		return
	}
	t, err := time.Parse(time.RFC3339, rec.Timestamp)
	if err != nil || t.Before(from) || !t.Before(to) {
		return
	}

	// リミット到達。同じ枠（種類 + リセット時刻）で何度も弾かれた場合は最初の1回だけ数える
	if q := rec.QuotaLimits; q != nil && rec.Error == "rate_limit" && q.Status == "rejected" {
		key := fmt.Sprintf("%s/%d", q.RateLimitType, q.ResetsAt)
		if old, ok := hitFirst[key]; !ok || t.Before(old.Time) {
			hitFirst[key] = limitHit{Time: t, Type: q.RateLimitType, ResetsAt: time.Unix(q.ResetsAt, 0)}
		}
		return
	}

	m := rec.Message
	if m == nil || m.Usage == nil || m.Model == "" || m.Model == "<synthetic>" {
		return
	}
	u := msgUsage{
		Input:         m.Usage.InputTokens,
		Output:        m.Usage.OutputTokens,
		CacheCreation: m.Usage.CacheCreationInputTokens,
		CacheRead:     m.Usage.CacheReadInputTokens,
	}
	if cc := m.Usage.CacheCreation; cc != nil {
		u.CacheCreation1h = min(cc.Ephemeral1h, u.CacheCreation)
	}
	msg := message{Time: t, Model: m.Model, msgUsage: u}
	if rec.CostUSD != nil {
		msg.Cost = *rec.CostUSD // 古い Claude Code はログにコストを記録していた
	} else {
		msg.Cost = pr.cost(m.Model, u)
	}
	if m.ID != "" && rec.RequestID != "" {
		key := m.ID + ":" + rec.RequestID
		if i, ok := seen[key]; ok {
			if old := &(*msgs)[i]; msg.Output > old.Output {
				msg.Time = old.Time
				*old = msg
			}
			return
		}
		seen[key] = len(*msgs)
	}
	*msgs = append(*msgs, msg)
}

// block は5時間ブロック。ccusage と同じく、最初の応答の時刻を時単位で切り捨てた時刻から5時間とし、
// 5時間を超えるか、前の応答から5時間以上空いたら新しいブロックにする。
type block struct {
	start  time.Time
	Cost   float64
	Models []string
}

func buildBlocks(msgs []message) []block {
	const dur = 5 * time.Hour
	var bs []block
	var last time.Time
	for _, m := range msgs {
		if len(bs) == 0 || m.Time.Sub(bs[len(bs)-1].start) > dur || m.Time.Sub(last) > dur {
			bs = append(bs, block{start: m.Time.UTC().Truncate(time.Hour)})
		}
		b := &bs[len(bs)-1]
		b.Cost += m.Cost
		b.Models = append(b.Models, m.Model)
		last = m.Time
	}
	var out []block
	for _, b := range bs {
		if b.Cost > 0 {
			out = append(out, b)
		}
	}
	return out
}
