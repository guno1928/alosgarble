


package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/printer"
	"go/scanner"
	"go/token"
	"path/filepath"
	"strings"
)

var printBuf1, printBuf2 bytes.Buffer



func printFile(lpkg *listedPackage, file *ast.File) ([]byte, error) {
	if lpkg.ToObfuscate {
		
		
		
		var newComments []*ast.CommentGroup
		for _, group := range file.Comments {
			var newGroup ast.CommentGroup
			for _, comment := range group.List {
				if strings.HasPrefix(comment.Text, "//go:") {
					newGroup.List = append(newGroup.List, comment)
				}
			}
			if len(newGroup.List) > 0 {
				newComments = append(newComments, &newGroup)
			}
		}
		file.Comments = newComments
	}

	printBuf1.Reset()
	printConfig := printer.Config{Mode: printer.RawFormat}
	if err := printConfig.Fprint(&printBuf1, fset, file); err != nil {
		return nil, err
	}
	src := printBuf1.Bytes()

	if !lpkg.ToObfuscate {
		
		
		
		return src, nil
	}

	fsetFile := fset.File(file.Pos())
	filename := filepath.Base(fsetFile.Name())
	newPrefix := ""
	if strings.HasPrefix(filename, "_cgo_") {
		newPrefix = "_cgo_"
	}

	
	
	
	
	
	

	
	
	
	var origCallOffsets []int
	nextOffset := -1
	for node := range ast.Preorder(file) {
		switch node := node.(type) {
		case *ast.CallExpr:
			nextOffset = fsetFile.Position(node.Pos()).Offset
		case *ast.Ident:
			origCallOffsets = append(origCallOffsets, nextOffset)
			nextOffset = -1
		}
	}

	copied := 0
	printBuf2.Reset()

	
	
	
	fmt.Fprintf(&printBuf2, "//line %s:1\n", newPrefix)

	
	
	
	
	
	var s scanner.Scanner
	fsetFile = fset.AddFile("", fset.Base(), len(src))
	s.Init(fsetFile, src, nil, scanner.ScanComments)

	identIndex := 0
	for {
		pos, tok, lit := s.Scan()
		switch tok {
		case token.EOF:
			
			printBuf2.Write(src[copied:])
			return printBuf2.Bytes(), nil
		case token.COMMENT:
			
			
			
			
			if strings.HasPrefix(lit, "//go:") {
				continue 
			}
			offset := fsetFile.Position(pos).Offset
			printBuf2.Write(src[copied:offset])
			copied = offset + len(lit)
		case token.IDENT:
			origOffset := origCallOffsets[identIndex]
			identIndex++
			if origOffset == -1 {
				continue 
			}
			newName := ""
			if !flagTiny {
				origPos := fmt.Sprintf("%s:%d", filename, origOffset)
				newName = hashWithPackage(lpkg, origPos) + ".go"
				
			}

			offset := fsetFile.Position(pos).Offset
			printBuf2.Write(src[copied:offset])
			copied = offset

			
			
			
			
			
			
			fmt.Fprintf(&printBuf2, " /*line %s%s:1*/ ", newPrefix, newName)
		}
	}
}
