

package main

import (
	"fmt"
	"go/types"
	_ "unsafe"
)





func typeutil_hash(t types.Type) uint32 {
	return typeutil_theHasher.Hash(t)
}



type typeutil_Hasher struct{}

var typeutil_theHasher typeutil_Hasher



func (h typeutil_Hasher) Hash(t types.Type) uint32 {
	return typeutil_hasher{
		inGenericSig: false,
		typeParamIDs: make(map[*types.TypeParam]uint32),
	}.hash(t)
}







type typeutil_hasher struct {
	inGenericSig bool
	typeParamIDs map[*types.TypeParam]uint32
}


func typeutil_hashString(s string) uint32 {
	var h uint32
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}


func (h typeutil_hasher) hash(t types.Type) uint32 {
	
	switch t := t.(type) {
	case *types.Basic:
		return uint32(t.Kind())

	case *types.Alias:
		return h.hash(types.Unalias(t))

	case *types.Array:
		return 9043 + 2*uint32(t.Len()) + 3*h.hash(t.Elem())

	case *types.Slice:
		return 9049 + 2*h.hash(t.Elem())

	case *types.Struct:
		var hash uint32 = 9059
		for i, n := 0, t.NumFields(); i < n; i++ {
			f := t.Field(i)
			if f.Anonymous() {
				hash += 8861
			}
			
			
			hash += typeutil_hashString(f.Name()) 
			hash += h.hash(f.Type())
		}
		return hash

	case *types.Pointer:
		return 9067 + 2*h.hash(t.Elem())

	case *types.Signature:
		var hash uint32 = 9091
		if t.Variadic() {
			hash *= 8863
		}

		tparams := t.TypeParams()
		if n := tparams.Len(); n > 0 {
			h.inGenericSig = true 

			for i := range n {
				tparam := tparams.At(i)
				hash += 7 * h.hash(tparam.Constraint())
			}
		}

		return hash + 3*h.hashTuple(t.Params()) + 5*h.hashTuple(t.Results())

	case *types.Union:
		return h.hashUnion(t)

	case *types.Interface:
		
		
		
		var hash uint32 = 9103

		
		for i, n := 0, t.NumMethods(); i < n; i++ {
			
			
			m := t.Method(i)
			
			
			hash += 3*typeutil_hashString(m.Name()) + 5*h.shallowHash(m.Type())
		}

		
		terms, err := typeparams_InterfaceTermSet(t)
		
		if err == nil {
			hash += h.hashTermSet(terms)
		}

		return hash

	case *types.Map:
		return 9109 + 2*h.hash(t.Key()) + 3*h.hash(t.Elem())

	case *types.Chan:
		return 9127 + 2*uint32(t.Dir()) + 3*h.hash(t.Elem())

	case *types.Named:
		hash := h.hashTypeName(t.Obj())
		targs := t.TypeArgs()
		for targ := range targs.Types() {
			hash += 2 * h.hash(targ)
		}
		return hash

	case *types.TypeParam:
		return h.hashTypeParam(t)

	case *types.Tuple:
		return h.hashTuple(t)
	}

	panic(fmt.Sprintf("%T: %v", t, t))
}

func (h typeutil_hasher) hashTuple(tuple *types.Tuple) uint32 {
	
	n := tuple.Len()
	hash := 9137 + 2*uint32(n)
	for i := range n {
		hash += 3 * h.hash(tuple.At(i).Type())
	}
	return hash
}

func (h typeutil_hasher) hashUnion(t *types.Union) uint32 {
	
	terms, err := typeparams_UnionTermSet(t)
	
	
	if err != nil {
		return 9151
	}
	return h.hashTermSet(terms)
}

func (h typeutil_hasher) hashTermSet(terms []*types.Term) uint32 {
	hash := 9157 + 2*uint32(len(terms))
	for _, term := range terms {
		
		termHash := h.hash(term.Type())
		if term.Tilde() {
			termHash *= 9161
		}
		hash += 3 * termHash
	}
	return hash
}


func (h typeutil_hasher) hashTypeParam(t *types.TypeParam) uint32 {
	
	
	
	
	
	
	
	
	
	
	
	if !h.inGenericSig {
		
		
		
		
		if id, ok := h.typeParamIDs[t]; ok {
			
			return 9173 + 3*id
		}
		id := uint32(len(h.typeParamIDs))
		h.typeParamIDs[t] = id
		return 9173 + 3*id
	}
	return 9173 + 3*uint32(t.Index())
}


func (typeutil_hasher) hashTypeName(tname *types.TypeName) uint32 {
	
	
	return typeutil_hashString(tname.Name())
	
	
	
	
}















func (h typeutil_hasher) shallowHash(t types.Type) uint32 {
	
	
	
	
	switch t := t.(type) {
	case *types.Alias:
		return h.shallowHash(types.Unalias(t))

	case *types.Signature:
		var hash uint32 = 604171
		if t.Variadic() {
			hash *= 971767
		}
		
		
		return hash + 1062599*h.shallowHash(t.Params()) + 1282529*h.shallowHash(t.Results())

	case *types.Tuple:
		n := t.Len()
		hash := 9137 + 2*uint32(n)
		for i := range n {
			hash += 53471161 * h.shallowHash(t.At(i).Type())
		}
		return hash

	case *types.Basic:
		return 45212177 * uint32(t.Kind())

	case *types.Array:
		return 1524181 + 2*uint32(t.Len())

	case *types.Slice:
		return 2690201

	case *types.Struct:
		return 3326489

	case *types.Pointer:
		return 4393139

	case *types.Union:
		return 562448657

	case *types.Interface:
		return 2124679 

	case *types.Map:
		return 9109

	case *types.Chan:
		return 9127

	case *types.Named:
		return h.hashTypeName(t.Obj())

	case *types.TypeParam:
		return h.hashTypeParam(t)
	}
	panic(fmt.Sprintf("shallowHash: %T: %v", t, t))
}
