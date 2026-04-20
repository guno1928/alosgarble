package ctrlflow

import (
	"fmt"
	"go/ast"
	"go/token"
	mathrand "math/rand"
	"strconv"

	ah "github.com/guno1928/alosgarble/internal/asthelper"
	"github.com/guno1928/alosgarble/internal/literals"
	"golang.org/x/tools/go/ssa"
)

var hardeningMap = map[string]dispatcherHardening{
	"xor":            xorHardening{},
	"delegate_table": delegateTableHardening{},
}

func newDispatcherHardening(names []string) dispatcherHardening {
	hardenings := make([]dispatcherHardening, len(names))
	for i, name := range names {
		h, ok := hardeningMap[name]
		if !ok {
			panic(fmt.Sprintf("unknown dispatcher hardening %q", name))
		}
		hardenings[i] = h
	}
	if len(hardenings) == 1 {
		return hardenings[0]
	}
	return multiHardening(hardenings)
}

func getRandomName(rnd *mathrand.Rand) string {
	return "_garble" + strconv.FormatUint(rnd.Uint64(), 32)
}

func generateKeys(count int, blacklistedKeys []int, rnd *mathrand.Rand) []int {
	m := make(map[int]bool, count)
	for _, i := range blacklistedKeys {
		m[i] = true
	}
	arr := make([]int, 0, count)
	for count > len(arr) {
		key := int(rnd.Int31())
		if key == 0 || m[key] {
			continue
		}
		arr = append(arr, key)
		m[key] = true
	}
	return arr
}

type dispatcherHardening interface {
	Apply(dispatcher []cfgInfo, ssaRemap map[ssa.Value]ast.Expr, rnd *mathrand.Rand) (ast.Decl, ast.Stmt)
}

type multiHardening []dispatcherHardening

func (r multiHardening) Apply(info []cfgInfo, ssaRemap map[ssa.Value]ast.Expr, rnd *mathrand.Rand) (ast.Decl, ast.Stmt) {
	return r[rnd.Intn(len(r))].Apply(info, ssaRemap, rnd)
}

type xorHardening struct{}

func (xorHardening) Apply(dispatcher []cfgInfo, ssaRemap map[ssa.Value]ast.Expr, rnd *mathrand.Rand) (ast.Decl, ast.Stmt) {
	globalKeyName, localKeyName := getRandomName(rnd), getRandomName(rnd)

	firstKey := int(rnd.Int31())
	secondKey := make([]byte, literals.MinSize+mathrand.Intn(literals.MinSize))
	if _, err := rnd.Read(secondKey); err != nil {
		panic(err)
	}

	globalKey := firstKey
	for _, b := range secondKey {
		globalKey ^= int(b)
	}

	newKeys := generateKeys(len(dispatcher), []int{globalKey}, rnd)
	for i, info := range dispatcher {
		k := newKeys[i]

		ssaRemap[info.CompareVar] = ah.IntLit(k ^ globalKey)
		ssaRemap[info.StoreVar] = &ast.ParenExpr{X: &ast.BinaryExpr{X: ast.NewIdent(localKeyName), Op: token.XOR, Y: ah.IntLit(k)}}
	}

	globalKeyDecl := &ast.GenDecl{
		Tok: token.VAR,
		Specs: []ast.Spec{
			&ast.ValueSpec{
				Names: []*ast.Ident{ast.NewIdent(globalKeyName)},
				Values: []ast.Expr{ah.CallExpr(&ast.FuncLit{
					Type: &ast.FuncType{
						Params: &ast.FieldList{List: []*ast.Field{{
							Names: []*ast.Ident{ast.NewIdent("secondKey")},
							Type:  &ast.ArrayType{Len: ah.IntLit(len(secondKey)), Elt: ast.NewIdent("byte")},
						}}},
						Results: &ast.FieldList{List: []*ast.Field{{
							Type: ast.NewIdent("int"),
						}}},
					},
					Body: &ast.BlockStmt{List: []ast.Stmt{
						ah.AssignDefineStmt(ast.NewIdent("r"), ah.IntLit(firstKey)),
						&ast.RangeStmt{
							Key:   ast.NewIdent("_"),
							Value: ast.NewIdent("b"),
							Tok:   token.DEFINE,
							X:     ast.NewIdent("secondKey"),
							Body: &ast.BlockStmt{List: []ast.Stmt{&ast.AssignStmt{
								Lhs: []ast.Expr{ast.NewIdent("r")},
								Tok: token.XOR_ASSIGN,
								Rhs: []ast.Expr{&ast.CallExpr{
									Fun:  ast.NewIdent("int"),
									Args: []ast.Expr{ast.NewIdent("b")},
								}},
							}}},
						},
						ah.ReturnStmt(ast.NewIdent("r")),
					}},
				}, ah.DataToArray(secondKey))},
			},
		},
	}
	return globalKeyDecl, ah.AssignDefineStmt(ast.NewIdent(localKeyName), ast.NewIdent(globalKeyName))
}

type delegateTableHardening struct{}

func (delegateTableHardening) Apply(dispatcher []cfgInfo, ssaRemap map[ssa.Value]ast.Expr, rnd *mathrand.Rand) (ast.Decl, ast.Stmt) {
	keySize := literals.MinSize + mathrand.Intn(literals.MinSize)

	delegateCount := min(keySize, len(dispatcher))

	delegateKeyIdxs := rnd.Perm(keySize)[:delegateCount]
	delegateLocalKeys := generateKeys(delegateCount, nil, rnd)

	key := make([]byte, keySize)
	if _, err := rnd.Read(key); err != nil {
		panic(err)
	}

	delegateIndexes := make([]int, len(dispatcher))
	delegateKeys := make([]int, len(dispatcher))
	for i := range delegateIndexes {
		delegateIdx := rnd.Intn(delegateCount)
		delegateIndexes[i] = delegateIdx
		delegateKeys[i] = int(key[delegateKeyIdxs[delegateIdx]]) ^ delegateLocalKeys[delegateIdx]
	}
	newKeys := generateKeys(len(dispatcher), delegateKeys, rnd)
	globalTableName := getRandomName(rnd)
	for i, info := range dispatcher {
		k, delegateIdx, delegateKey := newKeys[i], delegateIndexes[i], delegateKeys[i]
		encryptedKey := k ^ delegateKey

		ssaRemap[info.CompareVar] = ah.IntLit(k)
		ssaRemap[info.StoreVar] = ah.CallExpr(ah.IndexExprByExpr(ast.NewIdent(globalTableName), ah.IntLit(delegateIdx)), ah.IntLit(encryptedKey))
	}

	delegatesAst := make([]ast.Expr, delegateCount)
	for i := range delegateCount {

		delegateAst := &ast.FuncLit{
			Type: &ast.FuncType{
				Params: &ast.FieldList{List: []*ast.Field{{
					Names: []*ast.Ident{ast.NewIdent("i")},
					Type:  ast.NewIdent("int"),
				}}},
				Results: &ast.FieldList{List: []*ast.Field{{
					Type: ast.NewIdent("int"),
				}}},
			},
			Body: &ast.BlockStmt{List: []ast.Stmt{
				&ast.ReturnStmt{Results: []ast.Expr{
					&ast.BinaryExpr{
						X:  ast.NewIdent("i"),
						Op: token.XOR,
						Y: &ast.BinaryExpr{
							X: ah.CallExprByName("int", &ast.IndexExpr{
								X:     ast.NewIdent("key"),
								Index: ah.IntLit(delegateKeyIdxs[i]),
							}),
							Op: token.XOR,
							Y:  ah.IntLit(delegateLocalKeys[i]),
						},
					},
				}},
			}},
		}
		delegatesAst[i] = delegateAst
	}

	delegateTableDecl := &ast.GenDecl{
		Tok: token.VAR,
		Specs: []ast.Spec{
			&ast.ValueSpec{
				Names: []*ast.Ident{ast.NewIdent(globalTableName)},
				Values: []ast.Expr{
					&ast.CallExpr{
						Fun: &ast.ParenExpr{X: &ast.FuncLit{
							Type: &ast.FuncType{
								Params: &ast.FieldList{List: []*ast.Field{{
									Names: []*ast.Ident{ast.NewIdent("key")},
									Type:  &ast.ArrayType{Len: ah.IntLit(len(key)), Elt: ast.NewIdent("byte")},
								}}},
								Results: &ast.FieldList{List: []*ast.Field{{Type: &ast.ArrayType{
									Len: ah.IntLit(delegateCount),
									Elt: &ast.FuncType{
										Params: &ast.FieldList{List: []*ast.Field{{
											Type: ast.NewIdent("int"),
										}}},
										Results: &ast.FieldList{List: []*ast.Field{{
											Type: ast.NewIdent("int"),
										}}},
									},
								}}}},
							},
							Body: &ast.BlockStmt{List: []ast.Stmt{
								&ast.ReturnStmt{Results: []ast.Expr{&ast.CompositeLit{
									Type: &ast.ArrayType{
										Len: ah.IntLit(delegateCount),
										Elt: &ast.FuncType{
											Params: &ast.FieldList{List: []*ast.Field{{
												Type: ast.NewIdent("int"),
											}}},
											Results: &ast.FieldList{List: []*ast.Field{{
												Type: ast.NewIdent("int"),
											}}},
										},
									},
									Elts: delegatesAst,
								}}},
							}},
						}},
						Args: []ast.Expr{ah.DataToArray(key)},
					},
				},
			},
		},
	}

	return delegateTableDecl, nil
}
