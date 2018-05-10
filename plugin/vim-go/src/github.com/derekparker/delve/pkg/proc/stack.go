package proc

import (
	"debug/dwarf"
	"errors"
	"fmt"
	"strings"

	"github.com/derekparker/delve/pkg/dwarf/frame"
	"github.com/derekparker/delve/pkg/dwarf/op"
	"github.com/derekparker/delve/pkg/dwarf/reader"
)

// This code is partly adapted from runtime.gentraceback in
// $GOROOT/src/runtime/traceback.go

// Stackframe represents a frame in a system stack.
//
// Each stack frame has two locations Current and Call.
//
// For the topmost stackframe Current and Call are the same location.
//
// For stackframes after the first Current is the location corresponding to
// the return address and Call is the location of the CALL instruction that
// was last executed on the frame. Note however that Call.PC is always equal
// to Current.PC, because finding the correct value for Call.PC would
// require disassembling each function in the stacktrace.
//
// For synthetic stackframes generated for inlined function calls Current.Fn
// is the function containing the inlining and Call.Fn in the inlined
// function.
type Stackframe struct {
	Current, Call Location

	// Frame registers.
	Regs op.DwarfRegisters
	// High address of the stack.
	stackHi uint64
	// Return address for this stack frame (as read from the stack frame itself).
	Ret uint64
	// Address to the memory location containing the return address
	addrret uint64
	// Err is set if an error occurred during stacktrace
	Err error
	// SystemStack is true if this frame belongs to a system stack.
	SystemStack bool
	// Inlined is true if this frame is actually an inlined call.
	Inlined bool

	// lastpc is a memory address guaranteed to belong to the last instruction
	// executed in this stack frame.
	// For the topmost stack frame this will be the same as Current.PC and
	// Call.PC, for other stack frames it will usually be Current.PC-1, but
	// could be different when inlined calls are involved in the stacktrace.
	// Note that this address isn't guaranteed to belong to the start of an
	// instruction and, for this reason, should not be propagated outside of
	// pkg/proc.
	// Use this value to determine active lexical scopes for the stackframe.
	lastpc uint64
}

// FrameOffset returns the address of the stack frame, absolute for system
// stack frames or as an offset from stackhi for goroutine stacks (a
// negative value).
func (frame *Stackframe) FrameOffset() int64 {
	if frame.SystemStack {
		return frame.Regs.CFA
	}
	return frame.Regs.CFA - int64(frame.stackHi)
}

// FramePointerOffset returns the value of the frame pointer, absolute for
// system stack frames or as an offset from stackhi for goroutine stacks (a
// negative value).
func (frame *Stackframe) FramePointerOffset() int64 {
	if frame.SystemStack {
		return int64(frame.Regs.BP())
	}
	return int64(frame.Regs.BP()) - int64(frame.stackHi)
}

// ThreadStacktrace returns the stack trace for thread.
// Note the locations in the array are return addresses not call addresses.
func ThreadStacktrace(thread Thread, depth int) ([]Stackframe, error) {
	g, _ := GetG(thread)
	if g == nil {
		regs, err := thread.Registers(true)
		if err != nil {
			return nil, err
		}
		it := newStackIterator(thread.BinInfo(), thread, thread.BinInfo().Arch.RegistersToDwarfRegisters(regs), 0, nil, -1, nil)
		return it.stacktrace(depth)
	}
	return g.Stacktrace(depth)
}

func (g *G) stackIterator() (*stackIterator, error) {
	stkbar, err := g.stkbar()
	if err != nil {
		return nil, err
	}

	if g.Thread != nil {
		regs, err := g.Thread.Registers(true)
		if err != nil {
			return nil, err
		}
		return newStackIterator(g.variable.bi, g.Thread, g.variable.bi.Arch.RegistersToDwarfRegisters(regs), g.stackhi, stkbar, g.stkbarPos, g), nil
	}
	return newStackIterator(g.variable.bi, g.variable.mem, g.variable.bi.Arch.GoroutineToDwarfRegisters(g), g.stackhi, stkbar, g.stkbarPos, g), nil
}

// Stacktrace returns the stack trace for a goroutine.
// Note the locations in the array are return addresses not call addresses.
func (g *G) Stacktrace(depth int) ([]Stackframe, error) {
	it, err := g.stackIterator()
	if err != nil {
		return nil, err
	}
	return it.stacktrace(depth)
}

// NullAddrError is an error for a null address.
type NullAddrError struct{}

func (n NullAddrError) Error() string {
	return "NULL address"
}

// stackIterator holds information
// required to iterate and walk the program
// stack.
type stackIterator struct {
	pc    uint64
	top   bool
	atend bool
	frame Stackframe
	bi    *BinaryInfo
	mem   MemoryReadWriter
	err   error

	stackhi        uint64
	systemstack    bool
	stackBarrierPC uint64
	stkbar         []savedLR

	// regs is the register set for the current frame
	regs op.DwarfRegisters

	g           *G     // the goroutine being stacktraced, nil if we are stacktracing a goroutine-less thread
	g0_sched_sp uint64 // value of g0.sched.sp (see comments around its use)

	dwarfReader *dwarf.Reader
}

type savedLR struct {
	ptr uint64
	val uint64
}

func newStackIterator(bi *BinaryInfo, mem MemoryReadWriter, regs op.DwarfRegisters, stackhi uint64, stkbar []savedLR, stkbarPos int, g *G) *stackIterator {
	stackBarrierFunc := bi.LookupFunc["runtime.stackBarrier"] // stack barriers were removed in Go 1.9
	var stackBarrierPC uint64
	if stackBarrierFunc != nil && stkbar != nil {
		stackBarrierPC = stackBarrierFunc.Entry
		fn := bi.PCToFunc(regs.PC())
		if fn != nil && fn.Name == "runtime.stackBarrier" {
			// We caught the goroutine as it's executing the stack barrier, we must
			// determine whether or not g.stackPos has already been incremented or not.
			if len(stkbar) > 0 && stkbar[stkbarPos].ptr < regs.SP() {
				// runtime.stackBarrier has not incremented stkbarPos.
			} else if stkbarPos > 0 && stkbar[stkbarPos-1].ptr < regs.SP() {
				// runtime.stackBarrier has incremented stkbarPos.
				stkbarPos--
			} else {
				return &stackIterator{err: fmt.Errorf("failed to unwind through stackBarrier at SP %x", regs.SP())}
			}
		}
		stkbar = stkbar[stkbarPos:]
	}
	var g0_sched_sp uint64
	systemstack := true
	if g != nil {
		systemstack = g.SystemStack
		g0var, _ := g.variable.fieldVariable("m").structMember("g0")
		if g0var != nil {
			g0, _ := g0var.parseG()
			if g0 != nil {
				g0_sched_sp = g0.SP
			}
		}
	}
	return &stackIterator{pc: regs.PC(), regs: regs, top: true, bi: bi, mem: mem, err: nil, atend: false, stackhi: stackhi, stackBarrierPC: stackBarrierPC, stkbar: stkbar, systemstack: systemstack, g: g, g0_sched_sp: g0_sched_sp, dwarfReader: bi.dwarf.Reader()}
}

// Next points the iterator to the next stack frame.
func (it *stackIterator) Next() bool {
	if it.err != nil || it.atend {
		return false
	}
	callFrameRegs, ret, retaddr := it.advanceRegs()
	it.frame = it.newStackframe(ret, retaddr)

	if it.stkbar != nil && it.frame.Ret == it.stackBarrierPC && it.frame.addrret == it.stkbar[0].ptr {
		// Skip stack barrier frames
		it.frame.Ret = it.stkbar[0].val
		it.stkbar = it.stkbar[1:]
	}

	if it.switchStack() {
		return true
	}

	if it.frame.Ret <= 0 {
		it.atend = true
		return true
	}

	it.top = false
	it.pc = it.frame.Ret
	it.regs = callFrameRegs
	return true
}

// asmcgocallSPOffsetSaveSlot is the offset from systemstack.SP where
// (goroutine.SP - StackHi) is saved in runtime.asmcgocall after the stack
// switch happens.
const asmcgocallSPOffsetSaveSlot = 0x28

// switchStack will use the current frame to determine if it's time to
// switch between the system stack and the goroutine stack or vice versa.
// Sets it.atend when the top of the stack is reached.
func (it *stackIterator) switchStack() bool {
	if it.frame.Current.Fn == nil {
		return false
	}
	switch it.frame.Current.Fn.Name {
	case "runtime.asmcgocall":
		if it.top || !it.systemstack {
			return false
		}

		// This function is called by a goroutine to execute a C function and
		// switches from the goroutine stack to the system stack.
		// Since we are unwinding the stack from callee to caller we have  switch
		// from the system stack to the goroutine stack.

		off, _ := readIntRaw(it.mem, uintptr(it.regs.SP()+asmcgocallSPOffsetSaveSlot), int64(it.bi.Arch.PtrSize())) // reads "offset of SP from StackHi" from where runtime.asmcgocall saved it
		oldsp := it.regs.SP()
		it.regs.Reg(it.regs.SPRegNum).Uint64Val = uint64(int64(it.stackhi) - off)

		// runtime.asmcgocall can also be called from inside the system stack,
		// in that case no stack switch actually happens
		if it.regs.SP() == oldsp {
			return false
		}
		it.systemstack = false

		// advances to the next frame in the call stack
		it.frame.addrret = uint64(int64(it.regs.SP()) + int64(it.bi.Arch.PtrSize()))
		it.frame.Ret, _ = readUintRaw(it.mem, uintptr(it.frame.addrret), int64(it.bi.Arch.PtrSize()))
		it.pc = it.frame.Ret

		it.top = false
		return true

	case "runtime.cgocallback_gofunc":
		// For a detailed description of how this works read the long comment at
		// the start of $GOROOT/src/runtime/cgocall.go and the source code of
		// runtime.cgocallback_gofunc in $GOROOT/src/runtime/asm_amd64.s
		//
		// When a C functions calls back into go it will eventually call into
		// runtime.cgocallback_gofunc which is the function that does the stack
		// switch from the system stack back into the goroutine stack
		// Since we are going backwards on the stack here we see the transition
		// as goroutine stack -> system stack.

		if it.top || it.systemstack {
			return false
		}

		if it.g0_sched_sp <= 0 {
			return false
		}
		// entering the system stack
		it.regs.Reg(it.regs.SPRegNum).Uint64Val = it.g0_sched_sp
		// reads the previous value of g0.sched.sp that runtime.cgocallback_gofunc saved on the stack
		it.g0_sched_sp, _ = readUintRaw(it.mem, uintptr(it.regs.SP()), int64(it.bi.Arch.PtrSize()))
		it.top = false
		callFrameRegs, ret, retaddr := it.advanceRegs()
		frameOnSystemStack := it.newStackframe(ret, retaddr)
		it.pc = frameOnSystemStack.Ret
		it.regs = callFrameRegs
		it.systemstack = true
		return true

	case "runtime.goexit", "runtime.rt0_go", "runtime.mcall":
		// Look for "top of stack" functions.
		it.atend = true
		return true

	default:
		if it.systemstack && it.top && it.g != nil && strings.HasPrefix(it.frame.Current.Fn.Name, "runtime.") {
			// The runtime switches to the system stack in multiple places.
			// This usually happens through a call to runtime.systemstack but there
			// are functions that switch to the system stack manually (for example
			// runtime.morestack).
			// Since we are only interested in printing the system stack for cgo
			// calls we switch directly to the goroutine stack if we detect that the
			// function at the top of the stack is a runtime function.
			it.systemstack = false
			it.top = false
			it.pc = it.g.PC
			it.regs.Reg(it.regs.SPRegNum).Uint64Val = it.g.SP
			it.regs.Reg(it.regs.BPRegNum).Uint64Val = it.g.BP
			return true
		}

		return false
	}
}

// Frame returns the frame the iterator is pointing at.
func (it *stackIterator) Frame() Stackframe {
	return it.frame
}

// Err returns the error encountered during stack iteration.
func (it *stackIterator) Err() error {
	return it.err
}

// frameBase calculates the frame base pseudo-register for DWARF for fn and
// the current frame.
func (it *stackIterator) frameBase(fn *Function) int64 {
	it.dwarfReader.Seek(fn.offset)
	e, err := it.dwarfReader.Next()
	if err != nil {
		return 0
	}
	fb, _, _, _ := it.bi.Location(e, dwarf.AttrFrameBase, it.pc, it.regs)
	return fb
}

func (it *stackIterator) newStackframe(ret, retaddr uint64) Stackframe {
	if retaddr == 0 {
		it.err = NullAddrError{}
		return Stackframe{}
	}
	f, l, fn := it.bi.PCToLine(it.pc)
	if fn == nil {
		f = "?"
		l = -1
	} else {
		it.regs.FrameBase = it.frameBase(fn)
	}
	r := Stackframe{Current: Location{PC: it.pc, File: f, Line: l, Fn: fn}, Regs: it.regs, Ret: ret, addrret: retaddr, stackHi: it.stackhi, SystemStack: it.systemstack, lastpc: it.pc}
	if !it.top {
		fnname := ""
		if r.Current.Fn != nil {
			fnname = r.Current.Fn.Name
		}
		switch fnname {
		case "runtime.mstart", "runtime.systemstack_switch":
			// these frames are inserted by runtime.systemstack and there is no CALL
			// instruction to look for at pc - 1
			r.Call = r.Current
		default:
			r.lastpc = it.pc - 1
			r.Call.File, r.Call.Line, r.Call.Fn = it.bi.PCToLine(it.pc - 1)
			if r.Call.Fn == nil {
				r.Call.File = "?"
				r.Call.Line = -1
			}
			r.Call.PC = r.Current.PC
		}
	} else {
		r.Call = r.Current
	}
	return r
}

func (it *stackIterator) stacktrace(depth int) ([]Stackframe, error) {
	if depth < 0 {
		return nil, errors.New("negative maximum stack depth")
	}
	frames := make([]Stackframe, 0, depth+1)
	for it.Next() {
		frames = it.appendInlineCalls(frames, it.Frame())
		if len(frames) >= depth+1 {
			break
		}
	}
	if err := it.Err(); err != nil {
		if len(frames) == 0 {
			return nil, err
		}
		frames = append(frames, Stackframe{Err: err})
	}
	return frames, nil
}

func (it *stackIterator) appendInlineCalls(frames []Stackframe, frame Stackframe) []Stackframe {
	if frame.Call.Fn == nil {
		return append(frames, frame)
	}
	if frame.Call.Fn.cu.lineInfo == nil {
		return append(frames, frame)
	}

	callpc := frame.Call.PC
	if len(frames) > 0 {
		callpc--
	}

	irdr := reader.InlineStack(it.bi.dwarf, frame.Call.Fn.offset, callpc)
	for irdr.Next() {
		entry, offset := reader.LoadAbstractOrigin(irdr.Entry(), it.dwarfReader)

		fnname, okname := entry.Val(dwarf.AttrName).(string)
		fileidx, okfileidx := entry.Val(dwarf.AttrCallFile).(int64)
		line, okline := entry.Val(dwarf.AttrCallLine).(int64)

		if !okname || !okfileidx || !okline {
			break
		}
		if fileidx-1 < 0 || fileidx-1 >= int64(len(frame.Current.Fn.cu.lineInfo.FileNames)) {
			break
		}

		inlfn := &Function{Name: fnname, Entry: frame.Call.Fn.Entry, End: frame.Call.Fn.End, offset: offset, cu: frame.Call.Fn.cu}
		frames = append(frames, Stackframe{
			Current: frame.Current,
			Call: Location{
				frame.Call.PC,
				frame.Call.File,
				frame.Call.Line,
				inlfn,
			},
			Regs:        frame.Regs,
			stackHi:     frame.stackHi,
			Ret:         frame.Ret,
			addrret:     frame.addrret,
			Err:         frame.Err,
			SystemStack: frame.SystemStack,
			Inlined:     true,
			lastpc:      frame.lastpc,
		})

		frame.Call.File = frame.Current.Fn.cu.lineInfo.FileNames[fileidx-1].Path
		frame.Call.Line = int(line)
	}

	return append(frames, frame)
}

// advanceRegs calculates it.callFrameRegs using it.regs and the frame
// descriptor entry for the current stack frame.
// it.regs.CallFrameCFA is updated.
func (it *stackIterator) advanceRegs() (callFrameRegs op.DwarfRegisters, ret uint64, retaddr uint64) {
	fde, err := it.bi.frameEntries.FDEForPC(it.pc)
	var framectx *frame.FrameContext
	if _, nofde := err.(*frame.NoFDEForPCError); nofde {
		framectx = it.bi.Arch.FixFrameUnwindContext(nil, it.pc, it.bi)
	} else {
		framectx = it.bi.Arch.FixFrameUnwindContext(fde.EstablishFrame(it.pc), it.pc, it.bi)
	}

	cfareg, err := it.executeFrameRegRule(0, framectx.CFA, 0)
	if cfareg == nil {
		it.err = fmt.Errorf("CFA becomes undefined at PC %#x", it.pc)
		return op.DwarfRegisters{}, 0, 0
	}
	it.regs.CFA = int64(cfareg.Uint64Val)

	callFrameRegs = op.DwarfRegisters{ByteOrder: it.regs.ByteOrder, PCRegNum: it.regs.PCRegNum, SPRegNum: it.regs.SPRegNum, BPRegNum: it.regs.BPRegNum}

	// According to the standard the compiler should be responsible for emitting
	// rules for the RSP register so that it can then be used to calculate CFA,
	// however neither Go nor GCC do this.
	// In the following line we copy GDB's behaviour by assuming this is
	// implicit.
	// See also the comment in dwarf2_frame_default_init in
	// $GDB_SOURCE/dwarf2-frame.c
	callFrameRegs.AddReg(uint64(amd64DwarfSPRegNum), cfareg)

	for i, regRule := range framectx.Regs {
		reg, err := it.executeFrameRegRule(i, regRule, it.regs.CFA)
		callFrameRegs.AddReg(i, reg)
		if i == framectx.RetAddrReg {
			if reg == nil {
				if err == nil {
					err = fmt.Errorf("Undefined return address at %#x", it.pc)
				}
				it.err = err
			} else {
				ret = reg.Uint64Val
			}
			retaddr = uint64(it.regs.CFA + regRule.Offset)
		}
	}

	return callFrameRegs, ret, retaddr
}

func (it *stackIterator) executeFrameRegRule(regnum uint64, rule frame.DWRule, cfa int64) (*op.DwarfRegister, error) {
	switch rule.Rule {
	default:
		fallthrough
	case frame.RuleUndefined:
		return nil, nil
	case frame.RuleSameVal:
		reg := *it.regs.Reg(regnum)
		return &reg, nil
	case frame.RuleOffset:
		return it.readRegisterAt(regnum, uint64(cfa+rule.Offset))
	case frame.RuleValOffset:
		return op.DwarfRegisterFromUint64(uint64(cfa + rule.Offset)), nil
	case frame.RuleRegister:
		return it.regs.Reg(rule.Reg), nil
	case frame.RuleExpression:
		v, _, err := op.ExecuteStackProgram(it.regs, rule.Expression)
		if err != nil {
			return nil, err
		}
		return it.readRegisterAt(regnum, uint64(v))
	case frame.RuleValExpression:
		v, _, err := op.ExecuteStackProgram(it.regs, rule.Expression)
		if err != nil {
			return nil, err
		}
		return op.DwarfRegisterFromUint64(uint64(v)), nil
	case frame.RuleArchitectural:
		return nil, errors.New("architectural frame rules are unsupported")
	case frame.RuleCFA:
		if it.regs.Reg(rule.Reg) == nil {
			return nil, nil
		}
		return op.DwarfRegisterFromUint64(uint64(int64(it.regs.Uint64Val(rule.Reg)) + rule.Offset)), nil
	case frame.RuleFramePointer:
		curReg := it.regs.Reg(rule.Reg)
		if curReg == nil {
			return nil, nil
		}
		if curReg.Uint64Val <= uint64(cfa) {
			return it.readRegisterAt(regnum, curReg.Uint64Val)
		} else {
			newReg := *curReg
			return &newReg, nil
		}
	}
}

func (it *stackIterator) readRegisterAt(regnum uint64, addr uint64) (*op.DwarfRegister, error) {
	buf := make([]byte, it.bi.Arch.RegSize(regnum))
	_, err := it.mem.ReadMemory(buf, uintptr(addr))
	if err != nil {
		return nil, err
	}
	return op.DwarfRegisterFromBytes(buf), nil
}
