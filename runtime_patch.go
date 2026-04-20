package main

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"

	ah "github.com/guno1928/alosgarble/internal/asthelper"
)

func updateMagicValue(file *ast.File, magicValue uint32) {
	magicUpdated := false

	for _, decl := range file.Decls {
		decl, ok := decl.(*ast.GenDecl)
		if !ok || decl.Tok != token.CONST {
			continue
		}
		for _, spec := range decl.Specs {
			spec, ok := spec.(*ast.ValueSpec)
			if !ok || len(spec.Names) != 1 || len(spec.Values) != 1 {
				continue
			}
			if spec.Names[0].Name == "Go120PCLnTabMagic" {
				spec.Values[0] = &ast.BasicLit{
					Kind:  token.INT,
					Value: strconv.FormatUint(uint64(magicValue), 10),
				}
				magicUpdated = true
			}
		}
	}

	if !magicUpdated {
		panic("magic value not updated")
	}
}

func updateEntryOffset(file *ast.File, entryOffKey uint32) {

	const nameOffField = "nameOff"
	entryOffUpdated := false

	updateEntryOff := func(node ast.Node) bool {
		callExpr, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}

		textSelExpr, ok := callExpr.Fun.(*ast.SelectorExpr)
		if !ok || textSelExpr.Sel.Name != "textAddr" {
			return true
		}

		selExpr, ok := callExpr.Args[0].(*ast.SelectorExpr)
		if !ok {
			return true
		}

		callExpr.Args[0] = &ast.BinaryExpr{
			X:  selExpr,
			Op: token.XOR,
			Y: &ast.ParenExpr{X: &ast.BinaryExpr{
				X: ah.CallExpr(ast.NewIdent("uint32"), &ast.SelectorExpr{
					X:   selExpr.X,
					Sel: ast.NewIdent(nameOffField),
				}),
				Op: token.MUL,
				Y: &ast.BasicLit{
					Kind:  token.INT,
					Value: strconv.FormatUint(uint64(entryOffKey), 10),
				},
			}},
		}
		entryOffUpdated = true
		return false
	}

	var entryFunc *ast.FuncDecl
	for _, decl := range file.Decls {
		decl, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if decl.Name.Name == "entry" {
			entryFunc = decl
			break
		}
	}
	if entryFunc == nil {
		panic("entry function not found")
	}

	ast.Inspect(entryFunc, updateEntryOff)
	if !entryOffUpdated {
		panic("entryOff not found")
	}
}

func stripRuntime(basename string, file *ast.File) {
	stripPrints := func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if !ok {
			return true
		}

		switch id.Name {
		case "print", "println":
			id.Name = "hidePrint"
			return false
		default:
			return true
		}
	}

	for _, decl := range file.Decls {
		funcDecl, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}

		switch basename {
		case "error.go":

			switch funcDecl.Name.Name {
			case "printany", "printanycustomtype":
				funcDecl.Body.List = nil
			}
		case "mgcscavenge.go":

			if funcDecl.Name.Name == "printScavTrace" {
				funcDecl.Body.List = nil
			}
		case "mprof.go":

			if strings.HasPrefix(funcDecl.Name.Name, "trace") {
				funcDecl.Body.List = nil
			}
		case "panic.go":

			switch funcDecl.Name.Name {
			case "preprintpanics", "printpanics":
				funcDecl.Body.List = nil
			}
		case "print.go":

			if funcDecl.Name.Name == "hexdumpWords" {
				funcDecl.Body.List = nil
			}
		case "proc.go":

			if funcDecl.Name.Name == "schedtrace" {
				funcDecl.Body.List = nil
			}
		case "runtime1.go":
			switch funcDecl.Name.Name {
			case "setTraceback":

				funcDecl.Body.List = nil
			}
		case "traceback.go":

			switch funcDecl.Name.Name {
			case "tracebackdefers", "printcreatedby", "printcreatedby1", "traceback", "tracebacktrap", "traceback1", "printAncestorTraceback",
				"printAncestorTracebackFuncInfo", "goroutineheader", "tracebackothers", "tracebackHexdump", "printCgoTraceback":
				funcDecl.Body.List = nil
			case "printOneCgoTraceback":
				funcDecl.Body = ah.BlockStmt(ah.ReturnStmt(ast.NewIdent("false")))
			default:
				if strings.HasPrefix(funcDecl.Name.Name, "print") {
					funcDecl.Body.List = nil
				}
			}
		}

	}

	if basename == "print.go" {
		file.Decls = append(file.Decls, hidePrintDecl)
		return
	}

	ast.Inspect(file, stripPrints)
}

var hidePrintDecl = &ast.FuncDecl{
	Name: ast.NewIdent("hidePrint"),
	Type: &ast.FuncType{Params: &ast.FieldList{
		List: []*ast.Field{{
			Names: []*ast.Ident{{Name: "args"}},
			Type: &ast.Ellipsis{Elt: &ast.InterfaceType{
				Methods: &ast.FieldList{},
			}},
		}},
	}},
	Body: &ast.BlockStmt{},
}
