package linker

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"go/version"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"

	"github.com/bluekeyes/go-gitdiff/gitdiff"
	"github.com/rogpeppe/go-internal/lockedfile"
)

const (
	MagicValueEnv  = "GARBLE_LINK_MAGIC"
	TinyEnv        = "GARBLE_LINK_TINY"
	EntryOffKeyEnv = "GARBLE_LINK_ENTRYOFF_KEY"
)

//go:embed patches/*/*.patch
var linkerPatchesFS embed.FS

func loadLinkerPatches(majorGoVersion string) (version string, modFiles map[string]bool, patches [][]byte, err error) {
	modFiles = make(map[string]bool)
	versionHash := sha256.New()
	if err := fs.WalkDir(linkerPatchesFS, "patches/"+majorGoVersion, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		patchBytes, err := linkerPatchesFS.ReadFile(path)
		if err != nil {
			return err
		}

		if _, err := versionHash.Write(patchBytes); err != nil {
			return err
		}

		files, _, err := gitdiff.Parse(bytes.NewReader(patchBytes))
		if err != nil {
			return err
		}
		for _, file := range files {
			if file.IsNew || file.IsDelete || file.IsCopy || file.IsRename {
				return fmt.Errorf("unsupported patch type for %s: only modification patches are supported", file.OldName)
			}
			modFiles[file.OldName] = true
		}
		patches = append(patches, patchBytes)
		return nil
	}); err != nil {
		return "", nil, nil, err
	}
	version = base64.RawStdEncoding.EncodeToString(versionHash.Sum(nil))
	return
}

func copyFile(src, target string) error {
	targetDir := filepath.Dir(target)
	if err := os.MkdirAll(targetDir, 0o777); err != nil {
		return err
	}
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	targetFile, err := os.Create(target)
	if err != nil {
		return err
	}
	if _, err := io.Copy(targetFile, srcFile); err != nil {
		targetFile.Close()
		return err
	}
	return targetFile.Close()
}

func fileExists(path string) bool {
	stat, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !stat.IsDir()
}

func applyPatches(srcDir, workingDir string, modFiles map[string]bool, patches [][]byte) (map[string]string, error) {
	mod := make(map[string]string)
	for fileName := range modFiles {
		oldPath := filepath.Join(srcDir, fileName)
		newPath := filepath.Join(workingDir, fileName)
		mod[oldPath] = newPath

		if err := copyFile(oldPath, newPath); err != nil {
			return nil, err
		}
	}

	cmd := exec.Command("git", "--git-dir", workingDir, "apply", "--verbose")
	cmd.Dir = workingDir

	cmd.Env = append(cmd.Env, "LC_ALL=C")
	cmd.Stdin = bytes.NewReader(bytes.Join(patches, []byte("\n")))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to 'git apply' patches: %v:\n%s", err, out)
	}

	rx := regexp.MustCompile(`(?m)^Applied patch .+ cleanly\.$`)
	if appliedPatches := len(rx.FindAllIndex(out, -1)); appliedPatches != len(patches) {
		return nil, fmt.Errorf("expected %d applied patches, actually %d:\n\n%s", len(patches), appliedPatches, string(out))
	}
	return mod, nil
}

func cachePath(cacheDir string) (string, error) {

	cacheDir = filepath.Join(cacheDir, "tool")
	if err := os.MkdirAll(cacheDir, 0o777); err != nil {
		return "", err
	}
	goExe := ""
	if runtime.GOOS == "windows" {
		goExe = ".exe"
	}

	return filepath.Join(cacheDir, "link"+goExe), nil
}

func getCurrentVersion(goVersion, patchesVer string) string {

	return goVersion + " " + patchesVer + "\n"
}

const versionExt = ".version"

func checkVersion(linkerPath, goVersion, patchesVer string) (bool, error) {
	versionPath := linkerPath + versionExt
	version, err := os.ReadFile(versionPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	return string(version) == getCurrentVersion(goVersion, patchesVer), nil
}

func writeVersion(linkerPath, goVersion, patchesVer string) error {
	versionPath := linkerPath + versionExt
	return os.WriteFile(versionPath, []byte(getCurrentVersion(goVersion, patchesVer)), 0o777)
}

func buildLinker(goRoot, workingDir string, overlay map[string]string, outputLinkPath string) error {
	file, err := json.Marshal(&struct{ Replace map[string]string }{overlay})
	if err != nil {
		return err
	}
	overlayPath := filepath.Join(workingDir, "overlay.json")
	if err := os.WriteFile(overlayPath, file, 0o777); err != nil {
		return err
	}

	goCmd := filepath.Join(goRoot, "bin", "go")
	cmd := exec.Command(goCmd, "build", "-overlay", overlayPath, "-o", outputLinkPath, "cmd/link")

	cmd.Env = append(cmd.Environ(),
		"GOENV=off", "GOOS=", "GOARCH=", "GOEXPERIMENT=", "GOFLAGS=",
	)

	cmd.Dir = workingDir

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("compiler compile error: %v\n\n%s", err, string(out))
	}

	return nil
}
func PatchLinker(goRoot, goVersion, cacheDir, tempDir string) (string, func(), error) {
	patchesVer, modFiles, patches, err := loadLinkerPatches(version.Lang(goVersion))
	if err != nil {
		return "", nil, fmt.Errorf("cannot retrieve linker patches: %v", err)
	}

	outputLinkPath, err := cachePath(cacheDir)
	if err != nil {
		return "", nil, err
	}

	mutex := lockedfile.MutexAt(outputLinkPath + ".lock")
	unlock, err := mutex.Lock()
	if err != nil {
		return "", nil, err
	}

	successBuild := false
	defer func() {
		if !successBuild {
			unlock()
		}
	}()

	isCorrectVer, err := checkVersion(outputLinkPath, goVersion, patchesVer)
	if err != nil {
		return "", nil, err
	}
	if isCorrectVer && fileExists(outputLinkPath) {
		successBuild = true
		return outputLinkPath, unlock, nil
	}

	srcDir := filepath.Join(goRoot, "src")
	workingDir := filepath.Join(tempDir, "linker-src")

	overlay, err := applyPatches(srcDir, workingDir, modFiles, patches)
	if err != nil {
		return "", nil, err
	}
	if err := buildLinker(goRoot, workingDir, overlay, outputLinkPath); err != nil {
		return "", nil, err
	}
	if err := writeVersion(outputLinkPath, goVersion, patchesVer); err != nil {
		return "", nil, err
	}
	successBuild = true
	return outputLinkPath, unlock, nil
}
