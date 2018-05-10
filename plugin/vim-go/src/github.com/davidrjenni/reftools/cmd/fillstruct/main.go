// Copyright (c) 2017 David R. Jenni. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Fillstruct fills a struct literal with default values.
//
// For example, given the following types,
//
//	type User struct {
//		ID   int64
//		Name string
//		Addr *Address
//	}
//
//	type Address struct {
//		City   string
//		ZIP    int
//		LatLng [2]float64
//	}
//
// the following struct literal
//
//	var frank = User{}
//
// becomes:
//
//	var frank = User{
//		ID:   0,
//		Name: "",
//		Addr: &Address{
//			City: "",
//			ZIP:  0,
//			LatLng: [2]float64{
//				0.0,
//				0.0,
//			},
//		},
//	}
//
// after applying fillstruct.
//
// Usage:
//
// 	% fillstruct [-modified] -file=<filename> -offset=<byte offset> -line=<line number>
//
// Flags:
//
// -file:     filename
//
// -modified: read an archive of modified files from stdin
//
// -offset:   byte offset of the struct literal, optional if -line is present
//
// -line:     line number of the struct literal, optional if -offset is present
//
//
// If -offset as well as -line are present, then the tool first uses the
// more specific offset information. If there was no struct literal found
// at the given offset, then the line information is used.
//
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/build"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"log"
	"os"
	"path/filepath"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/buildutil"
	"golang.org/x/tools/go/loader"
)

var errNotFound = errors.New("no struct literal found at selection")

func main() {
	log.SetFlags(0)
	log.SetPrefix("fillstruct: ")

	var (
		filename = flag.String("file", "", "filename")
		modified = flag.Bool("modified", false, "read an archive of modified files from stdin")
		offset   = flag.Int("offset", 0, "byte offset of the struct literal, optional if -line is present")
		line     = flag.Int("line", 0, "line number of the struct literal, optional if -offset is present")
	)
	flag.Parse()

	if (*offset == 0 && *line == 0) || *filename == "" {
		flag.PrintDefaults()
		os.Exit(1)
	}

	path, err := absPath(*filename)
	if err != nil {
		log.Fatal(err)
	}

	lprog, err := load(path, *modified)
	if err != nil {
		log.Fatal(err)
	}

	if *offset > 0 {
		err = byOffset(lprog, path, *offset)
		switch err {
		case nil:
			return
		case errNotFound:
			// try to use line information
		default:
			log.Fatal(err)
		}
	}

	if *line > 0 {
		err = byLine(lprog, path, *line)
		switch err {
		case nil:
			return
		default:
			log.Fatal(err)
		}
	}

	log.Fatal(errNotFound)
}

func absPath(filename string) (string, error) {
	eval, err := filepath.EvalSymlinks(filename)
	if err != nil {
		return "", err
	}
	return filepath.Abs(eval)
}

func load(path string, modified bool) (*loader.Program, error) {
	ctx := &build.Default
	if modified {
		archive, err := buildutil.ParseOverlayArchive(os.Stdin)
		if err != nil {
			return nil, err
		}
		ctx = buildutil.OverlayContext(ctx, archive)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	pkg, err := buildutil.ContainingPackage(ctx, cwd, path)
	if err != nil {
		return nil, err
	}
	conf := &loader.Config{Build: ctx}
	allowErrors(conf)
	conf.ImportWithTests(pkg.ImportPath)
	return conf.Load()
}

func byOffset(lprog *loader.Program, path string, offset int) error {
	f, pkg, pos, err := findPos(lprog, path, offset)
	if err != nil {
		return err
	}

	lit, litInfo, err := findCompositeLit(f, pkg.Info, pos)
	if err != nil {
		return err
	}

	start := lprog.Fset.Position(lit.Pos()).Offset
	end := lprog.Fset.Position(lit.End()).Offset

	importNames := buildImportNameMap(f)
	newlit, lines := zeroValue(pkg.Pkg, importNames, lit, litInfo)
	out, err := prepareOutput(newlit, lines, start, end)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode([]output{out})
}

func findPos(lprog *loader.Program, path string, off int) (*ast.File, *loader.PackageInfo, token.Pos, error) {
	for _, pkg := range lprog.InitialPackages() {
		for _, f := range pkg.Files {
			if file := lprog.Fset.File(f.Pos()); file.Name() == path {
				if off > file.Size() {
					return nil, nil, 0,
						fmt.Errorf("file size (%d) is smaller than given offset (%d)",
							file.Size(), off)
				}
				return f, pkg, file.Pos(off), nil
			}
		}
	}

	return nil, nil, 0, fmt.Errorf("could not find file %q", path)
}

func findCompositeLit(f *ast.File, info types.Info, pos token.Pos) (*ast.CompositeLit, litInfo, error) {
	var linfo litInfo
	path, _ := astutil.PathEnclosingInterval(f, pos, pos)
	for i, n := range path {
		if lit, ok := n.(*ast.CompositeLit); ok {
			linfo.name, _ = info.Types[lit].Type.(*types.Named)
			linfo.typ, ok = info.Types[lit].Type.Underlying().(*types.Struct)
			if !ok {
				return nil, linfo, errNotFound
			}
			if expr, ok := path[i+1].(ast.Expr); ok {
				linfo.hideType = hideType(info.Types[expr].Type)
			}
			return lit, linfo, nil
		}
	}
	return nil, linfo, errNotFound
}

func byLine(lprog *loader.Program, path string, line int) (err error) {
	var f *ast.File
	var pkg *loader.PackageInfo
	for _, p := range lprog.InitialPackages() {
		for _, af := range p.Files {
			if file := lprog.Fset.File(af.Pos()); file.Name() == path {
				f = af
				pkg = p
			}
		}
	}
	if f == nil || pkg == nil {
		return fmt.Errorf("could not find file %q", path)
	}
	importNames := buildImportNameMap(f)

	var outs []output
	var prev types.Type
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		startLine := lprog.Fset.Position(lit.Pos()).Line
		endLine := lprog.Fset.Position(lit.End()).Line
		if !(startLine <= line && line <= endLine) {
			return true
		}

		var info litInfo
		info.name, _ = pkg.Types[lit].Type.(*types.Named)
		info.typ, ok = pkg.Types[lit].Type.Underlying().(*types.Struct)
		if !ok {
			prev = pkg.Types[lit].Type.Underlying()
			err = errNotFound
			return true
		}
		info.hideType = hideType(prev)

		startOff := lprog.Fset.Position(lit.Pos()).Offset
		endOff := lprog.Fset.Position(lit.End()).Offset
		newlit, lines := zeroValue(pkg.Pkg, importNames, lit, info)

		var out output
		out, err = prepareOutput(newlit, lines, startOff, endOff)
		if err != nil {
			return false
		}
		outs = append(outs, out)
		return false
	})
	if err != nil {
		return err
	}
	if len(outs) == 0 {
		return errNotFound
	}

	for i := len(outs)/2 - 1; i >= 0; i-- {
		opp := len(outs) - 1 - i
		outs[i], outs[opp] = outs[opp], outs[i]
	}

	return json.NewEncoder(os.Stdout).Encode(outs)
}

func hideType(t types.Type) bool {
	switch t.(type) {
	case *types.Array:
		return true
	case *types.Map:
		return true
	case *types.Slice:
		return true
	default:
		return false
	}
}

func buildImportNameMap(f *ast.File) map[string]string {
	imports := make(map[string]string)
	for _, i := range f.Imports {
		if i.Name != nil && i.Name.Name != "_" {
			path := i.Path.Value
			imports[path[1:len(path)-1]] = i.Name.Name
		}
	}
	return imports
}

type output struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Code  string `json:"code"`
}

func prepareOutput(n ast.Node, lines, start, end int) (output, error) {
	fset := token.NewFileSet()
	file := fset.AddFile("", -1, lines)
	for i := 1; i <= lines; i++ {
		file.AddLine(i)
	}

	var buf bytes.Buffer
	if err := format.Node(&buf, fset, n); err != nil {
		return output{}, err
	}
	return output{
		Start: start,
		End:   end,
		Code:  buf.String(),
	}, nil
}

func allowErrors(conf *loader.Config) {
	ctxt := *conf.Build
	ctxt.CgoEnabled = false
	conf.Build = &ctxt
	conf.AllowErrors = true
	conf.ParserMode = parser.AllErrors
	conf.TypeChecker.Error = func(error) {}
}
