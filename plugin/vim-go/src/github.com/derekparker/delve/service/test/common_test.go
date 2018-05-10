package service_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/derekparker/delve/service/api"
)

func assertNoError(err error, t *testing.T, s string) {
	if err != nil {
		_, file, line, _ := runtime.Caller(1)
		fname := filepath.Base(file)
		t.Fatalf("failed assertion at %s:%d: %s - %s\n", fname, line, s, err)
	}
}

func assertError(err error, t *testing.T, s string) {
	if err == nil {
		_, file, line, _ := runtime.Caller(1)
		fname := filepath.Base(file)
		t.Fatalf("failed assertion at %s:%d: %s (no error)\n", fname, line, s)
	}

	if strings.Contains(err.Error(), "Internal debugger error") {
		_, file, line, _ := runtime.Caller(1)
		fname := filepath.Base(file)
		t.Fatalf("failed assertion at %s:%d: %s internal debugger error: %v\n", fname, line, s, err)
	}
}

func init() {
	runtime.GOMAXPROCS(2)
}

type nextTest struct {
	begin, end int
}

func testProgPath(t *testing.T, name string) string {
	fp, err := filepath.Abs(fmt.Sprintf("_fixtures/%s.go", name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fp); err != nil {
		fp, err = filepath.Abs(fmt.Sprintf("../../_fixtures/%s.go", name))
		if err != nil {
			t.Fatal(err)
		}
	}
	return fp
}

type BreakpointLister interface {
	ListBreakpoints() ([]*api.Breakpoint, error)
}

func countBreakpoints(t *testing.T, c BreakpointLister) int {
	bps, err := c.ListBreakpoints()
	assertNoError(err, t, "ListBreakpoints()")
	bpcount := 0
	for _, bp := range bps {
		if bp.ID >= 0 {
			bpcount++
		}
	}
	return bpcount
}

type LocationFinder interface {
	FindLocation(api.EvalScope, string) ([]api.Location, error)
}

func findLocationHelper(t *testing.T, c LocationFinder, loc string, shouldErr bool, count int, checkAddr uint64) []uint64 {
	locs, err := c.FindLocation(api.EvalScope{-1, 0}, loc)
	t.Logf("FindLocation(\"%s\") → %v\n", loc, locs)

	if shouldErr {
		if err == nil {
			t.Fatalf("Resolving location <%s> didn't return an error: %v", loc, locs)
		}
	} else {
		if err != nil {
			t.Fatalf("Error resolving location <%s>: %v", loc, err)
		}
	}

	if (count >= 0) && (len(locs) != count) {
		t.Fatalf("Wrong number of breakpoints returned for location <%s> (got %d, expected %d)", loc, len(locs), count)
	}

	if checkAddr != 0 && checkAddr != locs[0].PC {
		t.Fatalf("Wrong address returned for location <%s> (got %#x, expected %#x)", loc, locs[0].PC, checkAddr)
	}

	addrs := make([]uint64, len(locs))
	for i := range locs {
		addrs[i] = locs[i].PC
	}
	return addrs
}

func getCurinstr(d3 api.AsmInstructions) *api.AsmInstruction {
	for i := range d3 {
		if d3[i].AtPC {
			return &d3[i]
		}
	}
	return nil
}
