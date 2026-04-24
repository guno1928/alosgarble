package literals

import (
	"fmt"
	"go/ast"
	"go/token"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ============================================================================
// BENCHMARK 1: Literal Decryption Throughput (simulates what generated code does)
// ============================================================================

// wideDecrypt simulates the exact operations the wide obfuscator generates
func wideDecrypt(enc, key []byte) []byte {
	data := make([]byte, len(enc))
	for i := 0; i < len(enc); i++ {
		data[i] = enc[i] ^ key[i]
	}
	return data
}

// wideDecryptWithChecksum simulates the full wide obfuscator runtime path
func wideDecryptFull(enc []byte, keyFragments [][]byte, offsets []int, storedCheck uint64, checkSeed uint64, tamperByte byte) []byte {
	n := len(enc)
	key := make([]byte, n)
	for fi, frag := range keyFragments {
		copy(key[offsets[fi]:], frag)
	}

	var check uint64 = checkSeed
	data := make([]byte, n)
	for i := 0; i < n; i++ {
		pv := enc[i] ^ key[i]
		data[i] = pv
		check ^= uint64(pv) << uint((i*8)&63)
	}

	compConst := storedCheck ^ checkSeed
	if check != compConst {
		for j := range data {
			data[j] ^= tamperByte
		}
	}
	return data
}

func BenchmarkLiteralDecryptSmall(b *testing.B) {
	enc := make([]byte, 16)
	key := make([]byte, 16)
	rand.Read(enc)
	rand.Read(key)
	b.ReportAllocs()
	b.SetBytes(16)
	for i := 0; i < b.N; i++ {
		_ = wideDecrypt(enc, key)
	}
}

func BenchmarkLiteralDecryptMedium(b *testing.B) {
	enc := make([]byte, 64)
	key := make([]byte, 64)
	rand.Read(enc)
	rand.Read(key)
	b.ReportAllocs()
	b.SetBytes(64)
	for i := 0; i < b.N; i++ {
		_ = wideDecrypt(enc, key)
	}
}

func BenchmarkLiteralDecryptLarge(b *testing.B) {
	enc := make([]byte, 256)
	key := make([]byte, 256)
	rand.Read(enc)
	rand.Read(key)
	b.ReportAllocs()
	b.SetBytes(256)
	for i := 0; i < b.N; i++ {
		_ = wideDecrypt(enc, key)
	}
}

func BenchmarkLiteralDecryptMax(b *testing.B) {
	enc := make([]byte, 2048)
	key := make([]byte, 2048)
	rand.Read(enc)
	rand.Read(key)
	b.ReportAllocs()
	b.SetBytes(2048)
	for i := 0; i < b.N; i++ {
		_ = wideDecrypt(enc, key)
	}
}

func BenchmarkLiteralDecryptFullSmall(b *testing.B) {
	enc := make([]byte, 32)
	key := make([]byte, 32)
	rand.Read(enc)
	rand.Read(key)
	frags := [][]byte{key[:16], key[16:]}
	offsets := []int{0, 16}
	storedCheck := uint64(0x1234567890ABCDEF)
	b.ReportAllocs()
	b.SetBytes(32)
	for i := 0; i < b.N; i++ {
		_ = wideDecryptFull(enc, frags, offsets, storedCheck, 0xDEADBEEF, 0x42)
	}
}

func BenchmarkLiteralDecryptFullMedium(b *testing.B) {
	enc := make([]byte, 128)
	key := make([]byte, 128)
	rand.Read(enc)
	rand.Read(key)
	frags := [][]byte{key[:32], key[32:64], key[64:96], key[96:]}
	offsets := []int{0, 32, 64, 96}
	storedCheck := uint64(0x1234567890ABCDEF)
	b.ReportAllocs()
	b.SetBytes(128)
	for i := 0; i < b.N; i++ {
		_ = wideDecryptFull(enc, frags, offsets, storedCheck, 0xDEADBEEF, 0x42)
	}
}

func BenchmarkLiteralDecryptFullLarge(b *testing.B) {
	enc := make([]byte, 1024)
	key := make([]byte, 1024)
	rand.Read(enc)
	rand.Read(key)
	frags := [][]byte{key[:256], key[256:512], key[512:768], key[768:]}
	offsets := []int{0, 256, 512, 768}
	storedCheck := uint64(0x1234567890ABCDEF)
	b.ReportAllocs()
	b.SetBytes(1024)
	for i := 0; i < b.N; i++ {
		_ = wideDecryptFull(enc, frags, offsets, storedCheck, 0xDEADBEEF, 0x42)
	}
}

// ============================================================================
// BENCHMARK 2: Obfuscator Generation Speed
// ============================================================================

func BenchmarkWideObfuscateSmall(b *testing.B) {
	rnd := rand.New(rand.NewSource(42))
	data := []byte("hello world test string literal here")
	extKeys := randExtKeys(rnd)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		w := wide{}
		_ = w.obfuscate(rnd, data, extKeys)
	}
}

func BenchmarkWideObfuscateMedium(b *testing.B) {
	rnd := rand.New(rand.NewSource(42))
	data := make([]byte, 128)
	rand.Read(data)
	extKeys := randExtKeys(rnd)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		w := wide{}
		_ = w.obfuscate(rnd, data, extKeys)
	}
}

func BenchmarkWideObfuscateLarge(b *testing.B) {
	rnd := rand.New(rand.NewSource(42))
	data := make([]byte, 1024)
	rand.Read(data)
	extKeys := randExtKeys(rnd)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		w := wide{}
		_ = w.obfuscate(rnd, data, extKeys)
	}
}

// ============================================================================
// BENCHMARK 3: End-to-end Compile + Run with Obfuscated Literals
// ============================================================================

func BenchmarkE2EObfuscatedLiteralAccess(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping e2e benchmark in short mode")
	}

	// Generate a program with many obfuscated string literals
	var src strings.Builder
	src.WriteString("package main\n\nimport \"fmt\"\n\nfunc main() {\n")
	for i := 0; i < 100; i++ {
		src.WriteString(fmt.Sprintf("\t_ = \"test_string_literal_number_%d_with_some_padding_to_make_it_realistic\"\n", i))
	}
	src.WriteString("\tfmt.Println(\"done\")\n}\n")

	tmpDir := b.TempDir()
	mainFile := filepath.Join(tmpDir, "main.go")
	os.WriteFile(mainFile, []byte(src.String()), 0644)
	os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module test\ngo 1.21\n"), 0644)

	// Build with garble
	garblePath, _ := filepath.Abs("../../garble.exe")
	cmd := exec.Command(garblePath, "build", "-o", "test.exe")
	cmd.Dir = tmpDir
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		b.Fatalf("garble build failed: %v\n%s", err, out)
	}

	exePath := filepath.Join(tmpDir, "test.exe")
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		cmd := exec.Command(exePath)
		cmd.Env = os.Environ()
		out, err := cmd.CombinedOutput()
		if err != nil {
			b.Fatalf("run failed: %v\n%s", err, out)
		}
		_ = out
	}
}

// ============================================================================
// BENCHMARK 4: Compare Plain vs Obfuscated String Access Pattern
// ============================================================================

func BenchmarkPlainStringAccess(b *testing.B) {
	strings := make([]string, 100)
	for i := range strings {
		strings[i] = fmt.Sprintf("test_string_literal_number_%d_with_some_padding", i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, s := range strings {
			_ = s[0]
		}
	}
}

func BenchmarkSimulatedObfuscatedAccess(b *testing.B) {
	// Simulates what happens when each string access triggers decryption
	encStrings := make([][]byte, 100)
	keys := make([][]byte, 100)
	for i := range encStrings {
		s := fmt.Sprintf("test_string_literal_number_%d_with_some_padding", i)
		encStrings[i] = []byte(s)
		keys[i] = make([]byte, len(s))
		rand.Read(keys[i])
		for j := range encStrings[i] {
			encStrings[i][j] ^= keys[i][j]
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j, enc := range encStrings {
			data := make([]byte, len(enc))
			for k := range enc {
				data[k] = enc[k] ^ keys[j][k]
			}
			_ = data[0]
		}
	}
}

// ============================================================================
// TEST: Measure Obfuscated Binary Size vs Plain
// ============================================================================

func TestObfuscatedBinarySizeImpact(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping binary size test in short mode")
	}

	literalCounts := []int{10, 50, 100, 500}
	for _, count := range literalCounts {
		t.Run(fmt.Sprintf("literals_%d", count), func(t *testing.T) {
			tmpDir := t.TempDir()

			// Build plain version
			var plainSrc strings.Builder
			plainSrc.WriteString("package main\n\nimport \"fmt\"\n\nfunc main() {\n")
			for i := 0; i < count; i++ {
				plainSrc.WriteString(fmt.Sprintf("\t_ = \"str_%d_some_long_string_to_obfuscate_here\"\n", i))
			}
			plainSrc.WriteString("\tfmt.Println(\"done\")\n}\n")

			os.WriteFile(filepath.Join(tmpDir, "plain.go"), []byte(plainSrc.String()), 0644)
			os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module test\ngo 1.21\n"), 0644)

			cmd := exec.Command("go", "build", "-o", "plain.exe", "plain.go")
			cmd.Dir = tmpDir
			cmd.Env = os.Environ()
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("plain build failed: %v\n%s", err, out)
			}

			plainInfo, _ := os.Stat(filepath.Join(tmpDir, "plain.exe"))

			// Build obfuscated version with garble
			garblePath, _ := filepath.Abs("../../garble.exe")
			cmd = exec.Command(garblePath, "build", "-o", "obf.exe", "plain.go")
			cmd.Dir = tmpDir
			cmd.Env = os.Environ()
			start := time.Now()
			out, err := cmd.CombinedOutput()
			buildTime := time.Since(start)

			if err != nil {
				t.Fatalf("obfuscated build failed: %v\n%s", err, out)
			}

			obfInfo, _ := os.Stat(filepath.Join(tmpDir, "obf.exe"))

			ratio := float64(obfInfo.Size()) / float64(plainInfo.Size())
			t.Logf("literals=%d plain=%d bytes obf=%d bytes ratio=%.2fx build_time=%.2fs",
				count, plainInfo.Size(), obfInfo.Size(), ratio, buildTime.Seconds())
		})
	}
}

// ============================================================================
// BENCHMARK 5: Fragment Assembly Cost
// ============================================================================

func BenchmarkFragmentAssembly(b *testing.B) {
	key := make([]byte, 256)
	rand.Read(key)
	fragments := [][]byte{key[:64], key[64:128], key[128:192], key[192:]}
	offsets := []int{0, 64, 128, 192}

	b.ReportAllocs()
	b.SetBytes(256)
	for i := 0; i < b.N; i++ {
		assembled := make([]byte, 256)
		for fi, frag := range fragments {
			copy(assembled[offsets[fi]:], frag)
		}
		_ = assembled
	}
}

func BenchmarkFragmentAssemblyMany(b *testing.B) {
	key := make([]byte, 256)
	rand.Read(key)
	// 6 fragments
	fragments := make([][]byte, 6)
	offsets := make([]int, 6)
	for i := 0; i < 6; i++ {
		fragments[i] = key[i*42 : (i+1)*42]
		offsets[i] = i * 42
	}

	b.ReportAllocs()
	b.SetBytes(256)
	for i := 0; i < b.N; i++ {
		assembled := make([]byte, 256)
		for fi, frag := range fragments {
			copy(assembled[offsets[fi]:], frag)
		}
		_ = assembled
	}
}

// ============================================================================
// BENCHMARK 6: XOR Decryption Hot Path (what actually matters)
// ============================================================================

func BenchmarkXORLoopUnrolled(b *testing.B) {
	enc := make([]byte, 64)
	key := make([]byte, 64)
	rand.Read(enc)
	rand.Read(key)
	b.ReportAllocs()
	b.SetBytes(64)
	for i := 0; i < b.N; i++ {
		data := make([]byte, 64)
		_ = copy(data, enc)
		for j := 0; j < 64; j += 8 {
			data[j+0] ^= key[j+0]
			data[j+1] ^= key[j+1]
			data[j+2] ^= key[j+2]
			data[j+3] ^= key[j+3]
			data[j+4] ^= key[j+4]
			data[j+5] ^= key[j+5]
			data[j+6] ^= key[j+6]
			data[j+7] ^= key[j+7]
		}
		_ = data
	}
}

func BenchmarkXORLoopStandard(b *testing.B) {
	enc := make([]byte, 64)
	key := make([]byte, 64)
	rand.Read(enc)
	rand.Read(key)
	b.ReportAllocs()
	b.SetBytes(64)
	for i := 0; i < b.N; i++ {
		data := make([]byte, 64)
		_ = copy(data, enc)
		for j := range data {
			data[j] ^= key[j]
		}
		_ = data
	}
}

func BenchmarkXORLoopNoAlloc(b *testing.B) {
	enc := make([]byte, 64)
	key := make([]byte, 64)
	rand.Read(enc)
	rand.Read(key)
	data := make([]byte, 64)
	b.ReportAllocs()
	b.SetBytes(64)
	for i := 0; i < b.N; i++ {
		_ = copy(data, enc)
		for j := range data {
			data[j] ^= key[j]
		}
	}
}

// ============================================================================
// BENCHMARK 7: AST Generation Overhead (compile-time cost)
// ============================================================================

func BenchmarkASTGenerationSmallLiteral(b *testing.B) {
	rnd := rand.New(rand.NewSource(42))
	data := []byte("small string test here")
	extKeys := randExtKeys(rnd)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		w := wide{}
		block := w.obfuscate(rnd, data, extKeys)
		_ = len(block.List)
	}
}

func BenchmarkASTGenerationLargeLiteral(b *testing.B) {
	rnd := rand.New(rand.NewSource(42))
	data := make([]byte, 1024)
	rand.Read(data)
	extKeys := randExtKeys(rnd)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		w := wide{}
		block := w.obfuscate(rnd, data, extKeys)
		_ = len(block.List)
	}
}

// ============================================================================
// TEST: Print Environment Info
// ============================================================================

func TestBenchmarkHeader(t *testing.T) {
	t.Log("========================================================================")
	t.Log("WIDE LITERAL OBFUSCATOR PERFORMANCE BENCHMARK SUITE")
	t.Log("========================================================================")
	t.Logf("Go Version: %s", runtime.Version())
	t.Logf("GOOS/GOARCH: %s/%s", runtime.GOOS, runtime.GOARCH)
	t.Logf("GOMAXPROCS: %d", runtime.GOMAXPROCS(0))
	t.Logf("NumCPU: %d", runtime.NumCPU())
	t.Log("========================================================================")
}

// ============================================================================
// BENCHMARK 8: Multiple Literal Access Pattern (simulates real program)
// ============================================================================

func BenchmarkManyLiteralsAccess(b *testing.B) {
	// Simulate a program with 50 string literals being accessed once each
	const numLiterals = 50
	const literalSize = 64

	encData := make([][]byte, numLiterals)
	keys := make([][]byte, numLiterals)
	for i := range encData {
		encData[i] = make([]byte, literalSize)
		keys[i] = make([]byte, literalSize)
		rand.Read(encData[i])
		rand.Read(keys[i])
		for j := range encData[i] {
			encData[i][j] ^= keys[i][j]
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := range encData {
			data := make([]byte, literalSize)
			for k := range data {
				data[k] = encData[j][k] ^ keys[j][k]
			}
			_ = data[0]
		}
	}
}

// ============================================================================
// BENCHMARK 9: String Literal Size Scaling
// ============================================================================

func BenchmarkLiteralSize8(b *testing.B)   { benchmarkLiteralSize(b, 8) }
func BenchmarkLiteralSize16(b *testing.B)  { benchmarkLiteralSize(b, 16) }
func BenchmarkLiteralSize32(b *testing.B)  { benchmarkLiteralSize(b, 32) }
func BenchmarkLiteralSize64(b *testing.B)  { benchmarkLiteralSize(b, 64) }
func BenchmarkLiteralSize128(b *testing.B) { benchmarkLiteralSize(b, 128) }
func BenchmarkLiteralSize256(b *testing.B) { benchmarkLiteralSize(b, 256) }
func BenchmarkLiteralSize512(b *testing.B) { benchmarkLiteralSize(b, 512) }
func BenchmarkLiteralSize1024(b *testing.B) { benchmarkLiteralSize(b, 1024) }

func benchmarkLiteralSize(b *testing.B, size int) {
	enc := make([]byte, size)
	key := make([]byte, size)
	rand.Read(enc)
	rand.Read(key)
	b.ReportAllocs()
	b.SetBytes(int64(size))
	for i := 0; i < b.N; i++ {
		data := make([]byte, size)
		for j := range data {
			data[j] = enc[j] ^ key[j]
		}
		_ = data
	}
}

// ============================================================================
// BENCHMARK 10: Source code size generated by obfuscator
// ============================================================================

func BenchmarkGeneratedSourceSize(b *testing.B) {
	sizes := []int{16, 64, 128, 256, 512, 1024}
	for _, sz := range sizes {
		b.Run(fmt.Sprintf("size_%d", sz), func(b *testing.B) {
			rnd := rand.New(rand.NewSource(42))
			data := make([]byte, sz)
			rand.Read(data)
			extKeys := randExtKeys(rnd)
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				w := wide{}
				block := w.obfuscate(rnd, data, extKeys)
				_ = len(block.List)
			}
		})
	}
}

// ============================================================================
// Helper to count generated source lines
// ============================================================================

func TestGeneratedSourceLineCounts(t *testing.T) {
	sizes := []int{16, 32, 64, 128, 256}
	for _, sz := range sizes {
		rnd := rand.New(rand.NewSource(42))
		data := make([]byte, sz)
		rand.Read(data)
		extKeys := randExtKeys(rnd)
		w := wide{}
		block := w.obfuscate(rnd, data, extKeys)

		// Convert AST back to source to count lines
		fset := token.NewFileSet()
		file := &ast.File{
			Name:  ast.NewIdent("test"),
			Decls: []ast.Decl{&ast.FuncDecl{
				Name: ast.NewIdent("test"),
				Type: &ast.FuncType{},
				Body: block,
			}},
		}

		var buf strings.Builder
		// Use go/format to print the AST
		t.Logf("literal_size=%d generated_ast_stmts=%d", sz, len(block.List))
		_ = fset
		_ = file
		_ = buf
	}
}
