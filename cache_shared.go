package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/mod/module"
)

//go:generate go run scripts/gen_go_std_tables.go

type sharedCacheType struct {
	ForwardBuildFlags []string

	CacheDir string

	ListedPackages map[string]*listedPackage

	BinaryContentID []byte

	GOGARBLE string

	GoCmd string

	GoEnv struct {
		GOOS   string
		GOARCH string

		GOVERSION string
		GOROOT    string
	}

	MainModulePath string

	GarbleGuardPkgs map[string]bool
}

var sharedCache *sharedCacheType

func loadSharedCache() error {
	if sharedCache != nil {
		panic("shared cache loaded twice?")
	}
	startTime := time.Now()
	f, err := os.Open(filepath.Join(sharedTempDir, "main-cache.gob"))
	if err != nil {
		return fmt.Errorf(`cannot open shared file: %v\ndid you run "go [command] -toolexec=garble" instead of "garble [command]"?`, err)
	}
	defer func() {
		log.Printf("shared cache loaded in %s from %s", debugSince(startTime), f.Name())
	}()
	defer f.Close()
	if err := gob.NewDecoder(f).Decode(&sharedCache); err != nil {
		return fmt.Errorf("cannot decode shared file: %v", err)
	}
	return nil
}

func saveSharedCache() (string, error) {
	if sharedCache == nil {
		panic("saving a missing cache?")
	}
	dir, err := os.MkdirTemp("", "garble-shared")
	if err != nil {
		return "", err
	}

	cachePath := filepath.Join(dir, "main-cache.gob")
	if err := writeGobExclusive(cachePath, &sharedCache); err != nil {
		return "", err
	}
	return dir, nil
}

func createExclusive(name string) (*os.File, error) {
	return os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o666)
}

func writeFileExclusive(name string, data []byte) error {
	f, err := createExclusive(name)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err2 := f.Close(); err == nil {
		err = err2
	}
	return err
}

func writeGobExclusive(name string, val any) error {
	f, err := createExclusive(name)
	if err != nil {
		return err
	}

	err = gob.NewEncoder(f).Encode(val)
	if err2 := f.Close(); err == nil {
		err = err2
	}
	return err
}

type listedPackageModule struct {
	Path    string
	Version string
}

type listedPackage struct {
	Name       string
	ImportPath string
	ForTest    string
	Export     string
	BuildID    string
	ImportMap  map[string]string
	Standard   bool

	Dir             string
	CompiledGoFiles []string
	SFiles          []string
	Imports         []string

	Module *listedPackageModule

	Error *packageError

	allDeps map[string]struct{}

	GarbleActionID [sha256.Size]byte `json:"-"`

	ToObfuscate bool `json:"-"`
}

func (p *listedPackage) hasDep(path string) bool {
	if p.allDeps == nil {
		p.allDeps = make(map[string]struct{}, len(p.Imports)*2)
		p.addImportsFrom(p)
	}
	_, ok := p.allDeps[path]
	return ok
}

func (p *listedPackage) addImportsFrom(from *listedPackage) {
	for _, path := range from.Imports {
		if path == "C" {

			continue
		}
		if path2 := from.ImportMap[path]; path2 != "" {
			path = path2
		}
		if _, ok := p.allDeps[path]; ok {
			continue
		}
		p.allDeps[path] = struct{}{}
		p.addImportsFrom(sharedCache.ListedPackages[path])
	}
}

type packageError struct {
	Pos string
	Err string
}

func (p *listedPackage) obfuscatedPackageName() string {
	if p.Name == "main" || !p.ToObfuscate {
		return p.Name
	}

	return hashWithPackage(p, p.Name)
}

func (p *listedPackage) obfuscatedSourceDir() string {
	return hashWithPackage(p, p.ImportPath)
}

func (p *listedPackage) obfuscatedImportPath() string {
	if p.Name == "main" && p.ForTest == "" {
		return "main"
	}
	if !p.ToObfuscate {
		return p.ImportPath
	}

	switch p.ImportPath {
	case "runtime", "reflect", "embed",

		"internal/runtime/syscall/linux",
		"internal/runtime/syscall/windows",
		"internal/runtime/startlinetest":
		return p.ImportPath
	}

	if _, ok := compilerIntrinsics[p.ImportPath]; ok {
		return p.ImportPath
	}

	if _, ok := runtimeAndLinknamed[p.ImportPath]; ok {
		return p.ImportPath
	}

	newPath := hashWithPackage(p, p.ImportPath)
	log.Printf("import path %q hashed with %x to %q", p.ImportPath, p.GarbleActionID, newPath)
	return newPath
}

var garbleBuildFlags = []string{"-trimpath", "-buildvcs=false"}

func appendListedPackages(packages []string, mainBuild bool) error {
	startTime := time.Now()
	args := []string{
		"list",

		"-json", "-export", "-compiled", "-e",
	}
	if mainBuild {

		args = append(args, "-deps")
	}
	args = append(args, garbleBuildFlags...)
	args = append(args, sharedCache.ForwardBuildFlags...)

	if !mainBuild {

		args = slices.DeleteFunc(args, func(arg string) bool {
			return strings.HasPrefix(arg, "-mod=") || strings.HasPrefix(arg, "-modfile=")
		})
	}

	args = append(args, packages...)
	cmd := exec.Command(sharedCache.GoCmd, args...)

	defer func() {
		log.Printf("original build info obtained in %s via: go %s", debugSince(startTime), strings.Join(args, " "))
	}()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("go list error: %v", err)
	}

	dec := json.NewDecoder(stdout)
	if sharedCache.ListedPackages == nil {
		sharedCache.ListedPackages = make(map[string]*listedPackage)
	}
	var pkgErrors strings.Builder
	for dec.More() {
		var pkg listedPackage
		if err := dec.Decode(&pkg); err != nil {
			return err
		}

		if perr := pkg.Error; perr != nil {
			if !mainBuild && strings.Contains(perr.Err, "build constraints exclude all Go files") {

			} else if !mainBuild && strings.Contains(perr.Err, "is not in std") {

			} else {
				if pkgErrors.Len() > 0 {
					pkgErrors.WriteString("\n")
				}
				if perr.Pos != "" {
					pkgErrors.WriteString(perr.Pos)
					pkgErrors.WriteString(": ")
				}

				pkgErrors.WriteString(strings.TrimRight(perr.Err, "\n"))
			}
		}

		if sharedCache.ListedPackages[pkg.ImportPath] != nil {
			return fmt.Errorf("duplicate package: %q", pkg.ImportPath)
		}
		if pkg.BuildID != "" {
			actionID := decodeBuildIDHash(splitActionID(pkg.BuildID))
			pkg.GarbleActionID = addGarbleToHash(actionID)
		}

		sharedCache.ListedPackages[pkg.ImportPath] = &pkg
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("go list error: %v:\nargs: %q\n%s", err, args, stderr.Bytes())
	}
	if pkgErrors.Len() > 0 {
		return errors.New(pkgErrors.String())
	}

	anyToObfuscate := false
	for path, pkg := range sharedCache.ListedPackages {

		if pkg.ForTest != "" {
			path = pkg.ForTest
		}
		switch {

		case runtimeAndDeps[path],
			path == "runtime/cgo",

			path == "crypto/internal/fips140", strings.HasPrefix(path, "crypto/internal/fips140/"):

		case len(pkg.CompiledGoFiles) == 0:

		case pkg.Name == "main" && strings.HasSuffix(path, ".test"),
			path == "command-line-arguments",
			strings.HasPrefix(path, "plugin/unnamed"),
			module.MatchPrefixPatterns(sharedCache.GOGARBLE, path):

			pkg.ToObfuscate = true
			anyToObfuscate = true
			if len(pkg.GarbleActionID) == 0 {
				return fmt.Errorf("package %q to be obfuscated lacks build id?", pkg.ImportPath)
			}
		}
	}

	if !anyToObfuscate && !module.MatchPrefixPatterns(sharedCache.GOGARBLE, "runtime") {
		return fmt.Errorf("GOGARBLE=%q does not match any packages to be built", sharedCache.GOGARBLE)
	}

	if mainBuild {

		for _, pkg := range sharedCache.ListedPackages {
			if pkg.Name == "main" && pkg.ForTest == "" && pkg.Module != nil && pkg.Module.Path != "" {
				sharedCache.MainModulePath = pkg.Module.Path
				break
			}
		}
		sharedCache.GarbleGuardPkgs = make(map[string]bool)
		if sharedCache.MainModulePath != "" {

			for _, pkg := range sharedCache.ListedPackages {
				if !pkg.ToObfuscate || pkg.Standard || pkg.ForTest != "" {
					continue
				}
				if pkg.Module != nil && pkg.Module.Path == sharedCache.MainModulePath {
					sharedCache.GarbleGuardPkgs[pkg.ImportPath] = true
				}
			}

			for _, pkg := range sharedCache.ListedPackages {
				if pkg.Module == nil || pkg.Module.Path != sharedCache.MainModulePath {
					continue
				}
				for _, imp := range pkg.Imports {
					impPkg := sharedCache.ListedPackages[imp]
					if impPkg == nil || !impPkg.ToObfuscate || impPkg.Standard || impPkg.ForTest != "" {
						continue
					}
					sharedCache.GarbleGuardPkgs[imp] = true
				}
			}
		} else {

			for _, pkg := range sharedCache.ListedPackages {
				if pkg.Name == "main" && pkg.ForTest == "" && pkg.ToObfuscate {
					sharedCache.GarbleGuardPkgs[pkg.ImportPath] = true
				}
			}
		}
	}

	return nil
}

var listedRuntimeAndLinknamed = false

var ErrNotFound = errors.New("not found")

var ErrNotDependency = errors.New("not a dependency")

func listPackage(from *listedPackage, path string) (*listedPackage, error) {
	if path == from.ImportPath {
		return from, nil
	}

	if path2 := from.ImportMap[path]; path2 != "" {
		path = path2
	}

	pkg, ok := sharedCache.ListedPackages[path]

	if from.Standard {
		if ok {
			return pkg, nil
		}
		if listedRuntimeAndLinknamed {
			return nil, fmt.Errorf("package %q still missing after go list call", path)
		}
		startTime := time.Now()
		missing := make([]string, 0, len(runtimeAndLinknamed))
		for linknamed := range runtimeAndLinknamed {
			switch {
			case sharedCache.ListedPackages[linknamed] != nil:

			case sharedCache.GoEnv.GOOS != "js" && linknamed == "syscall/js":

			case sharedCache.GoEnv.GOOS != "darwin" && sharedCache.GoEnv.GOOS != "ios" && linknamed == "crypto/x509/internal/macos":

			default:
				missing = append(missing, linknamed)
			}
		}
		slices.Sort(missing)

		if err := appendListedPackages(missing, false); err != nil {
			return nil, fmt.Errorf("failed to load missing runtime-linknamed packages: %v", err)
		}
		pkg, ok := sharedCache.ListedPackages[path]
		if !ok {
			return nil, fmt.Errorf("std listed another std package that we can't find: %s", path)
		}
		listedRuntimeAndLinknamed = true
		log.Printf("listed %d missing runtime-linknamed packages in %s", len(missing), debugSince(startTime))
		return pkg, nil
	}
	if !ok {
		return nil, fmt.Errorf("list %s: %w", path, ErrNotFound)
	}

	if from.hasDep(pkg.ImportPath) {
		return pkg, nil
	}

	if pkg.ImportPath == "runtime" {
		return pkg, nil
	}
	if sharedCache.ListedPackages["runtime"].hasDep(pkg.ImportPath) {
		return pkg, nil
	}

	return nil, fmt.Errorf("list %s: %w", path, ErrNotDependency)
}
