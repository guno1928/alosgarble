package main

import (
	"bytes"
	"cmp"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/token"
	"go/version"
	"io"
	"io/fs"
	"iter"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"strconv"
	"strings"
	"time"

	"github.com/guno1928/alosgarble/internal/linker"
)

const actionGraphFileName = "action-graph.json"

var forwardBuildFlags = map[string]bool{

	"-a": false,
	"-n": false,
	"-x": false,
	"-v": false,

	"-trimpath": false,
	"-toolexec": false,
	"-buildvcs": false,

	"-C":             true,
	"-asan":          true,
	"-asmflags":      true,
	"-buildmode":     true,
	"-compiler":      true,
	"-cover":         true,
	"-covermode":     true,
	"-coverpkg":      true,
	"-gccgoflags":    true,
	"-gcflags":       true,
	"-installsuffix": true,
	"-ldflags":       true,
	"-linkshared":    true,
	"-mod":           true,
	"-modcacherw":    true,
	"-modfile":       true,
	"-msan":          true,
	"-overlay":       true,
	"-p":             true,
	"-pgo":           true,
	"-pkgdir":        true,
	"-race":          true,
	"-tags":          true,
	"-work":          true,
	"-workfile":      true,
}

var booleanFlags = map[string]bool{

	"-a":          true,
	"-asan":       true,
	"-buildvcs":   true,
	"-cover":      true,
	"-i":          true,
	"-linkshared": true,
	"-modcacherw": true,
	"-msan":       true,
	"-n":          true,
	"-race":       true,
	"-trimpath":   true,
	"-v":          true,
	"-work":       true,
	"-x":          true,

	"-benchmem": true,
	"-c":        true,
	"-failfast": true,
	"-fullpath": true,
	"-json":     true,
	"-short":    true,
}

var flagSet = flag.NewFlagSet("garble", flag.ExitOnError)
var rxGarbleFlag = regexp.MustCompile(`-(?:literals|tiny|debug|debugdir|seed)(?:$|=)`)

var (
	flagLiterals bool
	flagTiny     bool
	flagDebug    bool
	flagDebugDir string
	flagSeed     seedFlag

	flagControlFlow = os.Getenv("GARBLE_EXPERIMENTAL_CONTROLFLOW") != "0"

	fset = token.NewFileSet()

	sharedTempDir = os.Getenv("GARBLE_SHARED")
)

func init() {
	flagSet.Usage = usage
	flagSet.BoolVar(&flagLiterals, "literals", true, "Obfuscate literals such as strings (always on)")
	flagSet.BoolVar(&flagTiny, "tiny", true, "Optimize for binary size, losing some ability to reverse the process (always on)")

	flagLiterals = true
	flagTiny = true
	flagSet.BoolVar(&flagDebug, "debug", false, "Print debug logs to stderr")
	flagSet.StringVar(&flagDebugDir, "debugdir", "", "Write source and obfuscated trees to a directory, e.g. -debugdir=out")
	flagSet.Var(&flagSeed, "seed", "Provide a base64-encoded seed, e.g. -seed=o9WDTZ4CN4w\nFor a random seed, provide -seed=random")
}

func main() {
	if dir := os.Getenv("GARBLE_WRITE_CPUPROFILES"); dir != "" {
		f, err := os.CreateTemp(dir, "garble-cpu-*.pprof")
		if err != nil {
			panic(err)
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			panic(err)
		}
		defer func() {
			pprof.StopCPUProfile()
			if err := f.Close(); err != nil {
				panic(err)
			}
		}()
	}
	defer func() {
		if dir := os.Getenv("GARBLE_WRITE_MEMPROFILES"); dir != "" {
			f, err := os.CreateTemp(dir, "garble-mem-*.pprof")
			if err != nil {
				panic(err)
			}
			runtime.GC()
			if err := pprof.WriteHeapProfile(f); err != nil {
				panic(err)
			}
			if err := f.Close(); err != nil {
				panic(err)
			}
		}
		if os.Getenv("GARBLE_WRITE_ALLOCS") == "true" {
			var memStats runtime.MemStats
			runtime.ReadMemStats(&memStats)
			fmt.Fprintf(os.Stderr, "garble allocs: %d\n", memStats.Mallocs)
		}
	}()
	flagSet.Parse(os.Args[1:])

	flagLiterals = true
	flagTiny = true
	log.SetPrefix("[garble] ")
	log.SetFlags(0)
	if flagDebug {

		log.SetOutput(&uniqueLineWriter{out: os.Stderr})
	} else {
		log.SetOutput(io.Discard)
	}
	args := flagSet.Args()
	if len(args) < 1 {
		usage()
		os.Exit(2)
	}

	if flagSeed.random {
		fmt.Fprintf(os.Stderr, "-seed chosen at random: %s\n", base64.RawStdEncoding.EncodeToString(flagSeed.bytes))
	}
	if err := mainErr(args); err != nil {
		if code, ok := err.(errJustExit); ok {
			os.Exit(int(code))
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type errJustExit int

func (e errJustExit) Error() string { return fmt.Sprintf("exit: %d", e) }

func mainErr(args []string) error {
	command, args := args[0], args[1:]

	if command != "toolexec" && len(args) == 1 && args[0] == "-V=full" {
		return fmt.Errorf(`did you run "go [command] -toolexec=garble" instead of "garble [command]"?`)
	}

	switch command {
	case "help":
		if hasHelpFlag(args) || len(args) > 1 {
			fmt.Fprintf(os.Stderr, "usage: garble help [command]\n")
			return errJustExit(0)
		}
		if len(args) == 1 {
			return mainErr([]string{args[0], "-h"})
		}
		usage()
		return errJustExit(0)
	case "version":
		if hasHelpFlag(args) || len(args) > 0 {
			fmt.Fprintf(os.Stderr, "usage: garble version\n")
			return errJustExit(2)
		}
		info, ok := debug.ReadBuildInfo()
		if !ok {

			fmt.Println("unknown")
			return nil
		}
		mod := &info.Main
		if mod.Replace != nil {
			mod = mod.Replace
		}

		fmt.Printf("%s %s\n\n", mod.Path, mod.Version)
		fmt.Printf("Build settings:\n")
		for _, setting := range info.Settings {
			if setting.Value == "" {
				continue
			}

			fmt.Printf("%16s %s\n", setting.Key, setting.Value)
		}
		return nil
	case "reverse":
		return commandReverse(args)
	case "build", "test", "run":
		buildArgs := args
		cmd, err := toolexecCmd(command, args)
		defer func() {
			if err := os.RemoveAll(os.Getenv("GARBLE_SHARED")); err != nil {
				fmt.Fprintf(os.Stderr, "could not clean up GARBLE_SHARED: %v\n", err)
			}

			if sharedCache != nil {
				fsCache, err := openCache()
				if err == nil {
					err = fsCache.Trim()
				}
				if err != nil {
					fmt.Fprintf(os.Stderr, "could not trim GARBLE_CACHE: %v\n", err)
				}
			}
		}()
		if err != nil {
			return err
		}
		var pbTotal int
		for _, pkg := range sharedCache.ListedPackages {
			if len(pkg.CompiledGoFiles) > 0 {
				pbTotal++
			}
		}
		_ = os.WriteFile(filepath.Join(sharedTempDir, progressLogFile), nil, 0o666)
		pb := startProgressBar(sharedTempDir, pbTotal)

		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		log.Printf("calling via toolexec: %s", cmd)
		buildErr := cmd.Run()
		stopProgressBar(pb)
		if buildErr != nil {
			return buildErr
		}
		if command == "build" {
			if outPath := flagValue(buildArgs, "-o"); outPath != "" {
				if err := patchOutputBinary(outPath); err != nil {
					return fmt.Errorf("patching output binary: %v", err)
				}
			}
		}
		return restoreDebugDirFromCache()

	case "toolexec":
		_, tool := filepath.Split(args[0])
		if runtime.GOOS == "windows" {
			tool = strings.TrimSuffix(tool, ".exe")
		}
		transform := transformMethods[tool]
		transformed := args[1:]
		if transform != nil {
			startTime := time.Now()
			log.Printf("transforming %s with args: %s", tool, strings.Join(transformed, " "))

			if err := loadSharedCache(); err != nil {
				return err
			}

			if len(args) == 2 && args[1] == "-V=full" {
				return alterToolVersion(tool, args)
			}
			var tf transformer
			toolexecImportPath := os.Getenv("TOOLEXEC_IMPORTPATH")
			tf.curPkg = sharedCache.ListedPackages[toolexecImportPath]
			if tf.curPkg == nil {
				return fmt.Errorf("TOOLEXEC_IMPORTPATH package not found in listed packages: %s", toolexecImportPath)
			}
			tf.origImporter = importerForPkg(tf.curPkg)

			var err error
			if transformed, err = transform(&tf, transformed); err != nil {
				return err
			}
			log.Printf("transformed args for %s in %s: %s", tool, debugSince(startTime), strings.Join(transformed, " "))
			if tool == "compile" && sharedTempDir != "" {
				logPath := filepath.Join(sharedTempDir, progressLogFile)
				if f, ferr := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o666); ferr == nil {
					fmt.Fprintln(f, toolexecImportPath)
					f.Close()
				}
			}
		} else {
			log.Printf("skipping transform on %s with args: %s", tool, strings.Join(transformed, " "))
		}

		executablePath := args[0]
		if tool == "link" {
			modifiedLinkPath, unlock, err := linker.PatchLinker(sharedCache.GoEnv.GOROOT, sharedCache.GoEnv.GOVERSION, sharedCache.CacheDir, sharedTempDir)
			if err != nil {
				return fmt.Errorf("cannot get modified linker: %v", err)
			}
			defer unlock()

			executablePath = modifiedLinkPath
			os.Setenv(linker.MagicValueEnv, strconv.FormatUint(uint64(magicValue()), 10))
			os.Setenv(linker.EntryOffKeyEnv, strconv.FormatUint(uint64(entryOffKey()), 10))
			if flagTiny {
				os.Setenv(linker.TinyEnv, "true")
			}

			log.Printf("replaced linker with: %s", executablePath)
		}

		cmd := exec.Command(executablePath, transformed...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("unknown command: %q", command)
	}
}

func toolexecCmd(command string, args []string) (*exec.Cmd, error) {

	flags, args := splitFlagsFromArgs(args)
	if hasHelpFlag(flags) {
		out, _ := exec.Command("go", command, "-h").CombinedOutput()
		fmt.Fprintf(os.Stderr, `
usage: garble [garble flags] %s [arguments]

This command wraps "go %s". Below is its help:

%s`[1:], command, command, out)
		return nil, errJustExit(2)
	}
	for _, flag := range flags {
		if rxGarbleFlag.MatchString(flag) {
			return nil, fmt.Errorf("garble flags must precede command, like: garble %s build ./pkg", flag)
		}
	}

	sharedCache = &sharedCacheType{}

	sharedCache.ForwardBuildFlags, _ = filterForwardBuildFlags(flags)
	if command == "test" {
		sharedCache.ForwardBuildFlags = append(sharedCache.ForwardBuildFlags, "-test")
	}

	if err := fetchGoEnv(); err != nil {
		return nil, err
	}

	if !goVersionOK() {
		return nil, errJustExit(1)
	}

	execPath, err := os.Executable()
	if err != nil {
		return nil, err
	}

	if dir := os.Getenv("GARBLE_CACHE"); dir != "" {
		sharedCache.CacheDir, err = filepath.Abs(dir)
		if err != nil {
			return nil, err
		}
	} else {
		parentDir, err := os.UserCacheDir()
		if err != nil {
			return nil, err
		}
		sharedCache.CacheDir = filepath.Join(parentDir, "garble")
	}

	binaryBuildID, err := buildidOf(execPath)
	if err != nil {
		return nil, err
	}
	sharedCache.BinaryContentID = decodeBuildIDHash(splitContentID(binaryBuildID))

	if err := appendListedPackages(args, true); err != nil {
		return nil, err
	}

	sharedTempDir, err = saveSharedCache()
	if err != nil {
		return nil, err
	}
	os.Setenv("GARBLE_SHARED", sharedTempDir)

	if flagDebugDir != "" {
		origDir := flagDebugDir
		flagDebugDir, err = filepath.Abs(flagDebugDir)
		if err != nil {
			return nil, err
		}
		sentinel := filepath.Join(flagDebugDir, ".garble-debugdir")
		if entries, err := os.ReadDir(flagDebugDir); errors.Is(err, fs.ErrNotExist) {
		} else if err == nil && len(entries) == 0 {

		} else if _, err := os.Lstat(sentinel); err == nil {

			if err := os.RemoveAll(flagDebugDir); err != nil {
				return nil, fmt.Errorf("could not empty debugdir: %v", err)
			}
		} else {
			return nil, fmt.Errorf("debugdir %q has unknown contents; empty it first", origDir)
		}

		if err := os.MkdirAll(flagDebugDir, 0o755); err != nil {
			return nil, fmt.Errorf("could not create debugdir directory: %v", err)
		}
		if err := os.WriteFile(sentinel, nil, 0o666); err != nil {
			return nil, fmt.Errorf("could not create debugdir sentinel: %v", err)
		}
	}

	goArgs := append([]string{command}, garbleBuildFlags...)

	var toolexecFlag strings.Builder
	toolexecFlag.WriteString("-toolexec=")
	quotedExecPath, err := cmdgoQuotedJoin([]string{execPath})
	if err != nil {

		return nil, err
	}
	toolexecFlag.WriteString(quotedExecPath)
	appendFlags(&toolexecFlag, false)
	toolexecFlag.WriteString(" toolexec")
	goArgs = append(goArgs, toolexecFlag.String())

	if flagControlFlow {
		goArgs = append(goArgs, "-debug-actiongraph", filepath.Join(sharedTempDir, actionGraphFileName))
	}
	if flagDebugDir != "" {
		needsRebuild, err := debugDirNeedsRebuild()
		if err != nil {
			return nil, err
		}
		if needsRebuild {

			goArgs = append(goArgs, "-a")
		}
	}
	if command == "test" {

		goArgs = append(goArgs, "-vet=off")
	}
	goArgs = append(goArgs, flags...)
	goArgs = append(goArgs, args...)

	return exec.Command("go", goArgs...), nil
}

type seedFlag struct {
	random bool
	bytes  []byte
}

func (f seedFlag) present() bool { return len(f.bytes) > 0 }

func (f seedFlag) String() string {
	return base64.RawStdEncoding.EncodeToString(f.bytes)
}

func (f *seedFlag) Set(s string) error {
	if s == "random" {
		f.random = true

		f.bytes = make([]byte, 8)
		if _, err := cryptorand.Read(f.bytes); err != nil {
			return fmt.Errorf("error generating random seed: %v", err)
		}
	} else {

		s = strings.TrimRight(s, "=")
		seed, err := base64.RawStdEncoding.DecodeString(s)
		if err != nil {
			return fmt.Errorf("error decoding seed: %v", err)
		}

		if len(seed) < 8 {
			return fmt.Errorf("-seed needs at least 8 bytes, have %d", len(seed))
		}
		if len(seed) > 8 {
			fmt.Fprintf(os.Stderr, "warning: -seed only uses the first 8 bytes, ignoring %d extra bytes\n", len(seed)-8)
		}
		f.bytes = seed
	}
	return nil
}

func goVersionOK() bool {
	const (
		minGoVersion  = "go1.26.0"
		unsupportedGo = "go1.27"
	)

	toolchainVersion := sharedCache.GoEnv.GOVERSION
	if toolchainVersion == "" {

		fmt.Fprintf(os.Stderr, "Go version is too old; please upgrade to %s or newer\n", minGoVersion)
		return false
	}

	if !version.IsValid(toolchainVersion) {
		fmt.Fprintf(os.Stderr, "Go version %q appears to be invalid or too old; use %s or newer\n", toolchainVersion, minGoVersion)
		return false
	}
	if version.Compare(toolchainVersion, minGoVersion) < 0 {
		fmt.Fprintf(os.Stderr, "Go version %q is too old; please upgrade to %s or newer\n", toolchainVersion, minGoVersion)
		return false
	}
	if version.Compare(toolchainVersion, unsupportedGo) >= 0 {
		fmt.Fprintf(os.Stderr, "Go version %q is too new; Go linker patches aren't available for %s or later yet\n", toolchainVersion, unsupportedGo)
		return false
	}

	builtVersion := cmp.Or(os.Getenv("GARBLE_TEST_GOVERSION"), runtime.Version())
	if !version.IsValid(builtVersion) {

		return true
	}
	if version.Compare(builtVersion, toolchainVersion) < 0 {
		fmt.Fprintf(os.Stderr, `
garble was built with %q and can't be used with the newer %q; rebuild it with a command like:
    go install github.com/guno1928/alosgarble@latest
`[1:], builtVersion, toolchainVersion)
		return false
	}

	return true
}

func usage() {
	fmt.Fprint(os.Stderr, `
Garble obfuscates Go code by wrapping the Go toolchain.

	garble [garble flags] command [go flags] [go arguments]

For example, to build an obfuscated program:

	garble build ./cmd/foo

Similarly, to combine garble flags and Go build flags:

	garble -literals build -tags=purego ./cmd/foo

The following commands are supported:

	build          replace "go build"
	test           replace "go test"
	run            replace "go run"
	reverse        de-obfuscate output such as stack traces
	version        print the version and build settings of the garble binary

To learn more about a command, run "garble help <command>".

garble accepts the following flags before a command:

`[1:])
	flagSet.PrintDefaults()
	fmt.Fprint(os.Stderr, `

For more information, see https://github.com/burrowers/garble.
`[1:])
}

func filterForwardBuildFlags(flags []string) (filtered []string, firstUnknown string) {
	for i := 0; i < len(flags); i++ {
		arg := flags[i]
		if strings.HasPrefix(arg, "--") {
			arg = arg[1:]
		}

		name, _, _ := strings.Cut(arg, "=")

		buildFlag := forwardBuildFlags[name]
		if buildFlag {
			filtered = append(filtered, arg)
		} else {
			firstUnknown = name
		}
		if booleanFlags[arg] || strings.Contains(arg, "=") {

			continue
		}

		if i++; buildFlag && i < len(flags) {
			filtered = append(filtered, flags[i])
		}
	}
	return filtered, firstUnknown
}

func splitFlagsFromFiles(all []string, ext string) (flags, paths []string) {
	for i := len(all) - 1; i >= 0; i-- {
		arg := all[i]
		if strings.HasPrefix(arg, "-") || !strings.HasSuffix(arg, ext) {
			cutoff := i + 1
			return all[:cutoff:cutoff], all[cutoff:]
		}
	}
	return nil, all
}

func flagValue(flags []string, name string) string {
	lastVal := ""
	for val := range flagValues(flags, name) {
		lastVal = val
	}
	return lastVal
}

func flagValues(flags []string, name string) iter.Seq[string] {
	return func(yield func(string) bool) {
		for i, arg := range flags {
			if val, ok := strings.CutPrefix(arg, name+"="); ok {

				if !yield(val) {
					return
				}
			}
			if arg == name {
				if i+1 < len(flags) {

					if !yield(flags[i+1]) {
						return
					}
				}
			}
		}
	}
}

func flagSetValue(flags []string, name, value string) []string {
	for i, arg := range flags {
		if strings.HasPrefix(arg, name+"=") {

			flags[i] = name + "=" + value
			return flags
		}
		if arg == name {
			if i+1 < len(flags) {

				flags[i+1] = value
				return flags
			}
			return flags
		}
	}
	return append(flags, name+"="+value)
}

var binaryMagic = [8]byte{0xF3, 0xA9, 0x2C, 0x71, 0xDE, 0x5B, 0x8E, 0x04}

func patchOutputBinary(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) >= 48 {
		if [8]byte(data[len(data)-48:]) == binaryMagic {
			data = data[:len(data)-48]
		}
	}
	finalSize := uint64(len(data)) + 48
	h := sha256.Sum256(data)
	var trailer [48]byte
	copy(trailer[0:8], binaryMagic[:])
	binary.LittleEndian.PutUint64(trailer[8:16], finalSize)
	copy(trailer[16:48], h[:])
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	_, werr := f.Write(trailer[:])
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

func fetchGoEnv() error {
	out, err := exec.Command("go", "env", "-json",

		"GOOS", "GOARCH", "GOMOD", "GOVERSION", "GOROOT",
	).Output()
	if err != nil {

		fmt.Fprintf(os.Stderr, `Can't find the Go toolchain: %v

This is likely due to Go not being installed/setup correctly.

To install Go, see: https://go.dev/doc/install
`, err)
		return errJustExit(1)
	}
	if err := json.Unmarshal(out, &sharedCache.GoEnv); err != nil {
		return fmt.Errorf(`cannot unmarshal from "go env -json": %w`, err)
	}

	sharedCache.GoEnv.GOVERSION, _, _ = strings.Cut(sharedCache.GoEnv.GOVERSION, " ")

	sharedCache.GoEnv.GOROOT, err = filepath.EvalSymlinks(sharedCache.GoEnv.GOROOT)
	if err != nil {
		return err
	}

	sharedCache.GoCmd = filepath.Join(sharedCache.GoEnv.GOROOT, "bin", "go")
	sharedCache.GOGARBLE = cmp.Or(os.Getenv("GOGARBLE"), "*")
	return nil
}

type uniqueLineWriter struct {
	out  io.Writer
	seen map[string]bool
}

func (w *uniqueLineWriter) Write(p []byte) (n int, err error) {
	if !flagDebug {
		panic("unexpected use of uniqueLineWriter with -debug unset")
	}
	if bytes.Count(p, []byte("\n")) != 1 {
		return 0, fmt.Errorf("log write wasn't just one line: %q", p)
	}
	if w.seen[string(p)] {
		return len(p), nil
	}
	if w.seen == nil {
		w.seen = make(map[string]bool)
	}
	w.seen[string(p)] = true
	return w.out.Write(p)
}

func debugSince(start time.Time) time.Duration {
	return time.Since(start).Truncate(10 * time.Microsecond)
}

func hasHelpFlag(flags []string) bool {
	for _, f := range flags {
		switch f {
		case "-h", "-help", "--help":
			return true
		}
	}
	return false
}
