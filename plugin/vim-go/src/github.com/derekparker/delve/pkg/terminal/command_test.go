package terminal

import (
	"flag"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/derekparker/delve/pkg/config"
	"github.com/derekparker/delve/pkg/proc/test"
	"github.com/derekparker/delve/service"
	"github.com/derekparker/delve/service/api"
	"github.com/derekparker/delve/service/rpc2"
	"github.com/derekparker/delve/service/rpccommon"
)

var testBackend string

func TestMain(m *testing.M) {
	flag.StringVar(&testBackend, "backend", "", "selects backend")
	flag.Parse()
	if testBackend == "" {
		testBackend = os.Getenv("PROCTEST")
		if testBackend == "" {
			testBackend = "native"
		}
	}
	os.Exit(m.Run())
}

type FakeTerminal struct {
	*Term
	t testing.TB
}

const logCommandOutput = false

func (ft *FakeTerminal) Exec(cmdstr string) (outstr string, err error) {
	outfh, err := ioutil.TempFile("", "cmdtestout")
	if err != nil {
		ft.t.Fatalf("could not create temporary file: %v", err)
	}

	stdout, stderr, termstdout := os.Stdout, os.Stderr, ft.Term.stdout
	os.Stdout, os.Stderr, ft.Term.stdout = outfh, outfh, outfh
	defer func() {
		os.Stdout, os.Stderr, ft.Term.stdout = stdout, stderr, termstdout
		outfh.Close()
		outbs, err1 := ioutil.ReadFile(outfh.Name())
		if err1 != nil {
			ft.t.Fatalf("could not read temporary output file: %v", err)
		}
		outstr = string(outbs)
		if logCommandOutput {
			ft.t.Logf("command %q -> %q", cmdstr, outstr)
		}
		os.Remove(outfh.Name())
	}()
	err = ft.cmds.Call(cmdstr, ft.Term)
	return
}

func (ft *FakeTerminal) MustExec(cmdstr string) string {
	outstr, err := ft.Exec(cmdstr)
	if err != nil {
		ft.t.Fatalf("Error executing <%s>: %v", cmdstr, err)
	}
	return outstr
}

func (ft *FakeTerminal) AssertExec(cmdstr, tgt string) {
	out := ft.MustExec(cmdstr)
	if out != tgt {
		ft.t.Fatalf("Error executing %q, expected %q got %q", cmdstr, tgt, out)
	}
}

func (ft *FakeTerminal) AssertExecError(cmdstr, tgterr string) {
	_, err := ft.Exec(cmdstr)
	if err == nil {
		ft.t.Fatalf("Expected error executing %q", cmdstr)
	}
	if err.Error() != tgterr {
		ft.t.Fatalf("Expected error %q executing %q, got error %q", tgterr, cmdstr, err.Error())
	}
}

func withTestTerminal(name string, t testing.TB, fn func(*FakeTerminal)) {
	if testBackend == "rr" {
		test.MustHaveRecordingAllowed(t)
	}
	os.Setenv("TERM", "dumb")
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("couldn't start listener: %s\n", err)
	}
	defer listener.Close()
	server := rpccommon.NewServer(&service.Config{
		Listener:    listener,
		ProcessArgs: []string{test.BuildFixture(name, 0).Path},
		Backend:     testBackend,
	}, false)
	if err := server.Run(); err != nil {
		t.Fatal(err)
	}
	client := rpc2.NewClient(listener.Addr().String())
	defer func() {
		dir, _ := client.TraceDirectory()
		client.Detach(true)
		if dir != "" {
			test.SafeRemoveAll(dir)
		}
	}()

	ft := &FakeTerminal{
		t:    t,
		Term: New(client, &config.Config{}),
	}
	fn(ft)
}

func TestCommandDefault(t *testing.T) {
	var (
		cmds = Commands{}
		cmd  = cmds.Find("non-existant-command", noPrefix)
	)

	err := cmd(nil, callContext{}, "")
	if err == nil {
		t.Fatal("cmd() did not default")
	}

	if err.Error() != "command not available" {
		t.Fatal("wrong command output")
	}
}

func TestCommandReplay(t *testing.T) {
	cmds := DebugCommands(nil)
	cmds.Register("foo", func(t *Term, ctx callContext, args string) error { return fmt.Errorf("registered command") }, "foo command")
	cmd := cmds.Find("foo", noPrefix)

	err := cmd(nil, callContext{}, "")
	if err.Error() != "registered command" {
		t.Fatal("wrong command output")
	}

	cmd = cmds.Find("", noPrefix)
	err = cmd(nil, callContext{}, "")
	if err.Error() != "registered command" {
		t.Fatal("wrong command output")
	}
}

func TestCommandReplayWithoutPreviousCommand(t *testing.T) {
	var (
		cmds = DebugCommands(nil)
		cmd  = cmds.Find("", noPrefix)
		err  = cmd(nil, callContext{}, "")
	)

	if err != nil {
		t.Error("Null command not returned", err)
	}
}

func TestCommandThread(t *testing.T) {
	var (
		cmds = DebugCommands(nil)
		cmd  = cmds.Find("thread", noPrefix)
	)

	err := cmd(nil, callContext{}, "")
	if err == nil {
		t.Fatal("thread terminal command did not default")
	}

	if err.Error() != "you must specify a thread" {
		t.Fatal("wrong command output: ", err.Error())
	}
}

func TestExecuteFile(t *testing.T) {
	breakCount := 0
	traceCount := 0
	c := &Commands{
		client: nil,
		cmds: []command{
			{aliases: []string{"trace"}, cmdFn: func(t *Term, ctx callContext, args string) error {
				traceCount++
				return nil
			}},
			{aliases: []string{"break"}, cmdFn: func(t *Term, ctx callContext, args string) error {
				breakCount++
				return nil
			}},
		},
	}

	fixturesDir := test.FindFixturesDir()
	err := c.executeFile(nil, filepath.Join(fixturesDir, "bpfile"))
	if err != nil {
		t.Fatalf("executeFile: %v", err)
	}

	if breakCount != 1 || traceCount != 1 {
		t.Fatalf("Wrong counts break: %d trace: %d\n", breakCount, traceCount)
	}
}

func TestIssue354(t *testing.T) {
	printStack([]api.Stackframe{}, "", false)
	printStack([]api.Stackframe{{api.Location{PC: 0, File: "irrelevant.go", Line: 10, Function: nil}, nil, nil, 0, 0, ""}}, "", false)
}

func TestIssue411(t *testing.T) {
	test.AllowRecording(t)
	withTestTerminal("math", t, func(term *FakeTerminal) {
		term.MustExec("break math.go:8")
		term.MustExec("trace math.go:9")
		term.MustExec("continue")
		out := term.MustExec("next")
		if !strings.HasPrefix(out, "> main.main()") {
			t.Fatalf("Wrong output for next: <%s>", out)
		}
	})
}

func TestScopePrefix(t *testing.T) {
	const goroutinesLinePrefix = "  Goroutine "
	const goroutinesCurLinePrefix = "* Goroutine "
	test.AllowRecording(t)
	withTestTerminal("goroutinestackprog", t, func(term *FakeTerminal) {
		term.MustExec("b stacktraceme")
		term.MustExec("continue")

		goroutinesOut := strings.Split(term.MustExec("goroutines"), "\n")
		agoroutines := []int{}
		nonagoroutines := []int{}
		curgid := -1

		for _, line := range goroutinesOut {
			iscur := strings.HasPrefix(line, goroutinesCurLinePrefix)
			if !iscur && !strings.HasPrefix(line, goroutinesLinePrefix) {
				continue
			}

			dash := strings.Index(line, " - ")
			if dash < 0 {
				continue
			}

			gid, err := strconv.Atoi(line[len(goroutinesLinePrefix):dash])
			if err != nil {
				continue
			}

			if iscur {
				curgid = gid
			}

			if idx := strings.Index(line, " main.agoroutine "); idx < 0 {
				nonagoroutines = append(nonagoroutines, gid)
				continue
			}

			agoroutines = append(agoroutines, gid)
		}

		if len(agoroutines) > 10 {
			t.Fatalf("Output of goroutines did not have 10 goroutines stopped on main.agoroutine (%d found): %q", len(agoroutines), goroutinesOut)
		}

		if len(agoroutines) < 10 {
			extraAgoroutines := 0
			for _, gid := range nonagoroutines {
				stackOut := strings.Split(term.MustExec(fmt.Sprintf("goroutine %d stack", gid)), "\n")
				for _, line := range stackOut {
					if strings.HasSuffix(line, " main.agoroutine") {
						extraAgoroutines++
						break
					}
				}
			}
			if len(agoroutines)+extraAgoroutines < 10 {
				t.Fatalf("Output of goroutines did not have 10 goroutines stopped on main.agoroutine (%d+%d found): %q", len(agoroutines), extraAgoroutines, goroutinesOut)
			}
		}

		if curgid < 0 {
			t.Fatalf("Could not find current goroutine in output of goroutines: %q", goroutinesOut)
		}

		seen := make([]bool, 10)
		for _, gid := range agoroutines {
			stackOut := strings.Split(term.MustExec(fmt.Sprintf("goroutine %d stack", gid)), "\n")
			fid := -1
			for _, line := range stackOut {
				space := strings.Index(line, " ")
				if space < 0 {
					continue
				}
				curfid, err := strconv.Atoi(line[:space])
				if err != nil {
					continue
				}

				if idx := strings.Index(line, " main.agoroutine"); idx >= 0 {
					fid = curfid
					break
				}
			}
			if fid < 0 {
				t.Fatalf("Could not find frame for goroutine %d: %v", gid, stackOut)
			}
			term.AssertExec(fmt.Sprintf("goroutine     %d    frame     %d     locals", gid, fid), "(no locals)\n")
			argsOut := strings.Split(term.MustExec(fmt.Sprintf("goroutine %d frame %d args", gid, fid)), "\n")
			if len(argsOut) != 4 || argsOut[3] != "" {
				t.Fatalf("Wrong number of arguments in goroutine %d frame %d: %v", gid, fid, argsOut)
			}
			out := term.MustExec(fmt.Sprintf("goroutine %d frame %d p i", gid, fid))
			ival, err := strconv.Atoi(out[:len(out)-1])
			if err != nil {
				t.Fatalf("could not parse value %q of i for goroutine %d frame %d: %v", out, gid, fid, err)
			}
			seen[ival] = true
		}

		for i := range seen {
			if !seen[i] {
				t.Fatalf("goroutine %d not found", i)
			}
		}

		term.MustExec("c")

		term.AssertExecError("frame", "not enough arguments")
		term.AssertExecError(fmt.Sprintf("goroutine %d frame 10 locals", curgid), fmt.Sprintf("Frame 10 does not exist in goroutine %d", curgid))
		term.AssertExecError("goroutine 9000 locals", "Unknown goroutine 9000")

		term.AssertExecError("print n", "could not find symbol value for n")
		term.AssertExec("frame 1 print n", "3\n")
		term.AssertExec("frame 2 print n", "2\n")
		term.AssertExec("frame 3 print n", "1\n")
		term.AssertExec("frame 4 print n", "0\n")
		term.AssertExecError("frame 5 print n", "could not find symbol value for n")

		term.MustExec("frame 2")
		term.AssertExec("print n", "2\n")
		term.MustExec("frame 4")
		term.AssertExec("print n", "0\n")
		term.MustExec("down")
		term.AssertExec("print n", "1\n")
		term.MustExec("down 2")
		term.AssertExec("print n", "3\n")
		term.AssertExecError("down 2", "Invalid frame -1")
		term.AssertExec("print n", "3\n")
		term.MustExec("up 2")
		term.AssertExec("print n", "1\n")
		term.AssertExecError("up 100", "Invalid frame 103")
		term.AssertExec("print n", "1\n")

		term.MustExec("step")
		term.AssertExecError("print n", "could not find symbol value for n")
		term.MustExec("frame 2")
		term.AssertExec("print n", "2\n")
	})
}

func TestOnPrefix(t *testing.T) {
	const prefix = "\ti: "
	test.AllowRecording(t)
	withTestTerminal("goroutinestackprog", t, func(term *FakeTerminal) {
		term.MustExec("b agobp main.agoroutine")
		term.MustExec("on agobp print i")

		seen := make([]bool, 10)

		for {
			outstr, err := term.Exec("continue")
			if err != nil {
				if !strings.Contains(err.Error(), "exited") {
					t.Fatalf("Unexpected error executing 'continue': %v", err)
				}
				break
			}
			out := strings.Split(outstr, "\n")

			for i := range out {
				if !strings.HasPrefix(out[i], "\ti: ") {
					continue
				}
				id, err := strconv.Atoi(out[i][len(prefix):])
				if err != nil {
					continue
				}
				if seen[id] {
					t.Fatalf("Goroutine %d seen twice\n", id)
				}
				seen[id] = true
			}
		}

		for i := range seen {
			if !seen[i] {
				t.Fatalf("Goroutine %d not seen\n", i)
			}
		}
	})
}

func TestNoVars(t *testing.T) {
	test.AllowRecording(t)
	withTestTerminal("locationsUpperCase", t, func(term *FakeTerminal) {
		term.MustExec("b main.main")
		term.MustExec("continue")
		term.AssertExec("args", "(no args)\n")
		term.AssertExec("locals", "(no locals)\n")
		term.AssertExec("vars filterThatMatchesNothing", "(no vars)\n")
	})
}

func TestOnPrefixLocals(t *testing.T) {
	const prefix = "\ti: "
	test.AllowRecording(t)
	withTestTerminal("goroutinestackprog", t, func(term *FakeTerminal) {
		term.MustExec("b agobp main.agoroutine")
		term.MustExec("on agobp args -v")

		seen := make([]bool, 10)

		for {
			outstr, err := term.Exec("continue")
			if err != nil {
				if !strings.Contains(err.Error(), "exited") {
					t.Fatalf("Unexpected error executing 'continue': %v", err)
				}
				break
			}
			out := strings.Split(outstr, "\n")

			for i := range out {
				if !strings.HasPrefix(out[i], "\ti: ") {
					continue
				}
				id, err := strconv.Atoi(out[i][len(prefix):])
				if err != nil {
					continue
				}
				if seen[id] {
					t.Fatalf("Goroutine %d seen twice\n", id)
				}
				seen[id] = true
			}
		}

		for i := range seen {
			if !seen[i] {
				t.Fatalf("Goroutine %d not seen\n", i)
			}
		}
	})
}

func countOccurrences(s string, needle string) int {
	count := 0
	for {
		idx := strings.Index(s, needle)
		if idx < 0 {
			break
		}
		count++
		s = s[idx+len(needle):]
	}
	return count
}

func TestIssue387(t *testing.T) {
	// a breakpoint triggering during a 'next' operation will interrupt it
	test.AllowRecording(t)
	withTestTerminal("issue387", t, func(term *FakeTerminal) {
		breakpointHitCount := 0
		term.MustExec("break dostuff")
		for {
			outstr, err := term.Exec("continue")
			breakpointHitCount += countOccurrences(outstr, "issue387.go:8")
			t.Log(outstr)
			if err != nil {
				if !strings.Contains(err.Error(), "exited") {
					t.Fatalf("Unexpected error executing 'continue': %v", err)
				}
				break
			}

			pos := 9

			for {
				outstr = term.MustExec("next")
				breakpointHitCount += countOccurrences(outstr, "issue387.go:8")
				t.Log(outstr)
				if countOccurrences(outstr, fmt.Sprintf("issue387.go:%d", pos)) == 0 {
					t.Fatalf("did not continue to expected position %d", pos)
				}
				pos++
				if pos >= 11 {
					break
				}
			}
		}
		if breakpointHitCount != 10 {
			t.Fatalf("Breakpoint hit wrong number of times, expected 10 got %d", breakpointHitCount)
		}
	})
}

func listIsAt(t *testing.T, term *FakeTerminal, listcmd string, cur, start, end int) {
	outstr := term.MustExec(listcmd)
	lines := strings.Split(outstr, "\n")

	t.Logf("%q: %q", listcmd, outstr)

	if !strings.Contains(lines[0], fmt.Sprintf(":%d", cur)) {
		t.Fatalf("Could not find current line number in first output line: %q", lines[0])
	}

	re := regexp.MustCompile(`(=>)?\s+(\d+):`)

	outStart, outEnd := 0, 0

	for _, line := range lines[1:] {
		if line == "" {
			continue
		}
		v := re.FindStringSubmatch(line)
		if len(v) != 3 {
			continue
		}
		curline, _ := strconv.Atoi(v[2])
		if v[1] == "=>" {
			if cur != curline {
				t.Fatalf("Wrong current line, got %d expected %d", curline, cur)
			}
		}
		if outStart == 0 {
			outStart = curline
		}
		outEnd = curline
	}

	if start != -1 || end != -1 {
		if outStart != start || outEnd != end {
			t.Fatalf("Wrong output range, got %d:%d expected %d:%d", outStart, outEnd, start, end)
		}
	}
}

func TestListCmd(t *testing.T) {
	withTestTerminal("testvariables", t, func(term *FakeTerminal) {
		term.MustExec("continue")
		term.MustExec("continue")
		listIsAt(t, term, "list", 24, 19, 29)
		listIsAt(t, term, "list 69", 69, 64, 70)
		listIsAt(t, term, "frame 1 list", 62, 57, 67)
		listIsAt(t, term, "frame 1 list 69", 69, 64, 70)
		_, err := term.Exec("frame 50 list")
		if err == nil {
			t.Fatalf("Expected error requesting 50th frame")
		}
	})
}

func TestReverseContinue(t *testing.T) {
	test.AllowRecording(t)
	if testBackend != "rr" {
		return
	}
	withTestTerminal("continuetestprog", t, func(term *FakeTerminal) {
		term.MustExec("break main.main")
		term.MustExec("break main.sayhi")
		listIsAt(t, term, "continue", 16, -1, -1)
		listIsAt(t, term, "continue", 12, -1, -1)
		listIsAt(t, term, "rewind", 16, -1, -1)
	})
}

func TestCheckpoints(t *testing.T) {
	test.AllowRecording(t)
	if testBackend != "rr" {
		return
	}
	withTestTerminal("continuetestprog", t, func(term *FakeTerminal) {
		term.MustExec("break main.main")
		listIsAt(t, term, "continue", 16, -1, -1)
		term.MustExec("checkpoint")
		term.MustExec("checkpoints")
		listIsAt(t, term, "next", 17, -1, -1)
		listIsAt(t, term, "next", 18, -1, -1)
		listIsAt(t, term, "restart c1", 16, -1, -1)
	})
}

func TestRestart(t *testing.T) {
	withTestTerminal("restartargs", t, func(term *FakeTerminal) {
		term.MustExec("break main.printArgs")
		term.MustExec("continue")
		if out := term.MustExec("print main.args"); !strings.Contains(out, ", []") {
			t.Fatalf("wrong args: %q", out)
		}
		// Reset the arg list
		term.MustExec("restart hello")
		term.MustExec("continue")
		if out := term.MustExec("print main.args"); !strings.Contains(out, ", [\"hello\"]") {
			t.Fatalf("wrong args: %q ", out)
		}
		// Restart w/o arg should retain the current args.
		term.MustExec("restart")
		term.MustExec("continue")
		if out := term.MustExec("print main.args"); !strings.Contains(out, ", [\"hello\"]") {
			t.Fatalf("wrong args: %q ", out)
		}
		// Empty arg list
		term.MustExec("restart -noargs")
		term.MustExec("continue")
		if out := term.MustExec("print main.args"); !strings.Contains(out, ", []") {
			t.Fatalf("wrong args: %q ", out)
		}
	})
}

func TestIssue827(t *testing.T) {
	// switching goroutines when the current thread isn't running any goroutine
	// causes nil pointer dereference.
	withTestTerminal("notify-v2", t, func(term *FakeTerminal) {
		go func() {
			time.Sleep(1 * time.Second)
			http.Get("http://127.0.0.1:8888/test")
			time.Sleep(1 * time.Second)
			term.client.Halt()
		}()
		term.MustExec("continue")
		term.MustExec("goroutine 1")
	})
}

func findCmdName(c *Commands, cmdstr string, prefix cmdPrefix) string {
	for _, v := range c.cmds {
		if v.match(cmdstr) {
			if prefix != noPrefix && v.allowedPrefixes&prefix == 0 {
				continue
			}
			return v.aliases[0]
		}
	}
	return ""
}

func TestConfig(t *testing.T) {
	var term Term
	term.conf = &config.Config{}
	term.cmds = DebugCommands(nil)

	err := configureCmd(&term, callContext{}, "nonexistent-parameter 10")
	if err == nil {
		t.Fatalf("expected error executing configureCmd(nonexistent-parameter)")
	}

	err = configureCmd(&term, callContext{}, "max-string-len 10")
	if err != nil {
		t.Fatalf("error executing configureCmd(max-string-len): %v", err)
	}
	if term.conf.MaxStringLen == nil {
		t.Fatalf("expected MaxStringLen 10, got nil")
	}
	if *term.conf.MaxStringLen != 10 {
		t.Fatalf("expected MaxStringLen 10, got: %d", *term.conf.MaxStringLen)
	}

	err = configureCmd(&term, callContext{}, "substitute-path a b")
	if err != nil {
		t.Fatalf("error executing configureCmd(substitute-path a b): %v", err)
	}
	if len(term.conf.SubstitutePath) != 1 || (term.conf.SubstitutePath[0] != config.SubstitutePathRule{"a", "b"}) {
		t.Fatalf("unexpected SubstitutePathRules after insert %v", term.conf.SubstitutePath)
	}

	err = configureCmd(&term, callContext{}, "substitute-path a")
	if err != nil {
		t.Fatalf("error executing configureCmd(substitute-path a): %v", err)
	}
	if len(term.conf.SubstitutePath) != 0 {
		t.Fatalf("unexpected SubstitutePathRules after delete %v", term.conf.SubstitutePath)
	}

	err = configureCmd(&term, callContext{}, "alias print blah")
	if err != nil {
		t.Fatalf("error executing configureCmd(alias print blah): %v", err)
	}
	if len(term.conf.Aliases["print"]) != 1 {
		t.Fatalf("aliases not changed after configure command %v", term.conf.Aliases)
	}
	if findCmdName(term.cmds, "blah", noPrefix) != "print" {
		t.Fatalf("new alias not found")
	}

	err = configureCmd(&term, callContext{}, "alias blah")
	if err != nil {
		t.Fatalf("error executing configureCmd(alias blah): %v", err)
	}
	if len(term.conf.Aliases["print"]) != 0 {
		t.Fatalf("alias not removed after configure command %v", term.conf.Aliases)
	}
	if findCmdName(term.cmds, "blah", noPrefix) != "" {
		t.Fatalf("new alias found after delete")
	}
}

func TestDisassembleAutogenerated(t *testing.T) {
	// Executing the 'disassemble' command on autogenerated code should work correctly
	withTestTerminal("math", t, func(term *FakeTerminal) {
		term.MustExec("break main.init")
		term.MustExec("continue")
		out := term.MustExec("disassemble")
		if !strings.Contains(out, "TEXT main.init(SB) ") {
			t.Fatalf("output of disassemble wasn't for the main.init function %q", out)
		}
	})
}

func TestIssue1090(t *testing.T) {
	// Exit while executing 'next' should report the "Process exited" error
	// message instead of crashing.
	withTestTerminal("math", t, func(term *FakeTerminal) {
		term.MustExec("break main.main")
		term.MustExec("continue")
		for {
			_, err := term.Exec("next")
			if err != nil && strings.Contains(err.Error(), " has exited with status ") {
				break
			}
		}
	})
}

func TestPrintContextParkedGoroutine(t *testing.T) {
	withTestTerminal("goroutinestackprog", t, func(term *FakeTerminal) {
		term.MustExec("break stacktraceme")
		term.MustExec("continue")

		// pick a goroutine that isn't running on a thread
		gid := ""
		gout := strings.Split(term.MustExec("goroutines"), "\n")
		t.Logf("goroutines -> %q", gout)
		for _, gline := range gout {
			if !strings.Contains(gline, "thread ") && strings.Contains(gline, "agoroutine") {
				if dash := strings.Index(gline, " - "); dash > 0 {
					gid = gline[len("  Goroutine "):dash]
					break
				}
			}
		}

		t.Logf("picked %q", gid)
		term.MustExec(fmt.Sprintf("goroutine %s", gid))

		frameout := strings.Split(term.MustExec("frame 0"), "\n")
		t.Logf("frame 0 -> %q", frameout)
		if strings.Contains(frameout[0], "stacktraceme") {
			t.Fatal("bad output for `frame 0` command on a parked goorutine")
		}

		listout := strings.Split(term.MustExec("list"), "\n")
		t.Logf("list -> %q", listout)
		if strings.Contains(listout[0], "stacktraceme") {
			t.Fatal("bad output for list command on a parked goroutine")
		}
	})
}
