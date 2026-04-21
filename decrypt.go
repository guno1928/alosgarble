package main

import (
	"flag"
	"fmt"
	"os"
)

var dbgLogMagic = [8]byte{0x41, 0x4C, 0x4F, 0x53, 0x44, 0x42, 0x47, 0x01}

func commandDecrypt(args []string) error {
	fs := flag.NewFlagSet("decrypt", flag.ContinueOnError)
	password := fs.String("password", "", "password used when building with -debugpassword")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: garble decrypt [-password <pass>] <logfile>")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Decrypts an alosgarble encrypted debug log file produced by a binary")
		fmt.Fprintln(os.Stderr, "built with:  garble -debug -debugpassword <pass> build .")
		fmt.Fprintln(os.Stderr, "")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return errJustExit(2)
	}
	return decryptLogFile(fs.Arg(0), *password)
}

func decryptLogFile(path, password string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("cannot read %s: %v", path, err)
	}
	if len(data) < 24 {
		return fmt.Errorf("file too short to be an encrypted debug log (%d bytes, need ≥ 24)", len(data))
	}
	if [8]byte(data[:8]) != dbgLogMagic {
		return fmt.Errorf("not an alosgarble encrypted debug log — wrong magic bytes\nhint: this file must have been built with:  garble -debug -debugpassword <pass> build .")
	}
	var salt [16]byte
	copy(salt[:], data[8:24])

	fmt.Fprintln(os.Stderr, "deriving key (this takes a moment)...")
	key := dbgDeriveKey(password, salt)
	fmt.Fprintln(os.Stderr, "key ready — decrypting messages")
	fmt.Fprintln(os.Stderr, "")

	offset := 24
	msgNum := 0
	for offset < len(data) {
		if offset+16 > len(data) {
			return fmt.Errorf("truncated block header at file offset %d", offset)
		}
		msgLen := uint32(data[offset]) | uint32(data[offset+1])<<8 | uint32(data[offset+2])<<16 | uint32(data[offset+3])<<24
		var nonce [12]byte
		copy(nonce[:], data[offset+4:offset+16])
		offset += 16
		if offset+int(msgLen) > len(data) {
			return fmt.Errorf("message %d claims %d bytes but only %d remain in file", msgNum, msgLen, len(data)-offset)
		}
		ct := data[offset : offset+int(msgLen)]
		offset += int(msgLen)

		plain := dbgCC20XOR(key, nonce, ct)
		if len(plain) < 4 || string(plain[:4]) != "ALOS" {
			return fmt.Errorf("decryption failed at message %d — wrong password or corrupted log file", msgNum)
		}
		fmt.Print(string(plain[4:]))
		msgNum++
	}
	fmt.Fprintln(os.Stderr, "")
	if msgNum == 0 {
		fmt.Fprintln(os.Stderr, "(log has no messages — process may have exited before any events were captured)")
	} else {
		fmt.Fprintf(os.Stderr, "%d message(s) decrypted successfully from %s\n", msgNum, path)
	}
	return nil
}

func dbgCC20QR(w *[16]uint32, a, b, c, d int) {
	w[a] += w[b]
	w[d] ^= w[a]
	w[d] = w[d]<<16 | w[d]>>16
	w[c] += w[d]
	w[b] ^= w[c]
	w[b] = w[b]<<12 | w[b]>>20
	w[a] += w[b]
	w[d] ^= w[a]
	w[d] = w[d]<<8 | w[d]>>24
	w[c] += w[d]
	w[b] ^= w[c]
	w[b] = w[b]<<7 | w[b]>>25
}

func dbgCC20Block(key [32]byte, nonce [12]byte, ctr uint32) [64]byte {
	var w [16]uint32
	w[0] = 0x61707865
	w[1] = 0x3320646e
	w[2] = 0x79622d32
	w[3] = 0x6b206574
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
		dbgCC20QR(&w, 0, 4, 8, 12)
		dbgCC20QR(&w, 1, 5, 9, 13)
		dbgCC20QR(&w, 2, 6, 10, 14)
		dbgCC20QR(&w, 3, 7, 11, 15)
		dbgCC20QR(&w, 0, 5, 10, 15)
		dbgCC20QR(&w, 1, 6, 11, 12)
		dbgCC20QR(&w, 2, 7, 8, 13)
		dbgCC20QR(&w, 3, 4, 9, 14)
	}
	for i := range w {
		w[i] += x[i]
	}
	var out [64]byte
	for i, v := range w {
		out[i*4] = byte(v)
		out[i*4+1] = byte(v >> 8)
		out[i*4+2] = byte(v >> 16)
		out[i*4+3] = byte(v >> 24)
	}
	return out
}

func dbgCC20XOR(key [32]byte, nonce [12]byte, data []byte) []byte {
	out := make([]byte, len(data))
	var ctr uint32
	for off := 0; off < len(data); off += 64 {
		ks := dbgCC20Block(key, nonce, ctr)
		end := off + 64
		if end > len(data) {
			end = len(data)
		}
		for i := off; i < end; i++ {
			out[i] = data[i] ^ ks[i-off]
		}
		ctr++
	}
	return out
}

func dbgDeriveKey(password string, salt [16]byte) [32]byte {
	var k [32]byte
	copy(k[:16], salt[:])
	for i := 16; i < 32; i++ {
		k[i] = ^k[i-16]
	}
	pb := []byte(password)
	if len(pb) == 0 {
		pb = []byte{0}
	}
	for round := 0; round < 100000; round++ {
		for j := range k {
			k[j] ^= pb[j%len(pb)] ^ byte(round>>uint(j%8))
			k[j] = k[j]<<1 | k[j]>>7
			k[j] ^= k[(j+7)%32]
		}
	}
	return k
}
