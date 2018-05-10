// Tests for loading variables that have complex location expressions. They
// are only produced for optimized code (for both Go and C) therefore we can
// not get the compiler to produce them reliably enough for tests.

package proc_test

import (
	"bytes"
	"debug/dwarf"
	"encoding/binary"
	"fmt"
	"go/constant"
	"testing"

	"github.com/derekparker/delve/pkg/dwarf/dwarfbuilder"
	"github.com/derekparker/delve/pkg/dwarf/godwarf"
	"github.com/derekparker/delve/pkg/dwarf/op"
	"github.com/derekparker/delve/pkg/proc"
	"github.com/derekparker/delve/pkg/proc/core"
)

const defaultCFA = 0xc420051d00

func fakeBinaryInfo(t *testing.T, dwb *dwarfbuilder.Builder) *proc.BinaryInfo {
	abbrev, aranges, frame, info, line, pubnames, ranges, str, loc, err := dwb.Build()
	assertNoError(err, t, "dwarfbuilder.Build")
	dwdata, err := dwarf.New(abbrev, aranges, frame, info, line, pubnames, ranges, str)
	assertNoError(err, t, "creating dwarf")

	bi := proc.NewBinaryInfo("linux", "amd64")
	bi.LoadFromData(dwdata, frame, line, loc)

	return &bi
}

// fakeMemory implements proc.MemoryReadWriter by reading from a byte slice.
// Byte 0 of "data"  is at address "base".
type fakeMemory struct {
	base uint64
	data []byte
}

func newFakeMemory(base uint64, contents ...interface{}) *fakeMemory {
	mem := &fakeMemory{base: base}
	var buf bytes.Buffer
	for _, x := range contents {
		binary.Write(&buf, binary.LittleEndian, x)
	}
	mem.data = buf.Bytes()
	return mem
}

func (mem *fakeMemory) ReadMemory(data []byte, addr uintptr) (int, error) {
	if uint64(addr) < mem.base {
		return 0, fmt.Errorf("read out of bounds %d %#x", len(data), addr)
	}
	start := uint64(addr) - mem.base
	end := uint64(len(data)) + start
	if end > uint64(len(mem.data)) {
		panic(fmt.Errorf("read out of bounds %d %#x", len(data), addr))
	}
	copy(data, mem.data[start:end])
	return len(data), nil
}

func (mem *fakeMemory) WriteMemory(uintptr, []byte) (int, error) {
	return 0, fmt.Errorf("not implemented")
}

func uintExprCheck(t *testing.T, scope *proc.EvalScope, expr string, tgt uint64) {
	thevar, err := scope.EvalExpression(expr, normalLoadConfig)
	assertNoError(err, t, fmt.Sprintf("EvalExpression(%s)", expr))
	if thevar.Unreadable != nil {
		t.Errorf("variable %q unreadable: %v", expr, thevar.Unreadable)
	} else {
		if v, _ := constant.Uint64Val(thevar.Value); v != tgt {
			t.Errorf("expected value %x got %x for %q", tgt, v, expr)
		}
	}
}

func dwarfExprCheck(t *testing.T, mem proc.MemoryReadWriter, regs op.DwarfRegisters, bi *proc.BinaryInfo, testCases map[string]uint16, fn *proc.Function) *proc.EvalScope {
	scope := &proc.EvalScope{Location: proc.Location{PC: 0x40100, Fn: fn}, Regs: regs, Mem: mem, Gvar: nil, BinInfo: bi}
	for name, value := range testCases {
		uintExprCheck(t, scope, name, uint64(value))
	}

	return scope
}

func dwarfRegisters(regs *core.Registers) op.DwarfRegisters {
	a := proc.AMD64Arch("linux")
	dwarfRegs := a.RegistersToDwarfRegisters(regs)
	dwarfRegs.CFA = defaultCFA
	dwarfRegs.FrameBase = defaultCFA
	return dwarfRegs
}

func TestDwarfExprRegisters(t *testing.T) {
	testCases := map[string]uint16{
		"a": 0x1234,
		"b": 0x4321,
		"c": 0x2143,
	}

	dwb := dwarfbuilder.New()

	uint16off := dwb.AddBaseType("uint16", dwarfbuilder.DW_ATE_unsigned, 2)

	dwb.AddSubprogram("main.main", 0x40100, 0x41000)
	dwb.Attr(dwarf.AttrFrameBase, dwarfbuilder.LocationBlock(op.DW_OP_call_frame_cfa))
	dwb.AddVariable("a", uint16off, dwarfbuilder.LocationBlock(op.DW_OP_reg0))
	dwb.AddVariable("b", uint16off, dwarfbuilder.LocationBlock(op.DW_OP_fbreg, int(8)))
	dwb.AddVariable("c", uint16off, dwarfbuilder.LocationBlock(op.DW_OP_regx, int(1)))
	dwb.TagClose()

	bi := fakeBinaryInfo(t, dwb)

	mainfn := bi.LookupFunc["main.main"]

	mem := newFakeMemory(defaultCFA, uint64(0), uint64(testCases["b"]), uint16(testCases["pair.v"]))
	regs := core.Registers{LinuxCoreRegisters: &core.LinuxCoreRegisters{}}
	regs.Rax = uint64(testCases["a"])
	regs.Rdx = uint64(testCases["c"])

	dwarfExprCheck(t, mem, dwarfRegisters(&regs), bi, testCases, mainfn)
}

func TestDwarfExprComposite(t *testing.T) {
	testCases := map[string]uint16{
		"pair.k": 0x8765,
		"pair.v": 0x5678,
		"n":      42,
	}

	const stringVal = "this is a string"

	dwb := dwarfbuilder.New()

	uint16off := dwb.AddBaseType("uint16", dwarfbuilder.DW_ATE_unsigned, 2)
	intoff := dwb.AddBaseType("int", dwarfbuilder.DW_ATE_signed, 8)

	byteoff := dwb.AddBaseType("uint8", dwarfbuilder.DW_ATE_unsigned, 1)

	byteptroff := dwb.TagOpen(dwarf.TagPointerType, "*uint8")
	dwb.Attr(godwarf.AttrGoKind, uint8(22))
	dwb.Attr(dwarf.AttrType, byteoff)
	dwb.TagClose()

	pairoff := dwb.AddStructType("main.pair", 4)
	dwb.Attr(godwarf.AttrGoKind, uint8(25))
	dwb.AddMember("k", uint16off, dwarfbuilder.LocationBlock(op.DW_OP_plus_uconst, uint(0)))
	dwb.AddMember("v", uint16off, dwarfbuilder.LocationBlock(op.DW_OP_plus_uconst, uint(2)))
	dwb.TagClose()

	stringoff := dwb.AddStructType("string", 16)
	dwb.Attr(godwarf.AttrGoKind, uint8(24))
	dwb.AddMember("str", byteptroff, dwarfbuilder.LocationBlock(op.DW_OP_plus_uconst, uint(0)))
	dwb.AddMember("len", intoff, dwarfbuilder.LocationBlock(op.DW_OP_plus_uconst, uint(8)))
	dwb.TagClose()

	dwb.AddSubprogram("main.main", 0x40100, 0x41000)
	dwb.AddVariable("pair", pairoff, dwarfbuilder.LocationBlock(
		op.DW_OP_reg2, op.DW_OP_piece, uint(2),
		op.DW_OP_call_frame_cfa, op.DW_OP_consts, int(16), op.DW_OP_plus, op.DW_OP_piece, uint(2)))
	dwb.AddVariable("s", stringoff, dwarfbuilder.LocationBlock(
		op.DW_OP_reg1, op.DW_OP_piece, uint(8),
		op.DW_OP_reg0, op.DW_OP_piece, uint(8)))
	dwb.AddVariable("n", intoff, dwarfbuilder.LocationBlock(op.DW_OP_reg3))
	dwb.TagClose()

	bi := fakeBinaryInfo(t, dwb)

	mainfn := bi.LookupFunc["main.main"]

	mem := newFakeMemory(defaultCFA, uint64(0), uint64(0), uint16(testCases["pair.v"]), []byte(stringVal))
	var regs core.Registers
	regs.LinuxCoreRegisters = &core.LinuxCoreRegisters{}
	regs.Rax = uint64(len(stringVal))
	regs.Rdx = defaultCFA + 18
	regs.Rcx = uint64(testCases["pair.k"])
	regs.Rbx = uint64(testCases["n"])

	scope := dwarfExprCheck(t, mem, dwarfRegisters(&regs), bi, testCases, mainfn)

	thevar, err := scope.EvalExpression("s", normalLoadConfig)
	assertNoError(err, t, fmt.Sprintf("EvalExpression(%s)", "s"))
	if thevar.Unreadable != nil {
		t.Errorf("variable \"s\" unreadable: %v", thevar.Unreadable)
	} else {
		if v := constant.StringVal(thevar.Value); v != stringVal {
			t.Errorf("expected value %q got %q", stringVal, v)
		}
	}
}

func TestDwarfExprLoclist(t *testing.T) {
	const before = 0x1234
	const after = 0x4321

	dwb := dwarfbuilder.New()

	uint16off := dwb.AddBaseType("uint16", dwarfbuilder.DW_ATE_unsigned, 2)

	dwb.AddSubprogram("main.main", 0x40100, 0x41000)
	dwb.AddVariable("a", uint16off, []dwarfbuilder.LocEntry{
		{0x40100, 0x40700, dwarfbuilder.LocationBlock(op.DW_OP_call_frame_cfa)},
		{0x40700, 0x41000, dwarfbuilder.LocationBlock(op.DW_OP_call_frame_cfa, op.DW_OP_consts, int(2), op.DW_OP_plus)},
	})
	dwb.TagClose()

	bi := fakeBinaryInfo(t, dwb)

	mainfn := bi.LookupFunc["main.main"]

	mem := newFakeMemory(defaultCFA, uint16(before), uint16(after))
	regs := core.Registers{LinuxCoreRegisters: &core.LinuxCoreRegisters{}}

	scope := &proc.EvalScope{Location: proc.Location{PC: 0x40100, Fn: mainfn}, Regs: dwarfRegisters(&regs), Mem: mem, Gvar: nil, BinInfo: bi}

	uintExprCheck(t, scope, "a", before)
	scope.PC = 0x40800
	uintExprCheck(t, scope, "a", after)
}
