package main

import (
	"bufio"
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"log"
	"maps"
	mathrand "math/rand"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/guno1928/alosgarble/internal/ctrlflow"
	"github.com/guno1928/alosgarble/internal/literals"
	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/ssa"
)

//go:generate go tool bundle -o cmdgo_quoted.go -prefix cmdgoQuoted cmd/internal/quoted
//go:generate sed -i /go:generate/d cmdgo_quoted.go

func computeLinkerVariableStrings(pkg *types.Package) (map[*types.Var]string, error) {
	linkerVariableStrings := make(map[*types.Var]string)

	ldflags, err := cmdgoQuotedSplit(flagValue(sharedCache.ForwardBuildFlags, "-ldflags"))
	if err != nil {
		return nil, err
	}
	for val := range flagValues(ldflags, "-X") {

		fullName, stringValue, found := strings.Cut(val, "=")
		if !found {
			continue
		}

		i := strings.LastIndexByte(fullName, '.')
		path, name := fullName[:i], fullName[i+1:]

		if path != pkg.Path() && (path != "main" || pkg.Name() != "main") {
			continue
		}

		obj, _ := pkg.Scope().Lookup(name).(*types.Var)
		if obj == nil {
			continue
		}
		linkerVariableStrings[obj] = stringValue
	}
	return linkerVariableStrings, nil
}

func typecheck(pkgPath string, files []*ast.File, origImporter importerWithMap) (*types.Package, *types.Info, error) {
	info := &types.Info{
		Types:      make(map[ast.Expr]types.TypeAndValue),
		Defs:       make(map[*ast.Ident]types.Object),
		Uses:       make(map[*ast.Ident]types.Object),
		Implicits:  make(map[ast.Node]types.Object),
		Scopes:     make(map[ast.Node]*types.Scope),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
		Instances:  make(map[*ast.Ident]types.Instance),
	}
	origTypesConfig := types.Config{

		Importer: origImporter,
		Sizes:    types.SizesFor("gc", sharedCache.GoEnv.GOARCH),
	}
	pkg, err := origTypesConfig.Check(pkgPath, fset, files, info)
	if err != nil {
		return nil, nil, fmt.Errorf("typecheck error: %v", err)
	}
	return pkg, info, err
}

func computeFieldToStruct(info *types.Info) map[*types.Var]*types.Struct {
	done := make(map[*types.Named]bool)
	fieldToStruct := make(map[*types.Var]*types.Struct)

	for _, tv := range info.Types {
		recordFieldToStruct(tv.Type, done, fieldToStruct)
	}
	return fieldToStruct
}

func recordFieldToStruct(typ types.Type, done map[*types.Named]bool, fieldToStruct map[*types.Var]*types.Struct) {
	switch typ := typ.(type) {
	case interface{ Elem() types.Type }:
		recordFieldToStruct(typ.Elem(), done, fieldToStruct)
	case *types.Alias:
		recordFieldToStruct(typ.Rhs(), done, fieldToStruct)
	case *types.Named:
		if done[typ] {
			return
		}
		done[typ] = true
		recordFieldToStruct(typ.Origin().Underlying(), done, fieldToStruct)
	case *types.Struct:
		for field := range typ.Fields() {
			if field != field.Origin() {

				return
			}
		}
		for field := range typ.Fields() {
			prev := fieldToStruct[field]
			if prev == nil {
				fieldToStruct[field] = typ
			} else if prev != typ {

				panic(fmt.Sprintf("inconsistent fieldToStruct results: %s vs %s", prev, typ))
			}
			if field.Embedded() {
				recordFieldToStruct(field.Type(), done, fieldToStruct)
			}
		}
	}
}

func isSafeForInstanceType(t types.Type) bool {
	switch t := types.Unalias(t).(type) {
	case *types.Basic:
		return t.Kind() != types.Invalid
	case *types.Named:
		if t.TypeParams().Len() > 0 {
			return false
		}
		return isSafeForInstanceType(t.Underlying())
	case *types.Signature:
		return t.TypeParams().Len() == 0
	case *types.Interface:
		return t.IsMethodSet()
	}
	return true
}

func namedType(t types.Type) *types.TypeName {
	switch t := t.(type) {
	case *types.Alias:
		return t.Obj()
	case *types.Named:
		return t.Obj()
	case *types.Pointer:
		return namedType(t.Elem())
	default:
		return nil
	}
}

func isTestSignature(sign *types.Signature) bool {
	if sign.Recv() != nil {
		return false
	}
	params := sign.Params()
	if params.Len() != 1 {
		return false
	}
	tname := namedType(params.At(0).Type())
	if tname == nil {
		return false
	}
	return tname.Pkg().Path() == "testing" && tname.Name() == "T"
}

func splitFlagsFromArgs(all []string) (flags, args []string) {
	for i := 0; i < len(all); i++ {
		arg := all[i]
		if !strings.HasPrefix(arg, "-") {
			return all[:i:i], all[i:]
		}
		if booleanFlags[arg] || strings.Contains(arg, "=") {

			continue
		}

		i++
	}
	return all, nil
}

func alterTrimpath(flags []string) []string {
	trimpath := flagValue(flags, "-trimpath")

	return flagSetValue(flags, "-trimpath", sharedTempDir+"=>;"+trimpath)
}

type transformer struct {
	curPkg *listedPackage

	curPkgCache pkgCache

	pkg  *types.Package
	info *types.Info

	linkerVariableStrings map[*types.Var]string

	fieldToStruct map[*types.Var]*types.Struct

	obfRand *mathrand.Rand

	origImporter importerWithMap

	usedAllImportsFiles map[*ast.File]bool

	guardInjected bool
	skipLiterals  bool
}

var transformMethods = map[string]func(*transformer, []string) ([]string, error){
	"asm":     (*transformer).transformAsm,
	"compile": (*transformer).transformCompile,
	"link":    (*transformer).transformLink,
}

func (tf *transformer) transformAsm(args []string) ([]string, error) {
	flags, paths := splitFlagsFromFiles(args, ".s")

	flags = flagSetValue(flags, "-p", tf.curPkg.obfuscatedImportPath())

	flags = alterTrimpath(flags)

	newPaths := make([]string, 0, len(paths))
	if !slices.Contains(args, "-gensymabis") {

		var replacer *strings.Replacer
		if nameMap := loadGoAsmNames(tf.curPkg); len(nameMap) > 0 {

			origNames := slices.SortedFunc(maps.Keys(nameMap), func(a, b string) int {
				return cmp.Compare(len(b), len(a))
			})
			pairs := make([]string, 0, 2*len(nameMap))
			for _, orig := range origNames {
				pairs = append(pairs, orig, nameMap[orig])
			}
			replacer = strings.NewReplacer(pairs...)
		}
		for _, path := range paths {
			name := hashWithPackage(tf.curPkg, filepath.Base(path)) + ".s"
			pkgDir := filepath.Join(sharedTempDir, tf.curPkg.obfuscatedSourceDir())
			newPath := filepath.Join(pkgDir, name)
			if replacer != nil {
				content, err := os.ReadFile(newPath)
				if err != nil {
					return nil, err
				}
				if new := replacer.Replace(string(content)); new != string(content) {
					if err := os.WriteFile(newPath, []byte(new), 0o666); err != nil {
						return nil, err
					}
				}
			}
			newPaths = append(newPaths, newPath)
		}
		return append(flags, newPaths...), nil
	}

	const missingHeader = "missing header path"
	newHeaderPaths := make(map[string]string)
	var debugArtifacts cachedDebugArtifacts
	if flagDebugDir != "" {
		debugArtifacts.SourceFiles = make(map[string][]byte)
		debugArtifacts.GarbledFiles = make(map[string][]byte)
	}
	var buf, includeBuf bytes.Buffer
	for _, path := range paths {
		buf.Reset()
		var asmContent bytes.Buffer
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		basename := filepath.Base(path)
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			if flagDebugDir != "" {
				asmContent.WriteString(line)
				asmContent.WriteByte('\n')
			}

			line, comment, hasComment := strings.Cut(line, "//")
			if hasComment && line == "" {
				buf.WriteString("//")
				buf.WriteString(comment)
				buf.WriteByte('\n')
				continue
			}

			if quoted, ok := strings.CutPrefix(line, "#include"); ok {
				quoted = strings.TrimSpace(quoted)
				includePath, err := strconv.Unquote(quoted)
				if err != nil {
					return nil, fmt.Errorf("cannot unquote %q: %v", quoted, err)
				}
				newPath := newHeaderPaths[includePath]
				switch newPath {
				case missingHeader:
					buf.WriteString(line)
					buf.WriteByte('\n')
					continue
				case "":
					includeBuf.Reset()
					content, err := os.ReadFile(includePath)
					if errors.Is(err, fs.ErrNotExist) {
						newHeaderPaths[includePath] = missingHeader
						buf.WriteString(line)
						buf.WriteByte('\n')
						continue
					} else if err != nil {
						return nil, err
					}
					basename := filepath.Base(includePath)
					if flagDebugDir != "" {
						debugArtifacts.SourceFiles[basename] = content
						if err := writeDebugDirFile(debugDirSourceSubdir, tf.curPkg, basename, content); err != nil {
							return nil, err
						}
					}
					tf.replaceAsmNames(&includeBuf, content)

					newPath = "garbled_" + basename
					content = includeBuf.Bytes()
					if _, err := tf.writeSourceFile(basename, newPath, content); err != nil {
						return nil, err
					}
					if flagDebugDir != "" {
						debugArtifacts.GarbledFiles[basename] = content
					}
					newHeaderPaths[includePath] = newPath
				}
				buf.WriteString("#include ")
				buf.WriteString(strconv.Quote(newPath))
				buf.WriteByte('\n')
				continue
			}

			tf.replaceAsmNames(&buf, []byte(line))
			buf.WriteByte('\n')
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
		f.Close()
		if flagDebugDir != "" {
			content := asmContent.Bytes()
			debugArtifacts.SourceFiles[basename] = content
			if err := writeDebugDirFile(debugDirSourceSubdir, tf.curPkg, basename, content); err != nil {
				return nil, err
			}
		}

		content := buf.Bytes()

		newName := hashWithPackage(tf.curPkg, basename) + ".s"
		if path, err := tf.writeSourceFile(basename, newName, content); err != nil {
			return nil, err
		} else {
			newPaths = append(newPaths, path)
		}
		if flagDebugDir != "" {
			debugArtifacts.GarbledFiles[basename] = content
		}
	}
	if err := saveDebugArtifactsForPkg(tf.curPkg, debugCacheKindAsm, debugArtifacts); err != nil {
		return nil, err
	}

	return append(flags, newPaths...), nil
}

func (tf *transformer) saveGoAsmNames() error {
	nameMap := make(map[string]string)
	scope := tf.pkg.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		tn, ok := obj.(*types.TypeName)
		if !ok {
			continue
		}
		strct, ok := tn.Type().Underlying().(*types.Struct)
		if !ok {
			continue
		}
		obfTypeName := hashWithPackage(tf.curPkg, name)
		nameMap[name+"__size"] = obfTypeName + "__size"
		for field := range strct.Fields() {
			obfFieldName := hashWithStruct(strct, field)
			nameMap[name+"_"+field.Name()] = obfTypeName + "_" + obfFieldName
		}
	}
	if len(nameMap) == 0 {
		return nil
	}
	fsCache, err := openCache()
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(nameMap); err != nil {
		return err
	}
	return fsCache.PutBytes(goAsmCacheID(tf.curPkg.GarbleActionID), buf.Bytes())
}

func goAsmCacheID(garbleActionID [sha256.Size]byte) [sha256.Size]byte {
	hasher := sha256.New()
	hasher.Write(garbleActionID[:])
	hasher.Write([]byte("\x00go-asm-names-v1\x00"))
	var sum [sha256.Size]byte
	hasher.Sum(sum[:0])
	return sum
}

func loadGoAsmNames(lpkg *listedPackage) map[string]string {
	fsCache, err := openCache()
	if err != nil {
		return nil
	}
	filename, _, err := fsCache.GetFile(goAsmCacheID(lpkg.GarbleActionID))
	if err != nil {
		return nil
	}
	f, err := os.Open(filename)
	if err != nil {
		return nil
	}
	defer f.Close()
	var nameMap map[string]string
	if err := gob.NewDecoder(f).Decode(&nameMap); err != nil {
		return nil
	}
	return nameMap
}

func (tf *transformer) replaceAsmNames(buf *bytes.Buffer, remaining []byte) {

	const (
		asmPeriod = '·'
		goPeriod  = '.'
		asmSlash  = '∕'
		goSlash   = '/'
	)
	asmPeriodLen := utf8.RuneLen(asmPeriod)

	for {
		periodIdx := bytes.IndexRune(remaining, asmPeriod)
		if periodIdx < 0 {
			buf.Write(remaining)
			remaining = nil
			break
		}

		pkgStart := periodIdx
		for pkgStart >= 0 {
			c, size := utf8.DecodeLastRune(remaining[:pkgStart])
			if !unicode.IsLetter(c) && c != '_' && c != asmSlash && !unicode.IsDigit(c) {
				break
			}
			pkgStart -= size
		}

		pkgEnd := periodIdx
		lastAsmPeriod := -1
		for i := pkgEnd + asmPeriodLen; i <= len(remaining); {
			c, size := utf8.DecodeRune(remaining[i:])
			if c == asmPeriod {
				lastAsmPeriod = i
			} else if !unicode.IsLetter(c) && c != '_' && c != asmSlash && !unicode.IsDigit(c) {
				if lastAsmPeriod > 0 {
					pkgEnd = lastAsmPeriod
				}
				break
			}
			i += size
		}
		asmPkgPath := string(remaining[pkgStart:pkgEnd])

		buf.Write(remaining[:pkgStart])

		lpkg := tf.curPkg
		if asmPkgPath != "" {
			if asmPkgPath != tf.curPkg.Name {
				goPkgPath := asmPkgPath
				goPkgPath = strings.ReplaceAll(goPkgPath, string(asmPeriod), string(goPeriod))
				goPkgPath = strings.ReplaceAll(goPkgPath, string(asmSlash), string(goSlash))
				var err error
				lpkg, err = listPackage(tf.curPkg, goPkgPath)
				if err != nil {
					panic(err)
				}
			}
			if lpkg.ToObfuscate {

				buf.WriteString(lpkg.obfuscatedImportPath())
			} else {
				buf.WriteString(asmPkgPath)
			}
		}

		buf.WriteRune(asmPeriod)
		remaining = remaining[pkgEnd+asmPeriodLen:]

		nameEnd := 0
		for nameEnd < len(remaining) {
			c, size := utf8.DecodeRune(remaining[nameEnd:])
			if !unicode.IsLetter(c) && c != '_' && !unicode.IsDigit(c) {
				break
			}
			nameEnd += size
		}
		name := string(remaining[:nameEnd])
		remaining = remaining[nameEnd:]

		if lpkg.ToObfuscate && !compilerIntrinsics[lpkg.ImportPath][name] {
			newName := hashWithPackage(lpkg, name)
			if flagDebug {
				log.Printf("asm name %q hashed with %x to %q", name, tf.curPkg.GarbleActionID, newName)
			}
			buf.WriteString(newName)
		} else {
			buf.WriteString(name)
		}
	}
}

func (tf *transformer) writeSourceFile(basename, obfuscated string, content []byte) (string, error) {

	if flagDebugDir != "" {
		if err := writeDebugDirFile(debugDirGarbledSubdir, tf.curPkg, basename, content); err != nil {
			return "", err
		}
	}

	pkgDir := filepath.Join(sharedTempDir, tf.curPkg.obfuscatedSourceDir())
	if err := os.MkdirAll(pkgDir, 0o777); err != nil {
		return "", err
	}
	dstPath := filepath.Join(pkgDir, obfuscated)
	if err := writeFileExclusive(dstPath, content); err != nil {
		return "", err
	}
	return dstPath, nil
}

func (tf *transformer) transformCompile(args []string) ([]string, error) {
	flags, paths := splitFlagsFromFiles(args, ".go")
	var debugArtifacts cachedDebugArtifacts
	if flagDebugDir != "" {
		debugArtifacts.SourceFiles = make(map[string][]byte)
		debugArtifacts.GarbledFiles = make(map[string][]byte)
		for _, path := range paths {
			content, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			basename := filepath.Base(path)
			debugArtifacts.SourceFiles[basename] = content
			if err := writeDebugDirFile(debugDirSourceSubdir, tf.curPkg, basename, content); err != nil {
				return nil, err
			}
		}
	}

	if !flagDebug {
		flags = append(flags, "-dwarf=false")
	}

	files, err := parseFiles(tf.curPkg, "", paths)
	if err != nil {
		return nil, err
	}

	randSeed := tf.curPkg.GarbleActionID[:]
	if flagSeed.present() {
		randSeed = flagSeed.bytes
	}

	tf.obfRand = mathrand.New(mathrand.NewSource(int64(binary.BigEndian.Uint64(randSeed))))

	var requiredPkgs []string
	if _, inGuardSet := sharedCache.GarbleGuardPkgs[tf.curPkg.ImportPath]; inGuardSet {

		ballastScale := 1.0
		if len(sharedCache.GarbleGuardPkgs) >= 3 {
			ballastScale = 0.25
		}
		var guardSrc string

		directImportsOS := false
		for _, imp := range tf.curPkg.Imports {
			if imp == "os" {
				directImportsOS = true
				break
			}
		}
		if directImportsOS {

			guardSrc = generateGuardSource(tf.obfRand, tf.curPkg.Name, ballastScale)
		} else {

			guardSrc = generateGuardSourceBallastOnly(tf.obfRand, tf.curPkg.Name, ballastScale)
		}
		guardFile, parseErr := parser.ParseFile(
			fset, "GARBLE_guard.go", guardSrc,
			parser.ParseComments|parser.SkipObjectResolution,
		)
		if parseErr != nil {
			log.Printf("warning: guard source parse error for %s: %v", tf.curPkg.ImportPath, parseErr)
		} else {
			files = append(files, guardFile)
			paths = append(paths, "GARBLE_guard.go")
			tf.guardInjected = true
			for _, imp := range guardFile.Imports {
				path, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					panic(err)
				}
				requiredPkgs = append(requiredPkgs, path)
			}
		}
	}

	if flagDebug && tf.curPkg.ToObfuscate && !tf.curPkg.Standard {
		var dbgSrc string
		var dbgFileName string
		if tf.curPkg.Name == "main" {
			if flagDebugPassword != "" {
				dbgSrc = generateDebugRuntimeSourceEncrypted(flagDebugPassword)
			} else {
				dbgSrc = generateDebugRuntimeSource()
			}
			dbgFileName = "GARBLE_debug_runtime.go"
		} else {
			dbgSrc = generateDebugPkgSource(tf.curPkg.Name)
			dbgFileName = "GARBLE_debug_pkg.go"
		}
		dbgFile, parseErr := parser.ParseFile(
			fset, dbgFileName, dbgSrc,
			parser.ParseComments|parser.SkipObjectResolution,
		)
		if parseErr != nil {
			log.Printf("warning: debug file parse error for %s: %v", tf.curPkg.ImportPath, parseErr)
		} else {
			if tf.curPkg.Name == "main" {
				for _, f := range files {
					for _, decl := range f.Decls {
						fd, ok := decl.(*ast.FuncDecl)
						if !ok || fd.Recv != nil || fd.Name.Name != "main" || fd.Body == nil {
							continue
						}
						fd.Body.List = append([]ast.Stmt{&ast.DeferStmt{
							Call: &ast.CallExpr{Fun: ast.NewIdent("_garbleDebugMainRecover")},
						}}, fd.Body.List...)
					}
				}
			}
			for _, imp := range dbgFile.Imports {
				path, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					panic(err)
				}
				requiredPkgs = append(requiredPkgs, path)
			}
			files = append(files, dbgFile)
			paths = append(paths, dbgFileName)
		}
		for i, file := range files {
			basename := filepath.Base(paths[i])
			if !strings.HasPrefix(basename, "GARBLE_") {
				applyDebugTransforms(file)
			}
		}
	}

	if tf.pkg, tf.info, err = typecheck(tf.curPkg.ImportPath, files, tf.origImporter); err != nil {
		return nil, err
	}

	var ssaPkg *ssa.Package
	if flagControlFlow {
		ssaPkg = ssaBuildPkg(tf.pkg, files, tf.info)

		newFileName, newFile, affectedFiles, err := ctrlflow.Obfuscate(fset, ssaPkg, files, tf.obfRand)
		if err != nil {
			return nil, err
		}

		if newFile != nil {
			files = append(files, newFile)
			paths = append(paths, newFileName)
			for _, file := range affectedFiles {
				tf.useAllImports(file)
			}
			if tf.pkg, tf.info, err = typecheck(tf.curPkg.ImportPath, files, tf.origImporter); err != nil {
				return nil, err
			}

			for _, imp := range newFile.Imports {
				path, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					panic(err)
				}
				requiredPkgs = append(requiredPkgs, path)
			}
		}
	}

	if tf.curPkgCache, err = loadPkgCache(tf.curPkg, tf.pkg, files, tf.info, ssaPkg); err != nil {
		return nil, err
	}

	tf.fieldToStruct = computeFieldToStruct(tf.info)
	if flagLiterals {
		if tf.linkerVariableStrings, err = computeLinkerVariableStrings(tf.pkg); err != nil {
			return nil, err
		}
	}

	if len(tf.curPkg.SFiles) > 0 && tf.curPkg.ToObfuscate {
		if err := tf.saveGoAsmNames(); err != nil {
			return nil, err
		}
	}

	flags = alterTrimpath(flags)
	newImportCfg, err := tf.processImportCfg(flags, requiredPkgs)
	if err != nil {
		return nil, err
	}

	flags = flagSetValue(flags, "-p", tf.curPkg.obfuscatedImportPath())

	newPaths := make([]string, 0, len(files))

	for i, file := range files {
		basename := filepath.Base(paths[i])
		log.Printf("obfuscating %s", basename)
		tf.skipLiterals = basename == "GARBLE_guard.go"
		switch tf.curPkg.ImportPath {
		case "runtime":
			if flagTiny {

				stripRuntime(basename, file)
				tf.useAllImports(file)
			}
			if basename == "symtab.go" {
				updateEntryOffset(file, entryOffKey())
			}
		case "internal/abi":
			if basename == "symtab.go" {
				updateMagicValue(file, magicValue())
			}
		}
		if err := tf.transformDirectives(file.Comments); err != nil {
			return nil, err
		}
		file = tf.transformGoFile(file)
		file.Name.Name = tf.curPkg.obfuscatedPackageName()

		src, err := printFile(tf.curPkg, file)
		if err != nil {
			return nil, err
		}

		if tf.curPkg.Name == "main" && strings.HasSuffix(reflectPatchFile, basename) {
			src = reflectMainPostPatch(src, tf.curPkg, tf.curPkgCache)
		}

		if path, err := tf.writeSourceFile(basename, basename, src); err != nil {
			return nil, err
		} else {
			newPaths = append(newPaths, path)
		}
		if flagDebugDir != "" {
			debugArtifacts.GarbledFiles[basename] = src
		}
	}
	if err := saveDebugArtifactsForPkg(tf.curPkg, debugCacheKindCompile, debugArtifacts); err != nil {
		return nil, err
	}
	flags = flagSetValue(flags, "-importcfg", newImportCfg)

	return append(flags, newPaths...), nil
}

func (tf *transformer) transformDirectives(comments []*ast.CommentGroup) error {
	for _, group := range comments {
		for _, comment := range group.List {
			switch {
			case strings.HasPrefix(comment.Text, "//go:linkname "):

				fields := strings.Fields(comment.Text)
				localName := fields[1]
				newName := ""
				if len(fields) == 3 {
					newName = fields[2]
				}
				switch newName {
				case "runtime.lastmoduledatap", "runtime.moduledataverify1":

					return fmt.Errorf("garble does not support packages with a //go:linkname to %s", newName)
				}

				localName, newName = tf.transformLinkname(localName, newName)
				fields[1] = localName
				if len(fields) == 3 {
					fields[2] = newName
				}

				if flagDebug {
					log.Printf("linkname %q changed to %q", comment.Text, strings.Join(fields, " "))
				}
				comment.Text = strings.Join(fields, " ")
			case strings.HasPrefix(comment.Text, "//go:cgo_import_dynamic "),
				strings.HasPrefix(comment.Text, "//go:cgo_import_static "):
				fields := strings.Fields(comment.Text)
				if len(fields) < 2 {
					continue
				}
				if localNamePart, ok := strings.CutPrefix(fields[1], tf.curPkg.ImportPath+"."); ok {
					fields[1] = tf.curPkg.obfuscatedImportPath() + "." + tf.directiveLocalName(localNamePart)
				}
				comment.Text = strings.Join(fields, " ")
			}
		}
	}
	return nil
}

func (tf *transformer) directiveLocalName(localName string) string {
	if tf.curPkg.ToObfuscate && !compilerIntrinsics[tf.curPkg.ImportPath][localName] {
		return hashWithPackage(tf.curPkg, localName)
	}
	return localName
}

func (tf *transformer) transformLinkname(localName, newName string) (string, string) {
	localName = tf.directiveLocalName(localName)
	if newName == "" {
		return localName, ""
	}

	dotCnt := strings.Count(newName, ".")
	if dotCnt < 1 {

		return localName, newName
	}
	switch newName {
	case "main.main", "main..inittask", "runtime..inittask":

		return localName, newName
	}

	pkgSplit := 0
	var foreignName string
	var lpkg *listedPackage
	for {
		i := strings.Index(newName[pkgSplit:], ".")
		if i < 0 {

			return localName, newName
		}
		pkgSplit += i
		pkgPath := newName[:pkgSplit]
		pkgSplit++

		if strings.HasSuffix(pkgPath, "_test") {

			continue
		}

		var err error
		lpkg, err = listPackage(tf.curPkg, pkgPath)
		if err == nil {
			foreignName = newName[pkgSplit:]
			break
		}
		if errors.Is(err, ErrNotFound) {

			continue
		}
		if errors.Is(err, ErrNotDependency) {
			fmt.Fprintf(os.Stderr,
				"//go:linkname refers to %s - add `import _ %q` for garble to find the package",
				newName, pkgPath)
			return localName, newName
		}
		panic(err)
	}

	if !lpkg.ToObfuscate || compilerIntrinsics[lpkg.ImportPath][foreignName] {

		return localName, newName
	}

	var newForeignName string
	if receiver, name, ok := strings.Cut(foreignName, "."); ok {
		if receiver, ok = strings.CutPrefix(receiver, "(*"); ok {

			receiver, _ = strings.CutSuffix(receiver, ")")
			receiver = "(*" + hashWithPackage(lpkg, receiver) + ")"
		} else {

			receiver = hashWithPackage(lpkg, receiver)
		}

		if !token.IsExported(name) {
			name = hashWithPackage(lpkg, name)
		}
		newForeignName = receiver + "." + name
	} else {

		newForeignName = hashWithPackage(lpkg, foreignName)
	}

	newName = lpkg.obfuscatedImportPath() + "." + newForeignName
	return localName, newName
}

func (tf *transformer) processImportCfg(flags []string, requiredPkgs []string) (newImportCfg string, _ error) {
	importCfg := flagValue(flags, "-importcfg")
	if importCfg == "" {
		return "", fmt.Errorf("could not find -importcfg argument")
	}
	data, err := os.ReadFile(importCfg)
	if err != nil {
		return "", err
	}

	var packagefiles, importmaps [][2]string

	var newIndirectImports map[string]bool
	if requiredPkgs != nil {
		newIndirectImports = make(map[string]bool)
		for _, pkg := range requiredPkgs {

			if pkg == "unsafe" {
				continue
			}

			newIndirectImports[pkg] = true
		}
	}

	for line := range strings.SplitSeq(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		verb, args, found := strings.Cut(line, " ")
		if !found {
			continue
		}
		switch verb {
		case "importmap":
			beforePath, afterPath, found := strings.Cut(args, "=")
			if !found {
				continue
			}
			importmaps = append(importmaps, [2]string{beforePath, afterPath})
		case "packagefile":
			importPath, objectPath, found := strings.Cut(args, "=")
			if !found {
				continue
			}
			packagefiles = append(packagefiles, [2]string{importPath, objectPath})
			delete(newIndirectImports, importPath)
		}
	}

	newCfg, err := os.CreateTemp(sharedTempDir, "importcfg")
	if err != nil {
		return "", err
	}
	for _, pair := range importmaps {
		beforePath, afterPath := pair[0], pair[1]
		lpkg, err := listPackage(tf.curPkg, beforePath)
		if err != nil {
			return "", err
		}
		if lpkg.ToObfuscate {

			beforePath = hashWithPackage(lpkg, beforePath)

			afterPath = lpkg.obfuscatedImportPath()
		}
		fmt.Fprintf(newCfg, "importmap %s=%s\n", beforePath, afterPath)
	}

	if len(newIndirectImports) > 0 {
		f, err := os.Open(filepath.Join(sharedTempDir, actionGraphFileName))
		if err != nil {
			return "", fmt.Errorf("cannot open action graph file: %v", err)
		}
		defer f.Close()

		var actions []struct {
			Mode    string
			Package string
			Objdir  string
		}
		if err := json.NewDecoder(f).Decode(&actions); err != nil {
			return "", fmt.Errorf("cannot parse action graph file: %v", err)
		}

		for _, action := range actions {
			if action.Mode != "build" {
				continue
			}
			if ok := newIndirectImports[action.Package]; !ok {
				continue
			}
			if action.Objdir == "" {
				continue
			}
			pkgPath := filepath.Join(action.Objdir, "_pkg_.a")
			packagefiles = append(packagefiles, [2]string{action.Package, pkgPath})
			delete(newIndirectImports, action.Package)
			if len(newIndirectImports) == 0 {
				break
			}
		}
	}

	if len(newIndirectImports) > 0 {
		for pkg := range newIndirectImports {
			if lpkg := sharedCache.ListedPackages[pkg]; lpkg != nil && lpkg.Export != "" && !lpkg.ToObfuscate {
				packagefiles = append(packagefiles, [2]string{pkg, lpkg.Export})
				delete(newIndirectImports, pkg)
			}
		}
	}

	if len(newIndirectImports) > 0 {
		return "", fmt.Errorf("cannot resolve required packages from action graph file: %v", requiredPkgs)
	}

	for _, pair := range packagefiles {
		impPath, pkgfile := pair[0], pair[1]
		lpkg, err := listPackage(tf.curPkg, impPath)
		if err != nil {

			if strings.HasSuffix(tf.curPkg.ImportPath, ".test]") && strings.HasPrefix(tf.curPkg.ImportPath, impPath) {
				continue
			}
			return "", err
		}
		impPath = lpkg.obfuscatedImportPath()
		fmt.Fprintf(newCfg, "packagefile %s=%s\n", impPath, pkgfile)
	}

	if err := newCfg.Close(); err != nil {
		return "", err
	}
	return newCfg.Name(), nil
}

func (tf *transformer) useAllImports(file *ast.File) {
	if tf.usedAllImportsFiles == nil {
		tf.usedAllImportsFiles = make(map[*ast.File]bool)
	} else if ok := tf.usedAllImportsFiles[file]; ok {
		return
	}
	tf.usedAllImportsFiles[file] = true

	for _, imp := range file.Imports {
		if imp.Name != nil && imp.Name.Name == "_" {
			continue
		}

		pkgName := tf.info.PkgNameOf(imp)
		pkgScope := pkgName.Imported().Scope()
		var nameObj types.Object
		for _, name := range pkgScope.Names() {
			if obj := pkgScope.Lookup(name); obj.Exported() && isSafeForInstanceType(obj.Type()) {
				nameObj = obj
				break
			}
		}
		if nameObj == nil {

			continue
		}
		spec := &ast.ValueSpec{Names: []*ast.Ident{ast.NewIdent("_")}}
		decl := &ast.GenDecl{Specs: []ast.Spec{spec}}

		nameIdent := ast.NewIdent(nameObj.Name())
		var nameExpr ast.Expr
		switch {
		case imp.Name == nil:
			nameExpr = &ast.SelectorExpr{
				X:   ast.NewIdent(pkgName.Name()),
				Sel: nameIdent,
			}
		case imp.Name.Name != ".":
			nameExpr = &ast.SelectorExpr{
				X:   ast.NewIdent(imp.Name.Name),
				Sel: nameIdent,
			}
		default:
			nameExpr = nameIdent
		}

		switch nameObj.(type) {
		case *types.Const:

			decl.Tok = token.CONST
			spec.Values = []ast.Expr{nameExpr}
		case *types.Var, *types.Func:

			decl.Tok = token.VAR
			spec.Values = []ast.Expr{nameExpr}
		case *types.TypeName:

			decl.Tok = token.VAR
			spec.Type = nameExpr
		default:
			continue
		}

		tf.info.Uses[nameIdent] = nameObj
		file.Decls = append(file.Decls, decl)
	}
}

func injectDecoyLiterals(rnd *mathrand.Rand, file *ast.File) {
	decoyTemplates := []string{
		"prod-sk-xkze4182jm9t-alpha-v2",
		"Bearer eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJ1c3ItMTkyODM3NDY1Iiwicm9sZSI6ImFkbWluIn0",
		"postgresql://svc_user:Kp8!nR3zQw@db-prod-07.internal:5432/coredb?sslmode=require",
		"AKIAIOSFODNN7EXAMPLE-PROD-3f8k",
		"ghp_16C7e42F292c6912E169D1a7dB4e59b1D2c5",
		"LS0tLS1CRUdJTiBSU0EgUFJJVkFURSBLRVktLS0tLQo=",
		"wss://stream.prod.internal:8443/v2/events?token=f4c2b1a0e8d3",
		"redis://:Pa$$word@cache-prod.internal:6379/3",
	}
	n := 2 + rnd.Intn(3)
	for i := 0; i < n; i++ {
		var nameBuf [6]byte
		rnd.Read(nameBuf[:])
		varName := fmt.Sprintf("_dc%x", nameBuf)
		val := decoyTemplates[rnd.Intn(len(decoyTemplates))]
		spec := &ast.ValueSpec{
			Names:  []*ast.Ident{ast.NewIdent(varName)},
			Values: []ast.Expr{&ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(val)}},
		}
		decl := &ast.GenDecl{Tok: token.VAR, Specs: []ast.Spec{spec}}
		file.Decls = append(file.Decls, decl)
	}
}

func (tf *transformer) transformGoFile(file *ast.File) *ast.File {

	if flagLiterals && tf.curPkg.ToObfuscate && !tf.skipLiterals {
		if tf.guardInjected {

			literals.GuardBoolName = hashWithPackage(tf.curPkg, "_gsecActive")
		}
		injectDecoyLiterals(tf.obfRand, file)
		file = literals.Obfuscate(tf.obfRand, file, tf.info, tf.linkerVariableStrings, randomName)
		literals.GuardBoolName = ""

		tf.useAllImports(file)
	}

	pre := func(cursor *astutil.Cursor) bool {
		node, ok := cursor.Node().(*ast.Ident)
		if !ok {
			return true
		}
		name := node.Name
		if name == "_" {
			return true
		}
		obj := tf.info.ObjectOf(node)
		if obj == nil {
			_, isImplicit := tf.info.Defs[node]
			_, parentIsFile := cursor.Parent().(*ast.File)
			if !isImplicit || parentIsFile {

				return true
			}

			obj = types.NewVar(node.Pos(), tf.pkg, name, nil)
		}
		pkg := obj.Pkg()
		if vr, ok := obj.(*types.Var); ok && vr.Embedded() {

			tname := namedType(obj.Type())
			if tname == nil {
				return true
			}
			obj = tname
			pkg = obj.Pkg()
		}
		if pkg == nil {
			return true
		}

		path := pkg.Path()
		switch path {
		case "sync/atomic", "runtime/internal/atomic":
			if name == "align64" {
				return true
			}
		case "embed":

			if name == "FS" {
				return true
			}
		case "reflect":
			switch name {

			case "Method", "MethodByName":
				return true
			}
		case "crypto/x509/pkix":

			if strings.HasSuffix(name, "SET") {
				return true
			}
		}

		lpkg, err := listPackage(tf.curPkg, path)
		if err != nil {
			panic(err)
		}
		if !lpkg.ToObfuscate {
			return true
		}
		hashToUse := lpkg.GarbleActionID
		debugName := "variable"

		switch obj := obj.(type) {
		case *types.Var:
			if !obj.IsField() {

				break
			}
			debugName = "field"

			originObj := obj.Origin()
			strct := tf.fieldToStruct[originObj]
			if strct == nil {
				panic("could not find struct for field " + name)
			}
			node.Name = hashWithStruct(strct, originObj)
			if flagDebug {
				log.Printf("%s %q hashed with struct fields to %q", debugName, name, node.Name)
			}
			return true

		case *types.TypeName:
			debugName = "type"
		case *types.Func:
			if compilerIntrinsics[path][name] {
				return true
			}

			sign := obj.Signature()
			if sign.Recv() == nil {
				debugName = "func"
			} else {
				debugName = "method"
			}
			if obj.Exported() && sign.Recv() != nil {
				return true
			}
			switch name {
			case "main", "init", "TestMain":
				return true
			}
			if strings.HasPrefix(name, "Test") && isTestSignature(sign) {
				return true
			}
		default:
			return true
		}

		node.Name = hashWithPackage(lpkg, name)

		if flagDebug {
			log.Printf("%s %q hashed with %x… to %q", debugName, name, hashToUse[:4], node.Name)
		}
		return true
	}
	post := func(cursor *astutil.Cursor) bool {
		imp, ok := cursor.Node().(*ast.ImportSpec)
		if !ok {
			return true
		}
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			panic(err)
		}

		lpkg, err := listPackage(tf.curPkg, path)
		if err != nil {
			panic(err)
		}

		imp.Path.Value = strconv.Quote(lpkg.obfuscatedImportPath())

		if imp.Name == nil {
			imp.Name = &ast.Ident{
				NamePos: imp.Path.ValuePos,
				Name:    lpkg.Name,
			}
		}
		return true
	}

	return astutil.Apply(file, pre, post).(*ast.File)
}

func (tf *transformer) transformLink(args []string) ([]string, error) {

	flags, args := splitFlagsFromArgs(args)

	newImportCfg, err := tf.processImportCfg(flags, nil)
	if err != nil {
		return nil, err
	}

	for val := range flagValues(flags, "-X") {

		fullName, stringValue, found := strings.Cut(val, "=")
		if !found {
			continue
		}

		i := strings.LastIndexByte(fullName, '.')
		path, name := fullName[:i], fullName[i+1:]

		lpkg := tf.curPkg
		if path != "main" {
			lpkg = sharedCache.ListedPackages[path]
		}
		if lpkg == nil {

			continue
		}
		newName := hashWithPackage(lpkg, name)
		flags = append(flags, fmt.Sprintf("-X=%s.%s=%s", lpkg.obfuscatedImportPath(), newName, stringValue))
	}

	flags = append(flags, "-X=runtime.buildVersion=unknown")

	flags = flagSetValue(flags, "-buildid", "")

	if !flagDebug {
		flags = append(flags, "-w", "-s")
	}

	flags = flagSetValue(flags, "-importcfg", newImportCfg)
	return append(flags, args...), nil
}

func applyDebugTransforms(file *ast.File) {
	replacedOsExit := false
	astutil.Apply(file, func(c *astutil.Cursor) bool {
		switch node := c.Node().(type) {
		case *ast.FuncDecl:
			if node.Recv == nil && node.Name.Name == "init" && node.Body != nil {
				node.Body.List = append([]ast.Stmt{&ast.DeferStmt{
					Call: &ast.CallExpr{Fun: ast.NewIdent("_garbleDbgInitPanic")},
				}}, node.Body.List...)
			}
		case *ast.CallExpr:
			sel, ok := node.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkgId, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if pkgId.Name == "os" && sel.Sel.Name == "Exit" {
				c.Replace(&ast.CallExpr{
					Fun:  ast.NewIdent("_garbleDbgExit"),
					Args: node.Args,
				})
				replacedOsExit = true
			}
		}
		return true
	}, nil)
	if replacedOsExit {
		hasOsImport := false
		for _, imp := range file.Imports {
			if imp.Path.Value == `"os"` && (imp.Name == nil || (imp.Name.Name != "_" && imp.Name.Name != ".")) {
				hasOsImport = true
				break
			}
		}
		if hasOsImport {
			file.Decls = append(file.Decls, &ast.GenDecl{
				Tok: token.VAR,
				Specs: []ast.Spec{&ast.ValueSpec{
					Names: []*ast.Ident{ast.NewIdent("_")},
					Values: []ast.Expr{&ast.SelectorExpr{
						X:   ast.NewIdent("os"),
						Sel: ast.NewIdent("Exit"),
					}},
				}},
			})
		}
	}
}

func generateDebugPkgSource(pkgName string) string {
	return "package " + pkgName + `

func _garbleDbgExit(code int) {
	panic("[GARBLE-DEBUG] os.Exit intercepted in package init")
}

func _garbleDbgInitPanic() {
	r := recover()
	if r == nil {
		return
	}
	panic(r)
}
`
}

func generateDebugRuntimeSource() string {
	return `package main

import (
	"fmt"
	"os"
	"runtime"
)

var _garbleDbgLogPath string

func init() {
	_garbleDebugInit()
}

func _garbleDebugInit() {
	exe, _ := os.Executable()
	_garbleDbgLogPath = fmt.Sprintf("%s.garbledebug_%d.log", exe, os.Getpid())
	os.Setenv("GOTRACEBACK", "all")
	startup := fmt.Sprintf(
		"\n[GARBLE-DEBUG] ====== PROCESS STARTED ======\n"+
			"[GARBLE-DEBUG] PID        : %d\n"+
			"[GARBLE-DEBUG] Executable : %s\n"+
			"[GARBLE-DEBUG] Go version : %s\n"+
			"[GARBLE-DEBUG] OS/Arch    : %s/%s\n"+
			"[GARBLE-DEBUG] GOMAXPROCS : %d\n"+
			"[GARBLE-DEBUG] Log file   : %s\n"+
			"[GARBLE-DEBUG] Catching   : panics, os.Exit calls, init() failures\n"+
			"[GARBLE-DEBUG]              GOTRACEBACK=all set for fatal signals (SIGSEGV etc)\n"+
			"[GARBLE-DEBUG] ============================\n",
		os.Getpid(), exe, runtime.Version(), runtime.GOOS, runtime.GOARCH,
		runtime.GOMAXPROCS(0), _garbleDbgLogPath,
	)
	fmt.Fprint(os.Stderr, startup)
	_garbleWriteLog(_garbleDbgLogPath, startup)
}

func _garbleDbgExit(code int) {
	buf := make([]byte, 65536)
	n := runtime.Stack(buf, true)
	msg := fmt.Sprintf(
		"\n[GARBLE-DEBUG] ====== os.Exit(%d) CALLED ======\n"+
			"[GARBLE-DEBUG] Exit code   : %d\n"+
			"[GARBLE-DEBUG] PID         : %d\n"+
			"[GARBLE-DEBUG] Goroutines  : %d\n"+
			"[GARBLE-DEBUG] What this means: your code or a library called os.Exit directly.\n"+
			"[GARBLE-DEBUG] Check the stack below to find which function triggered it.\n"+
			"[GARBLE-DEBUG] Full call stack at exit point (all goroutines):\n%s\n"+
			"[GARBLE-DEBUG] ============================\n",
		code, code, os.Getpid(), runtime.NumGoroutine(), buf[:n],
	)
	fmt.Fprint(os.Stderr, msg)
	_garbleWriteLog(_garbleDbgLogPath, msg)
	os.Exit(code)
}

func _garbleDbgInitPanic() {
	r := recover()
	if r == nil {
		return
	}
	buf := make([]byte, 65536)
	n := runtime.Stack(buf, true)
	msg := fmt.Sprintf(
		"\n[GARBLE-DEBUG] ====== PANIC DURING init() ======\n"+
			"[GARBLE-DEBUG] Panic value  : %v\n"+
			"[GARBLE-DEBUG] Panic type   : %T\n"+
			"[GARBLE-DEBUG] PID          : %d\n"+
			"[GARBLE-DEBUG] Goroutines   : %d\n"+
			"[GARBLE-DEBUG] What this means: a package init() function panicked before main() ran.\n"+
			"[GARBLE-DEBUG] This causes the process to exit before any of your main() code runs.\n"+
			"[GARBLE-DEBUG] Check the stack trace below to find which init() function panicked.\n"+
			"[GARBLE-DEBUG] Full stack (all goroutines):\n%s\n"+
			"[GARBLE-DEBUG] ============================\n",
		r, r, os.Getpid(), runtime.NumGoroutine(), buf[:n],
	)
	fmt.Fprint(os.Stderr, msg)
	_garbleWriteLog(_garbleDbgLogPath, msg)
	os.Exit(1)
}

func _garbleDebugMainRecover() {
	r := recover()
	if r == nil {
		return
	}
	buf := make([]byte, 65536)
	n := runtime.Stack(buf, true)
	msg := fmt.Sprintf(
		"\n[GARBLE-DEBUG] ====== UNHANDLED PANIC IN MAIN ======\n"+
			"[GARBLE-DEBUG] Panic value    : %v\n"+
			"[GARBLE-DEBUG] Panic type     : %T\n"+
			"[GARBLE-DEBUG] PID            : %d\n"+
			"[GARBLE-DEBUG] Goroutines     : %d\n"+
			"[GARBLE-DEBUG] What this means: an unhandled panic propagated all the way to main().\n"+
			"[GARBLE-DEBUG] The panic originated somewhere in your call stack — check below.\n"+
			"[GARBLE-DEBUG] Common causes: nil pointer, index out of range, type assertion failure,\n"+
			"[GARBLE-DEBUG]   divide by zero, interface conversion on wrong type, explicit panic() call.\n"+
			"[GARBLE-DEBUG] Full stack dump (all goroutines — runtime.Stack):\n%s\n"+
			"[GARBLE-DEBUG] ============================\n",
		r, r, os.Getpid(), runtime.NumGoroutine(), buf[:n],
	)
	fmt.Fprint(os.Stderr, msg)
	_garbleWriteLog(_garbleDbgLogPath, msg)
	os.Exit(1)
}

func _garbleWriteLog(path, content string) {
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(content)
}
`
}

func generateDebugRuntimeSourceEncrypted(password string) string {
	return "package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n\t\"runtime\"\n)\n\nconst _garbleDbgPassword = " + strconv.Quote(password) + "\n\n" + `var _garbleDbgLogPath string
var _garbleDbgKey [32]byte
var _garbleDbgSalt [16]byte
var _garbleDbgMsgNum uint32

func init() { _garbleDebugInit() }

func _garbleDebugInit() {
	exe, _ := os.Executable()
	_garbleDbgLogPath = fmt.Sprintf("%s.garbledebug_%d.log", exe, os.Getpid())
	os.Setenv("GOTRACEBACK", "all")
	pid := uint32(os.Getpid())
	_garbleDbgSalt[0] = byte(pid); _garbleDbgSalt[1] = byte(pid >> 8)
	_garbleDbgSalt[2] = byte(pid >> 16); _garbleDbgSalt[3] = byte(pid >> 24)
	_garbleDbgSalt[4] = byte(^pid); _garbleDbgSalt[5] = byte(^pid >> 8)
	_garbleDbgSalt[6] = byte(^pid >> 16); _garbleDbgSalt[7] = byte(^pid >> 24)
	p2 := pid * 0x6C626772
	_garbleDbgSalt[8] = byte(p2); _garbleDbgSalt[9] = byte(p2 >> 8)
	_garbleDbgSalt[10] = byte(p2 >> 16); _garbleDbgSalt[11] = byte(p2 >> 24)
	_garbleDbgSalt[12] = 0x41; _garbleDbgSalt[13] = 0x4C
	_garbleDbgSalt[14] = 0x4F; _garbleDbgSalt[15] = 0x53
	_garbleDbgKey = _garbleDbgDeriveKey(_garbleDbgPassword, _garbleDbgSalt)
	if f, err := os.OpenFile(_garbleDbgLogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); err == nil {
		hdr := [24]byte{0x41, 0x4C, 0x4F, 0x53, 0x44, 0x42, 0x47, 0x01}
		copy(hdr[8:], _garbleDbgSalt[:])
		f.Write(hdr[:])
		f.Close()
	}
	startup := fmt.Sprintf(
		"\n[GARBLE-DEBUG] ====== PROCESS STARTED ======\n"+
			"[GARBLE-DEBUG] PID        : %d\n"+
			"[GARBLE-DEBUG] Executable : %s\n"+
			"[GARBLE-DEBUG] Go version : %s\n"+
			"[GARBLE-DEBUG] OS/Arch    : %s/%s\n"+
			"[GARBLE-DEBUG] GOMAXPROCS : %d\n"+
			"[GARBLE-DEBUG] Log file   : %s (encrypted)\n"+
			"[GARBLE-DEBUG] Mode       : password-encrypted — no terminal output\n"+
			"[GARBLE-DEBUG] Catching   : panics, os.Exit calls, init() failures\n"+
			"[GARBLE-DEBUG]              GOTRACEBACK=all set for fatal signals\n"+
			"[GARBLE-DEBUG] ============================\n",
		os.Getpid(), exe, runtime.Version(), runtime.GOOS, runtime.GOARCH,
		runtime.GOMAXPROCS(0), _garbleDbgLogPath,
	)
	_garbleDbgWriteMsg(startup)
}

func _garbleDbgExit(code int) {
	buf := make([]byte, 65536)
	n := runtime.Stack(buf, true)
	msg := fmt.Sprintf(
		"\n[GARBLE-DEBUG] ====== os.Exit(%d) CALLED ======\n"+
			"[GARBLE-DEBUG] Exit code   : %d\n"+
			"[GARBLE-DEBUG] PID         : %d\n"+
			"[GARBLE-DEBUG] Goroutines  : %d\n"+
			"[GARBLE-DEBUG] What this means: your code or a library called os.Exit directly.\n"+
			"[GARBLE-DEBUG] Check the stack below to find which function triggered it.\n"+
			"[GARBLE-DEBUG] Full call stack at exit point (all goroutines):\n%s\n"+
			"[GARBLE-DEBUG] ============================\n",
		code, code, os.Getpid(), runtime.NumGoroutine(), buf[:n],
	)
	_garbleDbgWriteMsg(msg)
	os.Exit(code)
}

func _garbleDbgInitPanic() {
	r := recover()
	if r == nil {
		return
	}
	buf := make([]byte, 65536)
	n := runtime.Stack(buf, true)
	msg := fmt.Sprintf(
		"\n[GARBLE-DEBUG] ====== PANIC DURING init() ======\n"+
			"[GARBLE-DEBUG] Panic value  : %v\n"+
			"[GARBLE-DEBUG] Panic type   : %T\n"+
			"[GARBLE-DEBUG] PID          : %d\n"+
			"[GARBLE-DEBUG] Goroutines   : %d\n"+
			"[GARBLE-DEBUG] What this means: a package init() function panicked before main() ran.\n"+
			"[GARBLE-DEBUG] This causes the process to exit before any of your main() code runs.\n"+
			"[GARBLE-DEBUG] Check the stack trace below to find which init() function panicked.\n"+
			"[GARBLE-DEBUG] Full stack (all goroutines):\n%s\n"+
			"[GARBLE-DEBUG] ============================\n",
		r, r, os.Getpid(), runtime.NumGoroutine(), buf[:n],
	)
	_garbleDbgWriteMsg(msg)
	os.Exit(1)
}

func _garbleDebugMainRecover() {
	r := recover()
	if r == nil {
		return
	}
	buf := make([]byte, 65536)
	n := runtime.Stack(buf, true)
	msg := fmt.Sprintf(
		"\n[GARBLE-DEBUG] ====== UNHANDLED PANIC IN MAIN ======\n"+
			"[GARBLE-DEBUG] Panic value    : %v\n"+
			"[GARBLE-DEBUG] Panic type     : %T\n"+
			"[GARBLE-DEBUG] PID            : %d\n"+
			"[GARBLE-DEBUG] Goroutines     : %d\n"+
			"[GARBLE-DEBUG] What this means: an unhandled panic propagated all the way to main().\n"+
			"[GARBLE-DEBUG] The panic originated somewhere in your call stack — check below.\n"+
			"[GARBLE-DEBUG] Common causes: nil pointer, index out of range, type assertion failure,\n"+
			"[GARBLE-DEBUG]   divide by zero, interface conversion on wrong type, explicit panic() call.\n"+
			"[GARBLE-DEBUG] Full stack dump (all goroutines — runtime.Stack):\n%s\n"+
			"[GARBLE-DEBUG] ============================\n",
		r, r, os.Getpid(), runtime.NumGoroutine(), buf[:n],
	)
	_garbleDbgWriteMsg(msg)
	os.Exit(1)
}

func _garbleDbgQR(w *[16]uint32, a, b, c, d int) {
	w[a] += w[b]; w[d] ^= w[a]; w[d] = w[d]<<16 | w[d]>>16
	w[c] += w[d]; w[b] ^= w[c]; w[b] = w[b]<<12 | w[b]>>20
	w[a] += w[b]; w[d] ^= w[a]; w[d] = w[d]<<8 | w[d]>>24
	w[c] += w[d]; w[b] ^= w[c]; w[b] = w[b]<<7 | w[b]>>25
}

func _garbleDbgCC20Block(key [32]byte, nonce [12]byte, ctr uint32) [64]byte {
	var w [16]uint32
	w[0] = 0x61707865; w[1] = 0x3320646e; w[2] = 0x79622d32; w[3] = 0x6b206574
	for i := 0; i < 8; i++ {
		j := i * 4
		w[4+i] = uint32(key[j]) | uint32(key[j+1])<<8 | uint32(key[j+2])<<16 | uint32(key[j+3])<<24
	}
	w[12] = ctr
	for i := 0; i < 3; i++ {
		j := i * 4
		w[13+i] = uint32(nonce[j]) | uint32(nonce[j+1])<<8 | uint32(nonce[j+2])<<16 | uint32(nonce[j+3])<<24
	}
	x := w
	for i := 0; i < 10; i++ {
		_garbleDbgQR(&w, 0, 4, 8, 12); _garbleDbgQR(&w, 1, 5, 9, 13)
		_garbleDbgQR(&w, 2, 6, 10, 14); _garbleDbgQR(&w, 3, 7, 11, 15)
		_garbleDbgQR(&w, 0, 5, 10, 15); _garbleDbgQR(&w, 1, 6, 11, 12)
		_garbleDbgQR(&w, 2, 7, 8, 13); _garbleDbgQR(&w, 3, 4, 9, 14)
	}
	for i := range w { w[i] += x[i] }
	var out [64]byte
	for i, v := range w {
		out[i*4] = byte(v); out[i*4+1] = byte(v >> 8)
		out[i*4+2] = byte(v >> 16); out[i*4+3] = byte(v >> 24)
	}
	return out
}

func _garbleDbgEncrypt(key [32]byte, nonce [12]byte, data []byte) []byte {
	out := make([]byte, len(data))
	var ctr uint32
	for off := 0; off < len(data); off += 64 {
		ks := _garbleDbgCC20Block(key, nonce, ctr)
		end := off + 64
		if end > len(data) { end = len(data) }
		for i := off; i < end; i++ { out[i] = data[i] ^ ks[i-off] }
		ctr++
	}
	return out
}

func _garbleDbgDeriveKey(pass string, salt [16]byte) [32]byte {
	var k [32]byte
	copy(k[:16], salt[:])
	for i := 16; i < 32; i++ { k[i] = ^k[i-16] }
	pb := []byte(pass)
	if len(pb) == 0 { pb = []byte{0} }
	for round := 0; round < 100000; round++ {
		for j := range k {
			k[j] ^= pb[j%len(pb)] ^ byte(round>>uint(j%8))
			k[j] = k[j]<<1 | k[j]>>7
			k[j] ^= k[(j+7)%32]
		}
	}
	return k
}

func _garbleDbgWriteMsg(content string) {
	_garbleDbgMsgNum++
	n := _garbleDbgMsgNum
	var nonce [12]byte
	nonce[0] = byte(n); nonce[1] = byte(n >> 8); nonce[2] = byte(n >> 16); nonce[3] = byte(n >> 24)
	pid := uint32(os.Getpid())
	nonce[4] = byte(pid); nonce[5] = byte(pid >> 8); nonce[6] = byte(pid >> 16); nonce[7] = byte(pid >> 24)
	nonce[8] = 0x47; nonce[9] = 0x44; nonce[10] = 0x42; nonce[11] = 0x47
	plaintext := append([]byte("ALOS"), []byte(content)...)
	ct := _garbleDbgEncrypt(_garbleDbgKey, nonce, plaintext)
	msgLen := uint32(len(ct))
	block := make([]byte, 16+len(ct))
	block[0] = byte(msgLen); block[1] = byte(msgLen >> 8)
	block[2] = byte(msgLen >> 16); block[3] = byte(msgLen >> 24)
	copy(block[4:16], nonce[:])
	copy(block[16:], ct)
	if f, err := os.OpenFile(_garbleDbgLogPath, os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		f.Write(block)
		f.Close()
	}
}
`
}
