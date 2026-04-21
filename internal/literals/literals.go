package literals

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	mathrand "math/rand"
	"strings"

	ah "github.com/guno1928/alosgarble/internal/asthelper"
	"golang.org/x/tools/go/ast/astutil"
)

const MinSize = 8

const MaxSize = 2 << 10

const MaxSizeExpensive = 256

const (
	minStringJunkBytes = 2

	maxStringJunkBytes = 8
)

type NameProviderFunc func(rand *mathrand.Rand, baseName string) string

var GuardBoolName string

func Obfuscate(rand *mathrand.Rand, file *ast.File, info *types.Info, linkStrings map[*types.Var]string, nameFunc NameProviderFunc) *ast.File {
	or := newObfRand(rand, file, nameFunc)
	pre := func(cursor *astutil.Cursor) bool {
		switch node := cursor.Node().(type) {
		case *ast.FuncDecl:
			or.funcDepth++
			if node.Recv == nil && node.Name != nil && node.Name.Name == "init" {
				or.initDepth++
			}

			if node.Doc != nil {
				for _, comment := range node.Doc.List {
					if strings.HasPrefix(comment.Text, "//go:nosplit") {
						return false
					}
				}
			}
		case *ast.GenDecl:

			if node.Tok == token.CONST {
				return false
			}
		case *ast.ValueSpec:
			for _, name := range node.Names {
				obj, _ := info.Defs[name].(*types.Var)
				if obj == nil {
					continue
				}
				if _, e := linkStrings[obj]; e {

					return false
				}
			}
		}
		return true
	}

	post := func(cursor *astutil.Cursor) bool {
		if fd, ok := cursor.Node().(*ast.FuncDecl); ok {
			or.funcDepth--
			if fd.Recv == nil && fd.Name != nil && fd.Name.Name == "init" && or.initDepth > 0 {
				or.initDepth--
			}
		}
		node, ok := cursor.Node().(ast.Expr)
		if !ok {
			return true
		}

		typeAndValue := info.Types[node]
		if !typeAndValue.IsValue() {
			return true
		}

		if typeAndValue.Type == types.Typ[types.String] && typeAndValue.Value != nil {
			value := constant.StringVal(typeAndValue.Value)
			if len(value) < MinSize || len(value) > MaxSize {
				return true
			}

			cursor.Replace(withPos(obfuscateString(or, value), node.Pos()))

			return true
		}

		switch node := node.(type) {
		case *ast.UnaryExpr:

			if node.Op != token.AND {
				return true
			}

			if child, ok := node.X.(*ast.CompositeLit); ok {
				newnode := handleCompositeLiteral(or, true, child, info)
				if newnode != nil {
					cursor.Replace(newnode)
				}
			}

		case *ast.CompositeLit:

			parent, ok := cursor.Parent().(*ast.UnaryExpr)
			if ok && parent.Op == token.AND {
				return true
			}

			newnode := handleCompositeLiteral(or, false, node, info)
			if newnode != nil {
				cursor.Replace(newnode)
			}
		}

		return true
	}

	newFile := astutil.Apply(file, pre, post).(*ast.File)
	or.proxyDispatcher.AddToFile(newFile)
	return newFile
}

func handleCompositeLiteral(or *obfRand, isPointer bool, node *ast.CompositeLit, info *types.Info) ast.Node {
	if len(node.Elts) < MinSize || len(node.Elts) > MaxSize {
		return nil
	}

	byteType := types.Universe.Lookup("byte").Type()

	var arrayLen int64
	switch y := info.TypeOf(node.Type).(type) {
	case *types.Array:
		if y.Elem() != byteType {
			return nil
		}

		arrayLen = y.Len()

	case *types.Slice:
		if y.Elem() != byteType {
			return nil
		}

	default:
		return nil
	}

	data := make([]byte, 0, len(node.Elts))

	for _, el := range node.Elts {
		elType := info.Types[el]

		if elType.Value == nil || elType.Value.Kind() != constant.Int {
			return nil
		}

		value, ok := constant.Uint64Val(elType.Value)
		if !ok {
			panic(fmt.Sprintf("cannot parse byte value: %v", elType.Value))
		}

		data = append(data, byte(value))
	}

	if arrayLen > 0 {
		return withPos(obfuscateByteArray(or, isPointer, data, arrayLen), node.Pos())
	}

	return withPos(obfuscateByteSlice(or, isPointer, data), node.Pos())
}

func withPos(node ast.Node, pos token.Pos) ast.Node {
	for node := range ast.Preorder(node) {
		switch node := node.(type) {
		case *ast.BasicLit:
			node.ValuePos = pos
		case *ast.Ident:
			node.NamePos = pos
		case *ast.CompositeLit:
			node.Lbrace = pos
			node.Rbrace = pos
		case *ast.ArrayType:
			node.Lbrack = pos
		case *ast.FuncType:
			node.Func = pos
		case *ast.BinaryExpr:
			node.OpPos = pos
		case *ast.StarExpr:
			node.Star = pos
		case *ast.CallExpr:
			node.Lparen = pos
			node.Rparen = pos

		case *ast.GenDecl:
			node.TokPos = pos
		case *ast.ReturnStmt:
			node.Return = pos
		case *ast.ForStmt:
			node.For = pos
		case *ast.RangeStmt:
			node.For = pos
		case *ast.BranchStmt:
			node.TokPos = pos
		}
	}
	return node
}

func obfuscateString(or *obfRand, data string) *ast.CallExpr {
	obf := or.pickObfuscator(len(data))

	junkBytes := make([]byte, or.rnd.Intn(maxStringJunkBytes-minStringJunkBytes)+minStringJunkBytes)
	or.rnd.Read(junkBytes)
	splitIdx := or.rnd.Intn(len(junkBytes))

	extKeys := randExtKeys(or.rnd)

	plainData := []byte(data)
	plainDataWithJunkBytes := append(append(junkBytes[:splitIdx], plainData...), junkBytes[splitIdx:]...)

	block := obf.obfuscate(or.rnd, plainDataWithJunkBytes, extKeys)
	params, args := extKeysToParams(or, extKeys)

	funcTyp := &ast.FuncType{
		Params: &ast.FieldList{List: []*ast.Field{{
			Type: ah.ByteSliceType(),
		}}},
		Results: &ast.FieldList{List: []*ast.Field{{
			Type: ast.NewIdent("string"),
		}}},
	}
	funcVal := &ast.FuncLit{
		Type: &ast.FuncType{
			Params: &ast.FieldList{List: []*ast.Field{{
				Names: []*ast.Ident{ast.NewIdent("x")},
				Type:  ah.ByteSliceType(),
			}}},
			Results: &ast.FieldList{List: []*ast.Field{{
				Type: ast.NewIdent("string"),
			}}},
		},
		Body: ah.BlockStmt(
			ah.ReturnStmt(
				ah.CallExprByName("string",
					&ast.SliceExpr{
						X:    ast.NewIdent("x"),
						Low:  ah.IntLit(splitIdx),
						High: ah.IntLit(splitIdx + len(plainData)),
					},
				),
			),
		),
	}
	if GuardBoolName != "" && or.funcDepth > 0 && or.initDepth == 0 {

		block.List = append(block.List, guardActiveCheck(GuardBoolName))
	}
	block.List = append(block.List, ah.ReturnStmt(ah.CallExpr(or.proxyDispatcher.HideValue(funcVal, funcTyp), ast.NewIdent("data"))))
	return ah.LambdaCall(params, ast.NewIdent("string"), block, args)
}

func obfuscateByteSlice(or *obfRand, isPointer bool, data []byte) *ast.CallExpr {
	obf := or.pickObfuscator(len(data))

	extKeys := randExtKeys(or.rnd)
	block := obf.obfuscate(or.rnd, data, extKeys)
	params, args := extKeysToParams(or, extKeys)

	if isPointer {
		if GuardBoolName != "" && or.funcDepth > 0 && or.initDepth == 0 {
			block.List = append(block.List, guardActiveCheck(GuardBoolName))
		}
		block.List = append(block.List, ah.ReturnStmt(
			ah.UnaryExpr(token.AND, ast.NewIdent("data")),
		))
		return ah.LambdaCall(params, ah.StarExpr(ah.ByteSliceType()), block, args)
	}

	if GuardBoolName != "" && or.funcDepth > 0 && or.initDepth == 0 {
		block.List = append(block.List, guardActiveCheck(GuardBoolName))
	}
	block.List = append(block.List, ah.ReturnStmt(ast.NewIdent("data")))
	return ah.LambdaCall(params, ah.ByteSliceType(), block, args)
}

func obfuscateByteArray(or *obfRand, isPointer bool, data []byte, length int64) *ast.CallExpr {
	obf := or.pickObfuscator(len(data))

	extKeys := randExtKeys(or.rnd)
	block := obf.obfuscate(or.rnd, data, extKeys)
	params, args := extKeysToParams(or, extKeys)

	arrayType := ah.ByteArrayType(length)

	sliceToArray := []ast.Stmt{
		&ast.DeclStmt{
			Decl: &ast.GenDecl{
				Tok: token.VAR,
				Specs: []ast.Spec{&ast.ValueSpec{
					Names: []*ast.Ident{ast.NewIdent("newdata")},
					Type:  arrayType,
				}},
			},
		},
		&ast.RangeStmt{
			Key: ast.NewIdent("i"),
			Tok: token.DEFINE,
			X:   ast.NewIdent("data"),
			Body: ah.BlockStmt(
				ah.AssignStmt(
					ah.IndexExprByExpr(ast.NewIdent("newdata"), ast.NewIdent("i")),
					ah.IndexExprByExpr(ast.NewIdent("data"), ast.NewIdent("i")),
				),
			),
		},
	}

	var retexpr ast.Expr = ast.NewIdent("newdata")
	if isPointer {
		retexpr = ah.UnaryExpr(token.AND, retexpr)
	}

	retStmt := ah.ReturnStmt(retexpr)

	if GuardBoolName != "" && or.funcDepth > 0 && or.initDepth == 0 {
		block.List = append(block.List, sliceToArray...)
		block.List = append(block.List, guardActiveCheck(GuardBoolName))
		block.List = append(block.List, retStmt)
	} else {
		block.List = append(block.List, sliceToArray...)
		block.List = append(block.List, retStmt)
	}

	if isPointer {
		return ah.LambdaCall(params, ah.StarExpr(arrayType), block, args)
	}

	return ah.LambdaCall(params, arrayType, block, args)
}

func (or *obfRand) pickObfuscator(size int) obfuscator {
	if size < MinSize || size > MaxSize {
		panic(fmt.Sprintf("nextObfuscator called with size %d outside [%d, %d]", size, MinSize, MaxSize))
	}
	if size <= MaxSizeExpensive {
		return Obfuscators[or.rnd.Intn(len(Obfuscators))]
	}
	return CheapObfuscators[or.rnd.Intn(len(CheapObfuscators))]
}

func guardActiveCheck(boolName string) ast.Stmt {

	corruptLoop := &ast.RangeStmt{
		Key: ast.NewIdent("_gi"),
		Tok: token.DEFINE,
		X:   ast.NewIdent("data"),
		Body: ah.BlockStmt(
			&ast.AssignStmt{
				Lhs: []ast.Expr{
					&ast.IndexExpr{
						X:     ast.NewIdent("data"),
						Index: ast.NewIdent("_gi"),
					},
				},
				Tok: token.XOR_ASSIGN,
				Rhs: []ast.Expr{ah.IntLit(0xFF)},
			},
		),
	}

	return &ast.IfStmt{
		Cond: &ast.UnaryExpr{
			Op: token.NOT,
			X:  ast.NewIdent(boolName),
		},
		Body: ah.BlockStmt(corruptLoop),
	}
}
