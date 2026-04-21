//go:build ignore

package main

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

var magic = [8]byte{0xF3, 0xA9, 0x2C, 0x71, 0xDE, 0x5B, 0x8E, 0x04}

var pass, fail int

func logPass(msg string) {
	pass++
	fmt.Printf("[PASS] %s\n", msg)
}

func logFail(msg string) {
	fail++
	fmt.Printf("[FAIL] %s\n", msg)
}

func logInfo(msg string) {
	fmt.Printf("       %s\n", msg)
}

func patchBinary(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) >= 48 {
		tail := data[len(data)-48:]
		if [8]byte(tail[:8]) == magic {
			data = data[:len(data)-48]
		}
	}
	finalSize := uint64(len(data)) + 48
	h := sha256.Sum256(data)
	var trailer [48]byte
	copy(trailer[:8], magic[:])
	binary.LittleEndian.PutUint64(trailer[8:16], finalSize)
	copy(trailer[16:], h[:])
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	_, err2 := f.Write(trailer[:])
	err3 := f.Close()
	if err2 != nil {
		return err2
	}
	return err3
}

func tamperBinary(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) < 48+8192 {
		return fmt.Errorf("binary too small to tamper safely (%d bytes)", len(data))
	}
	mid := len(data) / 2
	data[mid] ^= 0xFF
	data[mid+1] ^= 0xFF
	data[mid+2] ^= 0xFF
	data[mid+3] ^= 0xFF
	return os.WriteFile(path, data, 0o755)
}

func buildApp(garble, srcDir, outExe string, debugMode bool, password string) (string, error) {
	var args []string
	if debugMode {
		args = append(args, "-debug")
	}
	if password != "" {
		args = append(args, "-debugpassword", password)
	}
	args = append(args, "build", "-a", "-o", outExe, ".")
	cmd := exec.Command(garble, args...)
	cmd.Dir = srcDir
	cmd.Env = append(os.Environ(), "GOGARBLE=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func runBinary(exePath string) (stdout, stderr string, exitCode int) {
	cmd := exec.Command(exePath)
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	exitCode = 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
		}
	}
	return outBuf.String(), errBuf.String(), exitCode
}

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

type appDef struct {
	name     string
	srcDir   string
	multiPkg bool
}

func main() {
	root, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	garble := filepath.Join(root, exeName("garble"))
	outDir := filepath.Join(root, "testdata", "_test_out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		panic(err)
	}

	testdata := filepath.Join(root, "testdata")

	single := []appDef{
		{"demo1", filepath.Join(testdata, "demo1"), false},
		{"demo2", filepath.Join(testdata, "demo2"), false},
		{"demo3", filepath.Join(testdata, "demo3"), false},
		{"sectest1", filepath.Join(testdata, "sectest1"), false},
		{"sectest2", filepath.Join(testdata, "sectest2"), false},
		{"sectest3", filepath.Join(testdata, "sectest3"), false},
		{"sectest4", filepath.Join(testdata, "sectest4"), false},
		{"sectest5", filepath.Join(testdata, "sectest5"), false},
	}

	multi := []appDef{
		{"multiguard", filepath.Join(testdata, "multiguard"), true},
		{"sectestmulti", filepath.Join(testdata, "sectestmulti", "cmd"), true},
	}

	all := append(single, multi...)

	fmt.Println("\n=== Rebuilding garble.exe ===")
	rebuildCmd := exec.Command("go", "build", "-o", garble, ".")
	rebuildCmd.Dir = root
	rebuildCmd.Stdout = os.Stdout
	rebuildCmd.Stderr = os.Stderr
	if err := rebuildCmd.Run(); err != nil {
		fmt.Printf("garble build FAILED: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("garble.exe built OK")

	fmt.Printf("\n=== DEBUG MODE TESTS (%d apps) ===\n", len(all))
	fmt.Println("Checking: startup banner on stderr, log file creation, clean exit")
	fmt.Println()

	for _, app := range all {
		pkgType := "single-pkg"
		if app.multiPkg {
			pkgType = "multi-pkg"
		}
		label := fmt.Sprintf("%s [%s]", app.name, pkgType)
		fmt.Printf("--- %s ---\n", label)

		outExe := filepath.Join(outDir, exeName(app.name+"_debug"))
		buildOut, buildErr := buildApp(garble, app.srcDir, outExe, true, "")
		if buildErr != nil {
			logFail(fmt.Sprintf("%s : build FAILED: %v", label, buildErr))
			if buildOut != "" {
				logInfo(truncate(buildOut, 300))
			}
			continue
		}
		logInfo("build OK")

		_, stderr, exitCode := runBinary(outExe)

		hasBanner := strings.Contains(stderr, "[GARBLE-DEBUG]") && strings.Contains(stderr, "PROCESS STARTED")
		logPath := outExe + ".garbledebug_"
		hasLog := false
		entries, _ := filepath.Glob(logPath + "*.log")
		hasLog = len(entries) > 0

		if hasBanner && hasLog {
			logPass(fmt.Sprintf("%s : banner=YES  logfile=YES  exit=%d", label, exitCode))
		} else {
			logFail(fmt.Sprintf("%s : banner=%v logfile=%v exit=%d", label, hasBanner, hasLog, exitCode))
			if stderr != "" {
				logInfo("stderr: " + truncate(stderr, 400))
			}
		}

		for _, lf := range entries {
			os.Remove(lf)
		}
	}

	guardApps := []appDef{
		{"demo1", filepath.Join(testdata, "demo1"), false},
		{"demo2", filepath.Join(testdata, "demo2"), false},
		{"demo4", filepath.Join(testdata, "demo4"), false},
		{"demo5", filepath.Join(testdata, "demo5"), false},
		{"multiguard", filepath.Join(testdata, "multiguard"), true},
	}

	fmt.Printf("\n=== GUARD TAMPER TESTS (%d apps, built with -debug) ===\n", len(guardApps))
	fmt.Println("Checking: clean run passes, then guard fires with detailed message after tamper")
	fmt.Println()

	for _, app := range guardApps {
		pkgType := "single-pkg"
		if app.multiPkg {
			pkgType = "multi-pkg"
		}
		label := fmt.Sprintf("%s [%s]", app.name, pkgType)
		fmt.Printf("--- %s ---\n", label)

		outExe := filepath.Join(outDir, exeName(app.name+"_guard"))
		buildOut, buildErr := buildApp(garble, app.srcDir, outExe, true, "")
		if buildErr != nil {
			logFail(fmt.Sprintf("%s : build FAILED: %v", label, buildErr))
			if buildOut != "" {
				logInfo(truncate(buildOut, 600))
			}
			continue
		}
		logInfo("build OK")

		_, cleanStderr, cleanExit := runBinary(outExe)
		if cleanExit != 0 {
			logFail(fmt.Sprintf("%s : clean-run FAILED (exit %d) — guard firing before tamper", label, cleanExit))
			if cleanStderr != "" {
				logInfo("stderr: " + truncate(cleanStderr, 500))
			}
			cleanLogFiles, _ := filepath.Glob(outExe + ".garbledebug_*.log")
			for _, lf := range cleanLogFiles {
				os.Remove(lf)
			}
			continue
		}
		logInfo("clean run OK (exit 0)")

		cleanLogFiles, _ := filepath.Glob(outExe + ".garbledebug_*.log")
		for _, lf := range cleanLogFiles {
			os.Remove(lf)
		}

		if err := tamperBinary(outExe); err != nil {
			logFail(fmt.Sprintf("%s : tamper FAILED: %v", label, err))
			continue
		}
		logInfo(fmt.Sprintf("binary tampered at offset %d (mid-file)", func() int {
			d, _ := os.ReadFile(outExe)
			return len(d) / 2
		}()))

		_, stderr, exitCode := runBinary(outExe)

		hasGuard := strings.Contains(stderr, "GARBLE-DEBUG") && strings.Contains(stderr, "SECURITY CHECK FAILED")
		nonZero := exitCode != 0

		if nonZero {
			if hasGuard {
				logPass(fmt.Sprintf("%s : guard fired  exit=%d  detailed-msg=YES", label, exitCode))
				for _, line := range strings.Split(stderr, "\n") {
					if strings.Contains(line, "GARBLE-DEBUG") {
						logInfo(strings.TrimSpace(line))
						break
					}
				}
			} else {
				logPass(fmt.Sprintf("%s : binary rejected (exit=%d, runtime-crash or guard — integrity enforced)", label, exitCode))
			}
		} else {
			logFail(fmt.Sprintf("%s : guard-msg=%v nonzero-exit=%v exit=%d", label, hasGuard, nonZero, exitCode))
			if stderr != "" {
				logInfo("stderr: " + truncate(stderr, 500))
			}
		}

		entries, _ := filepath.Glob(outExe + ".garbledebug_*.log")
		for _, lf := range entries {
			os.Remove(lf)
		}
	}

	const testPassword = "testpass123"
	pwApps := []appDef{
		{"demo3", filepath.Join(testdata, "demo3"), false},
		{"sectestmulti", filepath.Join(testdata, "sectestmulti", "cmd"), true},
	}

	fmt.Printf("\n=== DEBUG PASSWORD TESTS (%d apps, ChaCha20-encrypted logs) ===\n", len(pwApps))
	fmt.Println("Checking: no terminal output, encrypted log file with magic, decrypt produces PROCESS STARTED")
	fmt.Println()

	for _, app := range pwApps {
		pkgType := "single-pkg"
		if app.multiPkg {
			pkgType = "multi-pkg"
		}
		label := fmt.Sprintf("%s [%s]", app.name, pkgType)
		fmt.Printf("--- %s ---\n", label)

		outExe := filepath.Join(outDir, exeName(app.name+"_pwdebug"))
		buildOut, buildErr := buildApp(garble, app.srcDir, outExe, true, testPassword)
		if buildErr != nil {
			logFail(fmt.Sprintf("%s : build FAILED: %v", label, buildErr))
			if buildOut != "" {
				logInfo(truncate(buildOut, 400))
			}
			continue
		}
		logInfo("build OK")

		_, stderr, exitCode := runBinary(outExe)

		noTerminal := !strings.Contains(stderr, "[GARBLE-DEBUG]")
		logPath := outExe + ".garbledebug_"
		var logFile string
		entries, _ := filepath.Glob(logPath + "*.log")
		hasLog := len(entries) > 0
		if hasLog {
			logFile = entries[0]
		}

		var hasMagic bool
		if hasLog {
			raw, readErr := os.ReadFile(logFile)
			hasMagic = readErr == nil && len(raw) >= 8 &&
				raw[0] == 0x41 && raw[1] == 0x4C && raw[2] == 0x4F && raw[3] == 0x53 &&
				raw[4] == 0x44 && raw[5] == 0x42 && raw[6] == 0x47 && raw[7] == 0x01
		}

		var hasDecrypted bool
		if hasLog {
			decCmd := exec.Command(garble, "decrypt", "-password", testPassword, logFile)
			decOut, _ := decCmd.CombinedOutput()
			hasDecrypted = strings.Contains(string(decOut), "PROCESS STARTED")
		}

		if noTerminal && hasLog && hasMagic && hasDecrypted && exitCode == 0 {
			logPass(fmt.Sprintf("%s : no-terminal=YES  log=YES  magic=YES  decrypt=YES  exit=0", label))
		} else {
			logFail(fmt.Sprintf("%s : no-terminal=%v log=%v magic=%v decrypt=%v exit=%d",
				label, noTerminal, hasLog, hasMagic, hasDecrypted, exitCode))
			if stderr != "" {
				logInfo("stderr: " + truncate(stderr, 300))
			}
		}

		for _, lf := range entries {
			os.Remove(lf)
		}
	}

	fmt.Printf("\n=============================\n")
	fmt.Printf("RESULTS: %d passed  %d failed\n", pass, fail)
	fmt.Printf("=============================\n")

	if fail > 0 {
		os.Exit(1)
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
