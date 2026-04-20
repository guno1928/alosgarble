


package main

import (
	"bufio"
	"flag"
	"fmt"
	"go/ast"
	"go/types"
	"io"
	"os"
	"strings"
)


func commandReverse(args []string) error {
	flags, args := splitFlagsFromArgs(args)
	if hasHelpFlag(flags) || len(args) == 0 {
		fmt.Fprint(os.Stderr, `
usage: garble [garble flags] reverse [build flags] package [files]

For example, after building an obfuscated program as follows:

	garble -literals build -tags=mytag ./cmd/mycmd

One can reverse a captured panic stack trace as follows:

	garble -literals reverse -tags=mytag ./cmd/mycmd panic-output.txt
`[1:])
		return errJustExit(2)
	}

	pkg, args := args[0], args[1:]
	
	
	_, err := toolexecCmd("list", append(flags, pkg))
	defer os.RemoveAll(os.Getenv("GARBLE_SHARED"))
	if err != nil {
		return err
	}

	
	
	
	if _, firstUnknown := filterForwardBuildFlags(flags); firstUnknown != "" {
		
		
		return flag.NewFlagSet("", flag.ContinueOnError).Parse([]string{firstUnknown})
	}

	
	
	
	
	
	var replaces []string

	for _, lpkg := range sharedCache.ListedPackages {
		if !lpkg.ToObfuscate {
			continue
		}
		addHashedWithPackage := func(str string) {
			replaces = append(replaces, hashWithPackage(lpkg, str), str)
		}

		
		addHashedWithPackage(lpkg.ImportPath)

		
		
		
		for _, name := range lpkg.SFiles {
			newName := hashWithPackage(lpkg, name) + ".s"
			replaces = append(replaces, newName, name)
		}

		files, err := parseFiles(lpkg, lpkg.Dir, lpkg.CompiledGoFiles)
		if err != nil {
			return err
		}
		origImporter := importerForPkg(lpkg)
		_, info, err := typecheck(lpkg.ImportPath, files, origImporter)
		if err != nil {
			return err
		}
		fieldToStruct := computeFieldToStruct(info)
		for i, file := range files {
			goFile := lpkg.CompiledGoFiles[i]
			for node := range ast.Preorder(file) {
				switch node := node.(type) {

				
				
				case *ast.FuncDecl:
					addHashedWithPackage(node.Name.Name)
				case *ast.TypeSpec:
					addHashedWithPackage(node.Name.Name)
				case *ast.Field:
					for _, name := range node.Names {
						obj, _ := info.ObjectOf(name).(*types.Var)
						if obj == nil || !obj.IsField() {
							continue
						}
						originObj := obj.Origin()
						strct := fieldToStruct[originObj]
						if strct == nil {
							panic("could not find struct for field " + name.Name)
						}
						replaces = append(replaces, hashWithStruct(strct, originObj), name.Name)
					}

				case *ast.CallExpr:
					
					pos := fset.Position(node.Pos())
					origPos := fmt.Sprintf("%s:%d", goFile, pos.Offset)
					newFilename := hashWithPackage(lpkg, origPos) + ".go"

					
					
					replaces = append(replaces,
						newFilename+":1",
						fmt.Sprintf("%s/%s:%d", lpkg.ImportPath, goFile, pos.Line),
					)

					
					
					
					
					
					replaces = append(replaces,
						newFilename,
						fmt.Sprintf("%s/%s", lpkg.ImportPath, goFile),
					)
				}
			}
		}
	}
	repl := strings.NewReplacer(replaces...)

	if len(args) == 0 {
		modified, err := reverseContent(os.Stdout, os.Stdin, repl)
		if err != nil {
			return err
		}
		if !modified {
			return errJustExit(1)
		}
		return nil
	}
	
	anyModified := false
	for _, path := range args {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		modified, err := reverseContent(os.Stdout, f, repl)
		if err != nil {
			return err
		}
		anyModified = anyModified || modified
		f.Close() 
	}
	if !anyModified {
		return errJustExit(1)
	}
	return nil
}

func reverseContent(w io.Writer, r io.Reader, repl *strings.Replacer) (bool, error) {
	
	
	
	
	
	
	br := bufio.NewReader(r)
	modified := false
	for {
		
		
		
		line, readErr := br.ReadString('\n')

		newLine := repl.Replace(line)
		if newLine != line {
			modified = true
		}
		if _, err := io.WriteString(w, newLine); err != nil {
			return modified, err
		}
		if readErr == io.EOF {
			return modified, nil
		}
		if readErr != nil {
			return modified, readErr
		}
	}
}
