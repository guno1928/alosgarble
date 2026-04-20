package main

// Injected code below this line.

var _originalNamePairs = []string{}

var _originalNamesReplacer *_genericReplacer

//disabledgo:linkname _originalNamesInit internal/abi._originalNamesInit
func _originalNamesInit() {
	_originalNamesReplacer = _makeGenericReplacer(_originalNamePairs)
}

//disabledgo:linkname _originalNames internal/abi._originalNames
func _originalNames(name string) string {
	return _originalNamesReplacer.Replace(name)
}

func _hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[0:len(prefix)] == prefix
}

type _trieNode struct {
	value string

	priority int

	prefix string
	next   *_trieNode

	table []*_trieNode
}

func (t *_trieNode) add(key, val string, priority int, r *_genericReplacer) {
	if key == "" {
		if t.priority == 0 {
			t.value = val
			t.priority = priority
		}
		return
	}

	if t.prefix != "" {
		var n int
		for ; n < len(t.prefix) && n < len(key); n++ {
			if t.prefix[n] != key[n] {
				break
			}
		}
		if n == len(t.prefix) {
			t.next.add(key[n:], val, priority, r)
		} else if n == 0 {
			var prefixNode *_trieNode
			if len(t.prefix) == 1 {
				prefixNode = t.next
			} else {
				prefixNode = &_trieNode{
					prefix: t.prefix[1:],
					next:   t.next,
				}
			}
			keyNode := new(_trieNode)
			t.table = make([]*_trieNode, r.tableSize)
			t.table[r.mapping[t.prefix[0]]] = prefixNode
			t.table[r.mapping[key[0]]] = keyNode
			t.prefix = ""
			t.next = nil
			keyNode.add(key[1:], val, priority, r)
		} else {

			next := &_trieNode{
				prefix: t.prefix[n:],
				next:   t.next,
			}
			t.prefix = t.prefix[:n]
			t.next = next
			next.add(key[n:], val, priority, r)
		}
	} else if t.table != nil {

		m := r.mapping[key[0]]
		if t.table[m] == nil {
			t.table[m] = new(_trieNode)
		}
		t.table[m].add(key[1:], val, priority, r)
	} else {
		t.prefix = key
		t.next = new(_trieNode)
		t.next.add("", val, priority, r)
	}
}

func (r *_genericReplacer) lookup(s string, ignoreRoot bool) (val string, keylen int, found bool) {

	bestPriority := 0
	node := &r.root
	n := 0
	for node != nil {
		if node.priority > bestPriority && !(ignoreRoot && node == &r.root) {
			bestPriority = node.priority
			val = node.value
			keylen = n
			found = true
		}

		if s == "" {
			break
		}
		if node.table != nil {
			index := r.mapping[s[0]]
			if int(index) == r.tableSize {
				break
			}
			node = node.table[index]
			s = s[1:]
			n++
		} else if node.prefix != "" && _hasPrefix(s, node.prefix) {
			n += len(node.prefix)
			s = s[len(node.prefix):]
			node = node.next
		} else {
			break
		}
	}
	return
}

type _genericReplacer struct {
	root _trieNode

	tableSize int

	mapping [256]byte
}

func _makeGenericReplacer(oldnew []string) *_genericReplacer {
	r := new(_genericReplacer)

	for i := 0; i < len(oldnew); i += 2 {
		key := oldnew[i]
		for j := 0; j < len(key); j++ {
			r.mapping[key[j]] = 1
		}
	}

	for _, b := range r.mapping {
		r.tableSize += int(b)
	}

	var index byte
	for i, b := range r.mapping {
		if b == 0 {
			r.mapping[i] = byte(r.tableSize)
		} else {
			r.mapping[i] = index
			index++
		}
	}

	r.root.table = make([]*_trieNode, r.tableSize)

	for i := 0; i < len(oldnew); i += 2 {
		r.root.add(oldnew[i], oldnew[i+1], len(oldnew)-i, r)
	}
	return r
}

func (r *_genericReplacer) Replace(s string) string {
	dst := make([]byte, 0, len(s))
	var last int
	var prevMatchEmpty bool
	for i := 0; i <= len(s); {

		if i != len(s) && r.root.priority == 0 {
			index := int(r.mapping[s[i]])
			if index == r.tableSize || r.root.table[index] == nil {
				i++
				continue
			}
		}

		val, keylen, match := r.lookup(s[i:], prevMatchEmpty)
		prevMatchEmpty = match && keylen == 0
		if match {
			dst = append(dst, s[last:i]...)
			dst = append(dst, val...)
			i += keylen
			last = i
			continue
		}
		i++
	}
	if last != len(s) {
		dst = append(dst, s[last:]...)
	}
	return string(dst)
}
