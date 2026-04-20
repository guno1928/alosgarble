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

	flags = append(flags, "-dwarf=false")

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

			packagefiles = append(packagefiles, [2]string{action.Package, filepath.Join(action.Objdir, "_pkg_.a")})
			delete(newIndirectImports, action.Package)
			if len(newIndirectImports) == 0 {
				break
			}
		}

		if len(newIndirectImports) > 0 {
			return "", fmt.Errorf("cannot resolve required packages from action graph file: %v", requiredPkgs)
		}
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

	if flagLiterals && tf.curPkg.ToObfuscate {
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

	flags = append(flags, "-w", "-s")

	flags = flagSetValue(flags, "-importcfg", newImportCfg)
	return append(flags, args...), nil
}
