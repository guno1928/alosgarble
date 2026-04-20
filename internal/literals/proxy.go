package literals

import (
	"go/ast"
	"go/token"
	mathrand "math/rand"
	"strconv"

	ah "github.com/guno1928/alosgarble/internal/asthelper"
)

const (
	minStructCount    = 4
	maxStructCount    = 8
	minChildCount     = 1
	maxChildCount     = 3
	minJunkValueCount = 1
	maxJunkValueCount = 3
	minJunkArraySize  = 1
	maxJunkArraySize  = 8
)

type proxyValue struct {
	name     string
	typ, val ast.Expr
}

type proxyStruct struct {
	typeName, name string
	isPointer      bool

	values []*proxyValue

	children []*proxyStruct
	parent   *proxyStruct
}

type proxyDispatcher struct {
	rand     *mathrand.Rand
	nameFunc NameProviderFunc

	root           *proxyStruct
	flattenStructs []*proxyStruct
}

func (d *proxyDispatcher) initialize() {
	flattenStructs := make([]*proxyStruct, d.rand.Intn(maxStructCount-minStructCount)+minStructCount)
	for i := range flattenStructs {
		flattenStructs[i] = &proxyStruct{
			typeName:  d.nameFunc(d.rand, "proxyStructName"+strconv.Itoa(i)),
			name:      d.nameFunc(d.rand, "proxyStructFieldName"+strconv.Itoa(i)),
			isPointer: d.rand.Intn(2) == 0,
		}
	}

	root := &proxyStruct{
		name:     d.nameFunc(d.rand, "rootStructName"),
		typeName: d.nameFunc(d.rand, "rootStructType"),
		parent:   nil,
	}
	d.root = root

	unassigned := append([]*proxyStruct(nil), flattenStructs...)
	queue := []*proxyStruct{root}
	for len(unassigned) > 0 && len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		childCount := min(d.rand.Intn(maxChildCount-minChildCount)+minChildCount, len(unassigned))

		for i := 0; i < childCount; i++ {
			child := unassigned[0]
			unassigned = unassigned[1:]

			child.parent = current
			current.children = append(current.children, child)
			queue = append(queue, child)
		}
	}

	d.flattenStructs = append(flattenStructs, root)
}

func buildPath(strct *proxyStruct, valueName string) ast.Expr {
	var stack []*proxyStruct
	for s := strct; s != nil; s = s.parent {
		stack = append(stack, s)
	}

	var expr ast.Expr = ast.NewIdent(stack[len(stack)-1].name)
	for i := len(stack) - 2; i >= 0; i-- {
		expr = &ast.SelectorExpr{
			X:   expr,
			Sel: ast.NewIdent(stack[i].name),
		}
	}

	return &ast.SelectorExpr{
		X:   expr,
		Sel: ast.NewIdent(valueName),
	}
}

func (d *proxyDispatcher) HideValue(val, typ ast.Expr) ast.Expr {
	if d.root == nil {
		d.initialize()
	}

	strct := d.flattenStructs[d.rand.Intn(len(d.flattenStructs))]
	valueName := d.nameFunc(d.rand, strct.name+"_"+strct.typeName+"_"+strconv.Itoa(len(strct.values)))
	strct.values = append(strct.values, &proxyValue{
		name: valueName,
		typ:  typ,
		val:  val,
	})
	return buildPath(strct, valueName)
}

func (d *proxyDispatcher) generateStructLiteral(s *proxyStruct) ast.Expr {
	var fields []ast.Expr
	for _, child := range s.children {
		expr := d.generateStructLiteral(child)
		if child.isPointer {
			expr = &ast.UnaryExpr{
				Op: token.AND,
				X:  expr,
			}
		}
		fields = append(fields, &ast.KeyValueExpr{
			Key:   ast.NewIdent(child.name),
			Value: expr,
		})
	}

	for _, val := range s.values {
		fields = append(fields, &ast.KeyValueExpr{
			Key:   ast.NewIdent(val.name),
			Value: val.val,
		})
	}

	d.rand.Shuffle(len(fields), func(i, j int) {
		fields[i], fields[j] = fields[j], fields[i]
	})

	return &ast.CompositeLit{
		Type: ast.NewIdent(s.typeName),
		Elts: fields,
	}
}

func (d *proxyDispatcher) junkValue() *proxyValue {
	size := d.rand.Intn(maxJunkArraySize-minJunkArraySize+1) + minJunkArraySize
	data := make([]byte, size)
	d.rand.Read(data)

	dummyDataExpr := &ast.CompositeLit{
		Type: &ast.ArrayType{
			Len: ah.IntLit(size),
			Elt: ast.NewIdent("byte"),
		},
		Elts: ah.DataToArray(data).Elts,
	}
	return &proxyValue{
		name: d.nameFunc(d.rand, "junkValue"),
		typ: &ast.ArrayType{
			Len: ah.IntLit(size),
			Elt: ast.NewIdent("byte"),
		},
		val: dummyDataExpr,
	}
}

func (d *proxyDispatcher) AddToFile(file *ast.File) {
	if d.root == nil {
		return
	}

	for _, strct := range d.flattenStructs {
		dummyCount := d.rand.Intn(maxJunkValueCount-minJunkValueCount+1) + minJunkValueCount
		for range dummyCount {
			strct.values = append(strct.values, d.junkValue())
		}

		structType := &ast.StructType{Fields: &ast.FieldList{}}
		for _, child := range strct.children {
			var typ ast.Expr = ast.NewIdent(child.typeName)
			if child.isPointer {
				typ = &ast.StarExpr{X: typ}
			}
			structType.Fields.List = append(structType.Fields.List, &ast.Field{
				Names: []*ast.Ident{ast.NewIdent(child.name)},
				Type:  typ,
			})
		}

		for _, value := range strct.values {
			structType.Fields.List = append(structType.Fields.List, &ast.Field{
				Names: []*ast.Ident{ast.NewIdent(value.name)},
				Type:  value.typ,
			})
		}

		d.rand.Shuffle(len(structType.Fields.List), func(i, j int) {
			structType.Fields.List[i], structType.Fields.List[j] = structType.Fields.List[j], structType.Fields.List[i]
		})

		file.Decls = append(file.Decls, &ast.GenDecl{
			Tok: token.TYPE,
			Specs: []ast.Spec{&ast.TypeSpec{
				Name: ast.NewIdent(strct.typeName),
				Type: structType,
			}},
		})
	}

	file.Decls = append(file.Decls, &ast.GenDecl{
		Tok: token.VAR,
		Specs: []ast.Spec{&ast.ValueSpec{
			Names:  []*ast.Ident{ast.NewIdent(d.root.name)},
			Values: []ast.Expr{d.generateStructLiteral(d.root)},
		}},
	})
}

func newProxyDispatcher(rand *mathrand.Rand, nameFunc NameProviderFunc) *proxyDispatcher {
	return &proxyDispatcher{
		rand:     rand,
		nameFunc: nameFunc,
	}
}
