package codexitem

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
)

func TestScriptOf(t *testing.T) {
	t.Parallel()

	argvCases := []struct {
		argv []string
		want string
	}{
		{[]string{"/bin/zsh", "-lc", "ls -la"}, "ls -la"},
		{[]string{"bash", "-c", "echo hi"}, "echo hi"},
		{[]string{"git", "status"}, "git status"},
		{[]string{"grep", "-c", "foo"}, "grep -c foo"},
		{[]string{"/usr/local/bin/fish", "-c", "ls"}, "ls"},
	}
	for _, c := range argvCases {
		if got := scriptOfArgv(c.argv); got != c.want {
			t.Errorf("scriptOfArgv(%q) = %q, want %q", c.argv, got, c.want)
		}
	}

	stringCases := []struct {
		cmd  string
		want string
	}{
		{`/bin/zsh -lc 'ls -la'`, `ls -la`},
		{`/bin/zsh -lc "sed -n '1,260p' /x/SKILL.md"`, `sed -n '1,260p' /x/SKILL.md`},
		{`/bin/zsh -lc 'echo it'"'"'s'`, `echo it's`},
		{"/bin/zsh -lc \"echo \\\"a\\\" \\$HOME \\\\ \\` x\"", "echo \"a\" $HOME \\ ` x"},
		{`/bin/bash -c echo\ hi`, `echo hi`},
		{`git status`, `git status`},
		{`grep -c foo`, `grep -c foo`},
		{`/bin/bash -c $'a\nb'`, "a\nb"},
		{`/bin/bash -lc $'cat <<EOF\nhi\tit\'s \\ \x41\101\nEOF'`, "cat <<EOF\nhi\tit's \\ AA\nEOF"},
		// As in bash, a NUL ends the quoted text; the word goes on after the quote.
		{`/bin/bash -lc $'a\0b'c`, "ac"},
		{`/bin/bash -lc $'x\x00y\'z'w`, "xw"},
		{`/bin/bash -lc pre$'x\ny'post`, "prex\nypost"},
		{`/bin/bash -lc $'\u00e9'`, `/bin/bash -lc $'\u00e9'`},
		{`/bin/bash -lc $'\cA'`, `/bin/bash -lc $'\cA'`},
		{`/bin/bash -lc $'\xZZ'`, `/bin/bash -lc $'\xZZ'`},
		{`/bin/bash -lc $'\777'`, `/bin/bash -lc $'\777'`},
		{`/bin/bash -lc $'abc`, `/bin/bash -lc $'abc`},
		{`/bin/bash -c 'echo $x'`, `echo $x`},
		{`/bin/zsh -lc 'x`, `/bin/zsh -lc 'x`},
		{`'x`, `'x`},
		{`/bin/zsh -lc "x`, `/bin/zsh -lc "x`},
	}
	// A heredoc quoted ANSI-C style by the daemon is the rollout's argv script.
	heredoc := "cat <<EOF\nhi\nEOF"
	if got, want := scriptOfString(`/bin/bash -lc $'cat <<EOF\nhi\nEOF'`), scriptOfArgv([]string{"/bin/bash", "-lc", heredoc}); got != want || got != heredoc {
		t.Errorf("ANSI-C heredoc: scriptOfString = %q, scriptOfArgv = %q, want both %q", got, want, heredoc)
	}
	for _, c := range stringCases {
		if got := scriptOfString(c.cmd); got != c.want {
			t.Errorf("scriptOfString(%q) = %q, want %q", c.cmd, got, c.want)
		}
	}
}

func intp(n int) *int { return &n }

// kindCase is one item in both encodings and the Item both decode to, without
// the per-encoding fields (times, LineTS) and with TurnID "t1".
type kindCase struct {
	name    string
	rollout string // payload.item; empty when the case has no rollout form
	daemon  string // params.item of an item/completed; empty when it has no daemon form
	want    Item
}

var kindCases = []kindCase{
	{
		name:    "user",
		rollout: `{"type":"UserMessage","id":"u1","client_id":"c","content":[{"type":"text","text":"a","text_elements":[]},{"type":"image","image_url":"data:x"},{"type":"mention","name":"m","path":"/p","text":"junk"},{"type":"text","text":"b"}]}`,
		daemon:  `{"type":"userMessage","id":"u1","clientId":"c","content":[{"type":"text","text":"a","text_elements":[]},{"type":"image","url":"data:x"},{"type":"skill","name":"s","path":"/p","text":"junk"},{"type":"text","text":"b"}]}`,
		want:    Item{ID: "u1", Kind: KindUser, Status: StatusCompleted, Parts: []string{"a", "b"}},
	},
	{
		name:    "agent",
		rollout: `{"type":"AgentMessage","id":"m1","content":[{"type":"Text","text":"Hel"},{"type":"Text","text":"lo"}],"phase":"final_answer"}`,
		daemon:  `{"type":"agentMessage","id":"m1","text":"Hello","phase":"final_answer","memoryCitation":null}`,
		want:    Item{ID: "m1", Kind: KindAgent, Status: StatusCompleted, Text: "Hello"},
	},
	{
		name:    "command",
		rollout: `{"type":"CommandExecution","id":"e1","command":["/bin/zsh","-lc","ls -la"],"cwd":"file:///work/my%20repo","status":"failed","aggregated_output":"out\n","exit_code":1,"parsed_cmd":[]}`,
		daemon:  `{"type":"commandExecution","id":"e1","command":"/bin/zsh -lc 'ls -la'","cwd":"/work/my repo","status":"failed","aggregatedOutput":"out\n","exitCode":1,"commandActions":[]}`,
		want:    Item{ID: "e1", Kind: KindCommand, Status: StatusFailed, Command: "ls -la", CWD: "/work/my repo", Output: "out\n", ExitCode: intp(1)},
	},
	{
		name:    "fileChange add and update with a move",
		rollout: `{"type":"FileChange","id":"f1","changes":{"/w/b.go":{"type":"update","unified_diff":"@@ -1 +1 @@\n-a\n+b\n","move_path":"/w/c.go"},"/w/a.txt":{"type":"add","content":"hi\n"}},"status":"completed","stdout":"Success.","stderr":""}`,
		daemon:  `{"type":"fileChange","id":"f1","changes":[{"path":"/w/b.go","kind":{"type":"update","move_path":"/w/c.go"},"diff":"@@ -1 +1 @@\n-a\n+b\n"},{"path":"/w/a.txt","kind":{"type":"add"},"diff":"hi\n"}],"status":"completed"}`,
		want: Item{ID: "f1", Kind: KindFileChange, Status: StatusCompleted, Changes: []Change{
			{Path: "/w/a.txt", Kind: "add", Diff: "hi\n"},
			{Path: "/w/b.go", Kind: "update", MovePath: "/w/c.go", Diff: "@@ -1 +1 @@\n-a\n+b\n"},
		}},
	},
	{
		name:    "fileChange update in place",
		rollout: `{"type":"FileChange","id":"f2","changes":{"/w/b.go":{"type":"update","unified_diff":"@@ -1 +1 @@\n-a\n+b\n","move_path":null}},"status":"completed"}`,
		daemon:  `{"type":"fileChange","id":"f2","changes":[{"path":"/w/b.go","kind":{"type":"update","move_path":null},"diff":"@@ -1 +1 @@\n-a\n+b\n"}],"status":"completed"}`,
		want: Item{ID: "f2", Kind: KindFileChange, Status: StatusCompleted, Changes: []Change{
			{Path: "/w/b.go", Kind: "update", Diff: "@@ -1 +1 @@\n-a\n+b\n"},
		}},
	},
	{
		name:    "mcpTool success",
		rollout: `{"type":"McpToolCall","id":"c1","server":"node_repl","tool":"js","arguments":{ "code" : "2+2" },"status":"completed","result":{"content":[{"type":"text","text":"4"},{"type":"image","data":"x"},{"type":"text","text":"!"}],"isError":false},"error":null}`,
		daemon:  `{"type":"mcpToolCall","id":"c1","server":"node_repl","tool":"js","arguments":{"code":"2+2"},"status":"completed","result":{"content":[{"type":"text","text":"4"},{"type":"image","data":"x"},{"type":"text","text":"!"}]},"error":null}`,
		want:    Item{ID: "c1", Kind: KindMCPTool, Status: StatusCompleted, Server: "node_repl", Tool: "js", Arguments: json.RawMessage(`{"code":"2+2"}`), Output: "4!"},
	},
	{
		name:    "mcpTool failed with an error",
		rollout: `{"type":"McpToolCall","id":"c2","server":"s","tool":"t","arguments":null,"status":"failed","error":{"message":"denied"}}`,
		daemon:  `{"type":"mcpToolCall","id":"c2","server":"s","tool":"t","arguments":null,"status":"failed","result":null,"error":{"message":"denied"}}`,
		want:    Item{ID: "c2", Kind: KindMCPTool, Status: StatusFailed, Server: "s", Tool: "t", Error: "denied"},
	},
	{
		name:    "mcpTool result isError without a message",
		rollout: `{"type":"McpToolCall","id":"c3","server":"s","tool":"t","status":"completed","result":{"content":[{"type":"text","text":"boom"}],"isError":true}}`,
		want:    Item{ID: "c3", Kind: KindMCPTool, Status: StatusCompleted, Server: "s", Tool: "t", Output: "boom", Error: "tool reported an error"},
	},
	{
		name:    "webSearch",
		rollout: `{"type":"Extension","kind":"web.search","id":"w1","query":"codex changelog","action":{"type":"search","query":"codex changelog","queries":null},"results":[]}`,
		daemon:  `{"type":"webSearch","id":"w1","query":"codex changelog","action":{"type":"search","query":"codex changelog","queries":null},"results":[]}`,
		want:    Item{ID: "w1", Kind: KindWebSearch, Status: StatusCompleted, Query: "codex changelog"},
	},
	{
		name:    "imageView",
		rollout: `{"type":"ImageView","id":"i1","path":"/w/shot.png"}`,
		daemon:  `{"type":"imageView","id":"i1","path":"/w/shot.png"}`,
		want:    Item{ID: "i1", Kind: KindImageView, Status: StatusCompleted, Path: "/w/shot.png"},
	},
	{
		name:    "collabTool spawn",
		rollout: `{"type":"CollabAgentToolCall","id":"k1","tool":"spawnAgent","status":"completed","prompt":"look into it","receiver_thread_ids":["th2","th3"]}`,
		daemon:  `{"type":"collabAgentToolCall","id":"k1","tool":"spawnAgent","status":"completed","prompt":"look into it","receiverThreadIds":["th2","th3"]}`,
		want:    Item{ID: "k1", Kind: KindCollabTool, Status: StatusCompleted, Tool: "spawnAgent", Prompt: "look into it", ReceiverThreadIDs: []string{"th2", "th3"}},
	},
	{
		name:    "subAgentActivity",
		rollout: `{"type":"SubAgentActivity","id":"s1","kind":"started","agent_thread_id":"th2","agent_path":"/root/explorer"}`,
		daemon:  `{"type":"subAgentActivity","id":"s1","kind":"started","agentThreadId":"th2","agentPath":"/root/explorer"}`,
		want:    Item{ID: "s1", Kind: KindSubAgent, Status: StatusCompleted, AgentThreadID: "th2", AgentPath: "/root/explorer", ActivityKind: "started"},
	},
	{
		name:    "reasoning",
		rollout: `{"type":"Reasoning","id":"r1","summary_text":[],"raw_content":[]}`,
		daemon:  `{"type":"reasoning","id":"r1","summary":[],"content":["thinking"]}`,
		want:    Item{ID: "r1", Kind: KindOther, Status: StatusCompleted},
	},
	{
		name:    "other extension",
		rollout: `{"type":"Extension","kind":"clock.sleep","id":"x1","seconds":3}`,
		want:    Item{ID: "x1", Kind: KindOther, Status: StatusCompleted},
	},
	{
		name:   "other daemon type",
		daemon: `{"type":"contextCompaction","id":"x2"}`,
		want:   Item{ID: "x2", Kind: KindOther, Status: StatusCompleted},
	},
}

func rolloutLineWith(item string) string {
	return `{"timestamp":"2026-10-24T22:28:38.745Z","type":"event_msg","payload":{"type":"item_completed","thread_id":"th1","turn_id":"t1","item":` +
		item + `,"started_at_ms":100,"completed_at_ms":200}}`
}

func TestDecodeRolloutKinds(t *testing.T) {
	t.Parallel()
	for _, c := range kindCases {
		if c.rollout == "" {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, ok := DecodeRollout([]byte(rolloutLineWith(c.rollout)))
			if !ok {
				t.Fatal("DecodeRollout: ok = false")
			}
			want := c.want
			want.TurnID = "t1"
			want.StartedAtMs = 100
			want.CompletedAtMs = 200
			want.LineTS = "2026-10-24T22:28:38.745Z"
			assertItem(t, got, want)
		})
	}
}

func TestDecodeDaemonKinds(t *testing.T) {
	t.Parallel()
	for _, c := range kindCases {
		if c.daemon == "" {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			params := `{"item":` + c.daemon + `,"threadId":"th1","turnId":"t1","completedAtMs":200}`
			got, ok := DecodeDaemon("item/completed", []byte(params))
			if !ok {
				t.Fatal("DecodeDaemon: ok = false")
			}
			want := c.want
			want.TurnID = "t1"
			want.CompletedAtMs = 200
			assertItem(t, got, want)
		})
	}
}

func TestDecodeRolloutSkips(t *testing.T) {
	t.Parallel()
	for name, line := range map[string]string{
		"not JSON":          `{"timestamp":`,
		"response_item":     `{"timestamp":"x","type":"response_item","payload":{"type":"message","role":"user","content":[]}}`,
		"other event":       `{"timestamp":"x","type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}`,
		"item not readable": `{"timestamp":"x","type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"CommandExecution","id":"e1","command":7}}}`,
		"no item":           `{"timestamp":"x","type":"event_msg","payload":{"type":"item_completed","turn_id":"t1"}}`,
		"item without id":   `{"timestamp":"x","type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"AgentMessage","content":[{"type":"Text","text":"hi"}]}}}`,
	} {
		if _, ok := DecodeRollout([]byte(line)); ok {
			t.Errorf("%s: DecodeRollout ok = true, want false", name)
		}
	}
}

func TestDecodeDaemonSkipsAndStarted(t *testing.T) {
	t.Parallel()
	if _, ok := DecodeDaemon("item/agentMessage/delta", []byte(`{"itemId":"m1","delta":"Hi","threadId":"th1","turnId":"t1"}`)); ok {
		t.Error("item/agentMessage/delta: ok = true, want false")
	}
	if _, ok := DecodeDaemon("item/completed", []byte(`{"item":`)); ok {
		t.Error("truncated params: ok = true, want false")
	}
	if _, ok := DecodeDaemon("item/completed", []byte(`{"item":{"type":"agentMessage","id":"","text":"hi"},"turnId":"t1"}`)); ok {
		t.Error("item without id: ok = true, want false")
	}

	got, ok := DecodeDaemon("item/started", []byte(`{"item":{"type":"agentMessage","id":"m1","text":""},"threadId":"th1","turnId":"t1","startedAtMs":100}`))
	if !ok {
		t.Fatal("item/started agentMessage: ok = false")
	}
	assertItem(t, got, Item{ID: "m1", TurnID: "t1", Kind: KindAgent, Status: StatusInProgress, StartedAtMs: 100})

	got, ok = DecodeDaemon("item/started", []byte(`{"item":{"type":"commandExecution","id":"e1","command":"/bin/zsh -lc 'ls'","cwd":"/w","status":"inProgress","aggregatedOutput":null,"exitCode":null},"threadId":"th1","turnId":"t1","startedAtMs":100}`))
	if !ok {
		t.Fatal("item/started commandExecution: ok = false")
	}
	assertItem(t, got, Item{ID: "e1", TurnID: "t1", Kind: KindCommand, Status: StatusInProgress, StartedAtMs: 100, Command: "ls", CWD: "/w"})
}

func TestFailed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		item Item
		want bool
	}{
		{Item{Status: StatusCompleted, ExitCode: intp(0)}, false},
		{Item{Status: StatusCompleted, ExitCode: intp(2)}, true},
		{Item{Status: StatusFailed}, true},
		{Item{Status: StatusDeclined}, true},
		{Item{Status: StatusCompleted, Error: "tool reported an error"}, true},
		{Item{Status: StatusInProgress}, false},
	}
	for _, c := range cases {
		if got := c.item.Failed(); got != c.want {
			t.Errorf("%+v.Failed() = %v, want %v", c.item, got, c.want)
		}
	}
}

// TestBothEncodingsDecodeEqual decodes the same real session from its rollout
// and from the app-server's notifications, and checks every item present in
// both decodes to the same Item, times aside.
func TestBothEncodingsDecodeEqual(t *testing.T) {
	t.Parallel()

	daemon := map[string]Item{}
	for _, line := range fixtureLines(t, "daemon.jsonl") {
		var msg struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(line, &msg); err != nil {
			t.Fatalf("daemon.jsonl: %v", err)
		}
		if msg.Method != "item/completed" {
			continue
		}
		it, ok := DecodeDaemon(msg.Method, msg.Params)
		if !ok {
			t.Fatalf("daemon.jsonl: cannot decode %s", line)
		}
		daemon[it.ID] = it
	}

	pairs := 0
	kinds := map[Kind]bool{}
	for _, line := range fixtureLines(t, "rollout.jsonl") {
		r, ok := DecodeRollout(line)
		if !ok {
			if bytes.Contains(line, []byte(`"type":"item_completed"`)) {
				t.Errorf("rollout.jsonl: cannot decode item_completed line %s", line)
			}
			continue
		}
		d, found := daemon[r.ID]
		if !found {
			continue
		}
		pairs++
		kinds[r.Kind] = true
		r.StartedAtMs, r.CompletedAtMs, r.LineTS = 0, 0, ""
		d.StartedAtMs, d.CompletedAtMs = 0, 0
		if !reflect.DeepEqual(r, d) {
			t.Errorf("item %s (%s): rollout and daemon differ:%s", r.ID, r.Kind, diffItems(r, d))
		}
	}
	// The rollout has 28 items; the daemon capture lacks the first turn's user message.
	if pairs != 27 {
		t.Errorf("compared %d items present in both encodings, want 27", pairs)
	}
	for _, k := range []Kind{KindUser, KindAgent, KindCommand, KindFileChange, KindWebSearch, KindMCPTool} {
		if !kinds[k] {
			t.Errorf("no %s item compared across encodings", k)
		}
	}
}

func fixtureLines(t *testing.T, name string) [][]byte {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 64<<20)
	for sc.Scan() {
		if len(sc.Bytes()) > 0 {
			lines = append(lines, append([]byte(nil), sc.Bytes()...))
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

func assertItem(t *testing.T, got, want Item) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("decoded item differs (got / want):%s", diffItems(got, want))
	}
}

// diffItems lists the fields in which a and b differ.
func diffItems(a, b Item) string {
	var out strings.Builder
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	for i := range va.NumField() {
		fa, fb := va.Field(i).Interface(), vb.Field(i).Interface()
		if reflect.DeepEqual(fa, fb) {
			continue
		}
		fmt.Fprintf(&out, "\n  %s: %s / %s", va.Type().Field(i).Name, show(fa), show(fb))
	}
	return out.String()
}

func show(v any) string {
	switch v := v.(type) {
	case *int:
		if v == nil {
			return "nil"
		}
		return strconv.Itoa(*v)
	case json.RawMessage:
		return string(v)
	}
	return fmt.Sprintf("%#v", v)
}
