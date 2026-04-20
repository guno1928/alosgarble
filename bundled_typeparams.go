



package main

import (
	"errors"
	"fmt"
	"go/types"
	"os"
	"strings"
)

const typeparams_debug = false

var typeparams_ErrEmptyTypeSet = errors.New("empty type set")







func typeparams_InterfaceTermSet(iface *types.Interface) ([]*types.Term, error) {
	return typeparams_computeTermSet(iface)
}







func typeparams_UnionTermSet(union *types.Union) ([]*types.Term, error) {
	return typeparams_computeTermSet(union)
}

func typeparams_computeTermSet(typ types.Type) ([]*types.Term, error) {
	tset, err := typeparams_computeTermSetInternal(typ, make(map[types.Type]*typeparams_termSet), 0)
	if err != nil {
		return nil, err
	}
	if tset.terms.isEmpty() {
		return nil, typeparams_ErrEmptyTypeSet
	}
	if tset.terms.isAll() {
		return nil, nil
	}
	var terms []*types.Term
	for _, term := range tset.terms {
		terms = append(terms, types.NewTerm(term.tilde, term.typ))
	}
	return terms, nil
}






type typeparams_termSet struct {
	complete bool
	terms    typeparams_termlist
}

func typeparams_indentf(depth int, format string, args ...any) {
	fmt.Fprintf(os.Stderr, strings.Repeat(".", depth)+format+"\n", args...)
}

func typeparams_computeTermSetInternal(t types.Type, seen map[types.Type]*typeparams_termSet, depth int) (res *typeparams_termSet, err error) {
	if t == nil {
		panic("nil type")
	}

	if typeparams_debug {
		typeparams_indentf(depth, "%s", t.String())
		defer func() {
			if err != nil {
				typeparams_indentf(depth, "=> %s", err)
			} else {
				typeparams_indentf(depth, "=> %s", res.terms.String())
			}
		}()
	}

	const maxTermCount = 100
	if tset, ok := seen[t]; ok {
		if !tset.complete {
			return nil, fmt.Errorf("cycle detected in the declaration of %s", t)
		}
		return tset, nil
	}

	
	tset := new(typeparams_termSet)
	defer func() {
		tset.complete = true
	}()
	seen[t] = tset

	switch u := t.Underlying().(type) {
	case *types.Interface:
		
		
		tset.terms = typeparams_allTermlist
		for embedded := range u.EmbeddedTypes() {
			if _, ok := embedded.Underlying().(*types.TypeParam); ok {
				return nil, fmt.Errorf("invalid embedded type %T", embedded)
			}
			tset2, err := typeparams_computeTermSetInternal(embedded, seen, depth+1)
			if err != nil {
				return nil, err
			}
			tset.terms = tset.terms.intersect(tset2.terms)
		}
	case *types.Union:
		
		tset.terms = nil
		for t := range u.Terms() {
			var terms typeparams_termlist
			switch t.Type().Underlying().(type) {
			case *types.Interface:
				tset2, err := typeparams_computeTermSetInternal(t.Type(), seen, depth+1)
				if err != nil {
					return nil, err
				}
				terms = tset2.terms
			case *types.TypeParam, *types.Union:
				
				
				return nil, fmt.Errorf("invalid union term %T", t)
			default:
				if t.Type() == types.Typ[types.Invalid] {
					continue
				}
				terms = typeparams_termlist{{t.Tilde(), t.Type()}}
			}
			tset.terms = tset.terms.union(terms)
			if len(tset.terms) > maxTermCount {
				return nil, fmt.Errorf("exceeded max term count %d", maxTermCount)
			}
		}
	case *types.TypeParam:
		panic("unreachable")
	default:
		
		
		if u != types.Typ[types.Invalid] {
			tset.terms = typeparams_termlist{{false, t}}
		}
	}
	return tset, nil
}



func typeparams_under(t types.Type) types.Type {
	return t.Underlying()
}






type typeparams_termlist []*typeparams_term






var typeparams_allTermlist = typeparams_termlist{new(typeparams_term)}


const typeparams_termSep = " | "


func (xl typeparams_termlist) String() string {
	if len(xl) == 0 {
		return "∅"
	}
	var buf strings.Builder
	for i, x := range xl {
		if i > 0 {
			buf.WriteString(typeparams_termSep)
		}
		buf.WriteString(x.String())
	}
	return buf.String()
}


func (xl typeparams_termlist) isEmpty() bool {
	
	
	
	for _, x := range xl {
		if x != nil {
			return false
		}
	}
	return true
}


func (xl typeparams_termlist) isAll() bool {
	
	
	
	for _, x := range xl {
		if x != nil && x.typ == nil {
			return true
		}
	}
	return false
}


func (xl typeparams_termlist) norm() typeparams_termlist {
	
	
	used := make([]bool, len(xl))
	var rl typeparams_termlist
	for i, xi := range xl {
		if xi == nil || used[i] {
			continue
		}
		for j := i + 1; j < len(xl); j++ {
			xj := xl[j]
			if xj == nil || used[j] {
				continue
			}
			if u1, u2 := xi.union(xj); u2 == nil {
				
				
				
				
				
				
				if u1.typ == nil {
					return typeparams_allTermlist
				}
				xi = u1
				used[j] = true 
			}
		}
		rl = append(rl, xi)
	}
	return rl
}


func (xl typeparams_termlist) union(yl typeparams_termlist) typeparams_termlist {
	return append(xl, yl...).norm()
}


func (xl typeparams_termlist) intersect(yl typeparams_termlist) typeparams_termlist {
	if xl.isEmpty() || yl.isEmpty() {
		return nil
	}

	
	
	var rl typeparams_termlist
	for _, x := range xl {
		for _, y := range yl {
			if r := x.intersect(y); r != nil {
				rl = append(rl, r)
			}
		}
	}
	return rl.norm()
}







type typeparams_term struct {
	tilde bool 
	typ   types.Type
}

func (x *typeparams_term) String() string {
	switch {
	case x == nil:
		return "∅"
	case x.typ == nil:
		return "𝓤"
	case x.tilde:
		return "~" + x.typ.String()
	default:
		return x.typ.String()
	}
}


func (x *typeparams_term) union(y *typeparams_term) (_, _ *typeparams_term) {
	
	switch {
	case x == nil && y == nil:
		return nil, nil 
	case x == nil:
		return y, nil 
	case y == nil:
		return x, nil 
	case x.typ == nil:
		return x, nil 
	case y.typ == nil:
		return y, nil 
	}
	

	if x.disjoint(y) {
		return x, y 
	}
	

	
	
	
	
	if x.tilde || !y.tilde {
		return x, nil
	}
	return y, nil
}


func (x *typeparams_term) intersect(y *typeparams_term) *typeparams_term {
	
	switch {
	case x == nil || y == nil:
		return nil 
	case x.typ == nil:
		return y 
	case y.typ == nil:
		return x 
	}
	

	if x.disjoint(y) {
		return nil 
	}
	

	
	
	
	
	if !x.tilde || y.tilde {
		return x
	}
	return y
}



func (x *typeparams_term) disjoint(y *typeparams_term) bool {
	if typeparams_debug && (x.typ == nil || y.typ == nil) {
		panic("invalid argument(s)")
	}
	ux := x.typ
	if y.tilde {
		ux = typeparams_under(ux)
	}
	uy := y.typ
	if x.tilde {
		uy = typeparams_under(uy)
	}
	return !types.Identical(ux, uy)
}
