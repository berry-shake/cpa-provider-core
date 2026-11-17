// Package coretest runs the RPC snapshot tests of the provider plugins.
//
// RunSnapshots locks a plugin's behaviour at its RPC boundary. Every fixture
// in the binding's directory (testdata/snapshots) is replayed through the
// plugin's Call against a fake CPA host and a scripted httptest upstream
// (host.go), and every upstream request, host callback and RPC response is
// compared with the fixture. The fixture format and the normalisation rules
// are described in the README of cpa-provider-core.
//
//	go test ./internal/<id> -run TestSnapshots           # compare
//	go test ./internal/<id> -run TestSnapshots -update   # rewrite outputs
//
// Fixtures are the source of truth: -update rewrites only steps[].output
// (and writes a skeleton for a case of Binding.Cases that has no fixture
// yet); setup, upstream_script and steps[].rpc are never touched.
package coretest

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"gopkg.in/yaml.v3"
)

var updateSnapshots = flag.Bool("update", false, "rewrite the outputs of the RPC snapshot fixtures")

// Dir is where fixtures live, relative to the test's package.
const Dir = "testdata/snapshots"

// Clock is the time the plugin's clock must be pinned to. It shows up as
// the status "now", catalog and account fetch times and the auth refresh
// horizon, and it keeps account caches from expiring inside a case.
var Clock = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// Plugin is the plugin service as the runner drives it.
type Plugin interface {
	// Call sends one RPC. A binding may serve pseudo-methods of its own
	// here (Wokey's seam.refresh_tick) before passing the rest on.
	Call(method string, request []byte) []byte
	// WaitIdle blocks until background work started by a call (stream
	// pumps and what the plugin adds) has finished.
	WaitIdle()
	// RunQueuedRefresh runs, synchronously, the catalog refresh the
	// background loop would run for a queued kick, and reports whether one
	// was queued. DropQueuedRefresh discards it.
	RunQueuedRefresh() bool
	DropQueuedRefresh()
	// Close stops the service.
	Close()
}

// Binding ties the runner to one plugin. Only NewService, ProviderID and
// Cases are required; the rest are the plugin's own normalisation rules.
type Binding struct {
	// ProviderID is the auth provider the fake host parses files for.
	ProviderID string
	// NewService builds the service the way main.go does, with its clock
	// pinned to Clock, plugin.register kept from starting the catalog
	// refresh loop and the egress check pointed at upstreamURL + "/json"
	// (upstreamURL is the scripted upstream, "http://127.0.0.1:<port>").
	NewService func(version string, host func(method string, request []byte) ([]byte, error), upstreamURL string) Plugin
	// Cases generate the input skeletons of fixtures that do not exist yet.
	Cases func(t *testing.T) []Fixture
	// ConcurrentPaths are the upstream routes ("POST /api/show") the plugin
	// calls concurrently; consecutive requests to one are sorted by body.
	ConcurrentPaths []string
	// VolatileFields are member names replaced with "<volatile>" in
	// management.handle responses besides the common ones.
	VolatileFields []string
	// GeneratedIDs find values the plugin generates at random in
	// management.handle responses.
	GeneratedIDs []GeneratedID
}

// GeneratedID is one kind of value a plugin generates at random and returns
// from management.handle. Later inputs refer to the latest value of the kind
// by Placeholder; every value seen is written as "<volatile>" in outputs.
type GeneratedID struct {
	// Placeholder stands for the latest value in inputs ("{{proxy_id}}").
	Placeholder string
	// Find returns the values in a management response body decoded with
	// encoding/json.
	Find func(body any) []string
}

// Fixture is one snapshot case.
type Fixture struct {
	Case           string  `json:"case"`
	Description    string  `json:"description"`
	Setup          Setup   `json:"setup"`
	UpstreamScript []Reply `json:"upstream_script"`
	Steps          []Step  `json:"steps"`
}

// Setup prepares a case; it is not recorded.
type Setup struct {
	Version     string          `json:"version"`
	Config      json.RawMessage `json:"config"`
	Credentials []Credential    `json:"credentials"`
	// DropQueuedRefresh drops the catalog refresh a step queues instead of
	// running it after the step.
	DropQueuedRefresh bool `json:"drop_queued_refresh,omitempty"`
}

// Credential is an auth file put in the fake host's auth directory.
type Credential struct {
	Name   string          `json:"name"`
	Fields json.RawMessage `json:"fields"`
	// ReadOnly makes host.auth.save fail for the file.
	ReadOnly bool `json:"read_only,omitempty"`
}

// Step is one RPC and what it produced.
type Step struct {
	RPC    RPC             `json:"rpc"`
	Output json.RawMessage `json:"output"`
}

// RPC is the method and request a step sends.
type RPC struct {
	Method  string          `json:"method"`
	Request json.RawMessage `json:"request"`
}

// Output is what a step produced.
type Output struct {
	UpstreamRequests []UpstreamRequest `json:"upstream_requests"`
	HostCalls        []HostCall        `json:"host_calls"`
	Response         json.RawMessage   `json:"response"`
}

// HostCall is one host callback the plugin made.
type HostCall struct {
	Method  string          `json:"method"`
	Request json.RawMessage `json:"request"`
}

// LifecycleRequest mirrors CPA's rpcLifecycleRequest.
type LifecycleRequest struct {
	ConfigYAML         []byte `json:"config_yaml"`
	SchemaVersion      uint32 `json:"schema_version"`
	MultiAuthProviders bool   `json:"multi_auth_providers,omitempty"`
}

// RunSnapshots replays every fixture in Dir through the binding's plugin.
func RunSnapshots(t *testing.T, b Binding) {
	t.Helper()
	norm := newNormaliser(b.VolatileFields)
	if *updateSnapshots {
		writeMissingSnapshots(t, b)
	}
	paths, errGlob := filepath.Glob(filepath.Join(Dir, "*.json"))
	if errGlob != nil {
		t.Fatal(errGlob)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		t.Fatal("no snapshot fixtures; run go test -run TestSnapshots -update")
	}
	seen := map[string]bool{}
	for _, file := range paths {
		fixture := loadSnapshot(t, file)
		if want := strings.TrimSuffix(filepath.Base(file), ".json"); fixture.Case != want {
			t.Errorf("%s: case is %q, want %q", file, fixture.Case, want)
		}
		seen[fixture.Case] = true
		t.Run(fixture.Case, func(t *testing.T) { runSnapshot(t, b, norm, file, fixture) })
	}
	for _, fixture := range b.Cases(t) {
		if !seen[fixture.Case] {
			t.Errorf("case %s has no fixture; run go test -run TestSnapshots -update", fixture.Case)
		}
	}
}

func loadSnapshot(t *testing.T, file string) Fixture {
	t.Helper()
	raw, errRead := os.ReadFile(file)
	if errRead != nil {
		t.Fatal(errRead)
	}
	var fixture Fixture
	if errUnmarshal := json.Unmarshal(raw, &fixture); errUnmarshal != nil {
		t.Fatalf("%s: %v", file, errUnmarshal)
	}
	return fixture
}

// writeMissingSnapshots writes a skeleton for every case without a fixture.
func writeMissingSnapshots(t *testing.T, b Binding) {
	t.Helper()
	if errDir := os.MkdirAll(Dir, 0o755); errDir != nil {
		t.Fatal(errDir)
	}
	seen := map[string]bool{}
	for _, fixture := range b.Cases(t) {
		if seen[fixture.Case] {
			t.Fatalf("case %s is defined twice", fixture.Case)
		}
		seen[fixture.Case] = true
		file := filepath.Join(Dir, fixture.Case+".json")
		if _, errStat := os.Stat(file); errStat == nil {
			continue
		}
		writeSnapshot(t, file, fixture)
	}
}

func writeSnapshot(t *testing.T, file string, fixture Fixture) {
	t.Helper()
	raw, errMarshal := json.Marshal(fixture)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	pretty := prettyJSON(t, raw)
	if current, errRead := os.ReadFile(file); errRead == nil && bytes.Equal(current, pretty) {
		return
	}
	if errWrite := os.WriteFile(file, pretty, 0o644); errWrite != nil {
		t.Fatal(errWrite)
	}
}

func runSnapshot(t *testing.T, b Binding, norm *normaliser, file string, fixture Fixture) {
	run := newSnapshotRun(t, b, norm, fixture)
	outputs := make([]json.RawMessage, len(fixture.Steps))
	for i, step := range fixture.Steps {
		outputs[i] = run.step(i, step.RPC)
	}
	for _, key := range run.upstream.leftover() {
		t.Errorf("scripted upstream reply for %s was never requested", key)
	}
	if *updateSnapshots {
		if t.Failed() {
			return
		}
		for i := range fixture.Steps {
			fixture.Steps[i].Output = outputs[i]
		}
		writeSnapshot(t, file, fixture)
		return
	}
	for i, step := range fixture.Steps {
		if isNullJSON(step.Output) {
			t.Errorf("step %d (%s) has no recorded output; run with -update", i, step.RPC.Method)
			continue
		}
		want, got := string(prettyJSON(t, step.Output)), string(prettyJSON(t, outputs[i]))
		if want != got {
			t.Errorf("step %d (%s) output differs (-fixture +actual):\n%s", i, step.RPC.Method, lineDiff(want, got))
		}
	}
}

// snapshotRun is one replay of a fixture.
type snapshotRun struct {
	t        *testing.T
	binding  Binding
	norm     *normaliser
	svc      Plugin
	host     *snapshotHost
	upstream *snapshotUpstream
	drop     bool
	// toActual fills the placeholders of inputs; toPlaceholder puts them
	// back into outputs. Both are rebuilt when a generated ID turns up.
	toActual, toPlaceholder *strings.Replacer
	actual, placeholder     []string
	// latest maps the placeholder of each Binding.GeneratedIDs kind to its
	// latest value; generated are all values seen.
	latest    map[string]string
	generated []string
}

func newSnapshotRun(t *testing.T, b Binding, norm *normaliser, fixture Fixture) *snapshotRun {
	t.Helper()
	dataDir := t.TempDir()
	upstream := newSnapshotUpstream(fixture.UpstreamScript)
	t.Cleanup(upstream.Close)
	host := &snapshotHost{providerID: b.ProviderID}
	svc := b.NewService(fixture.Setup.Version, host.call, upstream.URL)
	host.plugin = svc
	t.Cleanup(svc.Close)

	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")
	replacements := []string{upstream.URL, "{{UPSTREAM}}", upstreamHost, "{{UPSTREAM_HOST}}", dataDir, "{{DATA_DIR}}"}
	if resolved, errResolve := filepath.EvalSymlinks(dataDir); errResolve == nil && resolved != dataDir {
		replacements = append(replacements, resolved, "{{DATA_DIR}}")
	}
	run := &snapshotRun{
		t:           t,
		binding:     b,
		norm:        norm,
		svc:         svc,
		host:        host,
		upstream:    upstream,
		drop:        fixture.Setup.DropQueuedRefresh,
		actual:      []string{"{{UPSTREAM}}", upstream.URL, "{{UPSTREAM_HOST}}", upstreamHost, "{{DATA_DIR}}", dataDir},
		placeholder: replacements,
		latest:      map[string]string{},
	}
	run.buildReplacers()

	config := run.configYAML(fixture.Setup.Config)
	register, _ := json.Marshal(LifecycleRequest{ConfigYAML: config, SchemaVersion: pluginabi.SchemaVersion})
	var envelope pluginabi.Envelope
	if errUnmarshal := json.Unmarshal(svc.Call(pluginabi.MethodPluginRegister, register), &envelope); errUnmarshal != nil || !envelope.OK {
		t.Fatalf("setup plugin.register failed: %+v %v", envelope.Error, errUnmarshal)
	}
	svc.WaitIdle()
	svc.DropQueuedRefresh()
	for _, cred := range fixture.Setup.Credentials {
		host.seed(cred.Name, run.compactInput(cred.Fields), cred.ReadOnly)
	}
	host.takeCalls()
	if requests, _ := upstream.take(); len(requests) > 0 {
		t.Fatalf("setup reached the upstream: %+v", requests)
	}
	return run
}

// buildReplacers makes toActual and toPlaceholder from the fixed
// placeholders and the generated IDs seen so far.
func (r *snapshotRun) buildReplacers() {
	actual := append([]string(nil), r.actual...)
	placeholders := make([]string, 0, len(r.latest))
	for placeholder := range r.latest {
		placeholders = append(placeholders, placeholder)
	}
	sort.Strings(placeholders)
	for _, placeholder := range placeholders {
		actual = append(actual, placeholder, r.latest[placeholder])
	}
	toPlaceholder := append([]string(nil), r.placeholder...)
	for _, value := range r.generated {
		toPlaceholder = append(toPlaceholder, value, volatileMarker)
	}
	r.toActual, r.toPlaceholder = strings.NewReplacer(actual...), strings.NewReplacer(toPlaceholder...)
}

// rememberGenerated records the Binding.GeneratedIDs in a management
// response.
func (r *snapshotRun) rememberGenerated(response []byte) {
	if len(r.binding.GeneratedIDs) == 0 {
		return
	}
	var envelope struct {
		Result struct {
			Body []byte
		} `json:"result"`
	}
	if json.Unmarshal(response, &envelope) != nil || len(envelope.Result.Body) == 0 {
		return
	}
	var body any
	if json.Unmarshal(envelope.Result.Body, &body) != nil {
		return
	}
	found := false
	for _, kind := range r.binding.GeneratedIDs {
		for _, value := range kind.Find(body) {
			if value == "" {
				continue
			}
			found = true
			r.latest[kind.Placeholder] = value
			if !slices.Contains(r.generated, value) {
				r.generated = append(r.generated, value)
			}
		}
	}
	if found {
		r.buildReplacers()
	}
}

// configYAML turns the fixture's config object into the YAML CPA passes.
func (r *snapshotRun) configYAML(raw json.RawMessage) []byte {
	if isNullJSON(raw) {
		return nil
	}
	var config map[string]any
	if errUnmarshal := json.Unmarshal([]byte(r.toActual.Replace(string(raw))), &config); errUnmarshal != nil {
		r.t.Fatalf("setup.config: %v", errUnmarshal)
	}
	out, errMarshal := yaml.Marshal(config)
	if errMarshal != nil {
		r.t.Fatalf("setup.config: %v", errMarshal)
	}
	return out
}

// compactInput fills the placeholders of a JSON input and compacts it,
// keeping its key order.
func (r *snapshotRun) compactInput(raw json.RawMessage) []byte {
	tree, errDecode := decodeOrdered([]byte(r.toActual.Replace(string(raw))))
	if errDecode != nil {
		r.t.Fatalf("decode input: %v", errDecode)
	}
	return marshalTree(tree, true)
}

func (r *snapshotRun) step(index int, rpc RPC) json.RawMessage {
	r.t.Helper()
	var request []byte
	if !isNullJSON(rpc.Request) {
		tree, errDecode := decodeOrdered([]byte(r.toActual.Replace(string(rpc.Request))))
		if errDecode != nil {
			r.t.Fatalf("step %d: decode request: %v", index, errDecode)
		}
		request = marshalTree(packByteFields(tree, ""), true)
	}
	response := r.svc.Call(rpc.Method, request)
	r.svc.WaitIdle()
	if r.drop {
		r.svc.DropQueuedRefresh()
	} else if r.svc.RunQueuedRefresh() {
		r.svc.WaitIdle()
	}
	if rpc.Method == pluginabi.MethodManagementHandle {
		r.rememberGenerated(response)
	}

	requests, unmatched := r.upstream.take()
	for _, key := range unmatched {
		r.t.Errorf("step %d (%s): unscripted upstream request %s", index, rpc.Method, key)
	}
	out := Output{UpstreamRequests: []UpstreamRequest{}, HostCalls: []HostCall{}}
	for _, req := range requests {
		header := http.Header{}
		for name, values := range req.Header {
			for _, value := range values {
				header[name] = append(header[name], r.toPlaceholder.Replace(value))
			}
		}
		req.Header = header
		req.Query = r.toPlaceholder.Replace(req.Query)
		req.Body = r.toPlaceholder.Replace(req.Body)
		out.UpstreamRequests = append(out.UpstreamRequests, req)
	}
	sortConcurrentRequests(out.UpstreamRequests, r.binding.ConcurrentPaths)
	for _, call := range r.host.takeCalls() {
		out.HostCalls = append(out.HostCalls, HostCall{Method: call.method, Request: r.canonical(call.request, false)})
	}
	out.Response = r.canonical(response, rpc.Method == pluginabi.MethodManagementHandle)
	raw, errMarshal := json.Marshal(out)
	if errMarshal != nil {
		r.t.Fatal(errMarshal)
	}
	return raw
}

// sortConcurrentRequests orders each run of consecutive requests to one of
// concurrent by body; the plugin makes them concurrently.
func sortConcurrentRequests(requests []UpstreamRequest, concurrent []string) {
	for i := 0; i < len(requests); {
		j := i
		for j < len(requests) && requests[j].Method == requests[i].Method && requests[j].Path == requests[i].Path && slices.Contains(concurrent, requests[i].Method+" "+requests[i].Path) {
			j++
		}
		if j-i > 1 {
			run := requests[i:j]
			sort.SliceStable(run, func(a, b int) bool { return run[a].Body < run[b].Body })
		}
		i = max(j, i+1)
	}
}

// canonical renders an RPC response or host-call request for the fixture.
func (r *snapshotRun) canonical(raw []byte, management bool) json.RawMessage {
	value := unpackByteFields(losslessJSON(raw), "")
	value = r.placeholders(value)
	if management {
		value = r.norm.volatile(value, "")
	}
	return marshalTree(value, true)
}

func (r *snapshotRun) placeholders(value any) any {
	switch typed := value.(type) {
	case string:
		return r.toPlaceholder.Replace(typed)
	case []any:
		for i := range typed {
			typed[i] = r.placeholders(typed[i])
		}
	case *orderedObject:
		for _, key := range typed.keys {
			typed.values[key] = r.placeholders(typed.values[key])
		}
	}
	return value
}

// byteFields are the members CPA's RPC types carry as []byte.
var byteFields = map[string]bool{
	"Payload": true, "OriginalRequest": true, "StorageJSON": true, "storage_json": true,
	"RawJSON": true, "config_yaml": true, "Body": true, "ResponseBody": true, "payload": true,
}

// unpackByteFields decodes the base64 of byte fields. It does not descend
// into the decoded content.
func unpackByteFields(value any, key string) any {
	switch typed := value.(type) {
	case string:
		if !byteFields[key] {
			return typed
		}
		decoded, errDecode := base64.StdEncoding.DecodeString(typed)
		if errDecode != nil || !utf8.Valid(decoded) {
			return typed
		}
		switch tree := losslessJSON(decoded).(type) {
		case *orderedObject, []any:
			return tree
		}
		return string(decoded)
	case []any:
		for i := range typed {
			typed[i] = unpackByteFields(typed[i], key)
		}
	case *orderedObject:
		for _, member := range typed.keys {
			typed.values[member] = unpackByteFields(typed.values[member], member)
		}
	}
	return value
}

// packByteFields is the inverse of unpackByteFields for fixture inputs.
func packByteFields(value any, key string) any {
	if byteFields[key] {
		switch typed := value.(type) {
		case string:
			return base64.StdEncoding.EncodeToString([]byte(typed))
		case *orderedObject, []any:
			return base64.StdEncoding.EncodeToString(marshalTree(typed, true))
		}
		return value
	}
	switch typed := value.(type) {
	case []any:
		for i := range typed {
			typed[i] = packByteFields(typed[i], key)
		}
	case *orderedObject:
		for _, member := range typed.keys {
			typed.values[member] = packByteFields(typed.values[member], member)
		}
	}
	return value
}

// commonVolatileFields are replaced in management responses; see the README.
var commonVolatileFields = []string{
	"time", "duration_ms", "ttft_ms", "last_frame_ms",
	"output_tokens_per_second", "e2e_tokens_per_second",
	"created_at", "proxy_id", "latency_ms",
	// The status view's figures of the plugin's own Go runtime, as a whole.
	"runtime",
}

// speedFields are dropped from request log entries.
var speedFields = map[string]bool{"output_tokens_per_second": true, "e2e_tokens_per_second": true}

const volatileMarker = "<volatile>"

// normaliser replaces the volatile values of management responses.
type normaliser struct {
	fields map[string]bool
	text   *regexp.Regexp
}

// newNormaliser builds the rules for the common volatile names and a
// binding's own.
func newNormaliser(extra []string) *normaliser {
	fields := map[string]bool{}
	for _, name := range append(append([]string(nil), commonVolatileFields...), extra...) {
		fields[name] = true
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, regexp.QuoteMeta(name))
	}
	sort.Strings(names)
	return &normaliser{
		fields: fields,
		text:   regexp.MustCompile(`"(` + strings.Join(names, "|") + `)":("(?:[^"\\]|\\.)*"|[^,}\]]*)`),
	}
}

func (n *normaliser) volatile(value any, parent string) any {
	switch typed := value.(type) {
	case string:
		return n.text.ReplaceAllString(typed, `"$1":"`+volatileMarker+`"`)
	case []any:
		for i := range typed {
			typed[i] = n.volatile(typed[i], parent+"[]")
		}
	case *orderedObject:
		if _, logEntry := typed.values["duration_ms"]; logEntry {
			typed.remove(speedFields)
		}
		for _, key := range typed.keys {
			switch {
			case n.fields[key]:
				typed.values[key] = volatileMarker
			case key == "id" && (parent == "proxy" || parent == "proxies[]"):
				typed.values[key] = volatileMarker
			default:
				typed.values[key] = n.volatile(typed.values[key], key)
			}
		}
	}
	return value
}

func isNullJSON(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

// CaseJSON renders a Go value (an RPC request as CPA builds it) as fixture
// JSON, byte fields decoded.
func CaseJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	if value == nil {
		return json.RawMessage("null")
	}
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	tree, errDecode := decodeOrdered(raw)
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	return marshalTree(unpackByteFields(tree, ""), true)
}

// orderedObject is a JSON object that keeps its member order.
type orderedObject struct {
	keys   []string
	values map[string]any
}

func newOrderedObject() *orderedObject {
	return &orderedObject{values: map[string]any{}}
}

func (o *orderedObject) set(key string, value any) {
	if _, exists := o.values[key]; !exists {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

// remove deletes the members named in drop.
func (o *orderedObject) remove(drop map[string]bool) {
	keys := o.keys[:0]
	for _, key := range o.keys {
		if drop[key] {
			delete(o.values, key)
			continue
		}
		keys = append(keys, key)
	}
	o.keys = keys
}

func (o *orderedObject) MarshalJSON() ([]byte, error) {
	return marshalTree(o, true), nil
}

// decodeOrdered decodes JSON into nil, bool, json.Number, string, []any and
// *orderedObject values.
func decodeOrdered(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, errValue := decodeOrderedValue(decoder)
	if errValue != nil {
		return nil, errValue
	}
	if _, errEnd := decoder.Token(); !errors.Is(errEnd, io.EOF) {
		return nil, fmt.Errorf("trailing data after JSON value")
	}
	return value, nil
}

func decodeOrderedValue(decoder *json.Decoder) (any, error) {
	token, errToken := decoder.Token()
	if errToken != nil {
		return nil, errToken
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delim {
	case '{':
		object := newOrderedObject()
		for decoder.More() {
			keyToken, errKey := decoder.Token()
			if errKey != nil {
				return nil, errKey
			}
			key, _ := keyToken.(string)
			value, errValue := decodeOrderedValue(decoder)
			if errValue != nil {
				return nil, errValue
			}
			object.set(key, value)
		}
		_, errEnd := decoder.Token()
		return object, errEnd
	case '[':
		array := []any{}
		for decoder.More() {
			value, errValue := decodeOrderedValue(decoder)
			if errValue != nil {
				return nil, errValue
			}
			array = append(array, value)
		}
		_, errEnd := decoder.Token()
		return array, errEnd
	}
	return nil, fmt.Errorf("unexpected delimiter %v", delim)
}

// losslessJSON decodes raw when it re-encodes byte for byte and otherwise
// returns it as a string.
func losslessJSON(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	tree, errDecode := decodeOrdered(raw)
	if errDecode != nil || !bytes.Equal(marshalTree(tree, true), raw) {
		return string(raw)
	}
	return tree
}

// marshalTree encodes a decoded tree compactly. With escapeHTML it matches
// encoding/json.Marshal.
func marshalTree(value any, escapeHTML bool) []byte {
	var buf bytes.Buffer
	writeTree(&buf, value, escapeHTML)
	return buf.Bytes()
}

func writeTree(buf *bytes.Buffer, value any, escapeHTML bool) {
	switch typed := value.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if typed {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case json.Number:
		buf.WriteString(typed.String())
	case string:
		writeJSONString(buf, typed, escapeHTML)
	case []any:
		buf.WriteByte('[')
		for i, item := range typed {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeTree(buf, item, escapeHTML)
		}
		buf.WriteByte(']')
	case *orderedObject:
		buf.WriteByte('{')
		for i, key := range typed.keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeJSONString(buf, key, escapeHTML)
			buf.WriteByte(':')
			writeTree(buf, typed.values[key], escapeHTML)
		}
		buf.WriteByte('}')
	default:
		raw, _ := json.Marshal(typed)
		buf.Write(raw)
	}
}

func writeJSONString(buf *bytes.Buffer, text string, escapeHTML bool) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(escapeHTML)
	_ = encoder.Encode(text)
	buf.Write(bytes.TrimSuffix(encoded.Bytes(), []byte("\n")))
}

// prettyJSON renders JSON the way fixtures are stored: member order kept,
// two-space indent, no HTML escaping, trailing newline.
func prettyJSON(t *testing.T, raw []byte) []byte {
	t.Helper()
	if isNullJSON(raw) {
		return []byte("null\n")
	}
	tree, errDecode := decodeOrdered(raw)
	if errDecode != nil {
		t.Fatalf("decode fixture JSON: %v", errDecode)
	}
	var out bytes.Buffer
	if errIndent := json.Indent(&out, marshalTree(tree, false), "", "  "); errIndent != nil {
		t.Fatalf("indent fixture JSON: %v", errIndent)
	}
	out.WriteByte('\n')
	return out.Bytes()
}

// lineDiff shows the differing region of two texts with some context.
func lineDiff(want, got string) string {
	a, b := strings.Split(want, "\n"), strings.Split(got, "\n")
	start := 0
	for start < len(a) && start < len(b) && a[start] == b[start] {
		start++
	}
	endA, endB := len(a)-1, len(b)-1
	for endA >= start && endB >= start && a[endA] == b[endB] {
		endA--
		endB--
	}
	const context, limit = 3, 120
	var out []string
	for i := max(0, start-context); i < start; i++ {
		out = append(out, "  "+a[i])
	}
	for i := start; i <= endA; i++ {
		out = append(out, "- "+a[i])
	}
	for i := start; i <= endB; i++ {
		out = append(out, "+ "+b[i])
	}
	for i := endA + 1; i < min(len(a), endA+1+context); i++ {
		out = append(out, "  "+a[i])
	}
	if len(out) > limit {
		out = append(out[:limit], fmt.Sprintf("... %d more lines", len(out)-limit))
	}
	return strings.Join(out, "\n")
}
