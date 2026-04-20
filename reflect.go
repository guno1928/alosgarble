package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"go/types"
	"log"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ssa"
)

//go:embed reflect_abi_code.go
var reflectAbiCode string
var reflectPatchFile = ""

func abiNamePatch(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	find := `return unsafe.String(n.DataChecked(1+i, "non-empty string"), l)`
	replace := `return _originalNames(unsafe.String(n.DataChecked(1+i, "non-empty string"), l))`

	str := strings.Replace(string(data), find, replace, 1)

	originalNames := `
//go:linkname _originalNames
func _originalNames(name string) string

//go:linkname _originalNamesInit
func _originalNamesInit()

func init() { _originalNamesInit() }
`

	return str + originalNames, nil
}







func reflectMainPrePatch(path string) (string, error) {
	if reflectPatchFile != "" {
		
		return "", nil
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	_, code, _ := strings.Cut(reflectAbiCode, "// Injected code below this line.")
	code = strings.ReplaceAll(code, "//disabledgo:", "//go:")
	
	code = strings.ReplaceAll(code, "minHashLength", strconv.Itoa(minHashLength))
	return string(content) + code, nil
}



func reflectMainPostPatch(file []byte, lpkg *listedPackage, pkg pkgCache) []byte {
	obfVarName := hashWithPackage(lpkg, "_originalNamePairs")
	namePairs := fmt.Appendf(nil, "%s = []string{", obfVarName)

	keys := slices.Sorted(maps.Keys(pkg.ReflectObjectNames))
	namePairsFilled := bytes.Clone(namePairs)
	for _, obf := range keys {
		namePairsFilled = fmt.Appendf(namePairsFilled, "%q, %q,", obf, pkg.ReflectObjectNames[obf])
	}

	return bytes.Replace(file, namePairs, namePairsFilled, 1)
}

type reflectInspector struct {
	lpkg *listedPackage
	pkg  *types.Package

	checkedAPIs map[string]bool

	propagatedInstr map[ssa.Instruction]bool

	result pkgCache
}


func (ri *reflectInspector) recordReflection(ssaPkg *ssa.Package) {
	if reflectSkipPkg[ssaPkg.Pkg.Path()] {
		return
	}

	prevDone := len(ri.result.ReflectAPIs) + len(ri.result.ReflectObjectNames)

	
	notCheckedAPIs := make(map[string]bool)
	for knownAPI := range maps.Keys(ri.result.ReflectAPIs) {
		if !ri.checkedAPIs[knownAPI] {
			notCheckedAPIs[knownAPI] = true
		}
	}

	ri.ignoreReflectedTypes(ssaPkg)

	
	
	maps.Copy(ri.checkedAPIs, notCheckedAPIs)

	
	newDone := len(ri.result.ReflectAPIs) + len(ri.result.ReflectObjectNames)
	if newDone > prevDone {
		ri.recordReflection(ssaPkg) 
	}
}



func (ri *reflectInspector) ignoreReflectedTypes(ssaPkg *ssa.Package) {
	
	
	
	
	if ri.pkg.Path() == "reflect" {
		scope := ri.pkg.Scope()
		ri.recursivelyRecordUsedForReflect(scope.Lookup("rtype").Type())
		ri.recursivelyRecordUsedForReflect(scope.Lookup("Value").Type())
	}

	for _, memb := range ssaPkg.Members {
		switch x := memb.(type) {
		case *ssa.Type:
			
			

			method := func(mset *types.MethodSet) {
				for at := range mset.Methods() {
					if m := ssaPkg.Prog.MethodValue(at); m != nil {
						ri.checkFunction(m)
					} else {
						m := at.Obj().(*types.Func)
						
						ri.checkInterfaceMethod(m)
					}
				}
			}

			
			mset := ssaPkg.Prog.MethodSets.MethodSet(x.Type())
			method(mset)

			mset = ssaPkg.Prog.MethodSets.MethodSet(types.NewPointer(x.Type()))
			method(mset)

		case *ssa.Function:
			
			

			ri.checkFunction(x)
		}
	}
}








func (ri *reflectInspector) checkMethodSignature(reflectParams map[int]bool, sig *types.Signature) {
	if sig.Recv() == nil {
		return
	}

	i := 0
	for param := range sig.Params().Variables() {
		if reflectParams[i] {
			i++
			continue
		}

		ignore := false
		switch x := param.Type().(type) {
		case *types.Struct:
			ignore = true
		case *types.Array:
			if _, ok := x.Elem().(*types.Struct); ok {
				ignore = true
			}
		case *types.Slice:
			if _, ok := x.Elem().(*types.Struct); ok {
				ignore = true
			}
		}

		if ignore {
			reflectParams[i] = true
			ri.recursivelyRecordUsedForReflect(param.Type())
		}
		i++
	}
}


func (ri *reflectInspector) checkInterfaceMethod(m *types.Func) {
	reflectParams := make(map[int]bool)
	methodName, _ := stripTypeArgs(m.FullName())

	maps.Copy(reflectParams, ri.result.ReflectAPIs[methodName])

	sig := m.Signature()
	if m.Exported() {
		ri.checkMethodSignature(reflectParams, sig)
	}

	if len(reflectParams) > 0 {
		ri.result.ReflectAPIs[methodName] = reflectParams

		
	}
}


func (ri *reflectInspector) checkFunction(fun *ssa.Function) {
	
	
	
	

	f, _ := ssaFuncOrigin(fun).Object().(*types.Func)
	var funcName string
	genericFunc := false
	if f != nil {
		funcName, genericFunc = stripTypeArgs(f.FullName())
	}

	reflectParams := make(map[int]bool)
	if funcName != "" {
		maps.Copy(reflectParams, ri.result.ReflectAPIs[funcName])

		if f.Exported() {
			ri.checkMethodSignature(reflectParams, fun.Signature)
		}
	}

	
	

	for _, block := range fun.Blocks {
		for _, inst := range block.Instrs {
			if ri.propagatedInstr[inst] {
				break 
			}

			
			switch inst := inst.(type) {
			case *ssa.Store:
				obj := typeToObj(inst.Addr.Type())
				if obj != nil && ri.usedForReflect(obj) {
					ri.recordArgReflected(inst.Val, make(map[ssa.Value]bool))
					ri.propagatedInstr[inst] = true
				}
			case *ssa.ChangeType:
				obj := typeToObj(inst.X.Type())
				if obj != nil && ri.usedForReflect(obj) {
					ri.recursivelyRecordUsedForReflect(inst.Type())
					ri.propagatedInstr[inst] = true
				}
			case *ssa.Call:
				callName := ""
				if callee := inst.Call.StaticCallee(); callee != nil {
					if obj, ok := ssaFuncOrigin(callee).Object().(*types.Func); ok && obj != nil {
						callName = obj.FullName()
					}
				}
				if callName == "" && inst.Call.Method != nil {
					callName = inst.Call.Method.FullName()
				}
				if callName == "" {
					callName = inst.Call.Value.String()
				}
				rawCallName := callName
				callName, genericCall := stripTypeArgs(callName)
				if flagDebug && genericCall {
					log.Printf("reflect: normalized call %q to %q", rawCallName, callName)
				}

				if ri.checkedAPIs[callName] {
					
					continue
				}

				

				
				knownParams := ri.result.ReflectAPIs[callName]
				for knownParam := range knownParams {
					sig := inst.Call.Signature()
					if sig == nil {
						continue
					}
					
					
					
					
					
					
					
					
					
					firstParamArg := len(inst.Call.Args) - sig.Params().Len()
					argPos := firstParamArg + knownParam
					if argPos < 0 || argPos >= len(inst.Call.Args) {
						continue
					}

					arg := inst.Call.Args[argPos]
					

					reflectedParam := ri.recordArgReflected(arg, make(map[ssa.Value]bool))
					if reflectedParam == nil {
						continue
					}

					pos := slices.Index(fun.Params, reflectedParam)
					if genericFunc {
						
						extra := len(fun.Params) - fun.Signature.Params().Len()
						if extra > 0 {
							pos -= extra
						}
					}
					if pos < 0 {
						continue
					}

					

					reflectParams[pos] = true
					if fun.Signature.Recv() != nil && pos > 0 {
						
						
						reflectParams[pos-1] = true
					}
					if flagDebug {
						log.Printf("reflect: %s marks param %d reflected via %s argument %T", fun, pos, callName, arg)
					}
				}
			}
		}
	}

	if len(reflectParams) > 0 {
		if funcName == "" {
			return
		}
		ri.result.ReflectAPIs[funcName] = reflectParams
		if flagDebug {
			log.Printf("reflect: function %s has reflected params %v", funcName, reflectParams)
		}

		
	}
}




func (ri *reflectInspector) recordArgReflected(val ssa.Value, visited map[ssa.Value]bool) *ssa.Parameter {
	
	if visited[val] {
		return nil
	}

	
	visited[val] = true

	switch val := val.(type) {
	case *ssa.IndexAddr:
		for _, ref := range *val.Referrers() {
			if store, ok := ref.(*ssa.Store); ok {
				ri.recordArgReflected(store.Val, visited)
			}
		}
		return ri.recordArgReflected(val.X, visited)
	case *ssa.Slice:
		return ri.recordArgReflected(val.X, visited)
	case *ssa.MakeInterface:
		return ri.recordArgReflected(val.X, visited)
	case *ssa.UnOp:
		for _, ref := range *val.Referrers() {
			if idx, ok := ref.(ssa.Value); ok {
				ri.recordArgReflected(idx, visited)
			}
		}
		return ri.recordArgReflected(val.X, visited)
	case *ssa.FieldAddr:
		return ri.recordArgReflected(val.X, visited)

	case *ssa.Alloc:
		
		ri.recursivelyRecordUsedForReflect(val.Type())

		for _, ref := range *val.Referrers() {
			if idx, ok := ref.(ssa.Value); ok {
				ri.recordArgReflected(idx, visited)
			}
		}

		
		visited := make(map[ssa.Value]bool)

		
		return relatedParam(val, visited)

	case *ssa.ChangeType:
		ri.recursivelyRecordUsedForReflect(val.X.Type())
		return ri.recordArgReflected(val.X, visited)
	case *ssa.MakeSlice, *ssa.MakeMap, *ssa.MakeChan, *ssa.Const:
		ri.recursivelyRecordUsedForReflect(val.Type())
	case *ssa.Global:
		ri.recursivelyRecordUsedForReflect(val.Type())

		
		
		
	case *ssa.Parameter:
		
		

		ri.recursivelyRecordUsedForReflect(val.Type())
		return val
	}

	return nil
}



func relatedParam(val ssa.Value, visited map[ssa.Value]bool) *ssa.Parameter {
	
	if visited[val] {
		return nil
	}

	

	visited[val] = true

	switch x := val.(type) {
	case *ssa.Parameter:
		
		return x
	case *ssa.UnOp:
		if param := relatedParam(x.X, visited); param != nil {
			return param
		}
	case *ssa.FieldAddr:
		


		if param := relatedParam(x.X, visited); param != nil {
			return param
		}
	}

	refs := val.Referrers()
	if refs == nil {
		return nil
	}

	for _, ref := range *refs {
		

		var param *ssa.Parameter
		switch ref := ref.(type) {
		case *ssa.FieldAddr:
			param = relatedParam(ref, visited)

		case *ssa.UnOp:
			param = relatedParam(ref, visited)

		case *ssa.Store:
			if param := relatedParam(ref.Val, visited); param != nil {
				return param
			}

			param = relatedParam(ref.Addr, visited)

		}

		if param != nil {
			return param
		}
	}
	return nil
}







func (ri *reflectInspector) recursivelyRecordUsedForReflect(t types.Type) {
	ri.recursivelyRecordUsedForReflectImpl(t, make(map[types.Type]bool))
}

func (ri *reflectInspector) recursivelyRecordUsedForReflectImpl(t types.Type, visited map[types.Type]bool) {
	if t == nil || visited[t] {
		return
	}
	visited[t] = true

	switch t := t.(type) {
	case *types.Alias:
		ri.recursivelyRecordUsedForReflectImpl(t.Rhs(), visited)

	case *types.Named:
		obj := t.Obj()
		if obj.Pkg() == nil {
			return
		}
		if ri.usedForReflect(obj) {
			return 
		}
		ri.recordUsedForReflect(obj, nil)
		
		
		ri.recursivelyRecordUsedForReflectImpl(t.Origin().Underlying(), visited)

	case *types.Struct:
		for i := range t.NumFields() {
			field := t.Field(i)
			if field.Pkg() != nil {
				
				
				originField := field.Origin()
				ri.recordUsedForReflect(originField, t)
			}
			ri.recursivelyRecordUsedForReflectImpl(field.Type(), visited)
		}

	case interface{ Elem() types.Type }:
		
		ri.recursivelyRecordUsedForReflectImpl(t.Elem(), visited)
	}
}



func (ri *reflectInspector) obfuscatedObjectName(obj types.Object, parent *types.Struct) string {
	pkg := obj.Pkg()
	if pkg == nil {
		return "" 
	}

	if v, ok := obj.(*types.Var); ok && parent != nil {
		return hashWithStruct(parent, v)
	}

	lpkg := ri.lpkg
	if pkg != ri.pkg {
		var ok bool
		lpkg, ok = sharedCache.ListedPackages[pkg.Path()]
		if !ok {
			panic("missing listed package for foreign reflected object: " + pkg.Path())
		}
	}
	return hashWithPackage(lpkg, obj.Name())
}



func (ri *reflectInspector) recordUsedForReflect(obj types.Object, parent *types.Struct) {
	obfName := ri.obfuscatedObjectName(obj, parent)
	if obfName == "" {
		return
	}
	ri.result.ReflectObjectNames[obfName] = obj.Name()
	if flagDebug {
		log.Printf("reflect: preserving object %s as %q -> %q", obj, obfName, obj.Name())
	}
}

func (ri *reflectInspector) usedForReflect(obj types.Object) bool {
	obfName := ri.obfuscatedObjectName(obj, nil)
	if obfName == "" {
		return false
	}
	
	
	
	_, ok := ri.result.ReflectObjectNames[obfName]
	return ok
}



func typeToObj(typ types.Type) types.Object {
	switch t := typ.(type) {
	case *types.Named:
		return t.Obj()
	case *types.Struct:
		if t.NumFields() > 0 {
			return t.Field(0)
		}
	case interface{ Elem() types.Type }:
		return typeToObj(t.Elem())
	}
	return nil
}





func stripTypeArgs(name string) (string, bool) {
	if !strings.Contains(name, "[") {
		return name, false
	}
	var b strings.Builder
	b.Grow(len(name))
	depth := 0
	for _, r := range name {
		switch r {
		case '[':
			depth++
		case ']':
			if depth > 0 {
				depth--
				continue
			}
			b.WriteRune(r)
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	return b.String(), true
}

func ssaFuncOrigin(fn *ssa.Function) *ssa.Function {
	if orig := fn.Origin(); orig != nil {
		return orig
	}
	return fn
}
