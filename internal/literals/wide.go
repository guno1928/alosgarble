package literals

import (
	"go/ast"
	"go/token"
	mathrand "math/rand"
	"strconv"

	ah "github.com/guno1928/alosgarble/internal/asthelper"
)

type wide struct{}

var _ obfuscator = wide{}

const fragLetters = "abcdefghijklmnopqrstuvwxyz"

var wideNamePool = [...]string{
	"rv", "bv", "cv", "dv", "ev", "fv", "nv", "sv", "tv", "wv", "xv", "yv", "zv",
	"buf", "tmp", "val", "idx", "cnt", "pos", "off", "cur", "acc", "ptr",
	"raw", "out", "dst", "src", "rem", "res", "ref",
	"st", "cs", "ks", "ds", "es", "fs", "gs", "hs", "ws", "ps", "ts",
	"it", "kp", "vp", "cp", "dp", "ep", "fp", "gp",
}

func widePickNames(rnd *mathrand.Rand, n int) []string {
	work := make([]string, len(wideNamePool))
	copy(work, wideNamePool[:])
	for i := 0; i < n; i++ {
		j := i + rnd.Intn(len(work)-i)
		work[i], work[j] = work[j], work[i]
	}
	return work[:n]
}

func (wide) obfuscate(obfRand *mathrand.Rand, data []byte, extKeys []*externalKey) *ast.BlockStmt {

	key := make([]byte, len(data))
	obfRand.Read(key)

	storedCheck := wideChecksum(data)

	enc := make([]byte, len(data))
	for i := range data {
		enc[i] = data[i] ^ key[i]
	}
	n := len(data)

	K := wideFragmentCount(obfRand, n)
	fragments, fragOffsets := wideFragmentKey(obfRand, key, K)

	declOrder := obfRand.Perm(K)
	aliasPerm := obfRand.Perm(K)
	aliasDeclOrder := obfRand.Perm(K)
	copyOrder := obfRand.Perm(K)

	fragName := make([]string, K)
	aliasName := make([]string, K)
	for declPos, fi := range declOrder {
		fragName[fi] = string(fragLetters[declPos])
	}
	for aliasPos, fi := range aliasPerm {
		aliasName[fi] = string(fragLetters[K+aliasPos])
	}

	numDecoyPairs := 1 + obfRand.Intn(2)
	type decoyPair struct {
		nameA, nameB string
		idx          int
		contentA     []byte
		contentB     []byte
	}
	decoys := make([]decoyPair, numDecoyPairs)
	for dp := range decoys {
		sz := 8 + obfRand.Intn(16)
		content := make([]byte, sz)
		obfRand.Read(content)
		ca := make([]byte, sz)
		copy(ca, content)
		cb := make([]byte, sz)
		copy(cb, content)
		decoys[dp] = decoyPair{
			nameA:    string(fragLetters[2*K+2*dp]),
			nameB:    string(fragLetters[2*K+2*dp+1]),
			idx:      obfRand.Intn(sz),
			contentA: ca,
			contentB: cb,
		}
	}

	sn := widePickNames(obfRand, 12+K)
	nEnc := sn[0]
	nKey := sn[1]
	nCheck := sn[2]
	nShift := sn[3]
	nI := sn[4]
	nKV := sn[5]
	nPV := sn[6]
	nJ := sn[7]
	nRem := sn[8]
	nEV := sn[9]
	nQ := sn[10]
	nST := sn[11]
	hop2Names := sn[12 : 12+K]

	loopStrategy := obfRand.Intn(13) // #4: 13 structural variants (was 10)

	keyAssembly := obfRand.Intn(6)

	tamperStrategy := obfRand.Intn(5)

	aliasDepth := obfRand.Intn(2)

	useAndReset := obfRand.Intn(2) == 0

	dataFirst := obfRand.Intn(2) == 0

	decoyBefore := obfRand.Intn(2) == 0

	checkSeed := obfRand.Uint64()
	compConst := storedCheck ^ checkSeed

	tamperByte := byte(1 + obfRand.Intn(255))

	dataPos := obfRand.Intn(3)

	type declEntry struct {
		name  string
		bytes []byte
	}
	declSeq := make([]declEntry, K+2*numDecoyPairs)
	for declPos, fi := range declOrder {
		declSeq[declPos] = declEntry{fragName[fi], fragments[fi]}
	}
	for dp, d := range decoys {
		declSeq[K+2*dp] = declEntry{d.nameA, d.contentA}
		declSeq[K+2*dp+1] = declEntry{d.nameB, d.contentB}
	}
	obfRand.Shuffle(len(declSeq), func(i, j int) {
		declSeq[i], declSeq[j] = declSeq[j], declSeq[i]
	})

	var stmts []ast.Stmt

	stmts = append(stmts, &ast.AssignStmt{
		Lhs: []ast.Expr{ast.NewIdent(nEnc)},
		Tok: token.DEFINE,
		Rhs: []ast.Expr{dataToByteSliceWithExtKeys(obfRand, enc, extKeys)},
	})

	for _, entry := range declSeq {
		stmts = append(stmts, &ast.AssignStmt{
			Lhs: []ast.Expr{ast.NewIdent(entry.name)},
			Tok: token.DEFINE,
			Rhs: []ast.Expr{dataToByteSliceWithExtKeys(obfRand, entry.bytes, extKeys)},
		})
	}

	for _, fi := range aliasDeclOrder {
		stmts = append(stmts, &ast.AssignStmt{
			Lhs: []ast.Expr{ast.NewIdent(aliasName[fi])},
			Tok: token.DEFINE,
			Rhs: []ast.Expr{ast.NewIdent(fragName[fi])},
		})
	}

	effectiveAlias := aliasName
	if aliasDepth == 1 {

		for _, fi := range aliasDeclOrder {
			stmts = append(stmts, &ast.AssignStmt{
				Lhs: []ast.Expr{ast.NewIdent(hop2Names[fi])},
				Tok: token.DEFINE,
				Rhs: []ast.Expr{ast.NewIdent(aliasName[fi])},
			})
		}
		effectiveAlias = hop2Names
	}

	makeDataDecl := func() ast.Stmt {
		return ah.AssignDefineStmt(
			ast.NewIdent("data"),
			&ast.CallExpr{
				Fun:  ast.NewIdent("make"),
				Args: []ast.Expr{&ast.ArrayType{Elt: ast.NewIdent("byte")}, ah.IntLit(n)},
			},
		)
	}

	if dataPos == 0 {
		stmts = append(stmts, makeDataDecl())
	}

	switch keyAssembly {

	case 0:

		stmts = append(stmts, ah.AssignDefineStmt(
			ast.NewIdent(nKey),
			&ast.CallExpr{Fun: ast.NewIdent("make"),
				Args: []ast.Expr{&ast.ArrayType{Elt: ast.NewIdent("byte")}, ah.IntLit(n)}},
		))
		if dataPos == 1 {
			stmts = append(stmts, makeDataDecl())
		}
		for _, fi := range copyOrder {
			low := wideOffsetExpr(fi, effectiveAlias)
			stmts = append(stmts, &ast.ExprStmt{X: &ast.CallExpr{
				Fun: ast.NewIdent("copy"),
				Args: []ast.Expr{
					&ast.SliceExpr{X: ast.NewIdent(nKey), Low: low},
					ast.NewIdent(effectiveAlias[fi]),
				},
			}})
		}

	case 1:

		stmts = append(stmts, ah.AssignDefineStmt(
			ast.NewIdent(nKey),
			&ast.CallExpr{Fun: ast.NewIdent("make"),
				Args: []ast.Expr{&ast.ArrayType{Elt: ast.NewIdent("byte")}, ah.IntLit(0), ah.IntLit(n)}},
		))
		if dataPos == 1 {
			stmts = append(stmts, makeDataDecl())
		}
		for fi := 0; fi < K; fi++ {
			stmts = append(stmts, &ast.AssignStmt{
				Lhs: []ast.Expr{ast.NewIdent(nKey)},
				Tok: token.ASSIGN,
				Rhs: []ast.Expr{&ast.CallExpr{
					Fun:      ast.NewIdent("append"),
					Args:     []ast.Expr{ast.NewIdent(nKey), ast.NewIdent(effectiveAlias[fi])},
					Ellipsis: token.Pos(1),
				}},
			})
		}

	case 2:

		stmts = append(stmts, ah.AssignDefineStmt(
			ast.NewIdent(nKey),
			&ast.CallExpr{Fun: ast.NewIdent("make"),
				Args: []ast.Expr{&ast.ArrayType{Elt: ast.NewIdent("byte")}, ah.IntLit(n)}},
		))
		if dataPos == 1 {
			stmts = append(stmts, makeDataDecl())
		}
		for _, fi := range copyOrder {
			stmts = append(stmts, &ast.ExprStmt{X: &ast.CallExpr{
				Fun: ast.NewIdent("copy"),
				Args: []ast.Expr{
					&ast.SliceExpr{X: ast.NewIdent(nKey), Low: ah.IntLit(fragOffsets[fi])},
					ast.NewIdent(effectiveAlias[fi]),
				},
			}})
		}

	case 3:

		if dataPos == 1 {
			stmts = append(stmts, makeDataDecl())
		}

		var chainExpr ast.Expr = &ast.CallExpr{
			Fun: ast.NewIdent("append"),
			Args: []ast.Expr{
				&ast.CallExpr{
					Fun:  &ast.ArrayType{Elt: ast.NewIdent("byte")},
					Args: []ast.Expr{ast.NewIdent("nil")},
				},
				ast.NewIdent(effectiveAlias[0]),
			},
			Ellipsis: token.Pos(1),
		}
		for fi := 1; fi < K; fi++ {
			chainExpr = &ast.CallExpr{
				Fun:      ast.NewIdent("append"),
				Args:     []ast.Expr{chainExpr, ast.NewIdent(effectiveAlias[fi])},
				Ellipsis: token.Pos(1),
			}
		}
		stmts = append(stmts, ah.AssignDefineStmt(ast.NewIdent(nKey), chainExpr))

	case 4:

		stmts = append(stmts, ah.AssignDefineStmt(
			ast.NewIdent(nKey),
			&ast.CallExpr{Fun: ast.NewIdent("make"),
				Args: []ast.Expr{&ast.ArrayType{Elt: ast.NewIdent("byte")}, ah.IntLit(n)}},
		))
		if dataPos == 1 {
			stmts = append(stmts, makeDataDecl())
		}
		for _, fi := range copyOrder {
			off := fragOffsets[fi]
			fragLen := len(fragments[fi])
			stmts = append(stmts, &ast.ExprStmt{X: &ast.CallExpr{
				Fun: ast.NewIdent("copy"),
				Args: []ast.Expr{
					&ast.SliceExpr{
						X:    ast.NewIdent(nKey),
						Low:  ah.IntLit(off),
						High: ah.IntLit(off + fragLen),
					},
					ast.NewIdent(effectiveAlias[fi]),
				},
			}})
		}

	case 5:

		stmts = append(stmts, &ast.DeclStmt{Decl: &ast.GenDecl{
			Tok: token.VAR,
			Specs: []ast.Spec{&ast.ValueSpec{
				Names: []*ast.Ident{ast.NewIdent(nKey)},
				Type:  &ast.ArrayType{Elt: ast.NewIdent("byte")},
			}},
		}})
		stmts = append(stmts, &ast.AssignStmt{
			Lhs: []ast.Expr{ast.NewIdent(nKey)},
			Tok: token.ASSIGN,
			Rhs: []ast.Expr{&ast.CallExpr{Fun: ast.NewIdent("make"),
				Args: []ast.Expr{&ast.ArrayType{Elt: ast.NewIdent("byte")}, ah.IntLit(n)}}},
		})
		if dataPos == 1 {
			stmts = append(stmts, makeDataDecl())
		}
		for _, fi := range copyOrder {
			low := wideOffsetExpr(fi, effectiveAlias)
			stmts = append(stmts, &ast.ExprStmt{X: &ast.CallExpr{
				Fun: ast.NewIdent("copy"),
				Args: []ast.Expr{
					&ast.SliceExpr{X: ast.NewIdent(nKey), Low: low},
					ast.NewIdent(effectiveAlias[fi]),
				},
			}})
		}
	}

	if dataPos == 2 {
		stmts = append(stmts, makeDataDecl())
	}

	stmts = append(stmts, &ast.DeclStmt{Decl: &ast.GenDecl{
		Tok: token.VAR,
		Specs: []ast.Spec{&ast.ValueSpec{
			Names:  []*ast.Ident{ast.NewIdent(nCheck)},
			Type:   ast.NewIdent("uint64"),
			Values: []ast.Expr{&ast.BasicLit{Kind: token.INT, Value: strconv.FormatUint(checkSeed, 10)}},
		}},
	}})

	makeDecoyCancel := func() []ast.Stmt {
		out := make([]ast.Stmt, 0, numDecoyPairs)
		for _, d := range decoys {
			out = append(out, &ast.AssignStmt{
				Lhs: []ast.Expr{ast.NewIdent(nCheck)},
				Tok: token.XOR_ASSIGN,
				Rhs: []ast.Expr{&ast.BinaryExpr{
					X: &ast.CallExpr{
						Fun:  ast.NewIdent("uint64"),
						Args: []ast.Expr{&ast.IndexExpr{X: ast.NewIdent(d.nameA), Index: ah.IntLit(d.idx)}},
					},
					Op: token.XOR,
					Y: &ast.CallExpr{
						Fun:  ast.NewIdent("uint64"),
						Args: []ast.Expr{&ast.IndexExpr{X: ast.NewIdent(d.nameB), Index: ah.IntLit(d.idx)}},
					},
				}},
			})
		}
		return out
	}

	var mainLoops []ast.Stmt

	inlineShift := func(idxExpr ast.Expr) ast.Expr {
		return &ast.BinaryExpr{
			X: &ast.BinaryExpr{
				X:  &ast.CallExpr{Fun: ast.NewIdent("uint"), Args: []ast.Expr{idxExpr}},
				Op: token.MUL, Y: ah.IntLit(8),
			},
			Op: token.AND, Y: ah.IntLit(63),
		}
	}

	moduloShift := func(idxExpr ast.Expr) ast.Expr {
		return &ast.BinaryExpr{
			X: &ast.BinaryExpr{
				X:  &ast.CallExpr{Fun: ast.NewIdent("uint"), Args: []ast.Expr{idxExpr}},
				Op: token.REM, Y: ah.IntLit(8),
			},
			Op: token.MUL, Y: ah.IntLit(8),
		}
	}

	checkAccum := func(valExpr, shiftExpr ast.Expr) ast.Stmt {
		return &ast.AssignStmt{
			Lhs: []ast.Expr{ast.NewIdent(nCheck)},
			Tok: token.XOR_ASSIGN,
			Rhs: []ast.Expr{&ast.BinaryExpr{
				X:  &ast.CallExpr{Fun: ast.NewIdent("uint64"), Args: []ast.Expr{valExpr}},
				Op: token.SHL, Y: shiftExpr,
			}},
		}
	}

	ordered := func(pvDecl, dataAsgn, accum ast.Stmt) []ast.Stmt {
		if dataFirst {
			return []ast.Stmt{pvDecl, dataAsgn, accum}
		}
		return []ast.Stmt{pvDecl, accum, dataAsgn}
	}

	switch loopStrategy {

	case 0:

		var sReset ast.Stmt
		if useAndReset {
			sReset = &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(nShift)}, Tok: token.AND_ASSIGN, Rhs: []ast.Expr{ah.IntLit(63)}}
		} else {
			sReset = &ast.IfStmt{
				Cond: &ast.BinaryExpr{X: ast.NewIdent(nShift), Op: token.GEQ, Y: ah.IntLit(64)},
				Body: &ast.BlockStmt{List: []ast.Stmt{
					&ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(nShift)}, Tok: token.ASSIGN, Rhs: []ast.Expr{ah.IntLit(0)}},
				}},
			}
		}
		stmts = append(stmts, &ast.DeclStmt{Decl: &ast.GenDecl{
			Tok:   token.VAR,
			Specs: []ast.Spec{&ast.ValueSpec{Names: []*ast.Ident{ast.NewIdent(nShift)}, Type: ast.NewIdent("uint")}},
		}})
		pvDecl := ah.AssignDefineStmt(ast.NewIdent(nPV), &ast.BinaryExpr{X: ah.IndexExpr(nEnc, ast.NewIdent(nI)), Op: token.XOR, Y: ast.NewIdent(nKV)})
		dataAsgn := &ast.AssignStmt{Lhs: []ast.Expr{ah.IndexExpr("data", ast.NewIdent(nI))}, Tok: token.ASSIGN, Rhs: []ast.Expr{ast.NewIdent(nPV)}}
		accum := checkAccum(ast.NewIdent(nPV), ast.NewIdent(nShift))
		body := ordered(pvDecl, dataAsgn, accum)
		body = append(body,
			&ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(nShift)}, Tok: token.ADD_ASSIGN, Rhs: []ast.Expr{ah.IntLit(8)}},
			sReset,
		)
		mainLoops = []ast.Stmt{&ast.RangeStmt{Key: ast.NewIdent(nI), Value: ast.NewIdent(nKV), Tok: token.DEFINE, X: ast.NewIdent(nKey), Body: &ast.BlockStmt{List: body}}}

	case 1:

		pvDecl := ah.AssignDefineStmt(ast.NewIdent(nPV), &ast.BinaryExpr{X: ah.IndexExpr(nEnc, ast.NewIdent(nI)), Op: token.XOR, Y: &ast.IndexExpr{X: ast.NewIdent(nKey), Index: ast.NewIdent(nI)}})
		dataAsgn := &ast.AssignStmt{Lhs: []ast.Expr{ah.IndexExpr("data", ast.NewIdent(nI))}, Tok: token.ASSIGN, Rhs: []ast.Expr{ast.NewIdent(nPV)}}
		accum := checkAccum(ast.NewIdent(nPV), inlineShift(ast.NewIdent(nI)))
		mainLoops = []ast.Stmt{&ast.ForStmt{
			Init: &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(nI)}, Tok: token.DEFINE, Rhs: []ast.Expr{ah.IntLit(0)}},
			Cond: &ast.BinaryExpr{X: ast.NewIdent(nI), Op: token.LSS, Y: ah.IntLit(n)},
			Post: &ast.IncDecStmt{X: ast.NewIdent(nI), Tok: token.INC},
			Body: &ast.BlockStmt{List: ordered(pvDecl, dataAsgn, accum)},
		}}

	case 2:

		pvDecl := ah.AssignDefineStmt(ast.NewIdent(nPV), &ast.BinaryExpr{X: ah.IndexExpr(nEnc, ast.NewIdent(nI)), Op: token.XOR, Y: &ast.IndexExpr{X: ast.NewIdent(nKey), Index: ast.NewIdent(nI)}})
		dataAsgn := &ast.AssignStmt{Lhs: []ast.Expr{ah.IndexExpr("data", ast.NewIdent(nI))}, Tok: token.ASSIGN, Rhs: []ast.Expr{ast.NewIdent(nPV)}}
		accum := checkAccum(ast.NewIdent(nPV), inlineShift(ast.NewIdent(nI)))
		mainLoops = []ast.Stmt{&ast.ForStmt{
			Init: &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(nI)}, Tok: token.DEFINE, Rhs: []ast.Expr{ah.IntLit(n - 1)}},
			Cond: &ast.BinaryExpr{X: ast.NewIdent(nI), Op: token.GEQ, Y: ah.IntLit(0)},
			Post: &ast.IncDecStmt{X: ast.NewIdent(nI), Tok: token.DEC},
			Body: &ast.BlockStmt{List: ordered(pvDecl, dataAsgn, accum)},
		}}

	case 3:

		pass1 := &ast.RangeStmt{Key: ast.NewIdent(nI), Value: ast.NewIdent(nKV), Tok: token.DEFINE, X: ast.NewIdent(nKey),
			Body: &ast.BlockStmt{List: []ast.Stmt{
				&ast.AssignStmt{Lhs: []ast.Expr{ah.IndexExpr("data", ast.NewIdent(nI))}, Tok: token.ASSIGN,
					Rhs: []ast.Expr{&ast.BinaryExpr{X: ah.IndexExpr(nEnc, ast.NewIdent(nI)), Op: token.XOR, Y: ast.NewIdent(nKV)}},
				},
			}},
		}
		pass2 := &ast.RangeStmt{Key: ast.NewIdent(nI), Value: ast.NewIdent(nPV), Tok: token.DEFINE, X: ast.NewIdent("data"),
			Body: &ast.BlockStmt{List: []ast.Stmt{checkAccum(ast.NewIdent(nPV), inlineShift(ast.NewIdent(nI)))}},
		}
		mainLoops = []ast.Stmt{pass1, pass2}

	case 4:

		stmts = append(stmts, ah.AssignDefineStmt(ast.NewIdent(nRem), ast.NewIdent(nKey)))
		pvDecl := ah.AssignDefineStmt(ast.NewIdent(nPV), &ast.BinaryExpr{
			X: ah.IndexExpr(nEnc, ast.NewIdent(nI)), Op: token.XOR,
			Y: &ast.IndexExpr{X: ast.NewIdent(nRem), Index: ah.IntLit(0)},
		})
		remAdvance := &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(nRem)}, Tok: token.ASSIGN,
			Rhs: []ast.Expr{&ast.SliceExpr{X: ast.NewIdent(nRem), Low: ah.IntLit(1)}},
		}
		dataAsgn := &ast.AssignStmt{Lhs: []ast.Expr{ah.IndexExpr("data", ast.NewIdent(nI))}, Tok: token.ASSIGN, Rhs: []ast.Expr{ast.NewIdent(nPV)}}
		accum := checkAccum(ast.NewIdent(nPV), inlineShift(ast.NewIdent(nI)))
		var body []ast.Stmt
		if dataFirst {
			body = []ast.Stmt{pvDecl, remAdvance, dataAsgn, accum}
		} else {
			body = []ast.Stmt{pvDecl, remAdvance, accum, dataAsgn}
		}
		mainLoops = []ast.Stmt{&ast.ForStmt{
			Init: &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(nI)}, Tok: token.DEFINE, Rhs: []ast.Expr{ah.IntLit(0)}},
			Cond: &ast.BinaryExpr{
				X:  &ast.CallExpr{Fun: ast.NewIdent("len"), Args: []ast.Expr{ast.NewIdent(nRem)}},
				Op: token.GTR, Y: ah.IntLit(0),
			},
			Post: &ast.IncDecStmt{X: ast.NewIdent(nI), Tok: token.INC},
			Body: &ast.BlockStmt{List: body},
		}}

	case 5:

		pvDecl := ah.AssignDefineStmt(ast.NewIdent(nPV), &ast.BinaryExpr{
			X: ast.NewIdent(nEV), Op: token.XOR,
			Y: &ast.IndexExpr{X: ast.NewIdent(nKey), Index: ast.NewIdent(nI)},
		})
		dataAsgn := &ast.AssignStmt{Lhs: []ast.Expr{ah.IndexExpr("data", ast.NewIdent(nI))}, Tok: token.ASSIGN, Rhs: []ast.Expr{ast.NewIdent(nPV)}}
		accum := checkAccum(ast.NewIdent(nPV), inlineShift(ast.NewIdent(nI)))
		mainLoops = []ast.Stmt{&ast.RangeStmt{Key: ast.NewIdent(nI), Value: ast.NewIdent(nEV), Tok: token.DEFINE, X: ast.NewIdent(nEnc),
			Body: &ast.BlockStmt{List: ordered(pvDecl, dataAsgn, accum)},
		}}

	case 6:

		pvDecl := ah.AssignDefineStmt(ast.NewIdent(nPV), &ast.BinaryExpr{X: ah.IndexExpr(nEnc, ast.NewIdent(nI)), Op: token.XOR, Y: &ast.IndexExpr{X: ast.NewIdent(nKey), Index: ast.NewIdent(nI)}})
		dataAsgn := &ast.AssignStmt{Lhs: []ast.Expr{ah.IndexExpr("data", ast.NewIdent(nI))}, Tok: token.ASSIGN, Rhs: []ast.Expr{ast.NewIdent(nPV)}}
		accum := checkAccum(ast.NewIdent(nPV), inlineShift(ast.NewIdent(nI)))
		mainLoops = []ast.Stmt{&ast.ForStmt{
			Init: &ast.AssignStmt{
				Lhs: []ast.Expr{ast.NewIdent(nI), ast.NewIdent(nQ)},
				Tok: token.DEFINE,
				Rhs: []ast.Expr{ah.IntLit(0), ah.IntLit(n - 1)},
			},
			Cond: &ast.BinaryExpr{X: ast.NewIdent(nI), Op: token.LSS, Y: ah.IntLit(n)},
			Post: &ast.AssignStmt{
				Lhs: []ast.Expr{ast.NewIdent(nI), ast.NewIdent(nQ)},
				Tok: token.ASSIGN,
				Rhs: []ast.Expr{
					&ast.BinaryExpr{X: ast.NewIdent(nI), Op: token.ADD, Y: ah.IntLit(1)},
					&ast.BinaryExpr{X: ast.NewIdent(nQ), Op: token.SUB, Y: ah.IntLit(1)},
				},
			},
			Body: &ast.BlockStmt{List: ordered(pvDecl, dataAsgn, accum)},
		}}

	case 7:

		shiftVals := []ast.Expr{ah.IntLit(0), ah.IntLit(8), ah.IntLit(16), ah.IntLit(24), ah.IntLit(32), ah.IntLit(40), ah.IntLit(48), ah.IntLit(56)}
		stmts = append(stmts, &ast.DeclStmt{Decl: &ast.GenDecl{
			Tok: token.VAR,
			Specs: []ast.Spec{&ast.ValueSpec{
				Names: []*ast.Ident{ast.NewIdent(nST)},
				Type:  &ast.ArrayType{Len: ah.IntLit(8), Elt: ast.NewIdent("uint")},
				Values: []ast.Expr{&ast.CompositeLit{
					Type: &ast.ArrayType{Len: ah.IntLit(8), Elt: ast.NewIdent("uint")},
					Elts: shiftVals,
				}},
			}},
		}})
		pvDecl := ah.AssignDefineStmt(ast.NewIdent(nPV), &ast.BinaryExpr{X: ah.IndexExpr(nEnc, ast.NewIdent(nI)), Op: token.XOR, Y: ast.NewIdent(nKV)})
		dataAsgn := &ast.AssignStmt{Lhs: []ast.Expr{ah.IndexExpr("data", ast.NewIdent(nI))}, Tok: token.ASSIGN, Rhs: []ast.Expr{ast.NewIdent(nPV)}}
		tblShift := &ast.IndexExpr{
			X:     ast.NewIdent(nST),
			Index: &ast.BinaryExpr{X: ast.NewIdent(nI), Op: token.REM, Y: ah.IntLit(8)},
		}
		accum := checkAccum(ast.NewIdent(nPV), tblShift)
		mainLoops = []ast.Stmt{&ast.RangeStmt{Key: ast.NewIdent(nI), Value: ast.NewIdent(nKV), Tok: token.DEFINE, X: ast.NewIdent(nKey),
			Body: &ast.BlockStmt{List: ordered(pvDecl, dataAsgn, accum)},
		}}

	case 8:

		pvDecl := ah.AssignDefineStmt(ast.NewIdent(nPV), &ast.BinaryExpr{X: ah.IndexExpr(nEnc, ast.NewIdent(nI)), Op: token.XOR, Y: &ast.IndexExpr{X: ast.NewIdent(nKey), Index: ast.NewIdent(nI)}})
		dataAsgn := &ast.AssignStmt{Lhs: []ast.Expr{ah.IndexExpr("data", ast.NewIdent(nI))}, Tok: token.ASSIGN, Rhs: []ast.Expr{ast.NewIdent(nPV)}}
		accum := checkAccum(ast.NewIdent(nPV), moduloShift(ast.NewIdent(nI)))
		mainLoops = []ast.Stmt{&ast.ForStmt{
			Init: &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(nI)}, Tok: token.DEFINE, Rhs: []ast.Expr{ah.IntLit(0)}},
			Cond: &ast.BinaryExpr{X: ast.NewIdent(nI), Op: token.LSS, Y: ah.IntLit(n)},
			Post: &ast.IncDecStmt{X: ast.NewIdent(nI), Tok: token.INC},
			Body: &ast.BlockStmt{List: ordered(pvDecl, dataAsgn, accum)},
		}}

	case 9:

		pass1 := &ast.RangeStmt{Key: ast.NewIdent(nI), Value: ast.NewIdent(nEV), Tok: token.DEFINE, X: ast.NewIdent(nEnc),
			Body: &ast.BlockStmt{List: []ast.Stmt{
				&ast.AssignStmt{Lhs: []ast.Expr{ah.IndexExpr("data", ast.NewIdent(nI))}, Tok: token.ASSIGN,
					Rhs: []ast.Expr{&ast.BinaryExpr{X: ast.NewIdent(nEV), Op: token.XOR, Y: &ast.IndexExpr{X: ast.NewIdent(nKey), Index: ast.NewIdent(nI)}}},
				},
			}},
		}
		pass2 := &ast.ForStmt{
			Init: &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(nI)}, Tok: token.DEFINE, Rhs: []ast.Expr{ah.IntLit(0)}},
			Cond: &ast.BinaryExpr{X: ast.NewIdent(nI), Op: token.LSS, Y: ah.IntLit(n)},
			Post: &ast.IncDecStmt{X: ast.NewIdent(nI), Tok: token.INC},
			Body: &ast.BlockStmt{List: []ast.Stmt{
				checkAccum(&ast.IndexExpr{X: ast.NewIdent("data"), Index: ast.NewIdent(nI)}, inlineShift(ast.NewIdent(nI))),
			}},
		}
		mainLoops = []ast.Stmt{pass1, pass2}

	case 10:
		mutPass := &ast.RangeStmt{
			Key:   ast.NewIdent(nI),
			Value: ast.NewIdent(nKV),
			Tok:   token.DEFINE,
			X:     ast.NewIdent(nKey),
			Body: &ast.BlockStmt{List: []ast.Stmt{
				&ast.AssignStmt{
					Lhs: []ast.Expr{&ast.IndexExpr{X: ast.NewIdent(nEnc), Index: ast.NewIdent(nI)}},
					Tok: token.XOR_ASSIGN,
					Rhs: []ast.Expr{ast.NewIdent(nKV)},
				},
			}},
		}
		readPass := &ast.RangeStmt{
			Key:   ast.NewIdent(nI),
			Value: ast.NewIdent(nPV),
			Tok:   token.DEFINE,
			X:     ast.NewIdent(nEnc),
			Body: &ast.BlockStmt{List: []ast.Stmt{
				&ast.AssignStmt{
					Lhs: []ast.Expr{&ast.IndexExpr{X: ast.NewIdent("data"), Index: ast.NewIdent(nI)}},
					Tok: token.ASSIGN,
					Rhs: []ast.Expr{ast.NewIdent(nPV)},
				},
				checkAccum(ast.NewIdent(nPV), inlineShift(ast.NewIdent(nI))),
			}},
		}
		mainLoops = []ast.Stmt{mutPass, readPass}

	case 11:
		stmts = append(stmts, &ast.AssignStmt{
			Lhs: []ast.Expr{ast.NewIdent(nI)},
			Tok: token.DEFINE,
			Rhs: []ast.Expr{ah.IntLit(0)},
		})
		pvDecl := ah.AssignDefineStmt(ast.NewIdent(nPV), &ast.BinaryExpr{
			X:  &ast.IndexExpr{X: ast.NewIdent(nEnc), Index: ast.NewIdent(nI)},
			Op: token.XOR,
			Y:  &ast.IndexExpr{X: ast.NewIdent(nKey), Index: ast.NewIdent(nI)},
		})
		dataAsgn := &ast.AssignStmt{
			Lhs: []ast.Expr{&ast.IndexExpr{X: ast.NewIdent("data"), Index: ast.NewIdent(nI)}},
			Tok: token.ASSIGN,
			Rhs: []ast.Expr{ast.NewIdent(nPV)},
		}
		mainLoops = []ast.Stmt{&ast.ForStmt{
			Cond: &ast.BinaryExpr{X: ast.NewIdent(nI), Op: token.LSS, Y: ah.IntLit(n)},
			Body: &ast.BlockStmt{List: []ast.Stmt{
				pvDecl,
				dataAsgn,
				checkAccum(ast.NewIdent(nPV), inlineShift(ast.NewIdent(nI))),
				&ast.IncDecStmt{X: ast.NewIdent(nI), Tok: token.INC},
			}},
		}}

	case 12:
		pvDecl := ah.AssignDefineStmt(ast.NewIdent(nPV), &ast.BinaryExpr{
			X:  &ast.IndexExpr{X: ast.NewIdent(nEnc), Index: ast.NewIdent(nI)},
			Op: token.XOR,
			Y:  &ast.IndexExpr{X: ast.NewIdent(nKey), Index: ast.NewIdent(nI)},
		})
		dataAsgn := &ast.AssignStmt{
			Lhs: []ast.Expr{&ast.IndexExpr{X: ast.NewIdent("data"), Index: ast.NewIdent(nI)}},
			Tok: token.ASSIGN,
			Rhs: []ast.Expr{ast.NewIdent(nPV)},
		}
		mainLoops = []ast.Stmt{&ast.ForStmt{
			Init: &ast.AssignStmt{
				Lhs: []ast.Expr{ast.NewIdent(nI), ast.NewIdent(nQ)},
				Tok: token.DEFINE,
				Rhs: []ast.Expr{ah.IntLit(0), ah.IntLit(n)},
			},
			Cond: &ast.BinaryExpr{X: ast.NewIdent(nI), Op: token.LSS, Y: ah.IntLit(n)},
			Post: &ast.AssignStmt{
				Lhs: []ast.Expr{ast.NewIdent(nI), ast.NewIdent(nQ)},
				Tok: token.ASSIGN,
				Rhs: []ast.Expr{
					&ast.BinaryExpr{X: ast.NewIdent(nI), Op: token.ADD, Y: ah.IntLit(1)},
					&ast.BinaryExpr{X: ast.NewIdent(nQ), Op: token.SUB, Y: ah.IntLit(1)},
				},
			},
			Body: &ast.BlockStmt{List: ordered(pvDecl, dataAsgn,
				checkAccum(ast.NewIdent(nPV), inlineShift(ast.NewIdent(nI))))},
		}}
	}

	if decoyBefore {
		stmts = append(stmts, makeDecoyCancel()...)
	}
	stmts = append(stmts, mainLoops...)
	if !decoyBefore {
		stmts = append(stmts, makeDecoyCancel()...)
	}

	xorAssign := func(idxExpr ast.Expr) ast.Stmt {
		return &ast.AssignStmt{
			Lhs: []ast.Expr{&ast.IndexExpr{X: ast.NewIdent("data"), Index: idxExpr}},
			Tok: token.XOR_ASSIGN,
			Rhs: []ast.Expr{ah.IntLit(int(tamperByte))},
		}
	}
	var corruptLoop ast.Stmt
	switch tamperStrategy {
	case 0:

		corruptLoop = &ast.RangeStmt{Key: ast.NewIdent(nJ), Tok: token.DEFINE, X: ast.NewIdent("data"),
			Body: &ast.BlockStmt{List: []ast.Stmt{xorAssign(ast.NewIdent(nJ))}},
		}
	case 1:

		corruptLoop = &ast.ForStmt{
			Init: &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(nJ)}, Tok: token.DEFINE, Rhs: []ast.Expr{ah.IntLit(0)}},
			Cond: &ast.BinaryExpr{X: ast.NewIdent(nJ), Op: token.LSS, Y: &ast.CallExpr{Fun: ast.NewIdent("len"), Args: []ast.Expr{ast.NewIdent("data")}}},
			Post: &ast.IncDecStmt{X: ast.NewIdent(nJ), Tok: token.INC},
			Body: &ast.BlockStmt{List: []ast.Stmt{xorAssign(ast.NewIdent(nJ))}},
		}
	case 2:

		corruptLoop = &ast.ForStmt{
			Init: &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(nJ)}, Tok: token.DEFINE,
				Rhs: []ast.Expr{&ast.BinaryExpr{X: &ast.CallExpr{Fun: ast.NewIdent("len"), Args: []ast.Expr{ast.NewIdent("data")}}, Op: token.SUB, Y: ah.IntLit(1)}},
			},
			Cond: &ast.BinaryExpr{X: ast.NewIdent(nJ), Op: token.GEQ, Y: ah.IntLit(0)},
			Post: &ast.IncDecStmt{X: ast.NewIdent(nJ), Tok: token.DEC},
			Body: &ast.BlockStmt{List: []ast.Stmt{xorAssign(ast.NewIdent(nJ))}},
		}
	case 3:

		corruptLoop = &ast.ForStmt{
			Init: &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(nJ)}, Tok: token.DEFINE, Rhs: []ast.Expr{ah.IntLit(0)}},
			Cond: &ast.BinaryExpr{X: ast.NewIdent(nJ), Op: token.LSS, Y: ah.IntLit(n)},
			Post: &ast.IncDecStmt{X: ast.NewIdent(nJ), Tok: token.INC},
			Body: &ast.BlockStmt{List: []ast.Stmt{xorAssign(ast.NewIdent(nJ))}},
		}
	case 4:

		corruptLoop = &ast.ForStmt{
			Init: &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(nJ)}, Tok: token.DEFINE, Rhs: []ast.Expr{ah.IntLit(n - 1)}},
			Cond: &ast.BinaryExpr{X: ast.NewIdent(nJ), Op: token.GEQ, Y: ah.IntLit(0)},
			Post: &ast.IncDecStmt{X: ast.NewIdent(nJ), Tok: token.DEC},
			Body: &ast.BlockStmt{List: []ast.Stmt{xorAssign(ast.NewIdent(nJ))}},
		}
	}
	stmts = append(stmts, &ast.IfStmt{
		Cond: &ast.BinaryExpr{
			X:  ast.NewIdent(nCheck),
			Op: token.NEQ,
			Y:  &ast.BasicLit{Kind: token.INT, Value: strconv.FormatUint(compConst, 10)},
		},
		Body: &ast.BlockStmt{List: []ast.Stmt{corruptLoop}},
	})

	return ah.BlockStmt(stmts...)
}

func wideFragmentCount(rnd *mathrand.Rand, n int) int {
	switch {
	case n < 24:
		return 2
	case n < 64:
		return 2 + rnd.Intn(2)
	default:
		return 3 + rnd.Intn(4)
	}
}

func wideFragmentKey(rnd *mathrand.Rand, key []byte, K int) (fragments [][]byte, offsets []int) {
	n := len(key)
	if K > n {
		K = n
	}

	pts := make([]int, n-1)
	for i := range pts {
		pts[i] = i + 1
	}
	for i := 0; i < K-1; i++ {
		j := i + rnd.Intn(len(pts)-i)
		pts[i], pts[j] = pts[j], pts[i]
	}
	splits := pts[:K-1]

	for i := 1; i < len(splits); i++ {
		for j := i; j > 0 && splits[j] < splits[j-1]; j-- {
			splits[j], splits[j-1] = splits[j-1], splits[j]
		}
	}

	boundaries := make([]int, K+1)
	boundaries[0] = 0
	for i, s := range splits {
		boundaries[i+1] = s
	}
	boundaries[K] = n

	fragments = make([][]byte, K)
	offsets = make([]int, K)
	for i := 0; i < K; i++ {
		offsets[i] = boundaries[i]
		size := boundaries[i+1] - boundaries[i]
		fragments[i] = make([]byte, size)
		copy(fragments[i], key[boundaries[i]:boundaries[i+1]])
	}
	return
}

func wideOffsetExpr(fi int, aliasName []string) ast.Expr {
	if fi == 0 {
		return ah.IntLit(0)
	}

	expr := ast.Expr(&ast.CallExpr{
		Fun:  ast.NewIdent("len"),
		Args: []ast.Expr{ast.NewIdent(aliasName[0])},
	})

	for j := 1; j < fi; j++ {
		expr = &ast.BinaryExpr{
			X:  expr,
			Op: token.ADD,
			Y: &ast.CallExpr{
				Fun:  ast.NewIdent("len"),
				Args: []ast.Expr{ast.NewIdent(aliasName[j])},
			},
		}
	}
	return expr
}

func wideChecksum(data []byte) uint64 {
	var check uint64
	var s uint
	for _, b := range data {
		check ^= uint64(b) << s
		s += 8
		if s >= 64 {
			s = 0
		}
	}
	return check
}
