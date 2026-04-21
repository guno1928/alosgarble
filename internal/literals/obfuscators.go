package literals

import (
	"fmt"
	"go/ast"
	"go/token"
	"math"
	mathrand "math/rand"
	"slices"
	"strconv"

	ah "github.com/guno1928/alosgarble/internal/asthelper"
)

type externalKeyProbability float32

const (
	lowProb    externalKeyProbability = 0.4
	normalProb externalKeyProbability = 0.6
	highProb   externalKeyProbability = 0.8
)

func (r externalKeyProbability) Try(rand *mathrand.Rand) bool {
	return rand.Float32() < float32(r)
}

type externalKey struct {
	name, typ string
	value     uint64
	bits      int
	refs      int
}

func (k *externalKey) Type() *ast.Ident {
	return ast.NewIdent(k.typ)
}

func (k *externalKey) Name() *ast.Ident {
	return ast.NewIdent(k.name)
}

func (k *externalKey) AddRef() {
	k.refs++
}

func (k *externalKey) IsUsed() bool {
	return k.refs > 0
}

type obfuscator interface {
	obfuscate(obfRand *mathrand.Rand, data []byte, extKeys []*externalKey) *ast.BlockStmt
}

var (
	Obfuscators = []obfuscator{
		wide{},
	}

	CheapObfuscators = []obfuscator{
		wide{},
	}
)

func genRandIntSlice(obfRand *mathrand.Rand, max, count int) []int {
	indexes := make([]int, count)
	for i := range count {
		indexes[i] = obfRand.Intn(max)
	}
	return indexes
}

func randOperator(obfRand *mathrand.Rand) token.Token {
	operatorTokens := [...]token.Token{token.XOR, token.ADD, token.SUB}
	return operatorTokens[obfRand.Intn(len(operatorTokens))]
}

func evalOperator(t token.Token, x, y byte) byte {
	switch t {
	case token.XOR:
		return x ^ y
	case token.ADD:
		return x + y
	case token.SUB:
		return x - y
	default:
		panic(fmt.Sprintf("unknown operator: %s", t))
	}
}

func operatorToReversedBinaryExpr(t token.Token, x, y ast.Expr) *ast.BinaryExpr {
	var op token.Token
	switch t {
	case token.XOR:
		op = token.XOR
	case token.ADD:
		op = token.SUB
	case token.SUB:
		op = token.ADD
	default:
		panic(fmt.Sprintf("unknown operator: %s", t))
	}
	return ah.BinaryExpr(x, op, y)
}

const (
	minExtKeyCount = 2

	maxExtKeyCount = 6

	minByteSliceExtKeyOps = 2

	maxByteSliceExtKeyOps = 12
)

var extKeyRanges = []struct {
	typ  string
	max  uint64
	bits int
}{
	{"uint8", math.MaxUint8, 8},
	{"uint16", math.MaxUint16, 16},
	{"uint32", math.MaxUint32, 32},
	{"uint64", math.MaxUint64, 64},
}

func randExtKey(rand *mathrand.Rand, idx int) *externalKey {
	r := extKeyRanges[rand.Intn(len(extKeyRanges))]
	return &externalKey{
		name:  "garbleExternalKey" + strconv.Itoa(idx),
		typ:   r.typ,
		value: rand.Uint64() & r.max,
		bits:  r.bits,
	}
}

func randExtKeys(rand *mathrand.Rand) []*externalKey {
	count := minExtKeyCount + rand.Intn(maxExtKeyCount-minExtKeyCount)
	keys := make([]*externalKey, count)
	for i := range count {
		keys[i] = randExtKey(rand, i)
	}
	return keys
}

func extKeysToParams(objRand *obfRand, keys []*externalKey) (params *ast.FieldList, args []ast.Expr) {
	params = &ast.FieldList{}
	for _, key := range keys {
		name := key.Name()
		if !key.IsUsed() {
			name.Name = "_"
		}
		params.List = append(params.List, ah.Field(key.Type(), name))

		var extKeyExpr ast.Expr = ah.UintLit(key.value)
		if lowProb.Try(objRand.rnd) {
			extKeyExpr = objRand.proxyDispatcher.HideValue(extKeyExpr, ast.NewIdent(key.typ))
		}
		args = append(args, extKeyExpr)
	}
	return
}

func (key *externalKey) ToExpr(b int) ast.Expr {
	var x ast.Expr = key.Name()
	if b > 0 {
		x = ah.BinaryExpr(x, token.SHR, ah.IntLit(b*8))
	}
	if key.typ != "uint8" {
		x = ah.CallExprByName("byte", x)
	}
	return x
}

func dataToByteSliceWithExtKeys(rand *mathrand.Rand, data []byte, extKeys []*externalKey) ast.Expr {
	extKeyOpCount := minByteSliceExtKeyOps + rand.Intn(maxByteSliceExtKeyOps-minByteSliceExtKeyOps)

	var stmts []ast.Stmt
	for range extKeyOpCount {
		key := extKeys[rand.Intn(len(extKeys))]
		key.AddRef()

		idx, op, b := rand.Intn(len(data)), randOperator(rand), rand.Intn(key.bits/8)
		data[idx] = evalOperator(op, data[idx], byte(key.value>>(b*8)))
		stmts = append(stmts, ah.AssignStmt(
			ah.IndexExpr("data", ah.IntLit(idx)),
			operatorToReversedBinaryExpr(op,
				ah.IndexExpr("data", ah.IntLit(idx)),
				key.ToExpr(b),
			),
		))
	}

	slices.Reverse(stmts)

	stmts = append([]ast.Stmt{ah.AssignDefineStmt(ast.NewIdent("data"), ah.DataToByteSlice(data))}, append(stmts, ah.ReturnStmt(ast.NewIdent("data")))...)
	return ah.LambdaCall(nil, ah.ByteSliceType(), ah.BlockStmt(stmts...), nil)
}

func byteLitWithExtKey(rand *mathrand.Rand, val byte, extKeys []*externalKey, extKeyProb externalKeyProbability) ast.Expr {
	if !extKeyProb.Try(rand) {
		return ah.IntLit(int(val))
	}

	key := extKeys[rand.Intn(len(extKeys))]
	key.AddRef()

	op, b := randOperator(rand), rand.Intn(key.bits/8)
	newVal := evalOperator(op, val, byte(key.value>>(b*8)))

	return operatorToReversedBinaryExpr(op,
		ah.CallExprByName("byte", ah.IntLit(int(newVal))),
		key.ToExpr(b),
	)
}

type obfRand struct {
	rnd *mathrand.Rand

	funcDepth       int
	initDepth       int
	proxyDispatcher *proxyDispatcher
}

func newObfRand(rand *mathrand.Rand, file *ast.File, nameFunc NameProviderFunc) *obfRand {
	return &obfRand{rnd: rand, proxyDispatcher: newProxyDispatcher(rand, nameFunc)}
}
