package requestlog

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testDB is the database file name the tests use.
const testDB = "test.db"

func TestRequestLogPersistAndReload(t *testing.T) {
	dir := t.TempDir()
	log := New(testDB, "test: ", nil)
	log.Configure(dir, 3, 0)
	for i := 0; i < 7; i++ {
		log.Add(Entry{Model: "m", Status: "success", InputTokens: 10, CachedTokens: 5})
	}
	if page := log.Query(Query{}); page.Total != 3 || page.Items[0].ID != "7" || page.Items[2].ID != "5" {
		t.Fatalf("row cap: %+v", page)
	}
	log.Close()
	if _, errStat := os.Stat(filepath.Join(dir, testDB)); errStat != nil {
		t.Fatalf("database not on disk: %v", errStat)
	}

	reloaded := New(testDB, "test: ", nil)
	defer reloaded.Close()
	reloaded.Configure(dir, 3, 0)
	page := reloaded.Query(Query{})
	if page.Total != 3 || page.Items[0].ID != "7" || page.Retention != 3 || page.Error != "" {
		t.Fatalf("page = %+v", page)
	}
	reloaded.Add(Entry{Model: "x", Status: "error", Error: "boom"})
	page = reloaded.Query(Query{Status: "error"})
	if page.Total != 1 || page.Items[0].ID != "8" {
		t.Fatalf("error page = %+v", page)
	}
	if page = reloaded.Query(Query{Search: "BOOM"}); page.Total != 1 {
		t.Fatalf("search = %+v", page)
	}
	if page = reloaded.Query(Query{Search: "%"}); page.Total != 0 {
		t.Fatalf("LIKE wildcards must be literal: %+v", page)
	}
	if page = reloaded.Query(Query{Status: "success"}); page.Summary.CacheHitRate == nil || *page.Summary.CacheHitRate != 0.5 || page.Summary.Requests != 2 {
		t.Fatalf("summary = %+v", page.Summary)
	}
	if page = reloaded.Query(Query{Limit: 1, Offset: 1}); len(page.Items) != 1 || page.Items[0].ID != "7" || page.Offset != 1 || page.Total != 3 {
		t.Fatalf("paging = %+v", page)
	}
	if reloaded.Clear() != nil || reloaded.Query(Query{}).Total != 0 {
		t.Fatal("clear")
	}
}

func TestRequestLogRangeAndAge(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	log := New(testDB, "test: ", nil)
	log.now = func() time.Time { return now }
	log.Configure("", 100, 0)
	defer log.Close()
	ttft, rate := int64(120), 42.5
	for _, age := range []time.Duration{40 * 24 * time.Hour, 3 * 24 * time.Hour, 2 * time.Hour, time.Minute} {
		log.Add(Entry{Time: now.Add(-age), Model: "m", Status: "success", OutputTokens: 1, TTFTMS: &ttft, TokensPerSecond: &rate})
	}
	if page := log.Query(Query{From: now.Add(-24 * time.Hour)}); page.Total != 2 || page.Summary.OutputTokens != 2 {
		t.Fatalf("last 24h = %+v", page)
	}
	page := log.Query(Query{From: now.Add(-7 * 24 * time.Hour), To: now.Add(-time.Hour)})
	if page.Total != 2 || page.Items[0].Time.Unix() != now.Add(-2*time.Hour).Unix() {
		t.Fatalf("custom range = %+v", page)
	}
	if got := page.Items[0]; got.TTFTMS == nil || *got.TTFTMS != 120 || got.TokensPerSecond == nil || *got.TokensPerSecond != 42.5 {
		t.Fatalf("nullable columns = %+v", got)
	}
	if page = log.Query(Query{To: now.Add(-time.Minute)}); page.Total != 3 {
		t.Fatalf("to is exclusive: %+v", page)
	}

	// Turning on an age limit prunes right away.
	log.Configure("", 100, 30)
	if page = log.Query(Query{}); page.Total != 3 || page.RetentionDays != 30 {
		t.Fatalf("age prune = %+v", page)
	}
	// Retention 0 stops logging and closes the database.
	log.Configure("", 0, 30)
	log.Add(Entry{Model: "m", Status: "success"})
	if page = log.Query(Query{}); page.Total != 0 || page.Retention != 0 {
		t.Fatalf("disabled = %+v", page)
	}
}

// Wokey's JSONL log from before SQLite is imported once and removed.
func TestRequestLogImportsJSONL(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"id":"41","time":"2026-03-01T10:00:00Z","endpoint":"/v1/messages","model":"wokey/a","stream":true,"status":"success","ttft_ms":90,"input_tokens":5}
{"id":"42","time":"2026-03-01T11:00:00Z","endpoint":"/v1/chat/completions","model":"wokey/b","stream":false,"status":"error","error":"boom"}
{"id":"43","time":"2026-03-01T1`
	if errWrite := os.WriteFile(filepath.Join(dir, legacyRequestLog), []byte(legacy), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	log := New(testDB, "test: ", nil)
	defer log.Close()
	log.Configure(dir, 100, 0)
	page := log.Query(Query{})
	if page.Total != 2 || page.Items[0].Model != "wokey/b" || page.Items[1].TTFTMS == nil || !page.Items[1].Stream {
		t.Fatalf("imported = %+v", page)
	}
	if want := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC); !page.Items[1].Time.Equal(want) {
		t.Fatalf("time = %v", page.Items[1].Time)
	}
	if _, errStat := os.Stat(filepath.Join(dir, legacyRequestLog)); !os.IsNotExist(errStat) {
		t.Fatal("legacy log not removed")
	}
}

// Wokey's database from early 0.1.7 (requests.db) takes the configured
// name when that does not exist yet.
func TestRequestLogRenamesOldDatabase(t *testing.T) {
	src, dir := t.TempDir(), t.TempDir()
	old := New(testDB, "test: ", nil)
	defer old.Close()
	old.Configure(src, 100, 0)
	for i := 0; i < 3; i++ {
		old.Add(Entry{Model: "m", Status: "success"})
	}
	// Copy the files while the database is still open, so the rows are only
	// in the WAL, as after a container stop that skipped the checkpoint.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		raw, errRead := os.ReadFile(filepath.Join(src, testDB+suffix))
		if errRead != nil {
			t.Fatal(errRead)
		}
		if errWrite := os.WriteFile(filepath.Join(dir, legacyRequestDB+suffix), raw, 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
	}

	log := New(testDB, "test: ", nil)
	defer log.Close()
	log.Configure(dir, 100, 0)
	if page := log.Query(Query{}); page.Total != 3 || page.Items[0].ID != "3" || page.Error != "" {
		t.Fatalf("renamed = %+v", page)
	}
	if _, errStat := os.Stat(filepath.Join(dir, legacyRequestDB)); !os.IsNotExist(errStat) {
		t.Fatal("old database left behind")
	}
}

// Without the legacy files, opening a data dir creates only the database.
func TestRequestLogWithoutLegacyFiles(t *testing.T) {
	dir := t.TempDir()
	log := New(testDB, "test: ", func(message string) { t.Errorf("warning: %s", message) })
	defer log.Close()
	log.Configure(dir, 100, 0)
	log.Add(Entry{Model: "m", Status: "success"})
	for _, name := range []string{legacyRequestDB, legacyRequestLog} {
		if _, errStat := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(errStat) {
			t.Fatalf("%s created", name)
		}
	}
	if page := log.Query(Query{}); page.Total != 1 {
		t.Fatalf("page = %+v", page)
	}
}

func TestRequestLogUpgradesOldTable(t *testing.T) {
	dir := t.TempDir()
	// A database written by early 0.1.7, before request_id and the 0.1.9
	// columns existed.
	db, errOpen := sql.Open("sqlite3", "file:"+filepath.ToSlash(filepath.Join(dir, testDB)))
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	cut, end := strings.Index(requestSchema, ",\n\trequest_id"), strings.Index(requestSchema, "\n);")
	if cut < 0 || end < cut {
		t.Fatal("schema layout changed")
	}
	oldSchema := requestSchema[:cut] + requestSchema[end:]
	if _, errExec := db.Exec(oldSchema); errExec != nil {
		t.Fatal(errExec)
	}
	if _, errExec := db.Exec(`INSERT INTO requests (time_ms, model, status) VALUES (?, 'old', 'success')`, time.Now().UnixMilli()); errExec != nil {
		t.Fatal(errExec)
	}
	_ = db.Close()

	log := New(testDB, "test: ", func(message string) { t.Errorf("warning: %s", message) })
	defer log.Close()
	log.Configure(dir, 100, 0)
	frames, last, reported := int64(42), int64(8100), false
	log.Add(Entry{
		Model: "new", Status: "success", RequestID: "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b",
		ClientPath: "/v1/messages", UserAgent: "claude-cli/2.1", AuthIndex: "9d3c", Egress: "socks5://p.example:1080",
		UpstreamStatus: 200, UpstreamRequestID: "req_1", RetryAfter: 7, FinishReason: "end_turn",
		Frames: &frames, LastFrameMS: &last, UsageReported: &reported,
	})
	page := log.Query(Query{})
	if page.Error != "" || page.Total != 2 || page.Items[0].RequestID != "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b" || page.Items[1].RequestID != "" {
		t.Fatalf("page = %+v", page)
	}
	e, old := page.Items[0], page.Items[1]
	if e.ClientPath != "/v1/messages" || e.UserAgent != "claude-cli/2.1" || e.AuthIndex != "9d3c" || e.Egress != "socks5://p.example:1080" ||
		e.UpstreamStatus != 200 || e.UpstreamRequestID != "req_1" || e.RetryAfter != 7 || e.FinishReason != "end_turn" ||
		e.Frames == nil || *e.Frames != 42 || e.LastFrameMS == nil || *e.LastFrameMS != 8100 || e.UsageReported == nil || *e.UsageReported {
		t.Fatalf("new entry = %+v", e)
	}
	if old.Frames != nil || old.LastFrameMS != nil || old.UsageReported != nil || old.UpstreamStatus != 0 {
		t.Fatalf("old entry = %+v", old)
	}
	// The attempts of one request are found by its full ID only.
	if found := log.Query(Query{RequestID: "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"}); found.Total != 1 || found.Items[0].Model != "new" {
		t.Fatalf("by request ID: %+v", found)
	}
	// CPA retried the request on another credential.
	log.Add(Entry{Model: "retry", Status: "error", RequestID: "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"})
	if found := log.Query(Query{}); found.Items[0].Attempts != 2 || found.Items[1].Attempts != 2 || found.Items[2].Attempts != 1 {
		t.Fatalf("attempts = %+v", found.Items)
	}
	if found := log.Query(Query{RequestID: "2e3f4a5b"}); found.Total != 0 {
		t.Fatalf("partial request ID matched: %+v", found)
	}
	var plan string
	if errPlan := log.db.QueryRow(`EXPLAIN QUERY PLAN SELECT id FROM requests WHERE request_id = ?`, "x").Scan(new(int), new(int), new(int), &plan); errPlan != nil || !strings.Contains(plan, "requests_request_id") {
		t.Fatalf("request ID lookup plan = %q, %v", plan, errPlan)
	}
	// The 8 characters CPA prints in its log lines find the entry.
	if found := log.Query(Query{Search: "2e3f4a5b"}); found.Total != 2 || found.Items[1].Model != "new" {
		t.Fatalf("search by short request ID: %+v", found)
	}
	// Reopening an upgraded database does not alter it again.
	log.Close()
	log.Configure("", 100, 0)
	log.Configure(dir, 100, 0)
	if page = log.Query(Query{}); page.Error != "" || page.Total != 3 {
		t.Fatalf("reopen: %+v", page)
	}
}

func TestConnectionsUseTheSmallPageCache(t *testing.T) {
	l := New("requests.db", "test: ", func(string) {})
	l.Configure(t.TempDir(), 100, 0)
	defer l.Close()
	var size int
	if err := l.db.QueryRow("PRAGMA cache_size").Scan(&size); err != nil {
		t.Fatal(err)
	}
	if size != -pageCacheKiB {
		t.Fatalf("cache_size = %d, want %d", size, -pageCacheKiB)
	}
}

func TestRowCapIsKeptInBatches(t *testing.T) {
	for retention, want := range map[int]int64{3: 1, 99: 1, 100: 1, 1000: 10, 100000: 100, 1000000: 100} {
		if got := pruneBatch(retention); got != want {
			t.Errorf("pruneBatch(%d) = %d, want %d", retention, got, want)
		}
	}
	l := New("requests.db", "test: ", func(string) {})
	l.Configure(t.TempDir(), 1000, 0)
	defer l.Close()
	add := func(n int) {
		for i := 0; i < n; i++ {
			l.Add(Entry{Model: "m", Status: "success"})
		}
	}
	total := func() int { return l.Query(Query{Limit: 1}).Total }
	add(1000)
	if got := total(); got != 1000 {
		t.Fatalf("at the cap: %d rows", got)
	}
	// Over the cap by less than a batch (10): the rows wait for the batch.
	add(9)
	if got := total(); got != 1009 {
		t.Fatalf("within a batch over the cap: %d rows", got)
	}
	// The insert that completes the batch brings the table back to the cap.
	add(1)
	if got := total(); got != 1000 {
		t.Fatalf("after a batch: %d rows", got)
	}
	if page := l.Query(Query{Limit: 1}); page.Items[0].ID != "1010" {
		t.Fatalf("newest row = %s", page.Items[0].ID)
	}
	// Changing the limits applies the cap at once.
	add(5)
	l.Configure(l.dir, 1000, 0)
	if got := total(); got != 1000 {
		t.Fatalf("after Configure: %d rows", got)
	}
}

// BenchmarkAddAtCap writes into a full log, where every insert used to be
// followed by a DELETE of the oldest row.
func BenchmarkAddAtCap(b *testing.B) {
	l := New("requests.db", "bench: ", func(string) {})
	l.Configure(b.TempDir(), 1000, 30)
	defer l.Close()
	for i := 0; i < 1000; i++ {
		l.Add(Entry{Model: "m", Status: "success"})
	}
	b.ReportAllocs()
	for b.Loop() {
		l.Add(Entry{Model: "wokey/claude-opus-5-5", Status: "success", HTTPStatus: 200, InputTokens: 160000, OutputTokens: 900})
	}
}

// BenchmarkAddBelowCap is the same insert while the log is not full.
func BenchmarkAddBelowCap(b *testing.B) {
	l := New("requests.db", "bench: ", func(string) {})
	l.Configure(b.TempDir(), 1<<30, 30)
	defer l.Close()
	b.ReportAllocs()
	for b.Loop() {
		l.Add(Entry{Model: "wokey/claude-opus-5-5", Status: "success", HTTPStatus: 200, InputTokens: 160000, OutputTokens: 900})
	}
}
