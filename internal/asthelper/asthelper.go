package asthelper

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"strconv"
)

func StringLit(value string) *ast.BasicLit {
	return &ast.BasicLit{
		Kind:  token.STRING,
		Value: fmt.Sprintf("%q", value),
	}
}

func IntLit(value int) *ast.BasicLit {
	return &ast.BasicLit{
		Kind:  token.INT,
		Value: strconv.Itoa(value),
	}
}

func IndexExpr(name string, index ast.Expr) *ast.IndexExpr {
	return &ast.IndexExpr{
		X:     ast.NewIdent(name),
		Index: index,
	}
}

func CallExpr(fun ast.Expr, args ...ast.Expr) *ast.CallExpr {
	return &ast.CallExpr{
		Fun:  fun,
		Args: args,
	}
}

func LambdaCall(params *ast.FieldList, resultType ast.Expr, block *ast.BlockStmt, args []ast.Expr) *ast.CallExpr {
	funcLit := &ast.FuncLit{
		Type: &ast.FuncType{
			Params: params,
			Results: &ast.FieldList{
				List: []*ast.Field{
					{Type: resultType},
				},
			},
		},
		Body: block,
	}
	return CallExpr(funcLit, args...)
}

func ReturnStmt(results ...ast.Expr) *ast.ReturnStmt {
	return &ast.ReturnStmt{
		Results: results,
	}
}

func BlockStmt(stmts ...ast.Stmt) *ast.BlockStmt {
	return &ast.BlockStmt{List: stmts}
}

func ExprStmt(expr ast.Expr) *ast.ExprStmt {
	return &ast.ExprStmt{X: expr}
}

func DataToByteSlice(data []byte) *ast.CallExpr {
	return &ast.CallExpr{
		Fun: &ast.ArrayType{
			Elt: &ast.Ident{Name: "byte"},
		},
		Args: []ast.Expr{StringLit(string(data))},
	}
}

func DataToArray(data []byte) *ast.CompositeLit {
	elts := make([]ast.Expr, len(data))
	for i, b := range data {
		elts[i] = IntLit(int(b))
	}

	return &ast.CompositeLit{
		Type: &ast.ArrayType{
			Len: IntLit(len(data)),
			Elt: ast.NewIdent("byte"),
		},
		Elts: elts,
	}
}

func SelectExpr(x ast.Expr, sel *ast.Ident) *ast.SelectorExpr {
	return &ast.SelectorExpr{
		X:   x,
		Sel: sel,
	}
}

func AssignDefineStmt(Lhs ast.Expr, Rhs ast.Expr) *ast.AssignStmt {
	return &ast.AssignStmt{
		Lhs: []ast.Expr{Lhs},
		Tok: token.DEFINE,
		Rhs: []ast.Expr{Rhs},
	}
}

func CallExprByName(fun string, args ...ast.Expr) *ast.CallExpr {
	return CallExpr(ast.NewIdent(fun), args...)
}

func AssignStmt(Lhs ast.Expr, Rhs ast.Expr) *ast.AssignStmt {
	return &ast.AssignStmt{
		Lhs: []ast.Expr{Lhs},
		Tok: token.ASSIGN,
		Rhs: []ast.Expr{Rhs},
	}
}

func IndexExprByExpr(xExpr, indexExpr ast.Expr) *ast.IndexExpr {
	return &ast.IndexExpr{X: xExpr, Index: indexExpr}
}

func UnaryExpr(op token.Token, x ast.Expr) *ast.UnaryExpr {
	return &ast.UnaryExpr{
		Op: op,
		X:  x,
	}
}

func StarExpr(x ast.Expr) *ast.StarExpr {
	return &ast.StarExpr{X: x}
}

func ArrayType(len ast.Expr, eltType ast.Expr) *ast.ArrayType {
	return &ast.ArrayType{
		Len: len,
		Elt: eltType,
	}
}

func ByteArrayType(len int64) *ast.ArrayType {
	lenLit := IntLit(int(len))
	return ArrayType(lenLit, ast.NewIdent("byte"))
}

func ByteSliceType() *ast.ArrayType {
	return &ast.ArrayType{Elt: ast.NewIdent("byte")}
}

func BinaryExpr(x ast.Expr, op token.Token, y ast.Expr) *ast.BinaryExpr {
	return &ast.BinaryExpr{
		X:  x,
		Op: op,
		Y:  y,
	}
}

func UintLit(value uint64) *ast.BasicLit {
	return &ast.BasicLit{
		Kind:  token.INT,
		Value: fmt.Sprint(value),
	}
}

func Field(typ ast.Expr, names ...*ast.Ident) *ast.Field {
	return &ast.Field{
		Names: names,
		Type:  typ,
	}
}

func ConstToAst(val constant.Value) ast.Expr {
	switch val.Kind() {
	case constant.Bool:
		return ast.NewIdent(val.ExactString())
	case constant.String:
		return &ast.BasicLit{Kind: token.STRING, Value: val.ExactString()}
	case constant.Int:
		return &ast.BasicLit{Kind: token.INT, Value: val.ExactString()}
	case constant.Float:
		return &ast.BasicLit{Kind: token.FLOAT, Value: val.String()}
	case constant.Complex:
		return CallExprByName("complex", ConstToAst(constant.Real(val)), ConstToAst(constant.Imag(val)))
	default:
		panic("unreachable")
	}
}
