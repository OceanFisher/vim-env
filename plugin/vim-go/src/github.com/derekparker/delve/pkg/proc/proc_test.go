package proc_test

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/derekparker/delve/pkg/dwarf/frame"
	"github.com/derekparker/delve/pkg/goversion"
	"github.com/derekparker/delve/pkg/proc"
	"github.com/derekparker/delve/pkg/proc/gdbserial"
	"github.com/derekparker/delve/pkg/proc/native"
	protest "github.com/derekparker/delve/pkg/proc/test"
)

var normalLoadConfig = proc.LoadConfig{true, 1, 64, 64, -1}
var testBackend string

func init() {
	runtime.GOMAXPROCS(4)
	os.Setenv("GOMAXPROCS", "4")
}

func TestMain(m *testing.M) {
	flag.StringVar(&testBackend, "backend", "", "selects backend")
	flag.Parse()
	if testBackend == "" {
		testBackend = os.Getenv("PROCTEST")
		if testBackend == "" {
			testBackend = "native"
		}
	}
	os.Exit(protest.RunTestsWithFixtures(m))
}

func withTestProcess(name string, t testing.TB, fn func(p proc.Process, fixture protest.Fixture)) {
	withTestProcessArgs(name, t, ".", []string{}, 0, fn)
}

func withTestProcessArgs(name string, t testing.TB, wd string, args []string, buildFlags protest.BuildFlags, fn func(p proc.Process, fixture protest.Fixture)) {
	fixture := protest.BuildFixture(name, buildFlags)
	var p proc.Process
	var err error
	var tracedir string

	switch testBackend {
	case "native":
		p, err = native.Launch(append([]string{fixture.Path}, args...), wd)
	case "lldb":
		p, err = gdbserial.LLDBLaunch(append([]string{fixture.Path}, args...), wd)
	case "rr":
		protest.MustHaveRecordingAllowed(t)
		t.Log("recording")
		p, tracedir, err = gdbserial.RecordAndReplay(append([]string{fixture.Path}, args...), wd, true)
		t.Logf("replaying %q", tracedir)
	default:
		t.Fatal("unknown backend")
	}
	if err != nil {
		t.Fatal("Launch():", err)
	}

	defer func() {
		p.Detach(true)
		if tracedir != "" {
			protest.SafeRemoveAll(tracedir)
		}
	}()

	fn(p, fixture)
}

func getRegisters(p proc.Process, t *testing.T) proc.Registers {
	regs, err := p.CurrentThread().Registers(false)
	if err != nil {
		t.Fatal("Registers():", err)
	}

	return regs
}

func dataAtAddr(thread proc.MemoryReadWriter, addr uint64) ([]byte, error) {
	data := make([]byte, 1)
	_, err := thread.ReadMemory(data, uintptr(addr))
	return data, err
}

func assertNoError(err error, t testing.TB, s string) {
	if err != nil {
		_, file, line, _ := runtime.Caller(1)
		fname := filepath.Base(file)
		t.Fatalf("failed assertion at %s:%d: %s - %s\n", fname, line, s, err)
	}
}

func currentPC(p proc.Process, t *testing.T) uint64 {
	regs, err := p.CurrentThread().Registers(false)
	if err != nil {
		t.Fatal(err)
	}

	return regs.PC()
}

func currentLineNumber(p proc.Process, t *testing.T) (string, int) {
	pc := currentPC(p, t)
	f, l, _ := p.BinInfo().PCToLine(pc)
	return f, l
}

func assertLineNumber(p proc.Process, t *testing.T, lineno int, descr string) (string, int) {
	f, l := currentLineNumber(p, t)
	if l != lineno {
		_, callerFile, callerLine, _ := runtime.Caller(1)
		t.Fatalf("%s expected line :%d got %s:%d\n\tat %s:%d", descr, lineno, f, l, callerFile, callerLine)
	}
	return f, l
}

func TestExit(t *testing.T) {
	protest.AllowRecording(t)
	withTestProcess("continuetestprog", t, func(p proc.Process, fixture protest.Fixture) {
		err := proc.Continue(p)
		pe, ok := err.(proc.ProcessExitedError)
		if !ok {
			t.Fatalf("Continue() returned unexpected error type %s", err)
		}
		if pe.Status != 0 {
			t.Errorf("Unexpected error status: %d", pe.Status)
		}
		if pe.Pid != p.Pid() {
			t.Errorf("Unexpected process id: %d", pe.Pid)
		}
	})
}

func TestExitAfterContinue(t *testing.T) {
	protest.AllowRecording(t)
	withTestProcess("continuetestprog", t, func(p proc.Process, fixture protest.Fixture) {
		_, err := setFunctionBreakpoint(p, "main.sayhi")
		assertNoError(err, t, "setFunctionBreakpoint()")
		assertNoError(proc.Continue(p), t, "First Continue()")
		err = proc.Continue(p)
		pe, ok := err.(proc.ProcessExitedError)
		if !ok {
			t.Fatalf("Continue() returned unexpected error type %s", pe)
		}
		if pe.Status != 0 {
			t.Errorf("Unexpected error status: %d", pe.Status)
		}
		if pe.Pid != p.Pid() {
			t.Errorf("Unexpected process id: %d", pe.Pid)
		}
	})
}

func setFunctionBreakpoint(p proc.Process, fname string) (*proc.Breakpoint, error) {
	addr, err := proc.FindFunctionLocation(p, fname, true, 0)
	if err != nil {
		return nil, err
	}
	return p.SetBreakpoint(addr, proc.UserBreakpoint, nil)
}

func setFileBreakpoint(p proc.Process, t *testing.T, fixture protest.Fixture, lineno int) *proc.Breakpoint {
	addr, err := proc.FindFileLocation(p, fixture.Source, lineno)
	if err != nil {
		t.Fatalf("FindFileLocation: %v", err)
	}
	bp, err := p.SetBreakpoint(addr, proc.UserBreakpoint, nil)
	if err != nil {
		t.Fatalf("SetBreakpoint: %v", err)
	}
	return bp
}

func TestHalt(t *testing.T) {
	stopChan := make(chan interface{}, 1)
	withTestProcess("loopprog", t, func(p proc.Process, fixture protest.Fixture) {
		_, err := setFunctionBreakpoint(p, "main.loop")
		assertNoError(err, t, "SetBreakpoint")
		assertNoError(proc.Continue(p), t, "Continue")
		if p, ok := p.(*native.Process); ok {
			for _, th := range p.ThreadList() {
				_, err := th.Registers(false)
				assertNoError(err, t, "Registers")
			}
		}
		resumeChan := make(chan struct{}, 1)
		go func() {
			<-resumeChan
			time.Sleep(100 * time.Millisecond)
			stopChan <- p.RequestManualStop()
		}()
		p.ResumeNotify(resumeChan)
		assertNoError(proc.Continue(p), t, "Continue")
		retVal := <-stopChan

		if err, ok := retVal.(error); ok && err != nil {
			t.Fatal()
		}

		// Loop through threads and make sure they are all
		// actually stopped, err will not be nil if the process
		// is still running.
		if p, ok := p.(*native.Process); ok {
			for _, th := range p.ThreadList() {
				if th, ok := th.(*native.Thread); ok {
					if !th.Stopped() {
						t.Fatal("expected thread to be stopped, but was not")
					}
				}
				_, err := th.Registers(false)
				assertNoError(err, t, "Registers")
			}
		}
	})
}

func TestStep(t *testing.T) {
	protest.AllowRecording(t)
	withTestProcess("testprog", t, func(p proc.Process, fixture protest.Fixture) {
		helloworldaddr, err := proc.FindFunctionLocation(p, "main.helloworld", false, 0)
		assertNoError(err, t, "FindFunctionLocation")

		_, err = p.SetBreakpoint(helloworldaddr, proc.UserBreakpoint, nil)
		assertNoError(err, t, "SetBreakpoint()")
		assertNoError(proc.Continue(p), t, "Continue()")

		regs := getRegisters(p, t)
		rip := regs.PC()

		err = p.CurrentThread().StepInstruction()
		assertNoError(err, t, "Step()")

		regs = getRegisters(p, t)
		if rip >= regs.PC() {
			t.Errorf("Expected %#v to be greater than %#v", regs.PC(), rip)
		}
	})
}

func TestBreakpoint(t *testing.T) {
	protest.AllowRecording(t)
	withTestProcess("testprog", t, func(p proc.Process, fixture protest.Fixture) {
		helloworldaddr, err := proc.FindFunctionLocation(p, "main.helloworld", false, 0)
		assertNoError(err, t, "FindFunctionLocation")

		bp, err := p.SetBreakpoint(helloworldaddr, proc.UserBreakpoint, nil)
		assertNoError(err, t, "SetBreakpoint()")
		assertNoError(proc.Continue(p), t, "Continue()")

		regs, err := p.CurrentThread().Registers(false)
		assertNoError(err, t, "Registers")
		pc := regs.PC()

		if bp.TotalHitCount != 1 {
			t.Fatalf("Breakpoint should be hit once, got %d\n", bp.TotalHitCount)
		}

		if pc-1 != bp.Addr && pc != bp.Addr {
			f, l, _ := p.BinInfo().PCToLine(pc)
			t.Fatalf("Break not respected:\nPC:%#v %s:%d\nFN:%#v \n", pc, f, l, bp.Addr)
		}
	})
}

func TestBreakpointInSeparateGoRoutine(t *testing.T) {
	protest.AllowRecording(t)
	withTestProcess("testthreads", t, func(p proc.Process, fixture protest.Fixture) {
		fnentry, err := proc.FindFunctionLocation(p, "main.anotherthread", false, 0)
		assertNoError(err, t, "FindFunctionLocation")

		_, err = p.SetBreakpoint(fnentry, proc.UserBreakpoint, nil)
		assertNoError(err, t, "SetBreakpoint")

		assertNoError(proc.Continue(p), t, "Continue")

		regs, err := p.CurrentThread().Registers(false)
		assertNoError(err, t, "Registers")
		pc := regs.PC()

		f, l, _ := p.BinInfo().PCToLine(pc)
		if f != "testthreads.go" && l != 8 {
			t.Fatal("Program did not hit breakpoint")
		}
	})
}

func TestBreakpointWithNonExistantFunction(t *testing.T) {
	withTestProcess("testprog", t, func(p proc.Process, fixture protest.Fixture) {
		_, err := p.SetBreakpoint(0, proc.UserBreakpoint, nil)
		if err == nil {
			t.Fatal("Should not be able to break at non existant function")
		}
	})
}

func TestClearBreakpointBreakpoint(t *testing.T) {
	withTestProcess("testprog", t, func(p proc.Process, fixture protest.Fixture) {
		fnentry, err := proc.FindFunctionLocation(p, "main.sleepytime", false, 0)
		assertNoError(err, t, "FindFunctionLocation")
		bp, err := p.SetBreakpoint(fnentry, proc.UserBreakpoint, nil)
		assertNoError(err, t, "SetBreakpoint()")

		bp, err = p.ClearBreakpoint(fnentry)
		assertNoError(err, t, "ClearBreakpoint()")

		data, err := dataAtAddr(p.CurrentThread(), bp.Addr)
		assertNoError(err, t, "dataAtAddr")

		int3 := []byte{0xcc}
		if bytes.Equal(data, int3) {
			t.Fatalf("Breakpoint was not cleared data: %#v, int3: %#v", data, int3)
		}

		if countBreakpoints(p) != 0 {
			t.Fatal("Breakpoint not removed internally")
		}
	})
}

type nextTest struct {
	begin, end int
}

func countBreakpoints(p proc.Process) int {
	bpcount := 0
	for _, bp := range p.Breakpoints().M {
		if bp.ID >= 0 {
			bpcount++
		}
	}
	return bpcount
}

type contFunc int

const (
	contContinue contFunc = iota
	contNext
	contStep
	contStepout
)

type seqTest struct {
	cf  contFunc
	pos int
}

func testseq(program string, contFunc contFunc, testcases []nextTest, initialLocation string, t *testing.T) {
	seqTestcases := make([]seqTest, len(testcases)+1)
	seqTestcases[0] = seqTest{contContinue, testcases[0].begin}
	for i := range testcases {
		if i > 0 {
			if testcases[i-1].end != testcases[i].begin {
				panic(fmt.Errorf("begin/end mismatch at index %d", i))
			}
		}
		seqTestcases[i+1] = seqTest{contFunc, testcases[i].end}
	}
	testseq2(t, program, initialLocation, seqTestcases)
}

const traceTestseq2 = false

func testseq2(t *testing.T, program string, initialLocation string, testcases []seqTest) {
	testseq2Args(".", []string{}, 0, t, program, initialLocation, testcases)
}

func testseq2Args(wd string, args []string, buildFlags protest.BuildFlags, t *testing.T, program string, initialLocation string, testcases []seqTest) {
	protest.AllowRecording(t)
	withTestProcessArgs(program, t, wd, args, buildFlags, func(p proc.Process, fixture protest.Fixture) {
		var bp *proc.Breakpoint
		var err error
		if initialLocation != "" {
			bp, err = setFunctionBreakpoint(p, initialLocation)
		} else if testcases[0].cf == contContinue {
			var pc uint64
			pc, err = proc.FindFileLocation(p, fixture.Source, testcases[0].pos)
			assertNoError(err, t, "FindFileLocation()")
			bp, err = p.SetBreakpoint(pc, proc.UserBreakpoint, nil)
		} else {
			panic("testseq2 can not set initial breakpoint")
		}
		if traceTestseq2 {
			t.Logf("initial breakpoint %v", bp)
		}
		assertNoError(err, t, "SetBreakpoint()")
		regs, err := p.CurrentThread().Registers(false)
		assertNoError(err, t, "Registers")

		f, ln := currentLineNumber(p, t)
		for i, tc := range testcases {
			switch tc.cf {
			case contNext:
				if traceTestseq2 {
					t.Log("next")
				}
				assertNoError(proc.Next(p), t, "Next() returned an error")
			case contStep:
				if traceTestseq2 {
					t.Log("step")
				}
				assertNoError(proc.Step(p), t, "Step() returned an error")
			case contStepout:
				if traceTestseq2 {
					t.Log("stepout")
				}
				assertNoError(proc.StepOut(p), t, "StepOut() returned an error")
			case contContinue:
				if traceTestseq2 {
					t.Log("continue")
				}
				assertNoError(proc.Continue(p), t, "Continue() returned an error")
				if i == 0 {
					if traceTestseq2 {
						t.Log("clearing initial breakpoint")
					}
					_, err := p.ClearBreakpoint(bp.Addr)
					assertNoError(err, t, "ClearBreakpoint() returned an error")
				}
			}

			f, ln = currentLineNumber(p, t)
			regs, _ = p.CurrentThread().Registers(false)
			pc := regs.PC()

			if traceTestseq2 {
				t.Logf("at %#x %s:%d", pc, f, ln)
			}
			if ln != tc.pos {
				t.Fatalf("Program did not continue to correct next location expected %d was %s:%d (%#x) (testcase %d)", tc.pos, filepath.Base(f), ln, pc, i)
			}
		}

		if countBreakpoints(p) != 0 {
			t.Fatal("Not all breakpoints were cleaned up", len(p.Breakpoints().M))
		}
	})
}

func TestNextGeneral(t *testing.T) {
	var testcases []nextTest

	ver, _ := goversion.Parse(runtime.Version())

	if ver.Major < 0 || ver.AfterOrEqual(goversion.GoVersion{1, 7, -1, 0, 0, ""}) {
		testcases = []nextTest{
			{17, 19},
			{19, 20},
			{20, 23},
			{23, 24},
			{24, 26},
			{26, 31},
			{31, 23},
			{23, 24},
			{24, 26},
			{26, 31},
			{31, 23},
			{23, 24},
			{24, 26},
			{26, 27},
			{27, 28},
			{28, 34},
		}
	} else {
		testcases = []nextTest{
			{17, 19},
			{19, 20},
			{20, 23},
			{23, 24},
			{24, 26},
			{26, 31},
			{31, 23},
			{23, 24},
			{24, 26},
			{26, 31},
			{31, 23},
			{23, 24},
			{24, 26},
			{26, 27},
			{27, 34},
		}
	}

	testseq("testnextprog", contNext, testcases, "main.testnext", t)
}

func TestNextConcurrent(t *testing.T) {
	testcases := []nextTest{
		{8, 9},
		{9, 10},
		{10, 11},
	}
	protest.AllowRecording(t)
	withTestProcess("parallel_next", t, func(p proc.Process, fixture protest.Fixture) {
		bp, err := setFunctionBreakpoint(p, "main.sayhi")
		assertNoError(err, t, "SetBreakpoint")
		assertNoError(proc.Continue(p), t, "Continue")
		f, ln := currentLineNumber(p, t)
		initV := evalVariable(p, t, "n")
		initVval, _ := constant.Int64Val(initV.Value)
		_, err = p.ClearBreakpoint(bp.Addr)
		assertNoError(err, t, "ClearBreakpoint()")
		for _, tc := range testcases {
			g, err := proc.GetG(p.CurrentThread())
			assertNoError(err, t, "GetG()")
			if p.SelectedGoroutine().ID != g.ID {
				t.Fatalf("SelectedGoroutine not CurrentThread's goroutine: %d %d", g.ID, p.SelectedGoroutine().ID)
			}
			if ln != tc.begin {
				t.Fatalf("Program not stopped at correct spot expected %d was %s:%d", tc.begin, filepath.Base(f), ln)
			}
			assertNoError(proc.Next(p), t, "Next() returned an error")
			f, ln = assertLineNumber(p, t, tc.end, "Program did not continue to the expected location")
			v := evalVariable(p, t, "n")
			vval, _ := constant.Int64Val(v.Value)
			if vval != initVval {
				t.Fatal("Did not end up on same goroutine")
			}
		}
	})
}

func TestNextConcurrentVariant2(t *testing.T) {
	// Just like TestNextConcurrent but instead of removing the initial breakpoint we check that when it happens is for other goroutines
	testcases := []nextTest{
		{8, 9},
		{9, 10},
		{10, 11},
	}
	protest.AllowRecording(t)
	withTestProcess("parallel_next", t, func(p proc.Process, fixture protest.Fixture) {
		_, err := setFunctionBreakpoint(p, "main.sayhi")
		assertNoError(err, t, "SetBreakpoint")
		assertNoError(proc.Continue(p), t, "Continue")
		f, ln := currentLineNumber(p, t)
		initV := evalVariable(p, t, "n")
		initVval, _ := constant.Int64Val(initV.Value)
		for _, tc := range testcases {
			t.Logf("test case %v", tc)
			g, err := proc.GetG(p.CurrentThread())
			assertNoError(err, t, "GetG()")
			if p.SelectedGoroutine().ID != g.ID {
				t.Fatalf("SelectedGoroutine not CurrentThread's goroutine: %d %d", g.ID, p.SelectedGoroutine().ID)
			}
			if ln != tc.begin {
				t.Fatalf("Program not stopped at correct spot expected %d was %s:%d", tc.begin, filepath.Base(f), ln)
			}
			assertNoError(proc.Next(p), t, "Next() returned an error")
			var vval int64
			for {
				v := evalVariable(p, t, "n")
				for _, thread := range p.ThreadList() {
					proc.GetG(thread)
				}
				vval, _ = constant.Int64Val(v.Value)
				if bpstate := p.CurrentThread().Breakpoint(); bpstate.Breakpoint == nil {
					if vval != initVval {
						t.Fatal("Did not end up on same goroutine")
					}
					break
				} else {
					if vval == initVval {
						t.Fatal("Initial breakpoint triggered twice for the same goroutine")
					}
					assertNoError(proc.Continue(p), t, "Continue 2")
				}
			}
			f, ln = assertLineNumber(p, t, tc.end, "Program did not continue to the expected location")
		}
	})
}

func TestNextFunctionReturn(t *testing.T) {
	testcases := []nextTest{
		{13, 14},
		{14, 15},
		{15, 35},
	}
	protest.AllowRecording(t)
	testseq("testnextprog", contNext, testcases, "main.helloworld", t)
}

func TestNextFunctionReturnDefer(t *testing.T) {
	var testcases []nextTest

	ver, _ := goversion.Parse(runtime.Version())

	if ver.Major < 0 || ver.AfterOrEqual(goversion.GoVersion{1, 9, -1, 0, 0, ""}) {
		testcases = []nextTest{
			{5, 6},
			{6, 9},
			{9, 10},
		}
	} else {
		testcases = []nextTest{
			{5, 8},
			{8, 9},
			{9, 10},
		}
	}
	protest.AllowRecording(t)
	testseq("testnextdefer", contNext, testcases, "main.main", t)
}

func TestNextNetHTTP(t *testing.T) {
	testcases := []nextTest{
		{11, 12},
		{12, 13},
	}
	withTestProcess("testnextnethttp", t, func(p proc.Process, fixture protest.Fixture) {
		go func() {
			// Wait for program to start listening.
			for {
				conn, err := net.Dial("tcp", "localhost:9191")
				if err == nil {
					conn.Close()
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			http.Get("http://localhost:9191")
		}()
		if err := proc.Continue(p); err != nil {
			t.Fatal(err)
		}
		f, ln := currentLineNumber(p, t)
		for _, tc := range testcases {
			if ln != tc.begin {
				t.Fatalf("Program not stopped at correct spot expected %d was %s:%d", tc.begin, filepath.Base(f), ln)
			}

			assertNoError(proc.Next(p), t, "Next() returned an error")

			f, ln = assertLineNumber(p, t, tc.end, "Program did not continue to correct next location")
		}
	})
}

func TestRuntimeBreakpoint(t *testing.T) {
	withTestProcess("testruntimebreakpoint", t, func(p proc.Process, fixture protest.Fixture) {
		err := proc.Continue(p)
		if err != nil {
			t.Fatal(err)
		}
		regs, err := p.CurrentThread().Registers(false)
		assertNoError(err, t, "Registers")
		pc := regs.PC()
		f, l, _ := p.BinInfo().PCToLine(pc)
		if l != 10 {
			t.Fatalf("did not respect breakpoint %s:%d", f, l)
		}
	})
}

func returnAddress(thread proc.Thread) (uint64, error) {
	locations, err := proc.ThreadStacktrace(thread, 2)
	if err != nil {
		return 0, err
	}
	if len(locations) < 2 {
		return 0, fmt.Errorf("no return address for function: %s", locations[0].Current.Fn.BaseName())
	}
	return locations[1].Current.PC, nil
}

func TestFindReturnAddress(t *testing.T) {
	protest.AllowRecording(t)
	withTestProcess("testnextprog", t, func(p proc.Process, fixture protest.Fixture) {
		start, _, err := p.BinInfo().LineToPC(fixture.Source, 24)
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.SetBreakpoint(start, proc.UserBreakpoint, nil)
		if err != nil {
			t.Fatal(err)
		}
		err = proc.Continue(p)
		if err != nil {
			t.Fatal(err)
		}
		addr, err := returnAddress(p.CurrentThread())
		if err != nil {
			t.Fatal(err)
		}
		_, l, _ := p.BinInfo().PCToLine(addr)
		if l != 40 {
			t.Fatalf("return address not found correctly, expected line 40")
		}
	})
}

func TestFindReturnAddressTopOfStackFn(t *testing.T) {
	protest.AllowRecording(t)
	withTestProcess("testreturnaddress", t, func(p proc.Process, fixture protest.Fixture) {
		fnName := "runtime.rt0_go"
		fnentry, err := proc.FindFunctionLocation(p, fnName, false, 0)
		assertNoError(err, t, "FindFunctionLocation")
		if _, err := p.SetBreakpoint(fnentry, proc.UserBreakpoint, nil); err != nil {
			t.Fatal(err)
		}
		if err := proc.Continue(p); err != nil {
			t.Fatal(err)
		}
		if _, err := returnAddress(p.CurrentThread()); err == nil {
			t.Fatal("expected error to be returned")
		}
	})
}

func TestSwitchThread(t *testing.T) {
	protest.AllowRecording(t)
	withTestProcess("testnextprog", t, func(p proc.Process, fixture protest.Fixture) {
		// With invalid thread id
		err := p.SwitchThread(-1)
		if err == nil {
			t.Fatal("Expected error for invalid thread id")
		}
		pc, err := proc.FindFunctionLocation(p, "main.main", true, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.SetBreakpoint(pc, proc.UserBreakpoint, nil)
		if err != nil {
			t.Fatal(err)
		}
		err = proc.Continue(p)
		if err != nil {
			t.Fatal(err)
		}
		var nt int
		ct := p.CurrentThread().ThreadID()
		for _, thread := range p.ThreadList() {
			if thread.ThreadID() != ct {
				nt = thread.ThreadID()
				break
			}
		}
		if nt == 0 {
			t.Fatal("could not find thread to switch to")
		}
		// With valid thread id
		err = p.SwitchThread(nt)
		if err != nil {
			t.Fatal(err)
		}
		if p.CurrentThread().ThreadID() != nt {
			t.Fatal("Did not switch threads")
		}
	})
}

func TestCGONext(t *testing.T) {
	// Test if one can do 'next' in a cgo binary
	// On OSX with Go < 1.5 CGO is not supported due to: https://github.com/golang/go/issues/8973
	if runtime.GOOS == "darwin" && strings.Contains(runtime.Version(), "1.4") {
		return
	}
	if os.Getenv("CGO_ENABLED") == "" {
		return
	}

	protest.AllowRecording(t)
	withTestProcess("cgotest", t, func(p proc.Process, fixture protest.Fixture) {
		pc, err := proc.FindFunctionLocation(p, "main.main", true, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.SetBreakpoint(pc, proc.UserBreakpoint, nil)
		if err != nil {
			t.Fatal(err)
		}
		err = proc.Continue(p)
		if err != nil {
			t.Fatal(err)
		}
		err = proc.Next(p)
		if err != nil {
			t.Fatal(err)
		}
	})
}

type loc struct {
	line int
	fn   string
}

func (l1 *loc) match(l2 proc.Stackframe) bool {
	if l1.line >= 0 {
		if l1.line != l2.Call.Line {
			return false
		}
	}
	return l1.fn == l2.Call.Fn.Name
}

func TestStacktrace(t *testing.T) {
	stacks := [][]loc{
		{{4, "main.stacktraceme"}, {8, "main.func1"}, {16, "main.main"}},
		{{4, "main.stacktraceme"}, {8, "main.func1"}, {12, "main.func2"}, {17, "main.main"}},
	}
	protest.AllowRecording(t)
	withTestProcess("stacktraceprog", t, func(p proc.Process, fixture protest.Fixture) {
		bp, err := setFunctionBreakpoint(p, "main.stacktraceme")
		assertNoError(err, t, "BreakByLocation()")

		for i := range stacks {
			assertNoError(proc.Continue(p), t, "Continue()")
			locations, err := proc.ThreadStacktrace(p.CurrentThread(), 40)
			assertNoError(err, t, "Stacktrace()")

			if len(locations) != len(stacks[i])+2 {
				t.Fatalf("Wrong stack trace size %d %d\n", len(locations), len(stacks[i])+2)
			}

			t.Logf("Stacktrace %d:\n", i)
			for i := range locations {
				t.Logf("\t%s:%d\n", locations[i].Call.File, locations[i].Call.Line)
			}

			for j := range stacks[i] {
				if !stacks[i][j].match(locations[j]) {
					t.Fatalf("Wrong stack trace pos %d\n", j)
				}
			}
		}

		p.ClearBreakpoint(bp.Addr)
		proc.Continue(p)
	})
}

func TestStacktrace2(t *testing.T) {
	withTestProcess("retstack", t, func(p proc.Process, fixture protest.Fixture) {
		assertNoError(proc.Continue(p), t, "Continue()")

		locations, err := proc.ThreadStacktrace(p.CurrentThread(), 40)
		assertNoError(err, t, "Stacktrace()")
		if !stackMatch([]loc{{-1, "main.f"}, {16, "main.main"}}, locations, false) {
			for i := range locations {
				t.Logf("\t%s:%d [%s]\n", locations[i].Call.File, locations[i].Call.Line, locations[i].Call.Fn.Name)
			}
			t.Fatalf("Stack error at main.f()\n%v\n", locations)
		}

		assertNoError(proc.Continue(p), t, "Continue()")
		locations, err = proc.ThreadStacktrace(p.CurrentThread(), 40)
		assertNoError(err, t, "Stacktrace()")
		if !stackMatch([]loc{{-1, "main.g"}, {17, "main.main"}}, locations, false) {
			for i := range locations {
				t.Logf("\t%s:%d [%s]\n", locations[i].Call.File, locations[i].Call.Line, locations[i].Call.Fn.Name)
			}
			t.Fatalf("Stack error at main.g()\n%v\n", locations)
		}
	})

}

func stackMatch(stack []loc, locations []proc.Stackframe, skipRuntime bool) bool {
	if len(stack) > len(locations) {
		return false
	}
	i := 0
	for j := range locations {
		if i >= len(stack) {
			break
		}
		if skipRuntime {
			if locations[j].Call.Fn == nil || strings.HasPrefix(locations[j].Call.Fn.Name, "runtime.") {
				continue
			}
		}
		if !stack[i].match(locations[j]) {
			return false
		}
		i++
	}
	return i >= len(stack)
}

func TestStacktraceGoroutine(t *testing.T) {
	mainStack := []loc{{14, "main.stacktraceme"}, {29, "main.main"}}
	agoroutineStacks := [][]loc{
		{{8, "main.agoroutine"}},
		{{9, "main.agoroutine"}},
		{{10, "main.agoroutine"}},
	}

	protest.AllowRecording(t)
	withTestProcess("goroutinestackprog", t, func(p proc.Process, fixture protest.Fixture) {
		bp, err := setFunctionBreakpoint(p, "main.stacktraceme")
		assertNoError(err, t, "BreakByLocation()")

		assertNoError(proc.Continue(p), t, "Continue()")

		gs, err := proc.GoroutinesInfo(p)
		assertNoError(err, t, "GoroutinesInfo")

		agoroutineCount := 0
		mainCount := 0

		for i, g := range gs {
			locations, err := g.Stacktrace(40)
			if err != nil {
				// On windows we do not have frame information for goroutines doing system calls.
				t.Logf("Could not retrieve goroutine stack for goid=%d: %v", g.ID, err)
				continue
			}

			if stackMatch(mainStack, locations, false) {
				mainCount++
			}

			found := false
			for _, agoroutineStack := range agoroutineStacks {
				if stackMatch(agoroutineStack, locations, true) {
					found = true
				}
			}

			if found {
				agoroutineCount++
			} else {
				t.Logf("Non-goroutine stack: %d (%d)", i, len(locations))
				for i := range locations {
					name := ""
					if locations[i].Call.Fn != nil {
						name = locations[i].Call.Fn.Name
					}
					t.Logf("\t%s:%d %s (%#x)\n", locations[i].Call.File, locations[i].Call.Line, name, locations[i].Current.PC)
				}
			}
		}

		if mainCount != 1 {
			t.Fatalf("Main goroutine stack not found %d", mainCount)
		}

		if agoroutineCount != 10 {
			t.Fatalf("Goroutine stacks not found (%d)", agoroutineCount)
		}

		p.ClearBreakpoint(bp.Addr)
		proc.Continue(p)
	})
}

func TestKill(t *testing.T) {
	if testBackend == "lldb" {
		// k command presumably works but leaves the process around?
		return
	}
	withTestProcess("testprog", t, func(p proc.Process, fixture protest.Fixture) {
		if err := p.Detach(true); err != nil {
			t.Fatal(err)
		}
		if !p.Exited() {
			t.Fatal("expected process to have exited")
		}
		if runtime.GOOS == "linux" {
			_, err := os.Open(fmt.Sprintf("/proc/%d/", p.Pid()))
			if err == nil {
				t.Fatal("process has not exited", p.Pid())
			}
		}
	})
}

func testGSupportFunc(name string, t *testing.T, p proc.Process, fixture protest.Fixture) {
	bp, err := setFunctionBreakpoint(p, "main.main")
	assertNoError(err, t, name+": BreakByLocation()")

	assertNoError(proc.Continue(p), t, name+": Continue()")

	g, err := proc.GetG(p.CurrentThread())
	assertNoError(err, t, name+": GetG()")

	if g == nil {
		t.Fatal(name + ": g was nil")
	}

	t.Logf(name+": g is: %v", g)

	p.ClearBreakpoint(bp.Addr)
}

func TestGetG(t *testing.T) {
	withTestProcess("testprog", t, func(p proc.Process, fixture protest.Fixture) {
		testGSupportFunc("nocgo", t, p, fixture)
	})

	// On OSX with Go < 1.5 CGO is not supported due to: https://github.com/golang/go/issues/8973
	if runtime.GOOS == "darwin" && strings.Contains(runtime.Version(), "1.4") {
		return
	}
	if os.Getenv("CGO_ENABLED") == "" {
		return
	}

	protest.AllowRecording(t)
	withTestProcess("cgotest", t, func(p proc.Process, fixture protest.Fixture) {
		testGSupportFunc("cgo", t, p, fixture)
	})
}

func TestContinueMulti(t *testing.T) {
	protest.AllowRecording(t)
	withTestProcess("integrationprog", t, func(p proc.Process, fixture protest.Fixture) {
		bp1, err := setFunctionBreakpoint(p, "main.main")
		assertNoError(err, t, "BreakByLocation()")

		bp2, err := setFunctionBreakpoint(p, "main.sayhi")
		assertNoError(err, t, "BreakByLocation()")

		mainCount := 0
		sayhiCount := 0
		for {
			err := proc.Continue(p)
			if p.Exited() {
				break
			}
			assertNoError(err, t, "Continue()")

			if bp := p.CurrentThread().Breakpoint(); bp.ID == bp1.ID {
				mainCount++
			}

			if bp := p.CurrentThread().Breakpoint(); bp.ID == bp2.ID {
				sayhiCount++
			}
		}

		if mainCount != 1 {
			t.Fatalf("Main breakpoint hit wrong number of times: %d\n", mainCount)
		}

		if sayhiCount != 3 {
			t.Fatalf("Sayhi breakpoint hit wrong number of times: %d\n", sayhiCount)
		}
	})
}

func TestBreakpointOnFunctionEntry(t *testing.T) {
	testseq2(t, "testprog", "main.main", []seqTest{{contContinue, 17}})
}

func TestProcessReceivesSIGCHLD(t *testing.T) {
	protest.AllowRecording(t)
	withTestProcess("sigchldprog", t, func(p proc.Process, fixture protest.Fixture) {
		err := proc.Continue(p)
		_, ok := err.(proc.ProcessExitedError)
		if !ok {
			t.Fatalf("Continue() returned unexpected error type %v", err)
		}
	})
}

func TestIssue239(t *testing.T) {
	withTestProcess("is sue239", t, func(p proc.Process, fixture protest.Fixture) {
		pos, _, err := p.BinInfo().LineToPC(fixture.Source, 17)
		assertNoError(err, t, "LineToPC()")
		_, err = p.SetBreakpoint(pos, proc.UserBreakpoint, nil)
		assertNoError(err, t, fmt.Sprintf("SetBreakpoint(%d)", pos))
		assertNoError(proc.Continue(p), t, fmt.Sprintf("Continue()"))
	})
}

func findFirstNonRuntimeFrame(p proc.Process) (proc.Stackframe, error) {
	frames, err := proc.ThreadStacktrace(p.CurrentThread(), 10)
	if err != nil {
		return proc.Stackframe{}, err
	}

	for _, frame := range frames {
		if frame.Current.Fn != nil && !strings.HasPrefix(frame.Current.Fn.Name, "runtime.") {
			return frame, nil
		}
	}
	return proc.Stackframe{}, fmt.Errorf("non-runtime frame not found")
}

func evalVariableOrError(p proc.Process, symbol string) (*proc.Variable, error) {
	var scope *proc.EvalScope
	var err error

	if testBackend == "rr" {
		var frame proc.Stackframe
		frame, err = findFirstNonRuntimeFrame(p)
		if err == nil {
			scope = proc.FrameToScope(p.BinInfo(), p.CurrentThread(), nil, frame)
		}
	} else {
		scope, err = proc.GoroutineScope(p.CurrentThread())
	}

	if err != nil {
		return nil, err
	}
	return scope.EvalVariable(symbol, normalLoadConfig)
}

func evalVariable(p proc.Process, t testing.TB, symbol string) *proc.Variable {
	v, err := evalVariableOrError(p, symbol)
	if err != nil {
		_, file, line, _ := runtime.Caller(1)
		fname := filepath.Base(file)
		t.Fatalf("%s:%d: EvalVariable(%q): %v", fname, line, symbol, err)
	}
	return v
}

func setVariable(p proc.Process, symbol, value string) error {
	scope, err := proc.GoroutineScope(p.CurrentThread())
	if err != nil {
		return err
	}
	return scope.SetVariable(symbol, value)
}

func TestVariableEvaluation(t *testing.T) {
	protest.AllowRecording(t)
	testcases := []struct {
		name        string
		st          reflect.Kind
		value       interface{}
		length, cap int64
		childrenlen int
	}{
		{"a1", reflect.String, "foofoofoofoofoofoo", 18, 0, 0},
		{"a11", reflect.Array, nil, 3, -1, 3},
		{"a12", reflect.Slice, nil, 2, 2, 2},
		{"a13", reflect.Slice, nil, 3, 3, 3},
		{"a2", reflect.Int, int64(6), 0, 0, 0},
		{"a3", reflect.Float64, float64(7.23), 0, 0, 0},
		{"a4", reflect.Array, nil, 2, -1, 2},
		{"a5", reflect.Slice, nil, 5, 5, 5},
		{"a6", reflect.Struct, nil, 2, 0, 2},
		{"a7", reflect.Ptr, nil, 1, 0, 1},
		{"a8", reflect.Struct, nil, 2, 0, 2},
		{"a9", reflect.Ptr, nil, 1, 0, 1},
		{"baz", reflect.String, "bazburzum", 9, 0, 0},
		{"neg", reflect.Int, int64(-1), 0, 0, 0},
		{"f32", reflect.Float32, float64(float32(1.2)), 0, 0, 0},
		{"c64", reflect.Complex64, complex128(complex64(1 + 2i)), 0, 0, 0},
		{"c128", reflect.Complex128, complex128(2 + 3i), 0, 0, 0},
		{"a6.Baz", reflect.Int, int64(8), 0, 0, 0},
		{"a7.Baz", reflect.Int, int64(5), 0, 0, 0},
		{"a8.Baz", reflect.String, "feh", 3, 0, 0},
		{"a8", reflect.Struct, nil, 2, 0, 2},
		{"i32", reflect.Array, nil, 2, -1, 2},
		{"b1", reflect.Bool, true, 0, 0, 0},
		{"b2", reflect.Bool, false, 0, 0, 0},
		{"f", reflect.Func, "main.barfoo", 0, 0, 0},
		{"ba", reflect.Slice, nil, 200, 200, 64},
	}

	withTestProcess("testvariables", t, func(p proc.Process, fixture protest.Fixture) {
		assertNoError(proc.Continue(p), t, "Continue() returned an error")

		for _, tc := range testcases {
			v := evalVariable(p, t, tc.name)

			if v.Kind != tc.st {
				t.Fatalf("%s simple type: expected: %s got: %s", tc.name, tc.st, v.Kind.String())
			}
			if v.Value == nil && tc.value != nil {
				t.Fatalf("%s value: expected: %v got: %v", tc.name, tc.value, v.Value)
			} else {
				switch v.Kind {
				case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
					x, _ := constant.Int64Val(v.Value)
					if y, ok := tc.value.(int64); !ok || x != y {
						t.Fatalf("%s value: expected: %v got: %v", tc.name, tc.value, v.Value)
					}
				case reflect.Float32, reflect.Float64:
					x, _ := constant.Float64Val(v.Value)
					if y, ok := tc.value.(float64); !ok || x != y {
						t.Fatalf("%s value: expected: %v got: %v", tc.name, tc.value, v.Value)
					}
				case reflect.Complex64, reflect.Complex128:
					xr, _ := constant.Float64Val(constant.Real(v.Value))
					xi, _ := constant.Float64Val(constant.Imag(v.Value))
					if y, ok := tc.value.(complex128); !ok || complex(xr, xi) != y {
						t.Fatalf("%s value: expected: %v got: %v", tc.name, tc.value, v.Value)
					}
				case reflect.String:
					if y, ok := tc.value.(string); !ok || constant.StringVal(v.Value) != y {
						t.Fatalf("%s value: expected: %v got: %v", tc.name, tc.value, v.Value)
					}
				}
			}
			if v.Len != tc.length {
				t.Fatalf("%s len: expected: %d got: %d", tc.name, tc.length, v.Len)
			}
			if v.Cap != tc.cap {
				t.Fatalf("%s cap: expected: %d got: %d", tc.name, tc.cap, v.Cap)
			}
			if len(v.Children) != tc.childrenlen {
				t.Fatalf("%s children len: expected %d got: %d", tc.name, tc.childrenlen, len(v.Children))
			}
		}
	})
}

func TestFrameEvaluation(t *testing.T) {
	protest.AllowRecording(t)
	withTestProcess("goroutinestackprog", t, func(p proc.Process, fixture protest.Fixture) {
		_, err := setFunctionBreakpoint(p, "main.stacktraceme")
		assertNoError(err, t, "setFunctionBreakpoint")
		assertNoError(proc.Continue(p), t, "Continue()")

		t.Logf("stopped on thread %d, goroutine: %#v", p.CurrentThread().ThreadID(), p.SelectedGoroutine())

		// Testing evaluation on goroutines
		gs, err := proc.GoroutinesInfo(p)
		assertNoError(err, t, "GoroutinesInfo")
		found := make([]bool, 10)
		for _, g := range gs {
			frame := -1
			frames, err := g.Stacktrace(10)
			if err != nil {
				t.Logf("could not stacktrace goroutine %d: %v\n", g.ID, err)
				continue
			}
			t.Logf("Goroutine %d", g.ID)
			logStacktrace(t, frames)
			for i := range frames {
				if frames[i].Call.Fn != nil && frames[i].Call.Fn.Name == "main.agoroutine" {
					frame = i
					break
				}
			}

			if frame < 0 {
				t.Logf("Goroutine %d: could not find correct frame", g.ID)
				continue
			}

			scope, err := proc.ConvertEvalScope(p, g.ID, frame)
			assertNoError(err, t, "ConvertEvalScope()")
			t.Logf("scope = %v", scope)
			v, err := scope.EvalVariable("i", normalLoadConfig)
			t.Logf("v = %v", v)
			if err != nil {
				t.Logf("Goroutine %d: %v\n", g.ID, err)
				continue
			}
			vval, _ := constant.Int64Val(v.Value)
			found[vval] = true
		}

		for i := range found {
			if !found[i] {
				t.Fatalf("Goroutine %d not found\n", i)
			}
		}

		// Testing evaluation on frames
		assertNoError(proc.Continue(p), t, "Continue() 2")
		g, err := proc.GetG(p.CurrentThread())
		assertNoError(err, t, "GetG()")

		for i := 0; i <= 3; i++ {
			scope, err := proc.ConvertEvalScope(p, g.ID, i+1)
			assertNoError(err, t, fmt.Sprintf("ConvertEvalScope() on frame %d", i+1))
			v, err := scope.EvalVariable("n", normalLoadConfig)
			assertNoError(err, t, fmt.Sprintf("EvalVariable() on frame %d", i+1))
			n, _ := constant.Int64Val(v.Value)
			t.Logf("frame %d n %d\n", i+1, n)
			if n != int64(3-i) {
				t.Fatalf("On frame %d value of n is %d (not %d)", i+1, n, 3-i)
			}
		}
	})
}

func TestPointerSetting(t *testing.T) {
	withTestProcess("testvariables2", t, func(p proc.Process, fixture protest.Fixture) {
		assertNoError(proc.Continue(p), t, "Continue() returned an error")

		pval := func(n int64) {
			variable := evalVariable(p, t, "p1")
			c0val, _ := constant.Int64Val(variable.Children[0].Value)
			if c0val != n {
				t.Fatalf("Wrong value of p1, *%d expected *%d", c0val, n)
			}
		}

		pval(1)

		// change p1 to point to i2
		scope, err := proc.GoroutineScope(p.CurrentThread())
		assertNoError(err, t, "Scope()")
		i2addr, err := scope.EvalExpression("i2", normalLoadConfig)
		assertNoError(err, t, "EvalExpression()")
		assertNoError(setVariable(p, "p1", fmt.Sprintf("(*int)(0x%x)", i2addr.Addr)), t, "SetVariable()")
		pval(2)

		// change the value of i2 check that p1 also changes
		assertNoError(setVariable(p, "i2", "5"), t, "SetVariable()")
		pval(5)
	})
}

func TestVariableFunctionScoping(t *testing.T) {
	withTestProcess("testvariables", t, func(p proc.Process, fixture protest.Fixture) {
		err := proc.Continue(p)
		assertNoError(err, t, "Continue() returned an error")

		evalVariable(p, t, "a1")
		evalVariable(p, t, "a2")

		// Move scopes, a1 exists here by a2 does not
		err = proc.Continue(p)
		assertNoError(err, t, "Continue() returned an error")

		evalVariable(p, t, "a1")

		_, err = evalVariableOrError(p, "a2")
		if err == nil {
			t.Fatalf("Can eval out of scope variable a2")
		}
	})
}

func TestRecursiveStructure(t *testing.T) {
	protest.AllowRecording(t)
	withTestProcess("testvariables2", t, func(p proc.Process, fixture protest.Fixture) {
		assertNoError(proc.Continue(p), t, "Continue()")
		v := evalVariable(p, t, "aas")
		t.Logf("v: %v\n", v)
	})
}

func TestIssue316(t *testing.T) {
	// A pointer loop that includes one interface should not send dlv into an infinite loop
	protest.AllowRecording(t)
	withTestProcess("testvariables2", t, func(p proc.Process, fixture protest.Fixture) {
		assertNoError(proc.Continue(p), t, "Continue()")
		evalVariable(p, t, "iface5")
	})
}

func TestIssue325(t *testing.T) {
	// nil pointer dereference when evaluating interfaces to function pointers
	protest.AllowRecording(t)
	withTestProcess("testvariables2", t, func(p proc.Process, fixture protest.Fixture) {
		assertNoError(proc.Continue(p), t, "Continue()")
		iface2fn1v := evalVariable(p, t, "iface2fn1")
		t.Logf("iface2fn1: %v\n", iface2fn1v)

		iface2fn2v := evalVariable(p, t, "iface2fn2")
		t.Logf("iface2fn2: %v\n", iface2fn2v)
	})
}

func TestBreakpointCounts(t *testing.T) {
	protest.AllowRecording(t)
	withTestProcess("bpcountstest", t, func(p proc.Process, fixture protest.Fixture) {
		addr, _, err := p.BinInfo().LineToPC(fixture.Source, 12)
		assertNoError(err, t, "LineToPC")
		bp, err := p.SetBreakpoint(addr, proc.UserBreakpoint, nil)
		assertNoError(err, t, "SetBreakpoint()")

		for {
			if err := proc.Continue(p); err != nil {
				if _, exited := err.(proc.ProcessExitedError); exited {
					break
				}
				assertNoError(err, t, "Continue()")
			}
		}

		t.Logf("TotalHitCount: %d", bp.TotalHitCount)
		if bp.TotalHitCount != 200 {
			t.Fatalf("Wrong TotalHitCount for the breakpoint (%d)", bp.TotalHitCount)
		}

		if len(bp.HitCount) != 2 {
			t.Fatalf("Wrong number of goroutines for breakpoint (%d)", len(bp.HitCount))
		}

		for _, v := range bp.HitCount {
			if v != 100 {
				t.Fatalf("Wrong HitCount for breakpoint (%v)", bp.HitCount)
			}
		}
	})
}

func BenchmarkArray(b *testing.B) {
	// each bencharr struct is 128 bytes, bencharr is 64 elements long
	protest.AllowRecording(b)
	b.SetBytes(int64(64 * 128))
	withTestProcess("testvariables2", b, func(p proc.Process, fixture protest.Fixture) {
		assertNoError(proc.Continue(p), b, "Continue()")
		for i := 0; i < b.N; i++ {
			evalVariable(p, b, "bencharr")
		}
	})
}

const doTestBreakpointCountsWithDetection = false

func TestBreakpointCountsWithDetection(t *testing.T) {
	if !doTestBreakpointCountsWithDetection {
		return
	}
	m := map[int64]int64{}
	protest.AllowRecording(t)
	withTestProcess("bpcountstest", t, func(p proc.Process, fixture protest.Fixture) {
		addr, _, err := p.BinInfo().LineToPC(fixture.Source, 12)
		assertNoError(err, t, "LineToPC")
		bp, err := p.SetBreakpoint(addr, proc.UserBreakpoint, nil)
		assertNoError(err, t, "SetBreakpoint()")

		for {
			if err := proc.Continue(p); err != nil {
				if _, exited := err.(proc.ProcessExitedError); exited {
					break
				}
				assertNoError(err, t, "Continue()")
			}
			for _, th := range p.ThreadList() {
				if bp := th.Breakpoint(); bp.Breakpoint == nil {
					continue
				}
				scope, err := proc.GoroutineScope(th)
				assertNoError(err, t, "Scope()")
				v, err := scope.EvalVariable("i", normalLoadConfig)
				assertNoError(err, t, "evalVariable")
				i, _ := constant.Int64Val(v.Value)
				v, err = scope.EvalVariable("id", normalLoadConfig)
				assertNoError(err, t, "evalVariable")
				id, _ := constant.Int64Val(v.Value)
				m[id] = i
			}

			total := int64(0)
			for i := range m {
				total += m[i] + 1
			}

			if uint64(total) != bp.TotalHitCount {
				t.Fatalf("Mismatched total count %d %d\n", total, bp.TotalHitCount)
			}
		}

		t.Logf("TotalHitCount: %d", bp.TotalHitCount)
		if bp.TotalHitCount != 200 {
			t.Fatalf("Wrong TotalHitCount for the breakpoint (%d)", bp.TotalHitCount)
		}

		if len(bp.HitCount) != 2 {
			t.Fatalf("Wrong number of goroutines for breakpoint (%d)", len(bp.HitCount))
		}

		for _, v := range bp.HitCount {
			if v != 100 {
				t.Fatalf("Wrong HitCount for breakpoint (%v)", bp.HitCount)
			}
		}
	})
}

func BenchmarkArrayPointer(b *testing.B) {
	// each bencharr struct is 128 bytes, benchparr is an array of 64 pointers to bencharr
	// each read will read 64 bencharr structs plus the 64 pointers of benchparr
	protest.AllowRecording(b)
	b.SetBytes(int64(64*128 + 64*8))
	withTestProcess("testvariables2", b, func(p proc.Process, fixture protest.Fixture) {
		assertNoError(proc.Continue(p), b, "Continue()")
		for i := 0; i < b.N; i++ {
			evalVariable(p, b, "bencharr")
		}
	})
}

func BenchmarkMap(b *testing.B) {
	// m1 contains 41 entries, each one has a value that's 2 int values (2* 8 bytes) and a string key
	// each string key has an average of 9 character
	// reading strings and the map structure imposes a overhead that we ignore here
	protest.AllowRecording(b)
	b.SetBytes(int64(41 * (2*8 + 9)))
	withTestProcess("testvariables2", b, func(p proc.Process, fixture protest.Fixture) {
		assertNoError(proc.Continue(p), b, "Continue()")
		for i := 0; i < b.N; i++ {
			evalVariable(p, b, "m1")
		}
	})
}

func BenchmarkGoroutinesInfo(b *testing.B) {
	protest.AllowRecording(b)
	withTestProcess("testvariables2", b, func(p proc.Process, fixture protest.Fixture) {
		assertNoError(proc.Continue(p), b, "Continue()")
		for i := 0; i < b.N; i++ {
			if p, ok := p.(proc.AllGCache); ok {
				allgcache := p.AllGCache()
				*allgcache = nil
			}
			_, err := proc.GoroutinesInfo(p)
			assertNoError(err, b, "GoroutinesInfo")
		}
	})
}

func TestIssue262(t *testing.T) {
	// Continue does not work when the current breakpoint is set on a NOP instruction
	protest.AllowRecording(t)
	withTestProcess("issue262", t, func(p proc.Process, fixture protest.Fixture) {
		addr, _, err := p.BinInfo().LineToPC(fixture.Source, 11)
		assertNoError(err, t, "LineToPC")
		_, err = p.SetBreakpoint(addr, proc.UserBreakpoint, nil)
		assertNoError(err, t, "SetBreakpoint()")

		assertNoError(proc.Continue(p), t, "Continue()")
		err = proc.Continue(p)
		if err == nil {
			t.Fatalf("No error on second continue")
		}
		_, exited := err.(proc.ProcessExitedError)
		if !exited {
			t.Fatalf("Process did not exit after second continue: %v", err)
		}
	})
}

func TestIssue305(t *testing.T) {
	// If 'next' hits a breakpoint on the goroutine it's stepping through
	// the internal breakpoints aren't cleared preventing further use of
	// 'next' command
	protest.AllowRecording(t)
	withTestProcess("issue305", t, func(p proc.Process, fixture protest.Fixture) {
		addr, _, err := p.BinInfo().LineToPC(fixture.Source, 5)
		assertNoError(err, t, "LineToPC()")
		_, err = p.SetBreakpoint(addr, proc.UserBreakpoint, nil)
		assertNoError(err, t, "SetBreakpoint()")

		assertNoError(proc.Continue(p), t, "Continue()")

		assertNoError(proc.Next(p), t, "Next() 1")
		assertNoError(proc.Next(p), t, "Next() 2")
		assertNoError(proc.Next(p), t, "Next() 3")
		assertNoError(proc.Next(p), t, "Next() 4")
		assertNoError(proc.Next(p), t, "Next() 5")
	})
}

func TestPointerLoops(t *testing.T) {
	// Pointer loops through map entries, pointers and slices
	// Regression test for issue #341
	protest.AllowRecording(t)
	withTestProcess("testvariables2", t, func(p proc.Process, fixture protest.Fixture) {
		assertNoError(proc.Continue(p), t, "Continue()")
		for _, expr := range []string{"mapinf", "ptrinf", "sliceinf"} {
			t.Logf("requesting %s", expr)
			v := evalVariable(p, t, expr)
			t.Logf("%s: %v\n", expr, v)
		}
	})
}

func BenchmarkLocalVariables(b *testing.B) {
	protest.AllowRecording(b)
	withTestProcess("testvariables", b, func(p proc.Process, fixture protest.Fixture) {
		assertNoError(proc.Continue(p), b, "Continue() returned an error")
		scope, err := proc.GoroutineScope(p.CurrentThread())
		assertNoError(err, b, "Scope()")
		for i := 0; i < b.N; i++ {
			_, err := scope.LocalVariables(normalLoadConfig)
			assertNoError(err, b, "LocalVariables()")
		}
	})
}

func TestCondBreakpoint(t *testing.T) {
	protest.AllowRecording(t)
	withTestProcess("parallel_next", t, func(p proc.Process, fixture protest.Fixture) {
		addr, _, err := p.BinInfo().LineToPC(fixture.Source, 9)
		assertNoError(err, t, "LineToPC")
		bp, err := p.SetBreakpoint(addr, proc.UserBreakpoint, nil)
		assertNoError(err, t, "SetBreakpoint()")
		bp.Cond = &ast.BinaryExpr{
			Op: token.EQL,
			X:  &ast.Ident{Name: "n"},
			Y:  &ast.BasicLit{Kind: token.INT, Value: "7"},
		}

		assertNoError(proc.Continue(p), t, "Continue()")

		nvar := evalVariable(p, t, "n")

		n, _ := constant.Int64Val(nvar.Value)
		if n != 7 {
			t.Fatalf("Stoppend on wrong goroutine %d\n", n)
		}
	})
}

func TestCondBreakpointError(t *testing.T) {
	protest.AllowRecording(t)
	withTestProcess("parallel_next", t, func(p proc.Process, fixture protest.Fixture) {
		addr, _, err := p.BinInfo().LineToPC(fixture.Source, 9)
		assertNoError(err, t, "LineToPC")
		bp, err := p.SetBreakpoint(addr, proc.UserBreakpoint, nil)
		assertNoError(err, t, "SetBreakpoint()")
		bp.Cond = &ast.BinaryExpr{
			Op: token.EQL,
			X:  &ast.Ident{Name: "nonexistentvariable"},
			Y:  &ast.BasicLit{Kind: token.INT, Value: "7"},
		}

		err = proc.Continue(p)
		if err == nil {
			t.Fatalf("No error on first Continue()")
		}

		if err.Error() != "error evaluating expression: could not find symbol value for nonexistentvariable" && err.Error() != "multiple errors evaluating conditions" {
			t.Fatalf("Unexpected error on first Continue(): %v", err)
		}

		bp.Cond = &ast.BinaryExpr{
			Op: token.EQL,
			X:  &ast.Ident{Name: "n"},
			Y:  &ast.BasicLit{Kind: token.INT, Value: "7"},
		}

		err = proc.Continue(p)
		if err != nil {
			if _, exited := err.(proc.ProcessExitedError); !exited {
				t.Fatalf("Unexpected error on second Continue(): %v", err)
			}
		} else {
			nvar := evalVariable(p, t, "n")

			n, _ := constant.Int64Val(nvar.Value)
			if n != 7 {
				t.Fatalf("Stoppend on wrong goroutine %d\n", n)
			}
		}
	})
}

func TestIssue356(t *testing.T) {
	// slice with a typedef does not get printed correctly
	protest.AllowRecording(t)
	withTestProcess("testvariables2", t, func(p proc.Process, fixture protest.Fixture) {
		assertNoError(proc.Continue(p), t, "Continue() returned an error")
		mmvar := evalVariable(p, t, "mainMenu")
		if mmvar.Kind != reflect.Slice {
			t.Fatalf("Wrong kind for mainMenu: %v\n", mmvar.Kind)
		}
	})
}

func TestStepIntoFunction(t *testing.T) {
	withTestProcess("teststep", t, func(p proc.Process, fixture protest.Fixture) {
		// Continue until breakpoint
		assertNoError(proc.Continue(p), t, "Continue() returned an error")
		// Step into function
		assertNoError(proc.Step(p), t, "Step() returned an error")
		// We should now be inside the function.
		loc, err := p.CurrentThread().Location()
		if err != nil {
			t.Fatal(err)
		}
		if loc.Fn.Name != "main.callme" {
			t.Fatalf("expected to be within the 'callme' function, was in %s instead", loc.Fn.Name)
		}
		if !strings.Contains(loc.File, "teststep") {
			t.Fatalf("debugger stopped at incorrect location: %s:%d", loc.File, loc.Line)
		}
		if loc.Line != 8 {
			t.Fatalf("debugger stopped at incorrect line: %d", loc.Line)
		}
	})
}

func TestIssue384(t *testing.T) {
	// Crash related to reading uninitialized memory, introduced by the memory prefetching optimization

	ver, _ := goversion.Parse(runtime.Version())
	if ver.Major < 0 || ver.AfterOrEqual(goversion.GoVersion{1, 10, -1, 0, 0, ""}) {
		// go 1.10 emits DW_AT_decl_line and we won't be able to evaluate 'st'
		// which is declared after line 13.
		t.Skip("can not evaluate not-yet-declared variables with go 1.10")
	}

	protest.AllowRecording(t)
	withTestProcess("issue384", t, func(p proc.Process, fixture protest.Fixture) {
		start, _, err := p.BinInfo().LineToPC(fixture.Source, 13)
		assertNoError(err, t, "LineToPC()")
		_, err = p.SetBreakpoint(start, proc.UserBreakpoint, nil)
		assertNoError(err, t, "SetBreakpoint()")
		assertNoError(proc.Continue(p), t, "Continue()")
		evalVariable(p, t, "st")
	})
}

func TestIssue332_Part1(t *testing.T) {
	// Next shouldn't step inside a function call
	protest.AllowRecording(t)
	withTestProcess("issue332", t, func(p proc.Process, fixture protest.Fixture) {
		start, _, err := p.BinInfo().LineToPC(fixture.Source, 8)
		assertNoError(err, t, "LineToPC()")
		_, err = p.SetBreakpoint(start, proc.UserBreakpoint, nil)
		assertNoError(err, t, "SetBreakpoint()")
		assertNoError(proc.Continue(p), t, "Continue()")
		assertNoError(proc.Next(p), t, "first Next()")
		locations, err := proc.ThreadStacktrace(p.CurrentThread(), 2)
		assertNoError(err, t, "Stacktrace()")
		if locations[0].Call.Fn == nil {
			t.Fatalf("Not on a function")
		}
		if locations[0].Call.Fn.Name != "main.main" {
			t.Fatalf("Not on main.main after Next: %s (%s:%d)", locations[0].Call.Fn.Name, locations[0].Call.File, locations[0].Call.Line)
		}
		if locations[0].Call.Line != 9 {
			t.Fatalf("Not on line 9 after Next: %s (%s:%d)", locations[0].Call.Fn.Name, locations[0].Call.File, locations[0].Call.Line)
		}
	})
}

func TestIssue332_Part2(t *testing.T) {
	// Step should skip a function's prologue
	// In some parts of the prologue, for some functions, the FDE data is incorrect
	// which leads to 'next' and 'stack' failing with error "could not find FDE for PC: <garbage>"
	// because the incorrect FDE data leads to reading the wrong stack address as the return address
	protest.AllowRecording(t)
	withTestProcess("issue332", t, func(p proc.Process, fixture protest.Fixture) {
		start, _, err := p.BinInfo().LineToPC(fixture.Source, 8)
		assertNoError(err, t, "LineToPC()")
		_, err = p.SetBreakpoint(start, proc.UserBreakpoint, nil)
		assertNoError(err, t, "SetBreakpoint()")
		assertNoError(proc.Continue(p), t, "Continue()")

		// step until we enter changeMe
		for {
			assertNoError(proc.Step(p), t, "Step()")
			locations, err := proc.ThreadStacktrace(p.CurrentThread(), 2)
			assertNoError(err, t, "Stacktrace()")
			if locations[0].Call.Fn == nil {
				t.Fatalf("Not on a function")
			}
			if locations[0].Call.Fn.Name == "main.changeMe" {
				break
			}
		}

		regs, err := p.CurrentThread().Registers(false)
		assertNoError(err, t, "Registers()")
		pc := regs.PC()
		pcAfterPrologue, err := proc.FindFunctionLocation(p, "main.changeMe", true, -1)
		assertNoError(err, t, "FindFunctionLocation()")
		pcEntry, err := proc.FindFunctionLocation(p, "main.changeMe", false, 0)
		if err != nil {
			t.Fatalf("got error while finding function location: %v", err)
		}
		if pcAfterPrologue == pcEntry {
			t.Fatalf("main.changeMe and main.changeMe:0 are the same (%x)", pcAfterPrologue)
		}
		if pc != pcAfterPrologue {
			t.Fatalf("Step did not skip the prologue: current pc: %x, first instruction after prologue: %x", pc, pcAfterPrologue)
		}

		assertNoError(proc.Next(p), t, "first Next()")
		assertNoError(proc.Next(p), t, "second Next()")
		assertNoError(proc.Next(p), t, "third Next()")
		err = proc.Continue(p)
		if _, exited := err.(proc.ProcessExitedError); !exited {
			assertNoError(err, t, "final Continue()")
		}
	})
}

func TestIssue396(t *testing.T) {
	withTestProcess("callme", t, func(p proc.Process, fixture protest.Fixture) {
		_, err := proc.FindFunctionLocation(p, "main.init", true, -1)
		assertNoError(err, t, "FindFunctionLocation()")
	})
}

func TestIssue414(t *testing.T) {
	// Stepping until the program exits
	protest.AllowRecording(t)
	withTestProcess("math", t, func(p proc.Process, fixture protest.Fixture) {
		start, _, err := p.BinInfo().LineToPC(fixture.Source, 9)
		assertNoError(err, t, "LineToPC()")
		_, err = p.SetBreakpoint(start, proc.UserBreakpoint, nil)
		assertNoError(err, t, "SetBreakpoint()")
		assertNoError(proc.Continue(p), t, "Continue()")
		for {
			err := proc.Step(p)
			if err != nil {
				if _, exited := err.(proc.ProcessExitedError); exited {
					break
				}
			}
			assertNoError(err, t, "Step()")
		}
	})
}

func TestPackageVariables(t *testing.T) {
	protest.AllowRecording(t)
	withTestProcess("testvariables", t, func(p proc.Process, fixture protest.Fixture) {
		err := proc.Continue(p)
		assertNoError(err, t, "Continue()")
		scope, err := proc.GoroutineScope(p.CurrentThread())
		assertNoError(err, t, "Scope()")
		vars, err := scope.PackageVariables(normalLoadConfig)
		assertNoError(err, t, "PackageVariables()")
		failed := false
		for _, v := range vars {
			if v.Unreadable != nil {
				failed = true
				t.Logf("Unreadable variable %s: %v", v.Name, v.Unreadable)
			}
		}
		if failed {
			t.Fatalf("previous errors")
		}
	})
}

func TestIssue149(t *testing.T) {
	ver, _ := goversion.Parse(runtime.Version())
	if ver.Major > 0 && !ver.AfterOrEqual(goversion.GoVersion{1, 7, -1, 0, 0, ""}) {
		return
	}
	// setting breakpoint on break statement
	withTestProcess("break", t, func(p proc.Process, fixture protest.Fixture) {
		_, err := proc.FindFileLocation(p, fixture.Source, 8)
		assertNoError(err, t, "FindFileLocation()")
	})
}

func TestPanicBreakpoint(t *testing.T) {
	protest.AllowRecording(t)
	withTestProcess("panic", t, func(p proc.Process, fixture protest.Fixture) {
		assertNoError(proc.Continue(p), t, "Continue()")
		bp := p.CurrentThread().Breakpoint()
		if bp.Breakpoint == nil || bp.Name != proc.UnrecoveredPanic {
			t.Fatalf("not on unrecovered-panic breakpoint: %v", bp)
		}
	})
}

func TestCmdLineArgs(t *testing.T) {
	expectSuccess := func(p proc.Process, fixture protest.Fixture) {
		err := proc.Continue(p)
		bp := p.CurrentThread().Breakpoint()
		if bp.Breakpoint != nil && bp.Name == proc.UnrecoveredPanic {
			t.Fatalf("testing args failed on unrecovered-panic breakpoint: %v", bp)
		}
		exit, exited := err.(proc.ProcessExitedError)
		if !exited {
			t.Fatalf("Process did not exit: %v", err)
		} else {
			if exit.Status != 0 {
				t.Fatalf("process exited with invalid status %d", exit.Status)
			}
		}
	}

	expectPanic := func(p proc.Process, fixture protest.Fixture) {
		proc.Continue(p)
		bp := p.CurrentThread().Breakpoint()
		if bp.Breakpoint == nil || bp.Name != proc.UnrecoveredPanic {
			t.Fatalf("not on unrecovered-panic breakpoint: %v", bp)
		}
	}

	// make sure multiple arguments (including one with spaces) are passed to the binary correctly
	withTestProcessArgs("testargs", t, ".", []string{"test"}, 0, expectSuccess)
	withTestProcessArgs("testargs", t, ".", []string{"-test"}, 0, expectPanic)
	withTestProcessArgs("testargs", t, ".", []string{"test", "pass flag"}, 0, expectSuccess)
	// check that arguments with spaces are *only* passed correctly when correctly called
	withTestProcessArgs("testargs", t, ".", []string{"test pass", "flag"}, 0, expectPanic)
	withTestProcessArgs("testargs", t, ".", []string{"test", "pass", "flag"}, 0, expectPanic)
	withTestProcessArgs("testargs", t, ".", []string{"test pass flag"}, 0, expectPanic)
	// and that invalid cases (wrong arguments or no arguments) panic
	withTestProcess("testargs", t, expectPanic)
	withTestProcessArgs("testargs", t, ".", []string{"invalid"}, 0, expectPanic)
	withTestProcessArgs("testargs", t, ".", []string{"test", "invalid"}, 0, expectPanic)
	withTestProcessArgs("testargs", t, ".", []string{"invalid", "pass flag"}, 0, expectPanic)
}

func TestIssue462(t *testing.T) {
	// Stacktrace of Goroutine 0 fails with an error
	if runtime.GOOS == "windows" {
		return
	}
	withTestProcess("testnextnethttp", t, func(p proc.Process, fixture protest.Fixture) {
		go func() {
			// Wait for program to start listening.
			for {
				conn, err := net.Dial("tcp", "localhost:9191")
				if err == nil {
					conn.Close()
					break
				}
				time.Sleep(50 * time.Millisecond)
			}

			p.RequestManualStop()
		}()

		assertNoError(proc.Continue(p), t, "Continue()")
		_, err := proc.ThreadStacktrace(p.CurrentThread(), 40)
		assertNoError(err, t, "Stacktrace()")
	})
}

func TestNextParked(t *testing.T) {
	protest.AllowRecording(t)
	withTestProcess("parallel_next", t, func(p proc.Process, fixture protest.Fixture) {
		bp, err := setFunctionBreakpoint(p, "main.sayhi")
		assertNoError(err, t, "SetBreakpoint()")

		// continue until a parked goroutine exists
		var parkedg *proc.G
		for parkedg == nil {
			err := proc.Continue(p)
			if _, exited := err.(proc.ProcessExitedError); exited {
				t.Log("could not find parked goroutine")
				return
			}
			assertNoError(err, t, "Continue()")

			gs, err := proc.GoroutinesInfo(p)
			assertNoError(err, t, "GoroutinesInfo()")

			// Search for a parked goroutine that we know for sure will have to be
			// resumed before the program can exit. This is a parked goroutine that:
			// 1. is executing main.sayhi
			// 2. hasn't called wg.Done yet
			for _, g := range gs {
				if g.Thread != nil {
					continue
				}
				frames, _ := g.Stacktrace(5)
				for _, frame := range frames {
					// line 11 is the line where wg.Done is called
					if frame.Current.Fn != nil && frame.Current.Fn.Name == "main.sayhi" && frame.Current.Line < 11 {
						parkedg = g
						break
					}
				}
				if parkedg != nil {
					break
				}
			}
		}

		assertNoError(p.SwitchGoroutine(parkedg.ID), t, "SwitchGoroutine()")
		p.ClearBreakpoint(bp.Addr)
		assertNoError(proc.Next(p), t, "Next()")

		if p.SelectedGoroutine().ID != parkedg.ID {
			t.Fatalf("Next did not continue on the selected goroutine, expected %d got %d", parkedg.ID, p.SelectedGoroutine().ID)
		}
	})
}

func TestStepParked(t *testing.T) {
	protest.AllowRecording(t)
	withTestProcess("parallel_next", t, func(p proc.Process, fixture protest.Fixture) {
		bp, err := setFunctionBreakpoint(p, "main.sayhi")
		assertNoError(err, t, "SetBreakpoint()")

		// continue until a parked goroutine exists
		var parkedg *proc.G
	LookForParkedG:
		for {
			err := proc.Continue(p)
			if _, exited := err.(proc.ProcessExitedError); exited {
				t.Log("could not find parked goroutine")
				return
			}
			assertNoError(err, t, "Continue()")

			gs, err := proc.GoroutinesInfo(p)
			assertNoError(err, t, "GoroutinesInfo()")

			for _, g := range gs {
				if g.Thread == nil && g.CurrentLoc.Fn != nil && g.CurrentLoc.Fn.Name == "main.sayhi" {
					parkedg = g
					break LookForParkedG
				}
			}
		}

		t.Logf("Parked g is: %v\n", parkedg)
		frames, _ := parkedg.Stacktrace(20)
		for _, frame := range frames {
			name := ""
			if frame.Call.Fn != nil {
				name = frame.Call.Fn.Name
			}
			t.Logf("\t%s:%d in %s (%#x)", frame.Call.File, frame.Call.Line, name, frame.Current.PC)
		}

		assertNoError(p.SwitchGoroutine(parkedg.ID), t, "SwitchGoroutine()")
		p.ClearBreakpoint(bp.Addr)
		assertNoError(proc.Step(p), t, "Step()")

		if p.SelectedGoroutine().ID != parkedg.ID {
			t.Fatalf("Step did not continue on the selected goroutine, expected %d got %d", parkedg.ID, p.SelectedGoroutine().ID)
		}
	})
}

func TestIssue509(t *testing.T) {
	fixturesDir := protest.FindFixturesDir()
	nomaindir := filepath.Join(fixturesDir, "nomaindir")
	cmd := exec.Command("go", "build", "-gcflags=-N -l", "-o", "debug")
	cmd.Dir = nomaindir
	assertNoError(cmd.Run(), t, "go build")
	exepath := filepath.Join(nomaindir, "debug")
	_, err := native.Launch([]string{exepath}, ".")
	if err == nil {
		t.Fatalf("expected error but none was generated")
	}
	if err != proc.NotExecutableErr {
		t.Fatalf("expected error \"%v\" got \"%v\"", proc.NotExecutableErr, err)
	}
	os.Remove(exepath)
}

func TestUnsupportedArch(t *testing.T) {
	ver, _ := goversion.Parse(runtime.Version())
	if ver.Major < 0 || !ver.AfterOrEqual(goversion.GoVersion{1, 6, -1, 0, 0, ""}) || ver.AfterOrEqual(goversion.GoVersion{1, 7, -1, 0, 0, ""}) {
		// cross compile (with -N?) works only on select versions of go
		return
	}

	fixturesDir := protest.FindFixturesDir()
	infile := filepath.Join(fixturesDir, "math.go")
	outfile := filepath.Join(fixturesDir, "_math_debug_386")

	cmd := exec.Command("go", "build", "-gcflags=-N -l", "-o", outfile, infile)
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "GOARCH=") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	cmd.Env = append(cmd.Env, "GOARCH=386")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build failed: %v: %v", err, string(out))
	}
	defer os.Remove(outfile)

	p, err := native.Launch([]string{outfile}, ".")
	switch err {
	case proc.UnsupportedLinuxArchErr, proc.UnsupportedWindowsArchErr, proc.UnsupportedDarwinArchErr:
		// all good
	case nil:
		p.Detach(true)
		t.Fatal("Launch is expected to fail, but succeeded")
	default:
		t.Fatal(err)
	}
}

func TestIssue573(t *testing.T) {
	// calls to runtime.duffzero and runtime.duffcopy jump directly into the middle
	// of the function and the internal breakpoint set by StepInto may be missed.
	protest.AllowRecording(t)
	withTestProcess("issue573", t, func(p proc.Process, fixture protest.Fixture) {
		fentry, _ := proc.FindFunctionLocation(p, "main.foo", false, 0)
		_, err := p.SetBreakpoint(fentry, proc.UserBreakpoint, nil)
		assertNoError(err, t, "SetBreakpoint()")
		assertNoError(proc.Continue(p), t, "Continue()")
		assertNoError(proc.Step(p), t, "Step() #1")
		assertNoError(proc.Step(p), t, "Step() #2") // Bug exits here.
		assertNoError(proc.Step(p), t, "Step() #3") // Third step ought to be possible; program ought not have exited.
	})
}

func TestTestvariables2Prologue(t *testing.T) {
	withTestProcess("testvariables2", t, func(p proc.Process, fixture protest.Fixture) {
		addrEntry, err := proc.FindFunctionLocation(p, "main.main", false, 0)
		assertNoError(err, t, "FindFunctionLocation - entrypoint")
		addrPrologue, err := proc.FindFunctionLocation(p, "main.main", true, 0)
		assertNoError(err, t, "FindFunctionLocation - postprologue")
		if addrEntry == addrPrologue {
			t.Fatalf("Prologue detection failed on testvariables2.go/main.main")
		}
	})
}

func TestNextDeferReturnAndDirectCall(t *testing.T) {
	// Next should not step into a deferred function if it is called
	// directly, only if it is called through a panic or a deferreturn.
	// Here we test the case where the function is called by a deferreturn
	testseq("defercall", contNext, []nextTest{
		{9, 10},
		{10, 11},
		{11, 12},
		{12, 13},
		{13, 28}}, "main.callAndDeferReturn", t)
}

func TestNextPanicAndDirectCall(t *testing.T) {
	// Next should not step into a deferred function if it is called
	// directly, only if it is called through a panic or a deferreturn.
	// Here we test the case where the function is called by a panic
	testseq("defercall", contNext, []nextTest{
		{15, 16},
		{16, 17},
		{17, 18},
		{18, 5}}, "main.callAndPanic2", t)
}

func TestStepCall(t *testing.T) {
	testseq("testnextprog", contStep, []nextTest{
		{34, 13},
		{13, 14}}, "", t)
}

func TestStepCallPtr(t *testing.T) {
	// Tests that Step works correctly when calling functions with a
	// function pointer.
	testseq("teststepprog", contStep, []nextTest{
		{9, 10},
		{10, 5},
		{5, 6},
		{6, 7},
		{7, 11}}, "", t)
}

func TestStepReturnAndPanic(t *testing.T) {
	// Tests that Step works correctly when returning from functions
	// and when a deferred function is called when panic'ing.
	ver, _ := goversion.Parse(runtime.Version())
	if ver.Major > 0 && ver.AfterOrEqual(goversion.GoVersion{1, 9, -1, 0, 0, ""}) && !ver.AfterOrEqual(goversion.GoVersion{1, 10, -1, 0, 0, ""}) {
		testseq("defercall", contStep, []nextTest{
			{17, 5},
			{5, 6},
			{6, 7},
			{7, 17},
			{17, 18},
			{18, 5},
			{5, 6},
			{6, 7}}, "", t)

	} else {
		testseq("defercall", contStep, []nextTest{
			{17, 5},
			{5, 6},
			{6, 7},
			{7, 18},
			{18, 5},
			{5, 6},
			{6, 7}}, "", t)
	}
}

func TestStepDeferReturn(t *testing.T) {
	// Tests that Step works correctly when a deferred function is
	// called during a return.
	testseq("defercall", contStep, []nextTest{
		{11, 5},
		{5, 6},
		{6, 7},
		{7, 12},
		{12, 13},
		{13, 5},
		{5, 6},
		{6, 7},
		{7, 13},
		{13, 28}}, "", t)
}

func TestStepIgnorePrivateRuntime(t *testing.T) {
	// Tests that Step will ignore calls to private runtime functions
	// (such as runtime.convT2E in this case)
	ver, _ := goversion.Parse(runtime.Version())

	if ver.Major > 0 && ver.AfterOrEqual(goversion.GoVersion{1, 7, -1, 0, 0, ""}) && !ver.AfterOrEqual(goversion.GoVersion{1, 10, -1, 0, 0, ""}) {
		testseq("teststepprog", contStep, []nextTest{
			{21, 13},
			{13, 14},
			{14, 15},
			{15, 14},
			{14, 17},
			{17, 22}}, "", t)
	} else if ver.Major < 0 || ver.AfterOrEqual(goversion.GoVersion{1, 10, -1, 0, 0, ""}) {
		testseq("teststepprog", contStep, []nextTest{
			{21, 13},
			{13, 14},
			{14, 15},
			{15, 22}}, "", t)
	} else {
		testseq("teststepprog", contStep, []nextTest{
			{21, 13},
			{13, 14},
			{14, 15},
			{15, 17},
			{17, 22}}, "", t)
	}
}

func TestIssue561(t *testing.T) {
	// Step fails to make progress when PC is at a CALL instruction
	// where a breakpoint is also set.
	protest.AllowRecording(t)
	withTestProcess("issue561", t, func(p proc.Process, fixture protest.Fixture) {
		setFileBreakpoint(p, t, fixture, 10)
		assertNoError(proc.Continue(p), t, "Continue()")
		assertNoError(proc.Step(p), t, "Step()")
		assertLineNumber(p, t, 5, "wrong line number after Step,")
	})
}

func TestStepOut(t *testing.T) {
	testseq2(t, "testnextprog", "main.helloworld", []seqTest{{contContinue, 13}, {contStepout, 35}})
}

func TestStepConcurrentDirect(t *testing.T) {
	protest.AllowRecording(t)
	withTestProcess("teststepconcurrent", t, func(p proc.Process, fixture protest.Fixture) {
		pc, err := proc.FindFileLocation(p, fixture.Source, 37)
		assertNoError(err, t, "FindFileLocation()")
		bp, err := p.SetBreakpoint(pc, proc.UserBreakpoint, nil)
		assertNoError(err, t, "SetBreakpoint()")

		assertNoError(proc.Continue(p), t, "Continue()")
		_, err = p.ClearBreakpoint(bp.Addr)
		assertNoError(err, t, "ClearBreakpoint()")

		for _, b := range p.Breakpoints().M {
			if b.Name == proc.UnrecoveredPanic {
				_, err := p.ClearBreakpoint(b.Addr)
				assertNoError(err, t, "ClearBreakpoint(unrecovered-panic)")
				break
			}
		}

		gid := p.SelectedGoroutine().ID

		seq := []int{37, 38, 13, 15, 16, 38}

		i := 0
		count := 0
		for {
			anyerr := false
			if p.SelectedGoroutine().ID != gid {
				t.Errorf("Step switched to different goroutine %d %d\n", gid, p.SelectedGoroutine().ID)
				anyerr = true
			}
			f, ln := currentLineNumber(p, t)
			if ln != seq[i] {
				if i == 1 && ln == 40 {
					// loop exited
					break
				}
				frames, err := proc.ThreadStacktrace(p.CurrentThread(), 20)
				if err != nil {
					t.Errorf("Could not get stacktrace of goroutine %d\n", p.SelectedGoroutine().ID)
				} else {
					t.Logf("Goroutine %d (thread: %d):", p.SelectedGoroutine().ID, p.CurrentThread().ThreadID())
					for _, frame := range frames {
						t.Logf("\t%s:%d (%#x)", frame.Call.File, frame.Call.Line, frame.Current.PC)
					}
				}
				t.Errorf("Program did not continue at expected location (%d) %s:%d [i %d count %d]", seq[i], f, ln, i, count)
				anyerr = true
			}
			if anyerr {
				t.FailNow()
			}
			i = (i + 1) % len(seq)
			if i == 0 {
				count++
			}
			assertNoError(proc.Step(p), t, "Step()")
		}

		if count != 100 {
			t.Fatalf("Program did not loop expected number of times: %d", count)
		}
	})
}

func TestStepConcurrentPtr(t *testing.T) {
	protest.AllowRecording(t)
	withTestProcess("teststepconcurrent", t, func(p proc.Process, fixture protest.Fixture) {
		pc, err := proc.FindFileLocation(p, fixture.Source, 24)
		assertNoError(err, t, "FindFileLocation()")
		_, err = p.SetBreakpoint(pc, proc.UserBreakpoint, nil)
		assertNoError(err, t, "SetBreakpoint()")

		for _, b := range p.Breakpoints().M {
			if b.Name == proc.UnrecoveredPanic {
				_, err := p.ClearBreakpoint(b.Addr)
				assertNoError(err, t, "ClearBreakpoint(unrecovered-panic)")
				break
			}
		}

		kvals := map[int]int64{}
		count := 0
		for {
			err := proc.Continue(p)
			_, exited := err.(proc.ProcessExitedError)
			if exited {
				break
			}
			assertNoError(err, t, "Continue()")

			f, ln := currentLineNumber(p, t)
			if ln != 24 {
				for _, th := range p.ThreadList() {
					t.Logf("thread %d stopped on breakpoint %v", th.ThreadID(), th.Breakpoint())
				}
				curbp := p.CurrentThread().Breakpoint()
				t.Fatalf("Program did not continue at expected location (24): %s:%d %#x [%v] (gid %d count %d)", f, ln, currentPC(p, t), curbp, p.SelectedGoroutine().ID, count)
			}

			gid := p.SelectedGoroutine().ID

			kvar := evalVariable(p, t, "k")
			k, _ := constant.Int64Val(kvar.Value)

			if oldk, ok := kvals[gid]; ok {
				if oldk >= k {
					t.Fatalf("Goroutine %d did not make progress?", gid)
				}
			}
			kvals[gid] = k

			assertNoError(proc.Step(p), t, "Step()")
			for p.Breakpoints().HasInternalBreakpoints() {
				if p.SelectedGoroutine().ID == gid {
					t.Fatalf("step did not step into function call (but internal breakpoints still active?) (%d %d)", gid, p.SelectedGoroutine().ID)
				}
				assertNoError(proc.Continue(p), t, "Continue()")
			}

			if p.SelectedGoroutine().ID != gid {
				t.Fatalf("Step switched goroutines (wanted: %d got: %d)", gid, p.SelectedGoroutine().ID)
			}

			f, ln = assertLineNumber(p, t, 13, "Step did not step into function call")

			count++
			if count > 50 {
				// this test could potentially go on for 10000 cycles, since that's
				// too slow we cut the execution after 50 cycles
				break
			}
		}

		if count == 0 {
			t.Fatalf("Breakpoint never hit")
		}
	})
}

func TestStepOutDefer(t *testing.T) {
	protest.AllowRecording(t)
	withTestProcess("testnextdefer", t, func(p proc.Process, fixture protest.Fixture) {
		pc, err := proc.FindFileLocation(p, fixture.Source, 9)
		assertNoError(err, t, "FindFileLocation()")
		bp, err := p.SetBreakpoint(pc, proc.UserBreakpoint, nil)
		assertNoError(err, t, "SetBreakpoint()")
		assertNoError(proc.Continue(p), t, "Continue()")
		p.ClearBreakpoint(bp.Addr)

		assertLineNumber(p, t, 9, "wrong line number")

		assertNoError(proc.StepOut(p), t, "StepOut()")

		f, l, _ := p.BinInfo().PCToLine(currentPC(p, t))
		if f == fixture.Source || l == 6 {
			t.Fatalf("wrong location %s:%d, expected to end somewhere in runtime", f, l)
		}
	})
}

func TestStepOutDeferReturnAndDirectCall(t *testing.T) {
	// StepOut should not step into a deferred function if it is called
	// directly, only if it is called through a panic.
	// Here we test the case where the function is called by a deferreturn
	testseq2(t, "defercall", "", []seqTest{
		{contContinue, 11},
		{contStepout, 28}})
}

const maxInstructionLength uint64 = 15

func TestStepOnCallPtrInstr(t *testing.T) {
	protest.AllowRecording(t)
	withTestProcess("teststepprog", t, func(p proc.Process, fixture protest.Fixture) {
		pc, err := proc.FindFileLocation(p, fixture.Source, 10)
		assertNoError(err, t, "FindFileLocation()")
		_, err = p.SetBreakpoint(pc, proc.UserBreakpoint, nil)
		assertNoError(err, t, "SetBreakpoint()")

		assertNoError(proc.Continue(p), t, "Continue()")

		found := false

		for {
			_, ln := currentLineNumber(p, t)
			if ln != 10 {
				break
			}
			regs, err := p.CurrentThread().Registers(false)
			assertNoError(err, t, "Registers()")
			pc := regs.PC()
			text, err := proc.Disassemble(p, nil, pc, pc+maxInstructionLength)
			assertNoError(err, t, "Disassemble()")
			if text[0].IsCall() {
				found = true
				break
			}
			assertNoError(p.StepInstruction(), t, "StepInstruction()")
		}

		if !found {
			t.Fatal("Could not find CALL instruction")
		}

		assertNoError(proc.Step(p), t, "Step()")

		assertLineNumber(p, t, 5, "Step continued to wrong line,")
	})
}

func TestIssue594(t *testing.T) {
	if runtime.GOOS == "darwin" && testBackend == "lldb" {
		// debugserver will receive an EXC_BAD_ACCESS for this, at that point
		// there is no way to reconvert this exception into a unix signal and send
		// it to the process.
		// This is a bug in debugserver/lldb:
		//  https://bugs.llvm.org//show_bug.cgi?id=22868
		return
	}
	// Exceptions that aren't caused by breakpoints should be propagated
	// back to the target.
	// In particular the target should be able to cause a nil pointer
	// dereference panic and recover from it.
	protest.AllowRecording(t)
	withTestProcess("issue594", t, func(p proc.Process, fixture protest.Fixture) {
		assertNoError(proc.Continue(p), t, "Continue()")
		var f string
		var ln int
		if testBackend == "rr" {
			frame, err := findFirstNonRuntimeFrame(p)
			assertNoError(err, t, "findFirstNonRuntimeFrame")
			f, ln = frame.Current.File, frame.Current.Line
		} else {
			f, ln = currentLineNumber(p, t)
		}
		if ln != 21 {
			t.Fatalf("Program stopped at %s:%d, expected :21", f, ln)
		}
	})
}

func TestStepOutPanicAndDirectCall(t *testing.T) {
	// StepOut should not step into a deferred function if it is called
	// directly, only if it is called through a panic.
	// Here we test the case where the function is called by a panic
	testseq2(t, "defercall", "", []seqTest{
		{contContinue, 17},
		{contStepout, 5}})
}

func TestWorkDir(t *testing.T) {
	wd := os.TempDir()
	// For Darwin `os.TempDir()` returns `/tmp` which is symlink to `/private/tmp`.
	if runtime.GOOS == "darwin" {
		wd = "/private/tmp"
	}
	protest.AllowRecording(t)
	withTestProcessArgs("workdir", t, wd, []string{}, 0, func(p proc.Process, fixture protest.Fixture) {
		addr, _, err := p.BinInfo().LineToPC(fixture.Source, 14)
		assertNoError(err, t, "LineToPC")
		p.SetBreakpoint(addr, proc.UserBreakpoint, nil)
		proc.Continue(p)
		v := evalVariable(p, t, "pwd")
		str := constant.StringVal(v.Value)
		if wd != str {
			t.Fatalf("Expected %s got %s\n", wd, str)
		}
	})
}

func TestNegativeIntEvaluation(t *testing.T) {
	testcases := []struct {
		name  string
		typ   string
		value interface{}
	}{
		{"ni8", "int8", int64(-5)},
		{"ni16", "int16", int64(-5)},
		{"ni32", "int32", int64(-5)},
	}
	protest.AllowRecording(t)
	withTestProcess("testvariables2", t, func(p proc.Process, fixture protest.Fixture) {
		assertNoError(proc.Continue(p), t, "Continue()")
		for _, tc := range testcases {
			v := evalVariable(p, t, tc.name)
			if typ := v.RealType.String(); typ != tc.typ {
				t.Fatalf("Wrong type for variable %q: %q (expected: %q)", tc.name, typ, tc.typ)
			}
			if val, _ := constant.Int64Val(v.Value); val != tc.value {
				t.Fatalf("Wrong value for variable %q: %v (expected: %v)", tc.name, val, tc.value)
			}
		}
	})
}

func TestIssue683(t *testing.T) {
	// Step panics when source file can not be found
	protest.AllowRecording(t)
	withTestProcess("issue683", t, func(p proc.Process, fixture protest.Fixture) {
		_, err := setFunctionBreakpoint(p, "main.main")
		assertNoError(err, t, "setFunctionBreakpoint()")
		assertNoError(proc.Continue(p), t, "First Continue()")
		for i := 0; i < 20; i++ {
			// eventually an error about the source file not being found will be
			// returned, the important thing is that we shouldn't panic
			err := proc.Step(p)
			if err != nil {
				break
			}
		}
	})
}

func TestIssue664(t *testing.T) {
	protest.AllowRecording(t)
	withTestProcess("issue664", t, func(p proc.Process, fixture protest.Fixture) {
		setFileBreakpoint(p, t, fixture, 4)
		assertNoError(proc.Continue(p), t, "Continue()")
		assertNoError(proc.Next(p), t, "Next()")
		assertLineNumber(p, t, 5, "Did not continue to correct location,")
	})
}

// Benchmarks (*Processs).Continue + (*Scope).FunctionArguments
func BenchmarkTrace(b *testing.B) {
	protest.AllowRecording(b)
	withTestProcess("traceperf", b, func(p proc.Process, fixture protest.Fixture) {
		_, err := setFunctionBreakpoint(p, "main.PerfCheck")
		assertNoError(err, b, "setFunctionBreakpoint()")
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			assertNoError(proc.Continue(p), b, "Continue()")
			s, err := proc.GoroutineScope(p.CurrentThread())
			assertNoError(err, b, "Scope()")
			_, err = s.FunctionArguments(proc.LoadConfig{false, 0, 64, 0, 3})
			assertNoError(err, b, "FunctionArguments()")
		}
		b.StopTimer()
	})
}

func TestNextInDeferReturn(t *testing.T) {
	// runtime.deferreturn updates the G struct in a way that for one
	// instruction leaves the curg._defer field non-nil but with curg._defer.fn
	// field being nil.
	// We need to deal with this without panicing.
	protest.AllowRecording(t)
	withTestProcess("defercall", t, func(p proc.Process, fixture protest.Fixture) {
		_, err := setFunctionBreakpoint(p, "runtime.deferreturn")
		assertNoError(err, t, "setFunctionBreakpoint()")
		assertNoError(proc.Continue(p), t, "First Continue()")
		for i := 0; i < 20; i++ {
			assertNoError(proc.Next(p), t, fmt.Sprintf("Next() %d", i))
		}
	})
}

func getg(goid int, gs []*proc.G) *proc.G {
	for _, g := range gs {
		if g.ID == goid {
			return g
		}
	}
	return nil
}

func TestStacktraceWithBarriers(t *testing.T) {
	// Go's Garbage Collector will insert stack barriers into stacks.
	// This stack barrier is inserted by overwriting the return address for the
	// stack frame with the address of runtime.stackBarrier.
	// The original return address is saved into the stkbar slice inside the G
	// struct.

	// In Go 1.9 stack barriers have been removed and this test must be disabled.
	if ver, _ := goversion.Parse(runtime.Version()); ver.Major < 0 || ver.AfterOrEqual(goversion.GoVersion{1, 9, -1, 0, 0, ""}) {
		return
	}

	// In Go 1.8 stack barriers are not inserted by default, this enables them.
	godebugOld := os.Getenv("GODEBUG")
	defer os.Setenv("GODEBUG", godebugOld)
	os.Setenv("GODEBUG", "gcrescanstacks=1")

	withTestProcess("binarytrees", t, func(p proc.Process, fixture protest.Fixture) {
		// We want to get a user goroutine with a stack barrier, to get that we execute the program until runtime.gcInstallStackBarrier is executed AND the goroutine it was executed onto contains a call to main.bottomUpTree
		_, err := setFunctionBreakpoint(p, "runtime.gcInstallStackBarrier")
		assertNoError(err, t, "setFunctionBreakpoint()")
		stackBarrierGoids := []int{}
		for len(stackBarrierGoids) == 0 {
			err := proc.Continue(p)
			if _, exited := err.(proc.ProcessExitedError); exited {
				t.Logf("Could not run test")
				return
			}
			assertNoError(err, t, "Continue()")
			gs, err := proc.GoroutinesInfo(p)
			assertNoError(err, t, "GoroutinesInfo()")
			for _, th := range p.ThreadList() {
				if bp := th.Breakpoint(); bp.Breakpoint == nil {
					continue
				}

				goidVar := evalVariable(p, t, "gp.goid")
				goid, _ := constant.Int64Val(goidVar.Value)

				if g := getg(int(goid), gs); g != nil {
					stack, err := g.Stacktrace(50)
					assertNoError(err, t, fmt.Sprintf("Stacktrace(goroutine = %d)", goid))
					for _, frame := range stack {
						if frame.Current.Fn != nil && frame.Current.Fn.Name == "main.bottomUpTree" {
							stackBarrierGoids = append(stackBarrierGoids, int(goid))
							break
						}
					}
				}
			}
		}

		if len(stackBarrierGoids) == 0 {
			t.Fatalf("Could not find a goroutine with stack barriers")
		}

		t.Logf("stack barrier goids: %v\n", stackBarrierGoids)

		assertNoError(proc.StepOut(p), t, "StepOut()")

		gs, err := proc.GoroutinesInfo(p)
		assertNoError(err, t, "GoroutinesInfo()")

		for _, goid := range stackBarrierGoids {
			g := getg(goid, gs)

			stack, err := g.Stacktrace(200)
			assertNoError(err, t, "Stacktrace()")

			// Check that either main.main or main.main.func1 appear in the
			// stacktrace of this goroutine, if we failed at resolving stack barriers
			// correctly the stacktrace will be truncated and neither main.main or
			// main.main.func1 will appear
			found := false
			for _, frame := range stack {
				if frame.Current.Fn == nil {
					continue
				}
				if name := frame.Current.Fn.Name; name == "main.main" || name == "main.main.func1" {
					found = true
				}
			}

			t.Logf("Stacktrace for %d:\n", goid)
			for _, frame := range stack {
				name := "<>"
				if frame.Current.Fn != nil {
					name = frame.Current.Fn.Name
				}
				t.Logf("\t%s [CFA: %x Ret: %x] at %s:%d", name, frame.Regs.CFA, frame.Ret, frame.Current.File, frame.Current.Line)
			}

			if !found {
				t.Logf("Truncated stacktrace for %d\n", goid)
			}
		}
	})
}

func TestAttachDetach(t *testing.T) {
	if testBackend == "lldb" && runtime.GOOS == "linux" {
		bs, _ := ioutil.ReadFile("/proc/sys/kernel/yama/ptrace_scope")
		if bs == nil || strings.TrimSpace(string(bs)) != "0" {
			t.Logf("can not run TestAttachDetach: %v\n", bs)
			return
		}
	}
	if testBackend == "rr" {
		return
	}
	fixture := protest.BuildFixture("testnextnethttp", 0)
	cmd := exec.Command(fixture.Path)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	assertNoError(cmd.Start(), t, "starting fixture")

	// wait for testnextnethttp to start listening
	t0 := time.Now()
	for {
		conn, err := net.Dial("tcp", "localhost:9191")
		if err == nil {
			conn.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
		if time.Since(t0) > 10*time.Second {
			t.Fatal("fixture did not start")
		}
	}

	var p proc.Process
	var err error

	switch testBackend {
	case "native":
		p, err = native.Attach(cmd.Process.Pid)
	case "lldb":
		path := ""
		if runtime.GOOS == "darwin" {
			path = fixture.Path
		}
		p, err = gdbserial.LLDBAttach(cmd.Process.Pid, path)
	default:
		err = fmt.Errorf("unknown backend %q", testBackend)
	}

	assertNoError(err, t, "Attach")
	go func() {
		time.Sleep(1 * time.Second)
		http.Get("http://localhost:9191")
	}()

	assertNoError(proc.Continue(p), t, "Continue")
	assertLineNumber(p, t, 11, "Did not continue to correct location,")

	assertNoError(p.Detach(false), t, "Detach")

	resp, err := http.Get("http://localhost:9191/nobp")
	assertNoError(err, t, "Page request after detach")
	bs, err := ioutil.ReadAll(resp.Body)
	assertNoError(err, t, "Reading /nobp page")
	if out := string(bs); !strings.Contains(out, "hello, world!") {
		t.Fatalf("/nobp page does not contain \"hello, world!\": %q", out)
	}

	cmd.Process.Kill()
}

func TestVarSum(t *testing.T) {
	protest.AllowRecording(t)
	withTestProcess("testvariables2", t, func(p proc.Process, fixture protest.Fixture) {
		assertNoError(proc.Continue(p), t, "Continue()")
		sumvar := evalVariable(p, t, "s1[0] + s1[1]")
		sumvarstr := constant.StringVal(sumvar.Value)
		if sumvarstr != "onetwo" {
			t.Fatalf("s1[0] + s1[1] == %q (expected \"onetwo\")", sumvarstr)
		}
		if sumvar.Len != int64(len(sumvarstr)) {
			t.Fatalf("sumvar.Len == %d (expected %d)", sumvar.Len, len(sumvarstr))
		}
	})
}

func TestPackageWithPathVar(t *testing.T) {
	protest.AllowRecording(t)
	withTestProcess("pkgrenames", t, func(p proc.Process, fixture protest.Fixture) {
		assertNoError(proc.Continue(p), t, "Continue()")
		evalVariable(p, t, "pkg.SomeVar")
		evalVariable(p, t, "pkg.SomeVar.X")
	})
}

func TestEnvironment(t *testing.T) {
	protest.AllowRecording(t)
	os.Setenv("SOMEVAR", "bah")
	withTestProcess("testenv", t, func(p proc.Process, fixture protest.Fixture) {
		assertNoError(proc.Continue(p), t, "Continue()")
		v := evalVariable(p, t, "x")
		vv := constant.StringVal(v.Value)
		t.Logf("v = %q", vv)
		if vv != "bah" {
			t.Fatalf("value of v is %q (expected \"bah\")", vv)
		}
	})
}

func getFrameOff(p proc.Process, t *testing.T) int64 {
	frameoffvar := evalVariable(p, t, "runtime.frameoff")
	frameoff, _ := constant.Int64Val(frameoffvar.Value)
	return frameoff
}

func TestRecursiveNext(t *testing.T) {
	protest.AllowRecording(t)
	testcases := []nextTest{
		{6, 7},
		{7, 10},
		{10, 11},
		{11, 17},
	}
	testseq("increment", contNext, testcases, "main.Increment", t)

	withTestProcess("increment", t, func(p proc.Process, fixture protest.Fixture) {
		bp, err := setFunctionBreakpoint(p, "main.Increment")
		assertNoError(err, t, "setFunctionBreakpoint")
		assertNoError(proc.Continue(p), t, "Continue")
		_, err = p.ClearBreakpoint(bp.Addr)
		assertNoError(err, t, "ClearBreakpoint")
		assertNoError(proc.Next(p), t, "Next 1")
		assertNoError(proc.Next(p), t, "Next 2")
		assertNoError(proc.Next(p), t, "Next 3")
		frameoff0 := getFrameOff(p, t)
		assertNoError(proc.Step(p), t, "Step")
		frameoff1 := getFrameOff(p, t)
		if frameoff0 == frameoff1 {
			t.Fatalf("did not step into function?")
		}
		assertLineNumber(p, t, 6, "program did not continue to expected location,")
		assertNoError(proc.Next(p), t, "Next 4")
		assertLineNumber(p, t, 7, "program did not continue to expected location,")
		assertNoError(proc.StepOut(p), t, "StepOut")
		assertLineNumber(p, t, 11, "program did not continue to expected location,")
		frameoff2 := getFrameOff(p, t)
		if frameoff0 != frameoff2 {
			t.Fatalf("frame offset mismatch %x != %x", frameoff0, frameoff2)
		}
	})
}

// TestIssue877 ensures that the environment variables starting with DYLD_ and LD_
// are passed when executing the binary on OSX via debugserver
func TestIssue877(t *testing.T) {
	if runtime.GOOS != "darwin" && testBackend == "lldb" {
		return
	}
	const envval = "/usr/local/lib"
	os.Setenv("DYLD_LIBRARY_PATH", envval)
	withTestProcess("issue877", t, func(p proc.Process, fixture protest.Fixture) {
		assertNoError(proc.Continue(p), t, "Continue()")
		v := evalVariable(p, t, "dyldenv")
		vv := constant.StringVal(v.Value)
		t.Logf("v = %q", vv)
		if vv != envval {
			t.Fatalf("value of v is %q (expected %q)", vv, envval)
		}
	})
}

func TestIssue893(t *testing.T) {
	// Test what happens when next is called immediately after launching the
	// executable, acceptable behaviors are: (a) no error, (b) no source at PC
	// error, (c) program runs to completion
	protest.AllowRecording(t)
	withTestProcess("increment", t, func(p proc.Process, fixture protest.Fixture) {
		err := proc.Next(p)
		if err == nil {
			return
		}
		if _, ok := err.(*frame.NoFDEForPCError); ok {
			return
		}
		if _, ok := err.(proc.ThreadBlockedError); ok {
			return
		}
		if _, ok := err.(*proc.NoSourceForPCError); ok {
			return
		}
		if _, ok := err.(proc.ProcessExitedError); ok {
			return
		}
		assertNoError(err, t, "Next")
	})
}

func TestStepInstructionNoGoroutine(t *testing.T) {
	protest.AllowRecording(t)
	withTestProcess("increment", t, func(p proc.Process, fixture protest.Fixture) {
		// Call StepInstruction immediately after launching the program, it should
		// work even though no goroutine is selected.
		assertNoError(p.StepInstruction(), t, "StepInstruction")
	})
}

func TestIssue871(t *testing.T) {
	protest.AllowRecording(t)
	withTestProcess("issue871", t, func(p proc.Process, fixture protest.Fixture) {
		assertNoError(proc.Continue(p), t, "Continue")

		var scope *proc.EvalScope
		var err error
		if testBackend == "rr" {
			var frame proc.Stackframe
			frame, err = findFirstNonRuntimeFrame(p)
			if err == nil {
				scope = proc.FrameToScope(p.BinInfo(), p.CurrentThread(), nil, frame)
			}
		} else {
			scope, err = proc.GoroutineScope(p.CurrentThread())
		}
		assertNoError(err, t, "scope")

		locals, err := scope.LocalVariables(normalLoadConfig)
		assertNoError(err, t, "LocalVariables")

		foundA, foundB := false, false

		for _, v := range locals {
			t.Logf("local %v", v)
			switch v.Name {
			case "a":
				foundA = true
				if v.Flags&proc.VariableEscaped == 0 {
					t.Errorf("variable a not flagged as escaped")
				}
			case "b":
				foundB = true
			}
		}

		if !foundA {
			t.Errorf("variable a not found")
		}

		if !foundB {
			t.Errorf("variable b not found")
		}
	})
}

func TestShadowedFlag(t *testing.T) {
	if ver, _ := goversion.Parse(runtime.Version()); ver.Major >= 0 && !ver.AfterOrEqual(goversion.GoVersion{1, 9, -1, 0, 0, ""}) {
		return
	}
	withTestProcess("testshadow", t, func(p proc.Process, fixture protest.Fixture) {
		assertNoError(proc.Continue(p), t, "Continue")
		scope, err := proc.GoroutineScope(p.CurrentThread())
		assertNoError(err, t, "GoroutineScope")
		locals, err := scope.LocalVariables(normalLoadConfig)
		assertNoError(err, t, "LocalVariables")
		foundShadowed := false
		foundNonShadowed := false
		for _, v := range locals {
			if v.Flags&proc.VariableShadowed != 0 {
				if v.Name != "a" {
					t.Errorf("wrong shadowed variable %s", v.Name)
				}
				foundShadowed = true
				if n, _ := constant.Int64Val(v.Value); n != 0 {
					t.Errorf("wrong value for shadowed variable a: %d", n)
				}
			} else {
				if v.Name != "a" {
					t.Errorf("wrong non-shadowed variable %s", v.Name)
				}
				foundNonShadowed = true
				if n, _ := constant.Int64Val(v.Value); n != 1 {
					t.Errorf("wrong value for non-shadowed variable a: %d", n)
				}
			}
		}
		if !foundShadowed {
			t.Error("could not find any shadowed variable")
		}
		if !foundNonShadowed {
			t.Error("could not find any non-shadowed variable")
		}
	})
}

func TestAttachStripped(t *testing.T) {
	if testBackend == "lldb" && runtime.GOOS == "linux" {
		bs, _ := ioutil.ReadFile("/proc/sys/kernel/yama/ptrace_scope")
		if bs == nil || strings.TrimSpace(string(bs)) != "0" {
			t.Logf("can not run TestAttachStripped: %v\n", bs)
			return
		}
	}
	if testBackend == "rr" {
		return
	}
	if runtime.GOOS == "darwin" {
		t.Log("-s does not produce stripped executables on macOS")
		return
	}
	fixture := protest.BuildFixture("testnextnethttp", protest.LinkStrip)
	cmd := exec.Command(fixture.Path)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	assertNoError(cmd.Start(), t, "starting fixture")

	// wait for testnextnethttp to start listening
	t0 := time.Now()
	for {
		conn, err := net.Dial("tcp", "localhost:9191")
		if err == nil {
			conn.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
		if time.Since(t0) > 10*time.Second {
			t.Fatal("fixture did not start")
		}
	}

	var p proc.Process
	var err error

	switch testBackend {
	case "native":
		p, err = native.Attach(cmd.Process.Pid)
	case "lldb":
		path := ""
		if runtime.GOOS == "darwin" {
			path = fixture.Path
		}
		p, err = gdbserial.LLDBAttach(cmd.Process.Pid, path)
	default:
		t.Fatalf("unknown backend %q", testBackend)
	}

	t.Logf("error is %v", err)

	if err == nil {
		p.Detach(true)
		t.Fatalf("expected error after attach, got nothing")
	} else {
		cmd.Process.Kill()
	}
	os.Remove(fixture.Path)
}

func TestIssue844(t *testing.T) {
	// Conditional breakpoints should not prevent next from working if their
	// condition isn't met.
	withTestProcess("nextcond", t, func(p proc.Process, fixture protest.Fixture) {
		setFileBreakpoint(p, t, fixture, 9)
		condbp := setFileBreakpoint(p, t, fixture, 10)
		condbp.Cond = &ast.BinaryExpr{
			Op: token.EQL,
			X:  &ast.Ident{Name: "n"},
			Y:  &ast.BasicLit{Kind: token.INT, Value: "11"},
		}
		assertNoError(proc.Continue(p), t, "Continue")
		assertNoError(proc.Next(p), t, "Next")
		assertLineNumber(p, t, 10, "continued to wrong location,")
	})
}

func logStacktrace(t *testing.T, frames []proc.Stackframe) {
	for j := range frames {
		name := "?"
		if frames[j].Current.Fn != nil {
			name = frames[j].Current.Fn.Name
		}

		t.Logf("\t%#x %#x %#x %s at %s:%d\n", frames[j].Call.PC, frames[j].FrameOffset(), frames[j].FramePointerOffset(), name, filepath.Base(frames[j].Call.File), frames[j].Call.Line)
	}
}

// stacktraceCheck checks that all the functions listed in tc appear in
// frames in the same order.
// Checks that all the functions in tc starting with "C." or with "!" are in
// a systemstack frame.
// Returns a slice m where m[i] is the index in frames of the function tc[i]
// or nil if any check fails.
func stacktraceCheck(t *testing.T, tc []string, frames []proc.Stackframe) []int {
	m := make([]int, len(tc))
	i, j := 0, 0
	for i < len(tc) {
		tcname := tc[i]
		tcsystem := strings.HasPrefix(tcname, "C.")
		if tcname[0] == '!' {
			tcsystem = true
			tcname = tcname[1:]
		}
		for j < len(frames) {
			name := "?"
			if frames[j].Current.Fn != nil {
				name = frames[j].Current.Fn.Name
			}
			if name == tcname {
				m[i] = j
				if tcsystem != frames[j].SystemStack {
					t.Logf("system stack check failed for frame %d (expected %v got %v)", j, tcsystem, frames[j].SystemStack)
					t.Logf("expected: %v\n", tc)
					return nil
				}
				break
			}

			j++
		}
		if j >= len(frames) {
			t.Logf("couldn't find frame %d %s", i, tc)
			t.Logf("expected: %v\n", tc)
			return nil
		}

		i++
	}
	return m
}

func frameInFile(frame proc.Stackframe, file string) bool {
	for _, loc := range []proc.Location{frame.Current, frame.Call} {
		if !strings.HasSuffix(loc.File, "/"+file) && !strings.HasSuffix(loc.File, "\\"+file) {
			return false
		}
		if loc.Line <= 0 {
			return false
		}
	}
	return true
}

func TestCgoStacktrace(t *testing.T) {
	if runtime.GOOS == "windows" {
		ver, _ := goversion.Parse(runtime.Version())
		if ver.Major > 0 && !ver.AfterOrEqual(goversion.GoVersion{1, 9, -1, 0, 0, ""}) {
			t.Skip("disabled on windows with go before version 1.9")
		}
	}
	if runtime.GOOS == "darwin" {
		ver, _ := goversion.Parse(runtime.Version())
		if ver.Major > 0 && !ver.AfterOrEqual(goversion.GoVersion{1, 8, -1, 0, 0, ""}) {
			t.Skip("disabled on macOS with go before version 1.8")
		}
	}

	// Tests that:
	// a) we correctly identify the goroutine while we are executing cgo code
	// b) that we can stitch together the system stack (where cgo code
	// executes) and the normal goroutine stack

	// Each test case describes how the stack trace should appear after a
	// continue. The first function on each test case is the topmost function
	// that should be found on the stack, the actual stack trace can have more
	// frame than those listed here but all the frames listed must appear in
	// the specified order.
	testCases := [][]string{
		[]string{"main.main"},
		[]string{"C.helloworld_pt2", "C.helloworld", "main.main"},
		[]string{"main.helloWorldS", "main.helloWorld", "C.helloworld_pt2", "C.helloworld", "main.main"},
		[]string{"C.helloworld_pt4", "C.helloworld_pt3", "main.helloWorldS", "main.helloWorld", "C.helloworld_pt2", "C.helloworld", "main.main"},
		[]string{"main.helloWorld2", "C.helloworld_pt4", "C.helloworld_pt3", "main.helloWorldS", "main.helloWorld", "C.helloworld_pt2", "C.helloworld", "main.main"}}

	var gid int

	frameOffs := map[string]int64{}
	framePointerOffs := map[string]int64{}

	withTestProcess("cgostacktest/", t, func(p proc.Process, fixture protest.Fixture) {
		for itidx, tc := range testCases {
			assertNoError(proc.Continue(p), t, fmt.Sprintf("Continue at iteration step %d", itidx))

			g, err := proc.GetG(p.CurrentThread())
			assertNoError(err, t, fmt.Sprintf("GetG at iteration step %d", itidx))

			if itidx == 0 {
				gid = g.ID
			} else {
				if gid != g.ID {
					t.Fatalf("wrong goroutine id at iteration step %d (expected %d got %d)", itidx, gid, g.ID)
				}
			}

			frames, err := g.Stacktrace(100)
			assertNoError(err, t, fmt.Sprintf("Stacktrace at iteration step %d", itidx))

			t.Logf("iteration step %d", itidx)
			logStacktrace(t, frames)

			m := stacktraceCheck(t, tc, frames)
			mismatch := (m == nil)

			for i, j := range m {
				if strings.HasPrefix(tc[i], "C.hellow") {
					if !frameInFile(frames[j], "hello.c") {
						t.Logf("position in %q is %s:%d (call %s:%d)", tc[i], frames[j].Current.File, frames[j].Current.Line, frames[j].Call.File, frames[j].Call.Line)
						mismatch = true
						break
					}
				}
				if frameOff, ok := frameOffs[tc[i]]; ok {
					if frameOff != frames[j].FrameOffset() {
						t.Logf("frame %s offset mismatch", tc[i])
					}
					if framePointerOffs[tc[i]] != frames[j].FramePointerOffset() {
						t.Logf("frame %s pointer offset mismatch", tc[i])
					}
				} else {
					frameOffs[tc[i]] = frames[j].FrameOffset()
					framePointerOffs[tc[i]] = frames[j].FramePointerOffset()
				}
			}

			// also check that ThreadStacktrace produces the same list of frames
			threadFrames, err := proc.ThreadStacktrace(p.CurrentThread(), 100)
			assertNoError(err, t, fmt.Sprintf("ThreadStacktrace at iteration step %d", itidx))

			if len(threadFrames) != len(frames) {
				mismatch = true
			} else {
				for j := range frames {
					if frames[j].Current.File != threadFrames[j].Current.File || frames[j].Current.Line != threadFrames[j].Current.Line {
						t.Logf("stack mismatch between goroutine stacktrace and thread stacktrace")
						t.Logf("thread stacktrace:")
						logStacktrace(t, threadFrames)
						mismatch = true
						break
					}
				}
			}
			if mismatch {
				t.Fatal("see previous loglines")
			}
		}
	})
}

func TestCgoSources(t *testing.T) {
	if runtime.GOOS == "windows" {
		ver, _ := goversion.Parse(runtime.Version())
		if ver.Major > 0 && !ver.AfterOrEqual(goversion.GoVersion{1, 9, -1, 0, 0, ""}) {
			t.Skip("disabled on windows with go before version 1.9")
		}
	}

	withTestProcess("cgostacktest/", t, func(p proc.Process, fixture protest.Fixture) {
		sources := p.BinInfo().Sources
		for _, needle := range []string{"main.go", "hello.c"} {
			found := false
			for _, k := range sources {
				if strings.HasSuffix(k, "/"+needle) || strings.HasSuffix(k, "\\"+needle) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("File %s not found", needle)
			}
		}
	})
}

func TestSystemstackStacktrace(t *testing.T) {
	// check that we can follow a stack switch initiated by runtime.systemstack()
	withTestProcess("panic", t, func(p proc.Process, fixture protest.Fixture) {
		_, err := setFunctionBreakpoint(p, "runtime.startpanic_m")
		assertNoError(err, t, "setFunctionBreakpoint()")
		assertNoError(proc.Continue(p), t, "first continue")
		assertNoError(proc.Continue(p), t, "second continue")
		g, err := proc.GetG(p.CurrentThread())
		assertNoError(err, t, "GetG")
		frames, err := g.Stacktrace(100)
		assertNoError(err, t, "stacktrace")
		logStacktrace(t, frames)
		m := stacktraceCheck(t, []string{"!runtime.startpanic_m", "runtime.startpanic", "main.main"}, frames)
		if m == nil {
			t.Fatal("see previous loglines")
		}
	})
}

func TestSystemstackOnRuntimeNewstack(t *testing.T) {
	// The bug being tested here manifests as follows:
	// - set a breakpoint somewhere or interrupt the program with Ctrl-C
	// - try to look at stacktraces of other goroutines
	// If one of the other goroutines is resizing its own stack the stack
	// command won't work for it.
	withTestProcess("binarytrees", t, func(p proc.Process, fixture protest.Fixture) {
		_, err := setFunctionBreakpoint(p, "main.main")
		assertNoError(err, t, "setFunctionBreakpoint(main.main)")
		assertNoError(proc.Continue(p), t, "first continue")

		g, err := proc.GetG(p.CurrentThread())
		assertNoError(err, t, "GetG")
		mainGoroutineID := g.ID

		_, err = setFunctionBreakpoint(p, "runtime.newstack")
		assertNoError(err, t, "setFunctionBreakpoint(runtime.newstack)")
		for {
			assertNoError(proc.Continue(p), t, "second continue")
			g, err = proc.GetG(p.CurrentThread())
			assertNoError(err, t, "GetG")
			if g.ID == mainGoroutineID {
				break
			}
		}
		frames, err := g.Stacktrace(100)
		assertNoError(err, t, "stacktrace")
		logStacktrace(t, frames)
		m := stacktraceCheck(t, []string{"!runtime.newstack", "main.main"}, frames)
		if m == nil {
			t.Fatal("see previous loglines")
		}
	})
}

func TestIssue1034(t *testing.T) {
	// The external linker on macOS produces an abbrev for DW_TAG_subprogram
	// without the "has children" flag, we should support this.
	withTestProcess("cgostacktest/", t, func(p proc.Process, fixture protest.Fixture) {
		_, err := setFunctionBreakpoint(p, "main.main")
		assertNoError(err, t, "setFunctionBreakpoint()")
		assertNoError(proc.Continue(p), t, "Continue()")
		frames, err := p.SelectedGoroutine().Stacktrace(10)
		assertNoError(err, t, "Stacktrace")
		scope := proc.FrameToScope(p.BinInfo(), p.CurrentThread(), nil, frames[2:]...)
		args, _ := scope.FunctionArguments(normalLoadConfig)
		assertNoError(err, t, "FunctionArguments()")
		if len(args) > 0 {
			t.Fatalf("wrong number of arguments for frame %v (%d)", frames[2], len(args))
		}
	})
}

func TestIssue1008(t *testing.T) {
	// The external linker on macOS inserts "end of sequence" extended opcodes
	// in debug_line. which we should support correctly.
	withTestProcess("cgostacktest/", t, func(p proc.Process, fixture protest.Fixture) {
		_, err := setFunctionBreakpoint(p, "main.main")
		assertNoError(err, t, "setFunctionBreakpoint()")
		assertNoError(proc.Continue(p), t, "Continue()")
		loc, err := p.CurrentThread().Location()
		assertNoError(err, t, "CurrentThread().Location()")
		t.Logf("location %v\n", loc)
		if !strings.HasSuffix(loc.File, "/main.go") {
			t.Errorf("unexpected location %s:%d\n", loc.File, loc.Line)
		}
		if loc.Line > 31 {
			t.Errorf("unexpected location %s:%d (file only has 30 lines)\n", loc.File, loc.Line)
		}
	})
}

func TestDeclLine(t *testing.T) {
	ver, _ := goversion.Parse(runtime.Version())
	if ver.Major > 0 && !ver.AfterOrEqual(goversion.GoVersion{1, 10, -1, 0, 0, ""}) {
		t.Skip("go 1.9 and prior versions do not emit DW_AT_decl_line")
	}

	withTestProcess("decllinetest", t, func(p proc.Process, fixture protest.Fixture) {
		assertNoError(proc.Continue(p), t, "Continue")
		scope, err := proc.GoroutineScope(p.CurrentThread())
		assertNoError(err, t, "GoroutineScope (1)")
		vars, err := scope.LocalVariables(normalLoadConfig)
		assertNoError(err, t, "LocalVariables (1)")
		if len(vars) != 1 {
			t.Fatalf("wrong number of variables %d", len(vars))
		}

		assertNoError(proc.Continue(p), t, "Continue")
		scope, err = proc.GoroutineScope(p.CurrentThread())
		assertNoError(err, t, "GoroutineScope (2)")
		scope.LocalVariables(normalLoadConfig)
		vars, err = scope.LocalVariables(normalLoadConfig)
		assertNoError(err, t, "LocalVariables (2)")
		if len(vars) != 2 {
			t.Fatalf("wrong number of variables %d", len(vars))
		}
	})
}

func TestIssue1137(t *testing.T) {
	withTestProcess("dotpackagesiface", t, func(p proc.Process, fixture protest.Fixture) {
		assertNoError(proc.Continue(p), t, "Continue()")
		v := evalVariable(p, t, "iface")
		assertNoError(v.Unreadable, t, "iface unreadable")
		v2 := evalVariable(p, t, "iface2")
		assertNoError(v2.Unreadable, t, "iface2 unreadable")
	})
}

func TestIssue1101(t *testing.T) {
	// If a breakpoint is hit close to process death on a thread that isn't the
	// group leader the process could die while we are trying to stop it.
	//
	// This can be easily reproduced by having the goroutine that's executing
	// main.main (which will almost always run on the thread group leader) wait
	// for a second goroutine before exiting, then setting a breakpoint on the
	// second goroutine and stepping through it (see TestIssue1101 in
	// proc_test.go).
	//
	// When stepping over the return instruction of main.f the deferred
	// wg.Done() call will be executed which will cause the main goroutine to
	// resume and proceed to exit. Both the temporary breakpoint on wg.Done and
	// the temporary breakpoint on the return address of main.f will be in
	// close proximity to main.main calling os.Exit() and causing the death of
	// the thread group leader.

	withTestProcess("issue1101", t, func(p proc.Process, fixture protest.Fixture) {
		_, err := setFunctionBreakpoint(p, "main.f")
		assertNoError(err, t, "setFunctionBreakpoint()")
		assertNoError(proc.Continue(p), t, "Continue()")
		assertNoError(proc.Next(p), t, "Next() 1")
		assertNoError(proc.Next(p), t, "Next() 2")
		lastCmd := "Next() 3"
		exitErr := proc.Next(p)
		if exitErr == nil {
			lastCmd = "final Continue()"
			exitErr = proc.Continue(p)
		}
		if pexit, exited := exitErr.(proc.ProcessExitedError); exited {
			if pexit.Status != 2 && testBackend != "lldb" {
				// looks like there's a bug with debugserver on macOS that sometimes
				// will report exit status 0 instead of the proper exit status.
				t.Fatalf("process exited status %d (expected 2)", pexit.Status)
			}
		} else {
			assertNoError(exitErr, t, lastCmd)
			t.Fatalf("process did not exit after %s", lastCmd)
		}
	})
}

func TestIssue1145(t *testing.T) {
	withTestProcess("issue1145", t, func(p proc.Process, fixture protest.Fixture) {
		setFileBreakpoint(p, t, fixture, 12)
		assertNoError(proc.Continue(p), t, "Continue()")
		resumeChan := make(chan struct{}, 1)
		p.ResumeNotify(resumeChan)
		go func() {
			<-resumeChan
			time.Sleep(100 * time.Millisecond)
			p.RequestManualStop()
		}()

		assertNoError(proc.Next(p), t, "Next()")
		if p.Breakpoints().HasInternalBreakpoints() {
			t.Fatal("has internal breakpoints after manual stop request")
		}
	})
}

func TestDisassembleGlobalVars(t *testing.T) {
	withTestProcess("teststepconcurrent", t, func(p proc.Process, fixture protest.Fixture) {
		mainfn := p.BinInfo().LookupFunc["main.main"]
		text, err := proc.Disassemble(p, nil, mainfn.Entry, mainfn.End)
		assertNoError(err, t, "Disassemble")
		found := false
		for i := range text {
			if strings.Index(text[i].Text(proc.IntelFlavour, p.BinInfo()), "main.v") > 0 {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("could not find main.v reference in disassembly")
		}
	})
}

func checkFrame(frame proc.Stackframe, fnname, file string, line int, inlined bool) error {
	if frame.Call.Fn == nil || frame.Call.Fn.Name != fnname {
		return fmt.Errorf("wrong function name: %s", fnname)
	}
	if frame.Call.File != file || frame.Call.Line != line {
		return fmt.Errorf("wrong file:line %s:%d", frame.Call.File, frame.Call.Line)
	}
	if frame.Inlined != inlined {
		if inlined {
			return fmt.Errorf("not inlined")
		} else {
			return fmt.Errorf("inlined")
		}
	}
	return nil
}

func TestInlinedStacktraceAndVariables(t *testing.T) {
	if ver, _ := goversion.Parse(runtime.Version()); ver.Major >= 0 && !ver.AfterOrEqual(goversion.GoVersion{1, 10, -1, 0, 0, ""}) {
		// Versions of go before 1.10 do not have DWARF information for inlined calls
		t.Skip("inlining not supported")
	}

	firstCallCheck := &scopeCheck{
		line: 7,
		ok:   false,
		varChecks: []varCheck{
			varCheck{
				name:   "a",
				typ:    "int",
				kind:   reflect.Int,
				hasVal: true,
				intVal: 3,
			},
			varCheck{
				name:   "z",
				typ:    "int",
				kind:   reflect.Int,
				hasVal: true,
				intVal: 9,
			},
		},
	}

	secondCallCheck := &scopeCheck{
		line: 7,
		ok:   false,
		varChecks: []varCheck{
			varCheck{
				name:   "a",
				typ:    "int",
				kind:   reflect.Int,
				hasVal: true,
				intVal: 4,
			},
			varCheck{
				name:   "z",
				typ:    "int",
				kind:   reflect.Int,
				hasVal: true,
				intVal: 16,
			},
		},
	}

	withTestProcessArgs("testinline", t, ".", []string{}, protest.EnableInlining, func(p proc.Process, fixture protest.Fixture) {
		pcs := p.BinInfo().AllPCsForFileLine(fixture.Source, 7)
		if len(pcs) < 2 {
			t.Fatalf("expected at least two locations for %s:%d (got %d: %#x)", fixture.Source, 6, len(pcs), pcs)
		}
		for _, pc := range pcs {
			_, err := p.SetBreakpoint(pc, proc.UserBreakpoint, nil)
			assertNoError(err, t, fmt.Sprintf("SetBreakpoint(%#x)", pc))
		}

		// first inlined call
		assertNoError(proc.Continue(p), t, "Continue")
		frames, err := proc.ThreadStacktrace(p.CurrentThread(), 20)
		assertNoError(err, t, "ThreadStacktrace")
		t.Logf("Stacktrace:\n")
		for i := range frames {
			t.Logf("\t%s at %s:%d\n", frames[i].Call.Fn.Name, frames[i].Call.File, frames[i].Call.Line)
		}

		if err := checkFrame(frames[0], "main.inlineThis", fixture.Source, 7, true); err != nil {
			t.Fatalf("Wrong frame 0: %v", err)
		}
		if err := checkFrame(frames[1], "main.main", fixture.Source, 18, false); err != nil {
			t.Fatalf("Wrong frame 1: %v", err)
		}

		if avar, _ := constant.Int64Val(evalVariable(p, t, "a").Value); avar != 3 {
			t.Fatalf("value of 'a' variable is not 3 (%d)", avar)
		}
		if zvar, _ := constant.Int64Val(evalVariable(p, t, "z").Value); zvar != 9 {
			t.Fatalf("value of 'z' variable is not 9 (%d)", zvar)
		}

		if _, ok := firstCallCheck.checkLocalsAndArgs(p, t); !ok {
			t.Fatalf("exiting for past errors")
		}

		// second inlined call
		assertNoError(proc.Continue(p), t, "Continue")
		frames, err = proc.ThreadStacktrace(p.CurrentThread(), 20)
		assertNoError(err, t, "ThreadStacktrace (2)")
		t.Logf("Stacktrace 2:\n")
		for i := range frames {
			t.Logf("\t%s at %s:%d\n", frames[i].Call.Fn.Name, frames[i].Call.File, frames[i].Call.Line)
		}

		if err := checkFrame(frames[0], "main.inlineThis", fixture.Source, 7, true); err != nil {
			t.Fatalf("Wrong frame 0: %v", err)
		}
		if err := checkFrame(frames[1], "main.main", fixture.Source, 19, false); err != nil {
			t.Fatalf("Wrong frame 1: %v", err)
		}

		if avar, _ := constant.Int64Val(evalVariable(p, t, "a").Value); avar != 4 {
			t.Fatalf("value of 'a' variable is not 3 (%d)", avar)
		}
		if zvar, _ := constant.Int64Val(evalVariable(p, t, "z").Value); zvar != 16 {
			t.Fatalf("value of 'z' variable is not 9 (%d)", zvar)
		}
		if bvar, err := evalVariableOrError(p, "b"); err == nil {
			t.Fatalf("expected error evaluating 'b', but it succeeded instead: %v", bvar)
		}

		if _, ok := secondCallCheck.checkLocalsAndArgs(p, t); !ok {
			t.Fatalf("exiting for past errors")
		}
	})
}

func TestInlineStep(t *testing.T) {
	if ver, _ := goversion.Parse(runtime.Version()); ver.Major >= 0 && !ver.AfterOrEqual(goversion.GoVersion{1, 10, -1, 0, 0, ""}) {
		// Versions of go before 1.10 do not have DWARF information for inlined calls
		t.Skip("inlining not supported")
	}
	testseq2Args(".", []string{}, protest.EnableInlining, t, "testinline", "", []seqTest{
		{contContinue, 18},
		{contStep, 6},
		{contStep, 7},
		{contStep, 18},
		{contStep, 19},
	})
}

func TestInlineNext(t *testing.T) {
	if ver, _ := goversion.Parse(runtime.Version()); ver.Major >= 0 && !ver.AfterOrEqual(goversion.GoVersion{1, 10, -1, 0, 0, ""}) {
		// Versions of go before 1.10 do not have DWARF information for inlined calls
		t.Skip("inlining not supported")
	}
	testseq2Args(".", []string{}, protest.EnableInlining, t, "testinline", "", []seqTest{
		{contContinue, 18},
		{contStep, 6},
		{contNext, 7},
		{contNext, 18},
		{contNext, 19},
	})
}

func TestInlineStepOver(t *testing.T) {
	if ver, _ := goversion.Parse(runtime.Version()); ver.Major >= 0 && !ver.AfterOrEqual(goversion.GoVersion{1, 10, -1, 0, 0, ""}) {
		// Versions of go before 1.10 do not have DWARF information for inlined calls
		t.Skip("inlining not supported")
	}
	testseq2Args(".", []string{}, protest.EnableInlining, t, "testinline", "", []seqTest{
		{contContinue, 18},
		{contNext, 18},
		{contNext, 19},
		{contNext, 19},
		{contNext, 20},
	})
}

func TestInlineStepOut(t *testing.T) {
	if ver, _ := goversion.Parse(runtime.Version()); ver.Major >= 0 && !ver.AfterOrEqual(goversion.GoVersion{1, 10, -1, 0, 0, ""}) {
		// Versions of go before 1.10 do not have DWARF information for inlined calls
		t.Skip("inlining not supported")
	}
	testseq2Args(".", []string{}, protest.EnableInlining, t, "testinline", "", []seqTest{
		{contContinue, 18},
		{contStep, 6},
		{contStepout, 18},
	})
}

func TestIssue951(t *testing.T) {
	if ver, _ := goversion.Parse(runtime.Version()); ver.Major >= 0 && !ver.AfterOrEqual(goversion.GoVersion{1, 9, -1, 0, 0, ""}) {
		t.Skip("scopes not implemented in <=go1.8")
	}

	withTestProcess("issue951", t, func(p proc.Process, fixture protest.Fixture) {
		assertNoError(proc.Continue(p), t, "Continue()")
		scope, err := proc.GoroutineScope(p.CurrentThread())
		assertNoError(err, t, "GoroutineScope")
		args, err := scope.FunctionArguments(normalLoadConfig)
		assertNoError(err, t, "FunctionArguments")
		t.Logf("%#v", args[0])
		if args[0].Flags&proc.VariableShadowed == 0 {
			t.Error("argument is not shadowed")
		}
		vars, err := scope.LocalVariables(normalLoadConfig)
		assertNoError(err, t, "LocalVariables")
		shadowed, notShadowed := 0, 0
		for i := range vars {
			t.Logf("var %d: %#v\n", i, vars[i])
			if vars[i].Flags&proc.VariableShadowed != 0 {
				shadowed++
			} else {
				notShadowed++
			}
		}
		if shadowed != 1 || notShadowed != 1 {
			t.Errorf("Wrong number of shadowed/non-shadowed local variables: %d %d", shadowed, notShadowed)
		}
	})
}
