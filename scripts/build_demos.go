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
)

var magic = [8]byte{0xF3, 0xA9, 0x2C, 0x71, 0xDE, 0x5B, 0x8E, 0x04}

func patchBinary(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	
	if len(data) >= 48 {
		tail := data[len(data)-48:]
		if [8]byte(tail[:8]) == magic {
			if binary.LittleEndian.Uint64(tail[8:16]) == uint64(len(data)) {
				data = data[:len(data)-48]
			}
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

func main() {
	root, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	garble := filepath.Join(root, "garble.exe")
	if runtime.GOOS != "windows" {
		garble = filepath.Join(root, "garble")
	}

	demos := []string{"demo1", "demo2", "demo3", "demo4", "demo5"}
	outDir := filepath.Join(root, "testdata", "demo_binaries")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		panic(err)
	}

	for _, name := range demos {
		src := filepath.Join(root, "testdata", name)
		outName := name
		if runtime.GOOS == "windows" {
			outName += ".exe"
		}
		out := filepath.Join(outDir, outName)

		fmt.Printf("[%s] building...\n", name)
		cmd := exec.Command(garble, "build", "-a", "-o", out, ".")
		cmd.Dir = src
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Printf("[%s] BUILD FAILED: %v\n", name, err)
			os.Exit(1)
		}
		fmt.Printf("[%s] built => %s\n", name, out)

		fmt.Printf("[%s] patching...\n", name)
		if err := patchBinary(out); err != nil {
			fmt.Printf("[%s] PATCH FAILED: %v\n", name, err)
			os.Exit(1)
		}
		fi, _ := os.Stat(out)
		fmt.Printf("[%s] patched  => %s (%d bytes)\n", name, out, fi.Size())
		fmt.Println()
	}

	fmt.Println("=== All demos built and patched ===")
	fmt.Printf("Binaries in: %s\n", outDir)
}
