package line

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/derekparker/delve/pkg/dwarf/util"
)

type Location struct {
	File    string
	Line    int
	Address uint64
	Delta   int
}

type StateMachine struct {
	dbl             *DebugLineInfo
	file            string
	line            int
	address         uint64
	column          uint
	isStmt          bool
	basicBlock      bool
	endSeq          bool
	lastWasStandard bool
	lastDelta       int
	// valid is true if the current value of the state machine is the address of
	// an instruction (using the terminology used by DWARF spec the current
	// value of the state machine should be appended to the matrix representing
	// the compilation unit)
	valid bool

	started bool

	buf     *bytes.Buffer // remaining instructions
	opcodes []opcodefn

	definedFiles []*FileEntry // files defined with DW_LINE_define_file

	lastAddress uint64
	lastFile    string
	lastLine    int
}

type opcodefn func(*StateMachine, *bytes.Buffer)

// Special opcodes
const (
	DW_LNS_copy             = 1
	DW_LNS_advance_pc       = 2
	DW_LNS_advance_line     = 3
	DW_LNS_set_file         = 4
	DW_LNS_set_column       = 5
	DW_LNS_negate_stmt      = 6
	DW_LNS_set_basic_block  = 7
	DW_LNS_const_add_pc     = 8
	DW_LNS_fixed_advance_pc = 9
)

// Extended opcodes
const (
	DW_LINE_end_sequence = 1
	DW_LINE_set_address  = 2
	DW_LINE_define_file  = 3
)

var standardopcodes = map[byte]opcodefn{
	DW_LNS_copy:             copyfn,
	DW_LNS_advance_pc:       advancepc,
	DW_LNS_advance_line:     advanceline,
	DW_LNS_set_file:         setfile,
	DW_LNS_set_column:       setcolumn,
	DW_LNS_negate_stmt:      negatestmt,
	DW_LNS_set_basic_block:  setbasicblock,
	DW_LNS_const_add_pc:     constaddpc,
	DW_LNS_fixed_advance_pc: fixedadvancepc,
}

var extendedopcodes = map[byte]opcodefn{
	DW_LINE_end_sequence: endsequence,
	DW_LINE_set_address:  setaddress,
	DW_LINE_define_file:  definefile,
}

func newStateMachine(dbl *DebugLineInfo, instructions []byte) *StateMachine {
	opcodes := make([]opcodefn, len(standardopcodes)+1)
	opcodes[0] = execExtendedOpcode
	for op := range standardopcodes {
		opcodes[op] = standardopcodes[op]
	}
	return &StateMachine{dbl: dbl, file: dbl.FileNames[0].Path, line: 1, buf: bytes.NewBuffer(instructions), opcodes: opcodes}
}

// Returns all PCs for a given file/line. Useful for loops where the 'for' line
// could be split amongst 2 PCs.
func (lineInfo *DebugLineInfo) AllPCsForFileLine(f string, l int) (pcs []uint64) {
	if lineInfo == nil {
		return nil
	}

	var (
		foundFile bool
		lastAddr  uint64
		sm        = newStateMachine(lineInfo, lineInfo.Instructions)
	)

	for {
		if err := sm.next(); err != nil {
			break
		}
		if foundFile && sm.file != f {
			return
		}
		if sm.line == l && sm.file == f && sm.address != lastAddr {
			foundFile = true
			if sm.valid {
				pcs = append(pcs, sm.address)
			}
			// Keep going until we're on a different line. We only care about
			// when a line comes back around (i.e. for loop) so get to next line,
			// and try to find the line we care about again.
			for {
				if err := sm.next(); err != nil {
					break
				}
				if l != sm.line {
					break
				}
			}
		}
	}
	return
}

var NoSourceError = errors.New("no source available")

func (lineInfo *DebugLineInfo) AllPCsBetween(begin, end uint64) ([]uint64, error) {
	if lineInfo == nil {
		return nil, NoSourceError
	}

	var (
		pcs      []uint64
		lastaddr uint64
		sm       = newStateMachine(lineInfo, lineInfo.Instructions)
	)

	for {
		if err := sm.next(); err != nil {
			break
		}
		if !sm.valid {
			continue
		}
		if sm.address > end {
			break
		}
		if sm.address >= begin && sm.address > lastaddr {
			lastaddr = sm.address
			pcs = append(pcs, sm.address)
		}
	}
	return pcs, nil
}

// copy returns a copy of this state machine, running the returned state
// machine will not affect sm.
func (sm *StateMachine) copy() *StateMachine {
	var r StateMachine
	r = *sm
	r.buf = bytes.NewBuffer(sm.buf.Bytes())
	return &r
}

// PCToLine returns the filename and line number associated with pc.
// If pc isn't found inside lineInfo's table it will return the filename and
// line number associated with the closest PC address preceding pc.
// basePC will be used for caching, it's normally the entry point for the
// function containing pc.
func (lineInfo *DebugLineInfo) PCToLine(basePC, pc uint64) (string, int) {
	if lineInfo == nil {
		return "", 0
	}
	if basePC > pc {
		panic(fmt.Errorf("basePC after pc %#x %#x", basePC, pc))
	}

	var sm *StateMachine
	if basePC == 0 {
		sm = newStateMachine(lineInfo, lineInfo.Instructions)
	} else {
		// Try to use the last state machine that we used for this function, if
		// there isn't one or it's already past pc try to clone the cached state
		// machine stopped at the entry point of the function.
		// As a last resort start from the start of the debug_line section.
		sm = lineInfo.lastMachineCache[basePC]
		if sm == nil || sm.lastAddress > pc {
			sm = lineInfo.stateMachineCache[basePC]
			if sm == nil {
				sm = newStateMachine(lineInfo, lineInfo.Instructions)
				sm.PCToLine(basePC)
				lineInfo.stateMachineCache[basePC] = sm
			}
			sm = sm.copy()
			lineInfo.lastMachineCache[basePC] = sm
		}
	}

	file, line, _ := sm.PCToLine(pc)
	return file, line
}

func (sm *StateMachine) PCToLine(pc uint64) (string, int, bool) {
	if !sm.started {
		if err := sm.next(); err != nil {
			return "", 0, false
		}
	}
	if sm.lastAddress > pc {
		return "", 0, false
	}
	for {
		if sm.valid {
			if sm.address > pc {
				return sm.lastFile, sm.lastLine, true
			}
			if sm.address == pc {
				return sm.file, sm.line, true
			}
		}
		if err := sm.next(); err != nil {
			break
		}
	}
	if sm.valid {
		return sm.file, sm.line, true
	}
	return "", 0, false
}

// LineToPC returns the first PC address associated with filename:lineno.
func (lineInfo *DebugLineInfo) LineToPC(filename string, lineno int) uint64 {
	if lineInfo == nil {
		return 0
	}

	var (
		foundFile bool
		sm        = newStateMachine(lineInfo, lineInfo.Instructions)
	)

	for {
		if err := sm.next(); err != nil {
			break
		}
		if foundFile && sm.file != filename {
			break
		}
		if sm.line == lineno && sm.file == filename {
			foundFile = true
			if sm.valid {
				return sm.address
			}
		}
	}
	return 0
}

func (sm *StateMachine) next() error {
	sm.started = true
	if sm.valid {
		sm.lastAddress, sm.lastFile, sm.lastLine = sm.address, sm.file, sm.line
	}
	if sm.endSeq {
		sm.endSeq = false
		sm.file = sm.dbl.FileNames[0].Path
		sm.line = 1
		sm.column = 0
		sm.isStmt = false
		sm.basicBlock = false
	}
	b, err := sm.buf.ReadByte()
	if err != nil {
		return err
	}
	if int(b) < len(sm.opcodes) {
		sm.lastWasStandard = b != 0
		sm.valid = false
		sm.opcodes[b](sm, sm.buf)
	} else if b < sm.dbl.Prologue.OpcodeBase {
		// unimplemented standard opcode, read the number of arguments specified
		// in the prologue and do nothing with them
		opnum := sm.dbl.Prologue.StdOpLengths[b-1]
		for i := 0; i < int(opnum); i++ {
			util.DecodeSLEB128(sm.buf)
		}
	} else {
		execSpecialOpcode(sm, b)
	}
	return nil
}

func execSpecialOpcode(sm *StateMachine, instr byte) {
	var (
		opcode  = uint8(instr)
		decoded = opcode - sm.dbl.Prologue.OpcodeBase
	)

	if sm.dbl.Prologue.InitialIsStmt == uint8(1) {
		sm.isStmt = true
	}

	sm.lastDelta = int(sm.dbl.Prologue.LineBase + int8(decoded%sm.dbl.Prologue.LineRange))
	sm.line += sm.lastDelta
	sm.address += uint64(decoded/sm.dbl.Prologue.LineRange) * uint64(sm.dbl.Prologue.MinInstrLength)
	sm.basicBlock = false
	sm.lastWasStandard = false
	sm.valid = true
}

func execExtendedOpcode(sm *StateMachine, buf *bytes.Buffer) {
	_, _ = util.DecodeULEB128(buf)
	b, _ := buf.ReadByte()
	if fn, ok := extendedopcodes[b]; ok {
		fn(sm, buf)
	}
}

func copyfn(sm *StateMachine, buf *bytes.Buffer) {
	sm.basicBlock = false
	sm.valid = true
}

func advancepc(sm *StateMachine, buf *bytes.Buffer) {
	addr, _ := util.DecodeULEB128(buf)
	sm.address += addr * uint64(sm.dbl.Prologue.MinInstrLength)
}

func advanceline(sm *StateMachine, buf *bytes.Buffer) {
	line, _ := util.DecodeSLEB128(buf)
	sm.line += int(line)
	sm.lastDelta = int(line)
}

func setfile(sm *StateMachine, buf *bytes.Buffer) {
	i, _ := util.DecodeULEB128(buf)
	if i-1 < uint64(len(sm.dbl.FileNames)) {
		sm.file = sm.dbl.FileNames[i-1].Path
	} else {
		j := (i - 1) - uint64(len(sm.dbl.FileNames))
		if j < uint64(len(sm.definedFiles)) {
			sm.file = sm.definedFiles[j].Path
		} else {
			sm.file = ""
		}
	}
}

func setcolumn(sm *StateMachine, buf *bytes.Buffer) {
	c, _ := util.DecodeULEB128(buf)
	sm.column = uint(c)
}

func negatestmt(sm *StateMachine, buf *bytes.Buffer) {
	sm.isStmt = !sm.isStmt
}

func setbasicblock(sm *StateMachine, buf *bytes.Buffer) {
	sm.basicBlock = true
}

func constaddpc(sm *StateMachine, buf *bytes.Buffer) {
	sm.address += uint64((255-sm.dbl.Prologue.OpcodeBase)/sm.dbl.Prologue.LineRange) * uint64(sm.dbl.Prologue.MinInstrLength)
}

func fixedadvancepc(sm *StateMachine, buf *bytes.Buffer) {
	var operand uint16
	binary.Read(buf, binary.LittleEndian, &operand)

	sm.address += uint64(operand)
}

func endsequence(sm *StateMachine, buf *bytes.Buffer) {
	sm.endSeq = true
	sm.valid = true
}

func setaddress(sm *StateMachine, buf *bytes.Buffer) {
	var addr uint64

	binary.Read(buf, binary.LittleEndian, &addr)

	sm.address = addr
}

func definefile(sm *StateMachine, buf *bytes.Buffer) {
	entry := readFileEntry(sm.dbl, sm.buf, false)
	sm.definedFiles = append(sm.definedFiles, entry)
}
