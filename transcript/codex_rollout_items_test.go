package transcript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/entireio/cli/internal/transcript/codexitem"
)

func readCodexItemsFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(codexItemTestdata, "rollout.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestHasCodexItems(t *testing.T) {
	t.Parallel()
	full, err := os.ReadFile(filepath.Join("testdata", "codex_full.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	item := `{"timestamp":"t","type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"AgentMessage","id":"m1","content":[{"type":"Text","text":"hi"}]}}}` + "\n"
	started := `{"timestamp":"t","type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}` + "\n"
	subagent := `{"timestamp":"t","type":"event_msg","payload":{"type":"item_completed","item":{"type":"SubAgentActivity","id":"call_1","kind":"started","agent_thread_id":"th2","agent_path":"/root/a"}}}` + "\n"
	old := `{"timestamp":"t","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}}` + "\n"
	cases := []struct {
		name    string
		content []byte
		want    bool
	}{
		{"items fixture", readCodexItemsFixture(t), true},
		{"older rollout", full, false},
		{"items without task_started", []byte(old + item), false},
		{"first turn without items", []byte(started + old + started + item), false},
		{"first turn with items", []byte(started + old + item + started + old), true},
		{"first turn with only a sub-agent item", []byte(started + old + subagent + started + old + item), false},
	}
	for _, c := range cases {
		if got := hasCodexItems(c.content); got != c.want {
			t.Errorf("%s: hasCodexItems = %v, want %v", c.name, got, c.want)
		}
	}
}

var codexItemsOpts = Options{Agent: "codex", CLIVersion: "0.5.1"}

func TestConvertCodexItems(t *testing.T) {
	t.Parallel()
	content := readCodexItemsFixture(t)
	out, err := Convert(content, codexItemsOpts)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	got := string(out)
	for _, want := range []string{
		`"type":"user"`,
		`Run ` + "`ls -la`",
		`"input":{"command":"ls -la"},"name":"Bash","result":{"output":"total 8\ndrwxr-xr-x`,
		`"input":{"changes":[{"path":"/work/repo/hello.txt","kind":"add","diff":"hello\n"}]},"name":"FileChange"`,
		`"input":{"query":"Codex CLI changelog site:developers.openai.com"},"name":"WebSearch"`,
		`"name":"cua_repl.js","result":{"output":"MCP tool call requires approval, but approval policy is never","status":"error"}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Convert output lacks %s", want)
		}
	}
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	var finalAgent string
	for _, l := range fixtureItems(t, content) {
		if l.Kind == codexitem.KindAgent {
			finalAgent = l.Text
		}
	}
	last := lines[len(lines)-1]
	if finalAgent == "" || !strings.Contains(last, mustJSONString(t, finalAgent)) {
		t.Errorf("last line is not the final agent message %q: %s", finalAgent, last)
	}
	// The last call is the final agent message alone. Its usage is the last
	// two total_token_usage snapshots' difference: input 400045-350367 =
	// 49678 of which cached 360320-311168 = 49152, so fresh 526; output
	// 1937-1816 = 121; reasoning 759-759 = 0 (omitted). The model is the
	// turn_context's.
	if !strings.Contains(last, `"model":"gpt-5.6-sol","input_tokens":526,"output_tokens":121,"cache_read_tokens":49152,"content"`) {
		t.Errorf("last line lacks the last call's usage: %s", last)
	}
	// Every call's usage is counted once: the lines sum to the last snapshot,
	// input 400045 (cached 360320), output 1937, reasoning 759.
	decoded, err := Decode(out)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	var in, cached, outTok, reasoning int64
	for _, l := range decoded {
		in += l.InputTokens + l.CacheReadTokens
		cached += l.CacheReadTokens
		outTok += l.OutputTokens
		reasoning += l.ReasoningTokens
	}
	if in != 400045 || cached != 360320 || outTok != 1937 || reasoning != 759 {
		t.Errorf("usage sums: input %d, cached %d, output %d, reasoning %d; want 400045, 360320, 1937, 759", in, cached, outTok, reasoning)
	}
	// A call's usage goes on its last assistant line: the call closed at
	// rollout line 18 has an agent message and a command; the command carries
	// 21412-21120 = 292 fresh, 21120 cached, 379 out, 154 reasoning.
	for _, l := range lines {
		switch {
		case strings.Contains(l, `"id":"exec-9cb89d94-65c4-4b82-be74-79ea14b5669e"`):
			if !strings.Contains(l, `"input_tokens":292,"output_tokens":379,"cache_read_tokens":21120,"reasoning_tokens":154,`) {
				t.Errorf("command line lacks its call's usage: %s", l)
			}
		case strings.Contains(l, `"id":"msg_08b42dda6a7b49f2016add31218f14819ba3be7367cc6ad815"`):
			if strings.Contains(l, `"input_tokens"`) {
				t.Errorf("a call's earlier line carries usage: %s", l)
			}
		}
	}
	for _, l := range lines {
		if strings.Contains(l, `"type":"user"`) && strings.Contains(l, "<environment_context>") {
			t.Errorf("injected context in a user line: %s", l)
		}
	}

	// By construction: without token counts, Convert is the fold of the
	// decoded items' records.
	var noUsage []byte
	for _, l := range bytes.SplitAfter(content, []byte("\n")) {
		if !bytes.Contains(l, []byte(`"type":"token_count"`)) {
			noUsage = append(noUsage, l...)
		}
	}
	out, err = Convert(noUsage, codexItemsOpts)
	if err != nil {
		t.Fatalf("Convert without token counts: %v", err)
	}
	var records []Record
	for _, it := range fixtureItems(t, noUsage) {
		records = append(records, codexItemRecords(it)...)
	}
	want, err := Fold(records, codexItemsOpts)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if !bytes.Equal(out, want) {
		t.Errorf("Convert without token counts differs from the fold of the items' records\ngot:\n%s\nwant:\n%s", out, want)
	}
}

func TestConvertWithBoundaryCodexItems(t *testing.T) {
	t.Parallel()
	content := readCodexItemsFixture(t)
	full, err := Convert(content, codexItemsOpts)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	fullLines := strings.SplitAfter(string(full), "\n")
	fullLines = fullLines[:len(fullLines)-1] // the empty string after the last newline
	n := bytes.Count(content, []byte("\n"))
	for start := 1; start <= n; start++ {
		opts := codexItemsOpts
		opts.StartLine = start
		gotFull, boundary, err := ConvertWithBoundary(content, opts)
		if err != nil {
			t.Fatalf("StartLine %d: %v", start, err)
		}
		if !bytes.Equal(gotFull, full) {
			t.Fatalf("StartLine %d: full output differs from Convert", start)
		}
		if boundary > len(fullLines) {
			t.Fatalf("StartLine %d: boundary %d beyond %d lines", start, boundary, len(fullLines))
		}
		delta, err := Convert(content, opts)
		if err != nil {
			t.Fatalf("StartLine %d: delta: %v", start, err)
		}
		if want := strings.Join(fullLines[boundary:], ""); string(delta) != want {
			t.Errorf("StartLine %d: delta is not the full output's last %d lines\ngot:\n%s\nwant:\n%s", start, len(fullLines)-boundary, delta, want)
		}
	}
}

// fixtureItems decodes the items of a rollout's item_completed lines.
func fixtureItems(t *testing.T, content []byte) []codexitem.Item {
	t.Helper()
	var items []codexitem.Item
	for _, l := range bytes.Split(content, []byte("\n")) {
		if it, ok := codexitem.DecodeRollout(l); ok {
			items = append(items, it)
		}
	}
	if len(items) == 0 {
		t.Fatal("no items in the rollout")
	}
	return items
}

func mustJSONString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// An item without an ID is skipped; the rest of the rollout converts.
func TestConvertCodexItemsSkipsItemWithoutID(t *testing.T) {
	t.Parallel()
	content := `{"timestamp":"t","type":"session_meta","payload":{"id":"s1"}}
{"timestamp":"t","type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}
{"timestamp":"t","type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"UserMessage","id":"u1","content":[{"type":"text","text":"hi"}]}}}
{"timestamp":"t","type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"AgentMessage","id":"","content":[{"type":"Text","text":"lost"}]}}}
{"timestamp":"t","type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"AgentMessage","id":"m2","content":[{"type":"Text","text":"Hello"}]}}}
`
	out, err := Convert([]byte(content), codexItemsOpts)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	want := `{"v":1,"agent":"codex","cli_version":"0.5.1","type":"user","ts":"t","id":"u1","content":[{"text":"hi"}]}
{"v":1,"agent":"codex","cli_version":"0.5.1","type":"assistant","ts":"t","id":"m2","content":[{"type":"text","text":"Hello"}]}
`
	if string(out) != want {
		t.Errorf("Convert:\n got %s\nwant %s", out, want)
	}
}

// An item may carry the words the converter scans for (here as an MCP
// argument key): it is still an item, not a turn_context line.
func TestConvertCodexItemsWordsInItems(t *testing.T) {
	t.Parallel()
	content := `{"timestamp":"t","type":"session_meta","payload":{"id":"s1"}}
{"timestamp":"t","type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}
{"timestamp":"t","type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"UserMessage","id":"u1","content":[{"type":"text","text":"hi"}]}}}
{"timestamp":"t","type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"AgentMessage","id":"m1","content":[{"type":"Text","text":"Hello"}]}}}
{"timestamp":"t","type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"McpToolCall","id":"c1","server":"s","tool":"t","status":"completed","arguments":{"turn_context":1}}}}
`
	out, err := Convert([]byte(content), codexItemsOpts)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if !strings.Contains(string(out), `"id":"c1"`) {
		t.Errorf("the MCP call naming turn_context was dropped:\n%s", out)
	}
}

// A call with no assistant line of its own (reasoning only) gets a usage-only
// line; a repeated snapshot adds nothing; a user message ends the open call.
func TestConvertCodexItemsCallUsage(t *testing.T) {
	t.Parallel()
	tc := func(ts string, in, cached, out int) string {
		return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d}}}}`, ts, in, cached, out) + "\n"
	}
	content := `{"timestamp":"t0","type":"session_meta","payload":{"id":"s1"}}
{"timestamp":"t0","type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}
{"timestamp":"t0","type":"turn_context","payload":{"turn_id":"t1","model":"m1"}}
{"timestamp":"t1","type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"UserMessage","id":"u1","content":[{"type":"text","text":"hi"}]}}}
{"timestamp":"t2","type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"Reasoning","id":"r1"}}}
` + tc("t3", 10, 4, 2) + tc("t4", 10, 4, 2) + `{"timestamp":"t5","type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"AgentMessage","id":"m1","content":[{"type":"Text","text":"Hello"}]}}}
` + tc("t6", 25, 10, 5)
	out, err := Convert([]byte(content), codexItemsOpts)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	want := `{"v":1,"agent":"codex","cli_version":"0.5.1","type":"user","ts":"t1","id":"u1","content":[{"text":"hi"}]}
{"v":1,"agent":"codex","cli_version":"0.5.1","type":"assistant","ts":"t3","model":"m1","input_tokens":6,"output_tokens":2,"cache_read_tokens":4,"content":null}
{"v":1,"agent":"codex","cli_version":"0.5.1","type":"assistant","ts":"t5","id":"m1","model":"m1","input_tokens":9,"output_tokens":3,"cache_read_tokens":6,"content":[{"type":"text","text":"Hello"}]}
`
	if string(out) != want {
		t.Errorf("Convert:\n got %s\nwant %s", out, want)
	}
}
