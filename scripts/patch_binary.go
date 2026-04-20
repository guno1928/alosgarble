//go:build ignore





















package main

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
)


var magic = [8]byte{0xF3, 0xA9, 0x2C, 0x71, 0xDE, 0x5B, 0x8E, 0x04}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: patch_binary <path-to-binary>")
		os.Exit(1)
	}
	path := os.Args[1]

	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading %s: %v\n", path, err)
		os.Exit(1)
	}

	
	
	
	if len(data) >= 48 {
		tail := data[len(data)-48:]
		if [8]byte(tail[:8]) == magic {
			fmt.Printf("existing trailer found — replacing\n")
			data = data[:len(data)-48]
		}
	}

	
	finalSize := uint64(len(data)) + 48

	
	h := sha256.Sum256(data)

	
	var trailer [48]byte
	copy(trailer[0:8], magic[:])
	binary.LittleEndian.PutUint64(trailer[8:16], finalSize)
	copy(trailer[16:48], h[:])

	
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error opening %s for write: %v\n", path, err)
		os.Exit(1)
	}
	if _, err := out.Write(trailer[:]); err != nil {
		out.Close()
		fmt.Fprintf(os.Stderr, "error writing trailer: %v\n", err)
		os.Exit(1)
	}
	if err := out.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "error closing: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("patched %s\n", path)
	fmt.Printf("  body:  %d bytes\n", len(data))
	fmt.Printf("  total: %d bytes (+ 48-byte trailer)\n", finalSize)
	fmt.Printf("  sha256 of body: %x\n", h)
	fmt.Printf("  magic: %x\n", magic)
}
