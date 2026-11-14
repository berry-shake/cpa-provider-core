package requestlog

import (
	"database/sql"
	"strings"
	"time"
)

// UsageWindow sums one credential's logged requests over a period.
type UsageWindow struct {
	Requests int64 `json:"requests"`
	Failed   int64 `json:"failed"`
	// InputTokens includes CachedTokens.
	InputTokens     int64 `json:"input_tokens"`
	CachedTokens    int64 `json:"cached_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	ReasoningTokens int64 `json:"reasoning_tokens"`
}

// Usage is one credential's usage over the last 5 hours, 24 hours, 7 days and
// 30 days, read from the log (OpenDesign's Go plan limits spend per 5 hours,
// 7 days and month). It counts what went through this plugin only.
type Usage struct {
	FiveHours UsageWindow `json:"five_hours"`
	Day       UsageWindow `json:"day"`
	Week      UsageWindow `json:"week"`
	Month     UsageWindow `json:"month"`
	// LogSince is when the oldest entry of the whole log was made; windows
	// reaching further back are only partly covered. Empty for an empty log.
	LogSince string `json:"log_since,omitempty"`
}

// Usage sums the entries of one credential, matched by its auth index (or,
// for entries without one, its auth file ID).
func (l *Log) Usage(authIndex, authID string, now time.Time) (Usage, string) {
	l.mu.Lock()
	db, openErr, retention := l.db, l.openErr, l.retention
	l.mu.Unlock()
	if db == nil {
		if openErr == "" && retention <= 0 {
			openErr = "the request log is turned off (log-retention: 0)"
		}
		return Usage{}, openErr
	}
	authIndex, authID = strings.TrimSpace(authIndex), strings.TrimSpace(authID)
	fiveHours := now.Add(-5 * time.Hour).UnixMilli()
	day := now.Add(-24 * time.Hour).UnixMilli()
	week := now.Add(-7 * 24 * time.Hour).UnixMilli()
	month := now.Add(-30 * 24 * time.Hour).UnixMilli()
	window := func(since string) string {
		return `COUNT(CASE WHEN time_ms >= ` + since + ` THEN 1 END),
			COUNT(CASE WHEN time_ms >= ` + since + ` AND status = 'error' THEN 1 END),
			SUM(CASE WHEN time_ms >= ` + since + ` THEN input_tokens END),
			SUM(CASE WHEN time_ms >= ` + since + ` THEN cached_tokens END),
			SUM(CASE WHEN time_ms >= ` + since + ` THEN output_tokens END),
			SUM(CASE WHEN time_ms >= ` + since + ` THEN reasoning_tokens END)`
	}
	query := `SELECT ` + window("?1") + `, ` + window("?2") + `, ` + window("?3") + `, ` + window("?4") + `
		FROM requests WHERE time_ms >= ?4 AND (auth_index = ?5 AND ?5 != '' OR auth_index = '' AND auth_id = ?6 AND ?6 != '')`
	var cols [24]sql.NullInt64
	dest := make([]any, len(cols))
	for i := range cols {
		dest[i] = &cols[i]
	}
	if errScan := db.QueryRow(query, fiveHours, day, week, month, authIndex, authID).Scan(dest...); errScan != nil {
		return Usage{}, "read request log: " + errScan.Error()
	}
	fill := func(w *UsageWindow, at int) {
		w.Requests, w.Failed = cols[at].Int64, cols[at+1].Int64
		w.InputTokens, w.CachedTokens = cols[at+2].Int64, cols[at+3].Int64
		w.OutputTokens, w.ReasoningTokens = cols[at+4].Int64, cols[at+5].Int64
	}
	var out Usage
	fill(&out.FiveHours, 0)
	fill(&out.Day, 6)
	fill(&out.Week, 12)
	fill(&out.Month, 18)
	var oldest sql.NullInt64
	if errOldest := db.QueryRow(`SELECT MIN(time_ms) FROM requests`).Scan(&oldest); errOldest == nil && oldest.Valid {
		out.LogSince = time.UnixMilli(oldest.Int64).UTC().Format(time.RFC3339)
	}
	return out, ""
}
