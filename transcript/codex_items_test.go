package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/entireio/cli/internal/transcript/codexitem"
)

// 2026-10-24T22:28:40.352Z and a second later.
const (
	codexItemStartMs = int64(1792880920352)
	codexItemDoneMs  = codexItemStartMs + 1000
	codexItemStartTS = `"2026-10-24T22:28:40.352Z"`
	codexItemDoneTS  = `"2026-10-24T22:28:41.352Z"`
)

// codexItemTestdata holds a real Codex session's scrubbed rollout and
// app-server notifications.
var codexItemTestdata = filepath.Join("..", "internal", "transcript", "codexitem", "testdata")

func codexIntp(n int) *int { return &n }

// codexRecordLines encodes records one JSON object per line, for comparison.
func codexRecordLines(t *testing.T, records []Record) string {
	t.Helper()
	var lines []string
	for _, r := range records {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("marshal record: %v", err)
		}
		lines = append(lines, string(b))
	}
	return strings.Join(lines, "\n")
}

func assertCodexRecords(t *testing.T, it codexitem.Item, want ...string) {
	t.Helper()
	got := codexRecordLines(t, codexItemRecords(it))
	if w := strings.Join(want, "\n"); got != w {
		t.Errorf("records differ\ngot:\n%s\nwant:\n%s", got, w)
	}
}

// doneCodexItem is a completed item with both times set.
func doneCodexItem(it codexitem.Item) codexitem.Item {
	if it.Status == "" {
		it.Status = codexitem.StatusCompleted
	}
	it.TurnID = "t1"
	it.StartedAtMs, it.CompletedAtMs = codexItemStartMs, codexItemDoneMs
	return it
}

func TestCodexItemRecordsUser(t *testing.T) {
	t.Parallel()
	assertCodexRecords(t, doneCodexItem(codexitem.Item{ID: "u1", Kind: codexitem.KindUser, Parts: []string{
		"<environment_context>\n  <cwd>/w</cwd>\n</environment_context>",
		"fix the bug",
		"",
		"and the test",
	}}),
		`{"op":"message","id":"u1","sourceId":"u1","role":"user","ts":`+codexItemDoneTS+`,"content":[{"text":"fix the bug\n\nand the test"}]}`)

	assertCodexRecords(t, doneCodexItem(codexitem.Item{ID: "u2", Kind: codexitem.KindUser, Parts: []string{
		"<environment_context>\n  <cwd>/w</cwd>\n</environment_context>",
	}}), "")
}

func TestCodexItemRecordsAgent(t *testing.T) {
	t.Parallel()
	assertCodexRecords(t, doneCodexItem(codexitem.Item{ID: "m1", Kind: codexitem.KindAgent, Text: "Done."}),
		`{"op":"message","id":"m1","sourceId":"m1","role":"assistant","ts":`+codexItemDoneTS+`,"content":[{"type":"text","text":"Done."}]}`)
	assertCodexRecords(t, doneCodexItem(codexitem.Item{ID: "m2", Kind: codexitem.KindAgent}))
}

func TestCodexItemRecordsCommand(t *testing.T) {
	t.Parallel()
	use := `{"op":"message","id":"e1","sourceId":"e1","role":"assistant","ts":%s,"content":[{"type":"tool_use","id":"e1","name":"Bash","input":{"command":"ls -la"}}]}`
	result := `{"op":"result","id":"e1","sourceId":"e1","ts":%s,"toolUseId":"e1","result":{"output":%q,"status":%q}}`
	cmd := codexitem.Item{ID: "e1", Kind: codexitem.KindCommand, Command: "ls -la", CWD: "/w"}

	ok := cmd
	ok.Output, ok.ExitCode = "total 0\n", codexIntp(0)
	assertCodexRecords(t, doneCodexItem(ok),
		fmt.Sprintf(use, codexItemDoneTS), fmt.Sprintf(result, codexItemDoneTS, "total 0\n", "success"))

	failed := cmd
	failed.Output, failed.ExitCode = "ls: x: No such file\n", codexIntp(2)
	assertCodexRecords(t, doneCodexItem(failed),
		fmt.Sprintf(use, codexItemDoneTS), fmt.Sprintf(result, codexItemDoneTS, "ls: x: No such file\n", "error"))

	running := cmd
	running.Status, running.StartedAtMs = codexitem.StatusInProgress, codexItemStartMs
	assertCodexRecords(t, running, fmt.Sprintf(use, codexItemStartTS))
	running.Output = "total"
	assertCodexRecords(t, running,
		fmt.Sprintf(use, codexItemStartTS), fmt.Sprintf(result, codexItemStartTS, "total", "success"))
}

func TestCodexItemRecordsFileChange(t *testing.T) {
	t.Parallel()
	it := doneCodexItem(codexitem.Item{ID: "f1", Kind: codexitem.KindFileChange, Changes: []codexitem.Change{
		{Path: "/w/a.txt", Kind: "add", Diff: "hi\n"},
		{Path: "/w/b.go", Kind: "update", MovePath: "/w/c.go", Diff: "@@ -1 +1 @@\n-a\n+b\n"},
	}})
	assertCodexRecords(t, it,
		`{"op":"message","id":"f1","sourceId":"f1","role":"assistant","ts":`+codexItemDoneTS+`,"content":[{"type":"tool_use","id":"f1","name":"FileChange","input":{"changes":[{"path":"/w/a.txt","kind":"add","diff":"hi\n"},{"path":"/w/b.go","kind":"update","move_path":"/w/c.go","diff":"@@ -1 +1 @@\n-a\n+b\n"}]}}]}`,
		`{"op":"result","id":"f1","sourceId":"f1","ts":`+codexItemDoneTS+`,"toolUseId":"f1","result":{"output":"","status":"success"}}`)

	it.Status = codexitem.StatusDeclined
	recs := codexItemRecords(it)
	if len(recs) != 2 || recs[1].Result == nil || recs[1].Result.Status != ToolStatusError {
		t.Errorf("declined file change: want an error result, got %s", codexRecordLines(t, recs))
	}
}

func TestCodexItemRecordsMCPTool(t *testing.T) {
	t.Parallel()
	assertCodexRecords(t, doneCodexItem(codexitem.Item{ID: "c1", Kind: codexitem.KindMCPTool, Server: "node_repl", Tool: "js",
		Arguments: json.RawMessage(`{"code":"2+2"}`), Output: "4"}),
		`{"op":"message","id":"c1","sourceId":"c1","role":"assistant","ts":`+codexItemDoneTS+`,"content":[{"type":"tool_use","id":"c1","name":"node_repl.js","input":{"code":"2+2"}}]}`,
		`{"op":"result","id":"c1","sourceId":"c1","ts":`+codexItemDoneTS+`,"toolUseId":"c1","result":{"output":"4","status":"success"}}`)

	assertCodexRecords(t, doneCodexItem(codexitem.Item{ID: "c2", Kind: codexitem.KindMCPTool, Server: "s", Tool: "t",
		Status: codexitem.StatusFailed, Error: "denied"}),
		`{"op":"message","id":"c2","sourceId":"c2","role":"assistant","ts":`+codexItemDoneTS+`,"content":[{"type":"tool_use","id":"c2","name":"s.t","input":{}}]}`,
		`{"op":"result","id":"c2","sourceId":"c2","ts":`+codexItemDoneTS+`,"toolUseId":"c2","result":{"output":"denied","status":"error"}}`)

	// Output and an error flag: the output is kept, the status is error.
	assertCodexRecords(t, doneCodexItem(codexitem.Item{ID: "c4", Kind: codexitem.KindMCPTool, Server: "s", Tool: "t",
		Output: "boom", Error: "tool reported an error"}),
		`{"op":"message","id":"c4","sourceId":"c4","role":"assistant","ts":`+codexItemDoneTS+`,"content":[{"type":"tool_use","id":"c4","name":"s.t","input":{}}]}`,
		`{"op":"result","id":"c4","sourceId":"c4","ts":`+codexItemDoneTS+`,"toolUseId":"c4","result":{"output":"boom","status":"error"}}`)

	// Running: the call, no result yet.
	assertCodexRecords(t, codexitem.Item{ID: "c3", Kind: codexitem.KindMCPTool, Server: "s", Tool: "t", Status: codexitem.StatusInProgress},
		`{"op":"message","id":"c3","sourceId":"c3","role":"assistant","content":[{"type":"tool_use","id":"c3","name":"s.t","input":{}}]}`)
}

func TestCodexItemRecordsWebSearchAndImageView(t *testing.T) {
	t.Parallel()
	assertCodexRecords(t, doneCodexItem(codexitem.Item{ID: "w1", Kind: codexitem.KindWebSearch, Query: "codex changelog"}),
		`{"op":"message","id":"w1","sourceId":"w1","role":"assistant","ts":`+codexItemDoneTS+`,"content":[{"type":"tool_use","id":"w1","name":"WebSearch","input":{"query":"codex changelog"}}]}`,
		`{"op":"result","id":"w1","sourceId":"w1","ts":`+codexItemDoneTS+`,"toolUseId":"w1","result":{"output":"","status":"success"}}`)
	assertCodexRecords(t, doneCodexItem(codexitem.Item{ID: "i1", Kind: codexitem.KindImageView, Path: "/w/shot.png"}),
		`{"op":"message","id":"i1","sourceId":"i1","role":"assistant","ts":`+codexItemDoneTS+`,"content":[{"type":"tool_use","id":"i1","name":"ImageView","input":{"path":"/w/shot.png"}}]}`,
		`{"op":"result","id":"i1","sourceId":"i1","ts":`+codexItemDoneTS+`,"toolUseId":"i1","result":{"output":"","status":"success"}}`)
}

func TestCodexItemRecordsSpawns(t *testing.T) {
	t.Parallel()
	assertCodexRecords(t, doneCodexItem(codexitem.Item{ID: "k1", Kind: codexitem.KindCollabTool, Tool: "spawnAgent",
		Prompt: "look into it", ReceiverThreadIDs: []string{"th2"}}),
		`{"op":"message","id":"k1","sourceId":"k1","role":"assistant","ts":`+codexItemDoneTS+`,"content":[{"type":"tool_use","id":"k1","name":"spawnAgent","input":{"prompt":"look into it"}}]}`,
		`{"op":"result","id":"k1","sourceId":"k1","ts":`+codexItemDoneTS+`,"toolUseId":"k1","result":{"output":"","status":"success","agent":{"id":"th2"}}}`)

	// Another collab tool: no agent, and no prompt in the input.
	assertCodexRecords(t, doneCodexItem(codexitem.Item{ID: "k2", Kind: codexitem.KindCollabTool, Tool: "wait", ReceiverThreadIDs: []string{"th2"}}),
		`{"op":"message","id":"k2","sourceId":"k2","role":"assistant","ts":`+codexItemDoneTS+`,"content":[{"type":"tool_use","id":"k2","name":"wait","input":{}}]}`,
		`{"op":"result","id":"k2","sourceId":"k2","ts":`+codexItemDoneTS+`,"toolUseId":"k2","result":{"output":"","status":"success"}}`)

	assertCodexRecords(t, doneCodexItem(codexitem.Item{ID: "s1", Kind: codexitem.KindSubAgent, ActivityKind: "started",
		AgentThreadID: "th3", AgentPath: "/root/explorer"}),
		`{"op":"message","id":"s1","sourceId":"s1","role":"assistant","ts":`+codexItemDoneTS+`,"content":[{"type":"tool_use","id":"s1","name":"spawn_agent","input":{"agent_path":"/root/explorer"}}]}`,
		`{"op":"result","id":"s1","sourceId":"s1","ts":`+codexItemDoneTS+`,"toolUseId":"s1","result":{"output":"","status":"success","agent":{"id":"th3"}}}`)

	// Started without a thread ID: the spawn, no agent.
	assertCodexRecords(t, doneCodexItem(codexitem.Item{ID: "s3", Kind: codexitem.KindSubAgent, ActivityKind: "started", AgentPath: "/root/b"}),
		`{"op":"message","id":"s3","sourceId":"s3","role":"assistant","ts":`+codexItemDoneTS+`,"content":[{"type":"tool_use","id":"s3","name":"spawn_agent","input":{"agent_path":"/root/b"}}]}`,
		`{"op":"result","id":"s3","sourceId":"s3","ts":`+codexItemDoneTS+`,"toolUseId":"s3","result":{"output":"","status":"success"}}`)

	assertCodexRecords(t, doneCodexItem(codexitem.Item{ID: "s2", Kind: codexitem.KindSubAgent, ActivityKind: "completed", AgentThreadID: "th3"}))
}

func TestCodexItemRecordsOtherHasNone(t *testing.T) {
	t.Parallel()
	assertCodexRecords(t, doneCodexItem(codexitem.Item{ID: "r1", Kind: codexitem.KindOther}))
}

func TestCodexItemRecordsTimestamp(t *testing.T) {
	t.Parallel()
	const line = "2026-10-24T22:28:41.400Z"
	cases := []struct {
		name               string
		status             codexitem.Status
		started, completed int64
		lineTS             string
		want               string
	}{
		{"done: completed time first", codexitem.StatusCompleted, codexItemStartMs, codexItemDoneMs, line, codexItemDoneTS},
		{"done: then the line's", codexitem.StatusCompleted, codexItemStartMs, 0, line, `"` + line + `"`},
		{"done: then the start", codexitem.StatusCompleted, codexItemStartMs, 0, "", codexItemStartTS},
		{"done: none", codexitem.StatusCompleted, 0, 0, "", ""},
		{"running: start first", codexitem.StatusInProgress, codexItemStartMs, codexItemDoneMs, line, codexItemStartTS},
		{"running: then the line's", codexitem.StatusInProgress, 0, codexItemDoneMs, line, `"` + line + `"`},
		{"running: never the completed time", codexitem.StatusInProgress, 0, codexItemDoneMs, "", ""},
	}
	for _, c := range cases {
		it := codexitem.Item{ID: "m1", Kind: codexitem.KindAgent, Text: "x", Status: c.status,
			StartedAtMs: c.started, CompletedAtMs: c.completed, LineTS: c.lineTS}
		recs := codexItemRecords(it)
		if len(recs) == 0 {
			t.Fatalf("%s: no records", c.name)
		}
		if got := string(recs[0].TS); got != c.want {
			t.Errorf("%s: ts = %s, want %s", c.name, got, c.want)
		}
	}
}

// Every kind's records fold without a validation error, and the started and
// completed records of a command fold to the completed line.
func TestCodexItemRecordsFold(t *testing.T) {
	t.Parallel()
	items := []codexitem.Item{
		{ID: "u1", Kind: codexitem.KindUser, Parts: []string{"hi"}},
		{ID: "m1", Kind: codexitem.KindAgent, Text: "Hello"},
		{ID: "e1", Kind: codexitem.KindCommand, Command: "ls", Status: codexitem.StatusInProgress},
		{ID: "e1", Kind: codexitem.KindCommand, Command: "ls", Output: "a\n", ExitCode: codexIntp(0)},
		{ID: "f1", Kind: codexitem.KindFileChange, Changes: []codexitem.Change{{Path: "/w/a", Kind: "add", Diff: "x\n"}}},
		{ID: "c1", Kind: codexitem.KindMCPTool, Server: "s", Tool: "t", Arguments: json.RawMessage(`{"a":1}`), Output: "ok"},
		{ID: "w1", Kind: codexitem.KindWebSearch, Query: "q"},
		{ID: "i1", Kind: codexitem.KindImageView, Path: "/w/p.png"},
		{ID: "k1", Kind: codexitem.KindCollabTool, Tool: "spawnAgent", Prompt: "p", ReceiverThreadIDs: []string{"th2"}},
		{ID: "s1", Kind: codexitem.KindSubAgent, ActivityKind: "started", AgentThreadID: "th3", AgentPath: "/root/a"},
		{ID: "r1", Kind: codexitem.KindOther},
	}
	f := NewFolder(Options{Agent: "codex", CLIVersion: "0.1.0"})
	for _, it := range items {
		if it.Status == "" {
			it = doneCodexItem(it)
		}
		if _, err := f.Apply(codexItemRecords(it)); err != nil {
			t.Fatalf("item %s (%s): %v", it.ID, it.Kind, err)
		}
	}
	lines := strings.Split(strings.TrimSuffix(string(f.Lines()), "\n"), "\n")
	if len(lines) != 9 {
		t.Fatalf("got %d lines, want 9 (reasoning has none):\n%s", len(lines), strings.Join(lines, "\n"))
	}
	wantCommand := `{"v":1,"agent":"codex","cli_version":"0.1.0","type":"assistant","ts":` + codexItemDoneTS +
		`,"id":"e1","content":[{"id":"e1","input":{"command":"ls"},"name":"Bash","result":{"output":"a\n","status":"success"},"type":"tool_use"}]}`
	if lines[2] != wantCommand {
		t.Errorf("folded command line:\n got %s\nwant %s", lines[2], wantCommand)
	}
}

// For every item the app-server completed, the live mapping gives exactly
// the records Convert's item path gives for the same item in the rollout.
func TestCodexNotificationRecordsMatchStored(t *testing.T) {
	t.Parallel()
	stored := map[string][]Record{}
	for _, line := range codexFixtureLines(t, "rollout.jsonl") {
		if it, ok := codexitem.DecodeRollout(line); ok {
			stored[it.ID] = codexItemRecords(it)
		}
	}
	compared := 0
	for _, line := range codexFixtureLines(t, "daemon.jsonl") {
		var msg struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(line, &msg); err != nil {
			t.Fatalf("daemon.jsonl: %v", err)
		}
		recs, ok := CodexNotificationRecords(msg.Method, msg.Params, CodexLive{})
		if !ok {
			t.Fatalf("CodexNotificationRecords: ok = false for %s", line)
		}
		if msg.Method != "item/completed" {
			continue
		}
		var p struct {
			Item struct {
				ID string `json:"id"`
			} `json:"item"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			t.Fatal(err)
		}
		want, found := stored[p.Item.ID]
		if !found {
			t.Errorf("item %s: not in the rollout", p.Item.ID)
			continue
		}
		compared++
		if !reflect.DeepEqual(recs, want) {
			t.Errorf("item %s: live and stored records differ\nlive:\n%s\nstored:\n%s", p.Item.ID, codexRecordLines(t, recs), codexRecordLines(t, want))
		}
	}
	if compared != 27 {
		t.Errorf("compared %d items, want 27", compared)
	}
	if _, ok := CodexNotificationRecords("item/agentMessage/delta", []byte(`{"itemId":"m1","delta":"x"}`), CodexLive{}); ok {
		t.Error("item/agentMessage/delta: ok = true, want false")
	}
}

func TestCodexNotificationRecordsLive(t *testing.T) {
	t.Parallel()
	str := func(s string) *string { return &s }
	started := func(item string) []byte {
		return []byte(`{"item":` + item + `,"threadId":"th1","turnId":"t1","startedAtMs":` + strconv.FormatInt(codexItemStartMs, 10) + `}`)
	}
	agent := started(`{"type":"agentMessage","id":"m1","text":""}`)
	command := started(`{"type":"commandExecution","id":"e1","command":"/bin/zsh -lc 'make test'","cwd":"/w","status":"inProgress","aggregatedOutput":null,"exitCode":null}`)
	use := `{"op":"message","id":"e1","sourceId":"e1","role":"assistant","ts":` + codexItemStartTS + `,"content":[{"type":"tool_use","id":"e1","name":"Bash","input":{"command":"make test"}}]}`
	cases := []struct {
		name   string
		params []byte
		live   CodexLive
		want   []string
	}{
		{"agent before any delta", agent, CodexLive{}, nil},
		{"agent text so far", agent, CodexLive{Text: str("Hel")},
			[]string{`{"op":"message","id":"m1","sourceId":"m1","role":"assistant","ts":` + codexItemStartTS + `,"content":[{"type":"text","text":"Hel"}]}`}},
		{"running command", command, CodexLive{}, []string{use}},
		{"command output so far", command, CodexLive{Output: str("ok 1\n")}, []string{use,
			`{"op":"result","id":"e1","sourceId":"e1","ts":` + codexItemStartTS + `,"toolUseId":"e1","result":{"output":"ok 1\n","status":"success"}}`}},
		{"interrupted command", command, CodexLive{Output: str("ok 1\n"), Interrupted: true}, []string{
			`{"op":"message","id":"e1","sourceId":"e1","role":"assistant","ts":` + codexItemStartTS + `,"content":[{"type":"tool_use","id":"e1","name":"Bash","input":{"command":"make test"}}]}`,
			`{"op":"result","id":"e1","sourceId":"e1","ts":` + codexItemStartTS + `,"toolUseId":"e1","result":{"output":"ok 1\n","status":"error"}}`}},
		{"text override ignored for a command", command, CodexLive{Text: str("x")}, []string{use}},
		{"fallback time", []byte(`{"item":{"type":"agentMessage","id":"m2","text":"Hi"},"turnId":"t1"}`), CodexLive{TS: "2026-10-24T22:28:41.400Z"},
			[]string{`{"op":"message","id":"m2","sourceId":"m2","role":"assistant","ts":"2026-10-24T22:28:41.400Z","content":[{"type":"text","text":"Hi"}]}`}},
	}
	for _, c := range cases {
		method := "item/started"
		if c.name == "fallback time" {
			method = "item/completed"
		}
		recs, ok := CodexNotificationRecords(method, c.params, c.live)
		if !ok {
			t.Fatalf("%s: ok = false", c.name)
		}
		if got, want := codexRecordLines(t, recs), strings.Join(c.want, "\n"); got != want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, want)
		}
	}
	// A completed item's status is final: Interrupted changes nothing.
	done := []byte(`{"item":{"type":"commandExecution","id":"e2","command":"ls","cwd":"/w","status":"completed","aggregatedOutput":"a\n","exitCode":0},"turnId":"t1"}`)
	recs, _ := CodexNotificationRecords("item/completed", done, CodexLive{Interrupted: true})
	if len(recs) != 2 || recs[1].Result.Status != ToolStatusSuccess {
		t.Errorf("completed command with Interrupted: %s", codexRecordLines(t, recs))
	}
}

// codexFixtureLines reads a Codex item fixture's non-blank lines.
func codexFixtureLines(t *testing.T, name string) [][]byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(codexItemTestdata, name))
	if err != nil {
		t.Fatal(err)
	}
	var lines [][]byte
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, 64<<20)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) > 0 {
			lines = append(lines, append([]byte(nil), sc.Bytes()...))
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}
