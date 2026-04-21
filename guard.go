package main

import (
	"fmt"
	"math/bits"
	mathrand "math/rand"
	"strings"
)

const (
	guardTables       = 36
	guardTablesVar    = 18
	guardTableSize    = 5600
	guardTableSizeVar = 2400
	guardHeavy        = 50
	guardHeavyVar     = 50
	guardMedium       = 50
	guardMediumVar    = 50
	guardLight        = 50
	guardLightVar     = 45000

	ballastTables       = 36
	ballastTablesVar    = 248
	ballastTableSize    = 5600
	ballastTableSizeVar = 3400
	ballastHeavy        = 50
	ballastHeavyVar     = 50
	ballastMedium       = 50
	ballastMediumVar    = 50
	ballastLight        = 50
	ballastLightVar     = 45000
)

type tableFLEntry struct {
	name        string
	first, last uint64
}

var hexEscapes [256][4]byte

func init() {
	const h = "0123456789abcdef"
	for i := range hexEscapes {
		hexEscapes[i] = [4]byte{'\\', 'x', h[i>>4], h[i&0xF]}
	}
}

func generateGuardSource(rnd *mathrand.Rand, pkgName string, scale float64) string {
	g := &ggen{r: rnd, ids: make(map[string]string)}
	g.emitScaled(pkgName, scale)
	return g.sb.String()
}

func generateGuardSourceSmall(rnd *mathrand.Rand) string {
	g := &ggen{r: rnd, ids: make(map[string]string)}
	g.emitSmall()
	return g.sb.String()
}

func generateGuardSourceBallastOnly(rnd *mathrand.Rand, pkgName string, scale float64) string {
	g := &ggen{r: rnd, ids: make(map[string]string)}
	g.emitBallastOnly(pkgName, scale)
	return g.sb.String()
}

func (g *ggen) emitBallastOnly(pkgName string, scale float64) {
	g.wf("package %s\n\n", pkgName)
	numTables := scaleInt(ballastTables+g.r.Intn(ballastTablesVar), scale)
	tableSize := scaleInt(ballastTableSize+g.r.Intn(ballastTableSizeVar), scale)
	numHeavy := scaleInt(ballastHeavy+g.r.Intn(ballastHeavyVar), scale)
	numMedium := scaleInt(ballastMedium+g.r.Intn(ballastMediumVar), scale)
	numLight := scaleInt(ballastLight+g.r.Intn(ballastLightVar), scale)
	tableNames := g.emitLookupTables(numTables, tableSize)
	g.emitGlobalsBallastOnly()
	g.emitBallastOnlyInit(tableNames, numHeavy)
	g.emitPrimitives()
	ballastNames := g.emitBallastFunctions(tableNames, numHeavy, numMedium, numLight)
	g.emitBallastChain(ballastNames)
}

func (g *ggen) emitGlobalsBallastOnly() {
	g.w("var _gsecActive bool\n")
	g.wf("var %s bool\n", g.id("gsecA"))
	g.wf("var %s bool\n", g.id("gsecB"))
	g.wf("var %s bool\n\n", g.id("gsecC"))
	g.wf("var %s uint32\n\n", g.id("gsecLevel"))
	for i := 0; i < 6+g.r.Intn(6); i++ {
		g.wf("var %s uint64 = %s\n", g.fresh(), g.u64())
	}
	g.nl()
}

func (g *ggen) emitBallastOnlyInit(tableNames []string, numHeavy int) {
	h := tableNames[0]
	chainName := g.id("chain")
	gsecA := g.id("gsecA")
	gsecB := g.id("gsecB")
	gsecC := g.id("gsecC")
	gsecLevel := g.id("gsecLevel")
	g.w("func init() {\n")
	g.wf("\t_ = %s[uint(%s[0])%%uint(%d)](uint64(%s[1]))\n", chainName, h, numHeavy, h)
	for _, bv := range g.ballastVars {
		g.wf("\t_ = %s[0]\n", bv)
	}
	g.wf("\t%s = true\n", gsecA)
	g.wf("\t%s = true\n", gsecB)
	g.wf("\t%s = true\n", gsecC)
	g.wf("\t%s = 3\n", gsecLevel)
	g.wf("\t_gsecActive = %s && %s && %s && %s == 3\n}\n\n", gsecA, gsecB, gsecC, gsecLevel)
}

type ggen struct {
	r               *mathrand.Rand
	ids             map[string]string
	sb              strings.Builder
	seq             int
	ballastVars     []string
	extraParamNames []string
	lookupSize      int
	lookupMask      int
	tableFL         []tableFLEntry
}

func (g *ggen) id(role string) string {
	if n, ok := g.ids[role]; ok {
		return n
	}
	n := g.fresh()
	g.ids[role] = n
	return n
}

func (g *ggen) fresh() string {
	var b [5]byte
	g.r.Read(b[:])
	s := fmt.Sprintf("_g%x%03x", b, g.seq)
	g.seq++
	return s
}

func (g *ggen) u64() string  { return fmt.Sprintf("0x%016X", g.r.Uint64()) }
func (g *ggen) u32() string  { return fmt.Sprintf("0x%08X", g.r.Uint32()) }
func (g *ggen) ou64() string { return fmt.Sprintf("0x%016X", g.r.Uint64()|1) }
func (g *ggen) rot() int     { return 1 + g.r.Intn(62) }

func (g *ggen) w(s string)            { g.sb.WriteString(s) }
func (g *ggen) wf(f string, a ...any) { fmt.Fprintf(&g.sb, f, a...) }
func (g *ggen) nl()                   { g.sb.WriteByte('\n') }

func (g *ggen) emitScaled(pkgName string, scale float64) {
	g.emitWithParams(pkgName,
		scaleInt(guardTables+g.r.Intn(guardTablesVar), scale),
		scaleInt(guardTableSize+g.r.Intn(guardTableSizeVar), scale),
		scaleInt(guardHeavy+g.r.Intn(guardHeavyVar), scale),
		scaleInt(guardMedium+g.r.Intn(guardMediumVar), scale),
		scaleInt(guardLight+g.r.Intn(guardLightVar), scale),
	)
}

func scaleInt(v int, scale float64) int {
	n := int(float64(v) * scale)
	if n < 1 {
		return 1
	}
	return n
}

func (g *ggen) emitSmall() {
	g.emitWithParams("main", 4+g.r.Intn(4), 32+g.r.Intn(32), 3+g.r.Intn(3), 3+g.r.Intn(3), 2+g.r.Intn(2))
}

func (g *ggen) emitWithParams(pkgName string, numTables, tableSize, numHeavy, numMedium, numLight int) {

	g.emitHeader(pkgName)
	tableNames := g.emitLookupTables(numTables, tableSize)
	g.emitGlobals()
	numEP := 4 + g.r.Intn(5)
	epNames := make([]string, numEP)
	for i := range epNames {
		epNames[i] = g.fresh()
	}
	g.extraParamNames = epNames
	g.emitExtraParamBallast(tableNames, epNames)
	g.emitInitAndRun(tableNames, numHeavy)
	g.emitFail()
	if flagDebug {
		g.emitDbgWrite()
	}
	g.emitExePath()
	g.emitValidatePath()
	g.emitOpenExe()
	g.emitGetSize()
	g.emitReadTrailer()
	g.emitMagicCheck()
	g.emitSizeCheck()
	g.emitSHA256()
	g.emitHashBody()
	g.emitCmpHash()
	g.emitPrimitives()
	ballastNames := g.emitBallastFunctions(tableNames, numHeavy, numMedium, numLight)
	g.emitBallastChain(ballastNames)
}

func (g *ggen) emitHeader(pkgName string) {
	if flagDebug {
		g.wf("package %s\n\nimport (\n\t\"fmt\"\n\t\"os\"\n\t\"runtime\"\n\t\"unsafe\"\n)\n\n", pkgName)
	} else {
		g.wf("package %s\n\nimport (\n\t\"os\"\n\t\"unsafe\"\n)\n\n", pkgName)
	}
}

func (g *ggen) emitDbgWrite() {
	if !flagDebug {
		return
	}
	name := g.id("dbgWrite")
	g.wf("func %s(code int) {\n", name)
	g.w("\tvar desc string\n\tvar detail string\n\tswitch code {\n")
	g.w("\tcase 1:\n\t\tdesc = \"os.Executable() failed — cannot locate own binary\"\n")
	g.w("\t\tdetail = \"The process could not determine its own executable path.\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]   Possible causes: running from a deleted file, sandboxed /proc access,\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]     running on a platform where /proc/self/exe is unavailable,\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]     or the binary was launched via a pipe/stdin redirect without a path.\"\n")
	g.w("\tcase 2:\n\t\tdesc = \"executable path validation failed — path is empty, too long, or contains null bytes\"\n")
	g.w("\t\tdetail = \"The executable path returned by os.Executable() did not pass length and character checks.\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]   Path must be 1–4096 bytes and must not contain null (0x00) bytes.\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]   This usually means a very unusual launch environment or filesystem corruption.\"\n")
	g.w("\tcase 3:\n\t\tdesc = \"os.Open() failed — cannot open own binary file for reading\"\n")
	g.w("\t\tdetail = \"The process found its own path but could not open the file.\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]   Possible causes: file was deleted after launch, insufficient read permissions,\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]     filesystem unmounted, or the binary is on a network share that disconnected.\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]   Try running the binary from a local filesystem with full read permissions.\"\n")
	g.w("\tcase 4:\n\t\tdesc = \"f.Stat() failed — cannot read own binary file size\"\n")
	g.w("\t\tdetail = \"The file was opened successfully but stat() failed or returned an unexpected size.\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]   Possible causes: file truncated to zero bytes, special device file,\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]     file > 1 GB (rejected by size guard), or OS-level stat error.\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]   Expected: a normal executable file between 112 bytes and 1 GiB.\"\n")
	g.w("\tcase 5:\n\t\tdesc = \"ReadAt() failed — cannot read the 48-byte integrity trailer from the binary\"\n")
	g.w("\t\tdetail = \"The last 48 bytes of the binary could not be read.\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]   Possible causes: the binary was stripped or truncated after build,\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]     the binary is smaller than 48 bytes (not a valid garbled binary),\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]     or a filesystem I/O error occurred during the read.\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]   Solution: ensure the binary was built with alosgarble and was not modified post-build.\"\n")
	g.w("\tcase 6:\n\t\tdesc = \"magic bytes mismatch — binary integrity header signature is wrong\"\n")
	g.w("\t\tdetail = \"The 8-byte magic signature at bytes [0..7] of the trailer does not match the expected value.\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]   Expected magic: F3 A9 2C 71 DE 5B 8E 04\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]   Possible causes:\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]     - Binary was patched, hex-edited, or disassembled and reassembled\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]     - Antivirus/EDR modified or quarantined the file\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]     - Binary was not built with alosgarble (missing trailer entirely)\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]     - File was truncated or the wrong file is being executed\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]     - Binary was stripped with 'strip', 'objcopy --strip-all', or similar tool\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]   Solution: rebuild the binary from source using alosgarble.\"\n")
	g.w("\tcase 7:\n\t\tdesc = \"file size field mismatch — trailer reports different size than actual file\"\n")
	g.w("\t\tdetail = \"The 8-byte size field at bytes [8..15] of the trailer does not match the actual file size.\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]   Possible causes:\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]     - Bytes were appended to the binary after build (e.g. by an installer or packer)\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]     - Bytes were removed from the binary (truncation)\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]     - File was modified by code signing, notarization, or UPX packing\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]   Solution: do not modify the binary after it is built by alosgarble.\"\n")
	g.w("\tcase 8:\n\t\tdesc = \"SHA-256 hash computation failed — could not read entire binary body\"\n")
	g.w("\t\tdetail = \"The hash function could not read all bytes of the binary body (everything except the 48-byte trailer).\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]   Possible causes: read error mid-file, binary being modified concurrently,\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]     or insufficient memory to allocate the read buffer (file may be very large).\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]   Expected: a clean ReadAt of (file_size - 48) bytes from offset 0.\"\n")
	g.w("\tcase 9:\n\t\tdesc = \"SHA-256 hash mismatch — binary content does not match embedded hash\"\n")
	g.w("\t\tdetail = \"The SHA-256 hash of the binary body (bytes 0 to file_size-48) does not match\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]   the 32-byte hash stored in the trailer at bytes [16..47].\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]   This is the most critical check — it means the binary was CHANGED after build.\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]   Possible causes:\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]     - Antivirus/EDR injected code or patched the binary\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]     - Manual hex-editing or binary patching\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]     - Code signing tool rewrote sections after garble build\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]     - Linker post-processing step modified the binary\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]     - File corruption during download/copy\\n\" +\n")
	g.w("\t\t\t\"[GARBLE-DEBUG]   Solution: rebuild from source and do not post-process the binary.\"\n")
	g.w("\tdefault:\n")
	g.w("\t\tif code >= 10 && code < 20 {\n")
	g.w("\t\t\tdesc = fmt.Sprintf(\"lookup table cross-check #%d failed\", code-10)\n")
	g.w("\t\t\tdetail = \"An internal integrity cross-check between two lookup tables failed.\\n\" +\n")
	g.w("\t\t\t\t\"[GARBLE-DEBUG]   This means the binary's data section was modified after build.\\n\" +\n")
	g.w("\t\t\t\t\"[GARBLE-DEBUG]   The embedded lookup tables are used as tamper-detection checksums.\\n\" +\n")
	g.w("\t\t\t\t\"[GARBLE-DEBUG]   Possible causes: binary patching, hex-editing of .rodata, or packer modification.\"\n")
	g.w("\t\t} else {\n")
	g.w("\t\t\tdesc = fmt.Sprintf(\"unknown integrity stage %d\", code)\n")
	g.w("\t\t\tdetail = \"An unrecognised check stage failed. This should not happen in a normal build.\"\n")
	g.w("\t\t}\n")
	g.w("\t}\n")
	g.w("\tmsg := fmt.Sprintf(\n")
	g.w("\t\t\"\\n[GARBLE-DEBUG] ====== SECURITY CHECK FAILED — PROCESS WILL EXIT ======\\n\"+\n")
	g.w("\t\t\"[GARBLE-DEBUG] Stage code  : %d\\n\"+\n")
	g.w("\t\t\"[GARBLE-DEBUG] Failure     : %s\\n\"+\n")
	g.w("\t\t\"[GARBLE-DEBUG] Detail      : %s\\n\"+\n")
	g.w("\t\t\"[GARBLE-DEBUG] PID         : %d\\n\",\n")
	g.w("\t\tcode, desc, detail, os.Getpid(),\n")
	g.w("\t)\n")
	g.w("\tfmt.Fprint(os.Stderr, msg)\n")
	g.w("\tbuf := make([]byte, 65536)\n")
	g.w("\tn := runtime.Stack(buf, true)\n")
	g.w("\tstackStr := fmt.Sprintf(\n")
	g.w("\t\t\"[GARBLE-DEBUG] Call stack at failure point (all goroutines):\\n%s\\n\"+\n")
	g.w("\t\t\"[GARBLE-DEBUG] ============================\\n\", buf[:n])\n")
	g.w("\tfmt.Fprint(os.Stderr, stackStr)\n")
	g.w("\texe, _ := os.Executable()\n")
	g.w("\tif exe != \"\" {\n")
	g.w("\t\tlogPath := fmt.Sprintf(\"%s.garbledebug_%d.log\", exe, os.Getpid())\n")
	g.w("\t\tlf, lerr := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)\n")
	g.w("\t\tif lerr == nil {\n")
	g.w("\t\t\tlf.WriteString(msg + stackStr)\n")
	g.w("\t\t\tlf.Close()\n")
	g.w("\t\t}\n")
	g.w("\t}\n")
	g.w("}\n\n")
}

func (g *ggen) emitLookupTables(count, size int) []string {
	if g.lookupSize == 0 {
		lookupBits := 7 + g.r.Intn(2)
		g.lookupSize = 1 << lookupBits
		g.lookupMask = g.lookupSize - 1
	}
	lookupEntries := g.lookupSize

	names := make([]string, count)
	for i := range names {
		names[i] = g.fresh()
	}

	for _, name := range names {
		g.wf("var %s = [%d]uint64{", name, lookupEntries)
		var firstVal, lastVal uint64
		for j := 0; j < lookupEntries; j++ {
			if j%8 == 0 {
				g.w("\n\t")
			}
			v := g.r.Uint64()
			if j == 0 {
				firstVal = v
			}
			lastVal = v
			g.wf("0x%016X,", v)
		}
		g.w("\n}\n\n")

		g.tableFL = append(g.tableFL, tableFLEntry{name: name, first: firstVal, last: lastVal})

		ballastElems := size - lookupEntries
		if ballastElems > 0 {
			bname := g.fresh()
			g.ballastVars = append(g.ballastVars, bname)
			ballastBytes := ballastElems * 8
			var sb strings.Builder
			sb.Grow(ballastBytes*4 + 3)
			sb.WriteByte('"')
			for j := 0; j < ballastElems; j++ {
				v := g.r.Uint64()
				for k := 0; k < 8; k++ {
					sb.Write(hexEscapes[byte(v>>(k*8))][:])
				}
			}
			sb.WriteByte('"')
			g.wf("var %s = %s\n\n", bname, sb.String())
		}
	}
	return names
}

func (g *ggen) emitGlobals() {
	g.wf("var %s = [64]uint32{\n", g.id("shaK"))
	for _, v := range gsha256K {
		g.wf("\t0x%08X,\n", v)
	}
	g.w("}\n\n")

	g.wf("var %s = [8]uint32{\n", g.id("shaH0"))
	for _, v := range gsha256H0 {
		g.wf("\t0x%08X,\n", v)
	}
	g.w("}\n\n")

	g.wf("var %s = [8]byte{0xF3,0xA9,0x2C,0x71,0xDE,0x5B,0x8E,0x04}\n\n", g.id("magic"))

	g.w("var _gsecActive bool\n")
	g.wf("var %s bool\n", g.id("gsecA"))
	g.wf("var %s bool\n", g.id("gsecB"))
	g.wf("var %s bool\n\n", g.id("gsecC"))
	g.wf("var %s uint32\n\n", g.id("gsecLevel"))

	for i := 0; i < 6+g.r.Intn(6); i++ {
		g.wf("var %s uint64 = %s\n", g.fresh(), g.u64())
	}
	g.nl()
}

func (g *ggen) emitInitAndRun(tableNames []string, numHeavy int) {
	runName := g.id("run")
	failName := g.id("fail")
	exePathName := g.id("exePath")
	validatePath := g.id("validatePath")
	openExeName := g.id("openExe")
	getSizeName := g.id("getSize")
	readTrailerName := g.id("readTrailer")
	magicOkName := g.id("magicOk")
	sizeOkName := g.id("sizeOk")
	hashBodyName := g.id("hashBody")
	cmpHashName := g.id("cmpHash")
	chainName := g.id("chain")
	gsecA := g.id("gsecA")
	gsecB := g.id("gsecB")
	gsecC := g.id("gsecC")
	gsecLevel := g.id("gsecLevel")

	g.wf("func init() { %s() }\n\n", runName)
	g.wf("func %s() {\n", runName)

	pv := g.fresh()
	g.wf("\t%s := uint64(%s)\n", pv, g.u64())

	g.wf("\t{ _uptr := uint64(uintptr(unsafe.Pointer(&%s[0]))); %s ^= _uptr*%s }\n",
		tableNames[0], pv, g.ou64())

	for i := 0; i < 4+g.r.Intn(6); i++ {
		g.emitMixStmt(pv, tableNames, 1)
	}
	g.wf("\t_ = %s\n", pv)

	ep, ok1 := g.fresh(), g.fresh()
	g.wf("\t%s, %s := %s()\n", ep, ok1, exePathName)
	g.wf("\tif !%s { %s(1); return }\n", ok1, failName)
	g.wf("\tif !%s(%s) { %s(2); return }\n", validatePath, ep, failName)

	fp, ok2 := g.fresh(), g.fresh()
	g.wf("\t%s, %s := %s(%s)\n", fp, ok2, openExeName, ep)
	g.wf("\tif !%s { %s(3); return }\n", ok2, failName)

	sz, ok3 := g.fresh(), g.fresh()
	g.wf("\t%s, %s := %s(%s)\n", sz, ok3, getSizeName, fp)
	g.wf("\tif !%s { %s.Close(); %s(4); return }\n", ok3, fp, failName)

	tr, ok4 := g.fresh(), g.fresh()
	g.wf("\t%s, %s := %s(%s, %s)\n", tr, ok4, readTrailerName, fp, sz)
	g.wf("\tif !%s { %s.Close(); %s(5); return }\n", ok4, fp, failName)

	g.wf("\tif !%s(%s) { %s.Close(); %s(6); return }\n", magicOkName, tr, fp, failName)
	g.wf("\tif !%s(%s, %s) { %s.Close(); %s(7); return }\n", sizeOkName, tr, sz, fp, failName)

	h, ok5 := g.fresh(), g.fresh()
	g.wf("\t%s, %s := %s(%s, %s)\n", h, ok5, hashBodyName, fp, sz)
	g.wf("\t%s.Close()\n", fp)
	g.wf("\tif !%s { %s(8); return }\n", ok5, failName)
	g.wf("\tif !%s(%s, %s) { %s(9); return }\n", cmpHashName, h, tr, failName)

	g.wf("\t%s = true\n", gsecA)
	g.wf("\t%s++\n", gsecLevel)

	numCV := 8 + g.r.Intn(8) // 8–15 cross-checks
	if numCV > len(g.tableFL)-1 {
		numCV = len(g.tableFL) - 1
	}
	if numCV > 0 {
		perm := g.r.Perm(len(g.tableFL) - 1)
		for i := 0; i < numCV; i++ {
			idx := perm[i]
			fl0 := g.tableFL[idx]
			fl1 := g.tableFL[idx+1]
			rot := 1 + g.r.Intn(62)
			expected := bits.RotateLeft64(fl0.last, rot) ^ fl1.first
			g.wf("\t{ _cv := (%s[%d]<<%d)|(%s[%d]>>%d); if _cv^%s[0] != 0x%016X { %s(1%d); return } }\n",
				fl0.name, g.lookupSize-1, rot,
				fl0.name, g.lookupSize-1, 64-rot,
				fl1.name, expected,
				failName, i)
		}
	}

	for i, epName := range g.extraParamNames {
		tbl := tableNames[g.r.Intn(len(tableNames))]
		g.wf("\t{ _ep%d := %s(uint64(%s[%d&%d]), %s, %s); _ = _ep%d }\n",
			i, epName, tbl, i, g.lookupMask, g.u64(), g.u64(), i)
	}

	g.wf("\t_ = %s[uint(%s[31])%%uint(%d)](uint64(%s))\n", chainName, h, numHeavy, sz)

	g.wf("\t%s = true\n", gsecB)
	g.wf("\t%s++\n", gsecLevel)

	for _, bv := range g.ballastVars {
		g.wf("\t_ = %s[0]\n", bv)
	}

	g.wf("\t%s = true\n", gsecC)
	g.wf("\t%s++\n", gsecLevel)
	g.wf("\t_gsecActive = %s && %s && %s && %s == 3\n}\n\n",
		gsecA, gsecB, gsecC, gsecLevel)
}

func (g *ggen) emitMixStmt(v string, tables []string, indent int) {
	tab := strings.Repeat("\t", indent)
	tbl := tables[g.r.Intn(len(tables))]
	m := g.lookupMask
	switch g.r.Intn(6) {
	case 0:
		g.wf("%s%s ^= %s[%s&%d]\n", tab, v, tbl, v, m)
	case 1:
		g.wf("%s%s += %s[(%s>>8)&%d]\n", tab, v, tbl, v, m)
	case 2:
		r := g.rot()
		g.wf("%s%s = (%s<<%d)|(%s>>%d)\n", tab, v, v, r, v, 64-r)
	case 3:
		g.wf("%s%s ^= %s ^ %s[(%s^%s)&%d]\n", tab, v, g.u64(), tbl, v, g.u64(), m)
	case 4:
		g.wf("%s%s = %s*%s ^ %s[%s&%d]\n", tab, v, v, g.ou64(), tbl, v, m)
	case 5:
		r := g.rot()
		g.wf("%s%s = ((%s>>%d)|(%s<<%d)) ^ %s\n", tab, v, v, r, v, 64-r, g.u64())
	}
}

func (g *ggen) emitFail() {
	name := g.id("fail")
	k1, k2, k3 := g.ou64(), g.ou64(), g.ou64()
	r1, r2, r3 := g.rot(), g.rot(), g.rot()
	xv := g.u64()
	gsecA := g.id("gsecA")
	gsecB := g.id("gsecB")
	gsecC := g.id("gsecC")

	g.wf("func %s(code int) {\n", name)
	g.wf("\t_gsecActive = false\n")
	g.wf("\t%s = false\n", gsecA)
	g.wf("\t%s = false\n", gsecB)
	g.wf("\t%s = false\n", gsecC)
	if flagDebug {
		g.wf("\t%s(code)\n", g.id("dbgWrite"))
	}
	g.wf("\tacc := uint64(code)*%s ^ %s\n", k1, xv)
	g.wf("\tacc = (acc<<%d)|(acc>>%d)\n", r1, 64-r1)
	g.wf("\tacc = acc*%s ^ uint64(code+1)\n", k2)
	g.wf("\tacc ^= acc>>31\n")
	g.wf("\tacc = (acc<<%d)|(acc>>%d)\n", r2, 64-r2)
	g.wf("\tacc = acc*%s\n", k3)
	g.wf("\tacc ^= acc>>17\n")
	g.wf("\tacc = (acc<<%d)|(acc>>%d)\n", r3, 64-r3)
	g.w("\tos.Exit(int(acc&0xFE) + 1)\n}\n\n")
}

func (g *ggen) emitExePath() {
	g.wf("func %s() (string, bool) {\n", g.id("exePath"))
	g.w("\tp, err := os.Executable()\n")
	g.w("\tif err != nil { return \"\", false }\n")
	g.w("\tif len(p) == 0 || len(p) > 4096 { return \"\", false }\n")
	g.w("\treturn p, true\n}\n\n")
}

func (g *ggen) emitValidatePath() {
	g.wf("func %s(p string) bool {\n", g.id("validatePath"))
	g.w("\tn := len(p)\n")
	g.w("\tif n < 1 || n > 4096 { return false }\n")
	g.w("\tfor i := 0; i < n; i++ { if p[i] == 0 { return false } }\n")
	g.w("\treturn true\n}\n\n")
}

func (g *ggen) emitOpenExe() {
	g.wf("func %s(p string) (*os.File, bool) {\n", g.id("openExe"))
	g.w("\tif len(p) == 0 { return nil, false }\n")
	g.w("\tf, err := os.Open(p)\n")
	g.w("\tif err != nil || f == nil { return nil, false }\n")
	g.w("\treturn f, true\n}\n\n")
}

func (g *ggen) emitGetSize() {
	g.wf("func %s(f *os.File) (int64, bool) {\n", g.id("getSize"))
	g.w("\tif f == nil { return 0, false }\n")
	g.w("\tinfo, err := f.Stat()\n")
	g.w("\tif err != nil { return 0, false }\n")
	g.w("\tsz := info.Size()\n")
	g.w("\tif sz < 112 || sz > 1<<30 { return 0, false }\n")
	g.w("\treturn sz, true\n}\n\n")
}

func (g *ggen) emitReadTrailer() {
	g.wf("func %s(f *os.File, size int64) ([48]byte, bool) {\n", g.id("readTrailer"))
	g.w("\tvar t [48]byte\n")
	g.w("\tif f == nil { return t, false }\n")
	g.w("\toff := size - 48\n")
	g.w("\tif off < 0 { return t, false }\n")
	g.w("\tn, err := f.ReadAt(t[:], off)\n")
	g.w("\tif err != nil || n != 48 { return t, false }\n")
	g.w("\treturn t, true\n}\n\n")
}

func (g *ggen) emitMagicCheck() {
	master := g.id("magicOk")
	checkers := make([]string, 8)
	for i := range checkers {
		checkers[i] = g.fresh()
	}
	magic := [8]byte{0xF3, 0xA9, 0x2C, 0x71, 0xDE, 0x5B, 0x8E, 0x04}
	magicName := g.id("magic")

	g.wf("func %s(t [48]byte) bool {\n", master)
	for _, c := range checkers {
		g.wf("\tif !%s(t) { return false }\n", c)
	}
	g.w("\treturn true\n}\n\n")

	for i, c := range checkers {
		m := magic[i]
		g.wf("func %s(t [48]byte) bool {\n", c)
		g.wf("\tv := t[%d]\n", i)
		switch i & 3 {
		case 0:
			g.wf("\tif v != %s[%d] { return false }\n", magicName, i)
			g.wf("\t_ = uint64(v)*%s\n", g.ou64())
		case 1:
			g.wf("\tif v > %s[%d] || v < %s[%d] { return false }\n", magicName, i, magicName, i)
			g.wf("\tx := uint64(v)^uint64(v)>>3\n")
			g.wf("\tif x == 0 && v != 0 { return false }\n")
		case 2:
			g.wf("\tif v != %s[%d] { return false }\n", magicName, i)
			g.wf("\tx := uint32(v)*%s; _ = x\n", g.u32())
		case 3:
			k := g.ou64()
			g.wf("\ta := uint64(v)*%s\n", k)
			g.wf("\tb := uint64(%s[%d])*%s\n", magicName, i, k)
			g.wf("\tif a != b || v != 0x%02X { return false }\n", m)
		}
		g.w("\treturn true\n}\n\n")
	}
}

func (g *ggen) emitSizeCheck() {
	extract := g.fresh()
	sizeOk := g.id("sizeOk")

	g.wf("func %s(t [48]byte) uint64 {\n", extract)
	g.w("\tvar v uint64\n")
	g.w("\tfor i := 0; i < 8; i++ { v |= uint64(t[8+i]) << (uint(i)*8) }\n")
	g.w("\treturn v\n}\n\n")

	g.wf("func %s(t [48]byte, actual int64) bool {\n", sizeOk)
	g.wf("\texpected := %s(t)\n", extract)
	g.w("\tif expected == 0 || expected > 1<<30 { return false }\n")
	g.w("\tif int64(expected) != actual { return false }\n")
	g.w("\treturn true\n}\n\n")
}

func (g *ggen) emitSHA256() {
	blk := g.id("shaBlock")
	shaK := g.id("shaK")
	shaH0 := g.id("shaH0")
	_ = shaH0

	g.wf("func %s(state *[8]uint32, block *[64]byte) {\n", blk)
	g.w("\tvar w [64]uint32\n")
	g.w("\tfor i:=0; i<16; i++ { j:=i*4; w[i]=uint32(block[j])<<24|uint32(block[j+1])<<16|uint32(block[j+2])<<8|uint32(block[j+3]) }\n")

	g.w("\tfor i:=16; i<64; i++ {\n")
	g.w("\t\tv15:=w[i-15]; gam0:=((v15>>7)|(v15<<25))^((v15>>18)|(v15<<14))^(v15>>3)\n")
	g.w("\t\tv2:=w[i-2];   gam1:=((v2>>17)|(v2<<15))^((v2>>19)|(v2<<13))^(v2>>10)\n")
	g.w("\t\tw[i]=gam0+w[i-16]+gam1+w[i-7]\n")
	g.w("\t}\n")
	g.w("\ta,b,c,d,e,f,gg,h := state[0],state[1],state[2],state[3],state[4],state[5],state[6],state[7]\n")

	g.w("\tfor i:=0; i<64; i++ {\n")
	g.wf("\t\tsig1:=((e>>6)|(e<<26))^((e>>11)|(e<<21))^((e>>25)|(e<<7))\n")
	g.wf("\t\tch:=(e&f)|(^e&gg)\n")
	g.wf("\t\tt1:=h+sig1+ch+%s[i]+w[i]\n", shaK)
	g.wf("\t\tsig0:=((a>>2)|(a<<30))^((a>>13)|(a<<19))^((a>>22)|(a<<10))\n")
	g.wf("\t\tmaj:=(a&b)|(a&c)|(b&c)\n")
	g.wf("\t\tt2:=sig0+maj\n")
	g.w("\t\th=gg;gg=f;f=e;e=d+t1;d=c;c=b;b=a;a=t1+t2\n\t}\n")
	g.w("\tstate[0]+=a;state[1]+=b;state[2]+=c;state[3]+=d\n")
	g.w("\tstate[4]+=e;state[5]+=f;state[6]+=gg;state[7]+=h\n}\n\n")
}

func (g *ggen) emitHashBody() {
	name := g.id("hashBody")
	blk := g.id("shaBlock")
	shaH0 := g.id("shaH0")

	g.wf("func %s(f *os.File, size int64) ([32]byte, bool) {\n", name)
	g.w("\tvar zero [32]byte\n")
	g.w("\tif f == nil || size < 48 { return zero, false }\n")
	g.w("\tbodyLen := size - 48\n")
	g.w("\tif bodyLen < 0 || bodyLen > 1<<30 { return zero, false }\n")
	g.w("\tdata := make([]byte, bodyLen)\n")
	g.w("\tn, _ := f.ReadAt(data, 0)\n")
	g.w("\tif int64(n) != bodyLen { return zero, false }\n")
	g.wf("\tstate := %s\n", shaH0)
	g.w("\tvar block [64]byte\n\tvar pending [64]byte\n\tvar pendingN int\n")
	g.w("\tremaining := data\n")
	g.w("\tfor len(remaining) > 0 {\n")
	g.w("\t\tspace := 64 - pendingN\n")
	g.w("\t\tif len(remaining) < space {\n")
	g.w("\t\t\tcopy(pending[pendingN:], remaining); pendingN += len(remaining); remaining = nil\n")
	g.w("\t\t} else {\n")
	g.w("\t\t\tcopy(pending[pendingN:], remaining[:space])\n")
	g.wf("\t\t\tcopy(block[:], pending[:]); %s(&state, &block); pendingN = 0\n", blk)
	g.w("\t\t\tremaining = remaining[space:]\n")
	g.w("\t\t}\n\t}\n")
	g.w("\tbitLen := uint64(bodyLen)*8\n")
	g.w("\tvar padBuf [128]byte\n\tcopy(padBuf[:], pending[:pendingN])\n\tpadBuf[pendingN] = 0x80\n")
	g.w("\tvar padLen int\n\tif pendingN < 56 { padLen = 64 } else { padLen = 128 }\n")
	g.w("\tpadBuf[padLen-8]=byte(bitLen>>56);padBuf[padLen-7]=byte(bitLen>>48)\n")
	g.w("\tpadBuf[padLen-6]=byte(bitLen>>40);padBuf[padLen-5]=byte(bitLen>>32)\n")
	g.w("\tpadBuf[padLen-4]=byte(bitLen>>24);padBuf[padLen-3]=byte(bitLen>>16)\n")
	g.w("\tpadBuf[padLen-2]=byte(bitLen>>8);padBuf[padLen-1]=byte(bitLen)\n")
	g.wf("\tcopy(block[:], padBuf[:64]); %s(&state, &block)\n", blk)
	g.w("\tif padLen == 128 {\n")
	g.wf("\t\tcopy(block[:], padBuf[64:]); %s(&state, &block)\n", blk)
	g.w("\t}\n")
	g.w("\tvar out [32]byte\n")
	g.w("\tfor i:=0; i<8; i++ { j:=i*4; out[j]=byte(state[i]>>24);out[j+1]=byte(state[i]>>16);out[j+2]=byte(state[i]>>8);out[j+3]=byte(state[i]) }\n")
	g.w("\treturn out, true\n}\n\n")
}

func (g *ggen) emitCmpHash() {
	master := g.id("cmpHash")
	checkers := make([]string, 4)
	for i := range checkers {
		checkers[i] = g.fresh()
	}
	g.wf("func %s(got [32]byte, t [48]byte) bool {\n", master)
	for _, c := range checkers {
		g.wf("\tif !%s(got, t) { return false }\n", c)
	}
	g.w("\treturn true\n}\n\n")
	for i, c := range checkers {
		lo := i * 8
		g.wf("func %s(got [32]byte, t [48]byte) bool {\n", c)
		g.wf("\tfor i := %d; i < %d; i++ { if got[i] != t[16+i] { return false } }\n", lo, lo+8)
		g.w("\treturn true\n}\n\n")
	}
}

func (g *ggen) emitPrimitives() {
	rotl := g.id("rotl")
	xsh := g.id("xsh")

	g.wf("func %s(v uint64, n uint) uint64 {\n", rotl)
	g.w("\tif n == 0 { return v }\n\tn &= 63\n\treturn (v<<n)|(v>>(64-n))\n}\n\n")

	c1, c2, c3 := g.u64(), g.u64(), g.u64()
	s1 := 7 + g.r.Intn(6)
	s2 := 11 + g.r.Intn(6)
	s3 := 17 + g.r.Intn(6)
	g.wf("func %s(x uint64) uint64 {\n", xsh)
	g.wf("\tif x == 0 { x = %s }\n", c1)
	g.wf("\tx ^= x << %d\n", s1)
	g.wf("\tx ^= x >> %d\n", s2)
	g.wf("\tx ^= x << %d\n", s3)
	g.wf("\tx ^= %s ^ %s\n", c2, c3)
	g.w("\treturn x\n}\n\n")
}

func (g *ggen) emitBallastFunctions(tableNames []string, numHeavy, numMedium, numLight int) []string {
	total := numHeavy + numMedium + numLight
	names := make([]string, total)
	for i := range names {
		names[i] = g.fresh()
	}

	for i := 0; i < numHeavy; i++ {
		g.emitHeavyBallast(names[i], tableNames)
	}
	for i := numHeavy; i < numHeavy+numMedium; i++ {
		g.emitMediumBallast(names[i], tableNames, names[:i])
	}
	for i := numHeavy + numMedium; i < total; i++ {
		g.emitLightBallast(names[i], tableNames, names[:i])
	}
	return names
}

func (g *ggen) emitHeavyBallast(name string, tables []string) {
	tbl1 := tables[g.r.Intn(len(tables))]
	tbl2 := tables[g.r.Intn(len(tables))]
	tbl3 := tables[g.r.Intn(len(tables))]
	numOps := 120 + g.r.Intn(120)
	m := g.lookupMask

	g.wf("func %s(x uint64) uint64 {\n", name)
	g.wf("\tacc := x ^ %s\n", g.u64())
	g.wf("\tb := x * %s\n", g.ou64())

	for i := 0; i < numOps; i++ {
		switch g.r.Intn(10) {
		case 0:
			g.wf("\tacc ^= %s[acc&%d] + b\n", tbl1, m)
		case 1:
			g.wf("\tacc = acc*%s ^ %s[b&%d]\n", g.ou64(), tbl2, m)
		case 2:
			r := g.rot()
			g.wf("\tacc = (acc<<%d)|(acc>>%d)\n", r, 64-r)
		case 3:
			g.wf("\tb ^= %s[b&%d] ^ acc\n", tbl1, m)
		case 4:
			g.wf("\tacc += %s[(acc^b)&%d]\n", tbl2, m)
		case 5:
			r := g.rot()
			g.wf("\tb = (b<<%d)|(b>>%d) ^ acc\n", r, 64-r)
		case 6:
			g.wf("\tacc, b = b, acc+b*%s\n", g.ou64())
		case 7:
			g.wf("\tacc ^= %s[acc&%d] ^ %s[b&%d] ^ %s[(acc>>32)&%d]\n",
				tbl1, m, tbl2, m, tbl3, m)
		case 8:
			g.wf("\tif %s[acc&%d]|%s[b&%d]|%s[(acc>>32)&%d] != 0 { acc ^= %s }\n",
				tbl1, m, tbl2, m, tbl3, m, g.u64())
		case 9:
			r := g.rot()
			g.wf("\tacc = ((acc<<%d)|(acc>>%d)) + b*%s\n", r, 64-r, g.ou64())
		}
	}
	g.w("\treturn acc ^ b\n}\n\n")
}

func (g *ggen) emitMediumBallast(name string, tables []string, prev []string) {
	tbl := tables[g.r.Intn(len(tables))]
	tbl2 := tables[g.r.Intn(len(tables))]
	numOps := 30 + g.r.Intn(20)
	k1, k2 := g.ou64(), g.ou64()
	r1 := g.rot()
	m := g.lookupMask

	g.wf("func %s(x uint64) uint64 {\n", name)
	g.wf("\tacc := x*%s ^ %s\n", k1, g.u64())
	if len(prev) > 0 && g.r.Intn(2) == 0 {
		other := prev[g.r.Intn(len(prev))]
		g.wf("\tacc = %s(acc ^ %s)\n", other, g.u64())
	}

	for i := 0; i < numOps; i++ {
		switch g.r.Intn(8) {
		case 0:
			g.wf("\tacc ^= %s[acc&%d]\n", tbl, m)
		case 1:
			g.wf("\tacc = acc*%s + %s\n", k2, g.u64())
		case 2:
			g.wf("\tacc = (acc<<%d)|(acc>>%d)\n", r1, 64-r1)
		case 3:
			g.wf("\tacc += %s[(acc>>8)&%d] ^ %s\n", tbl, m, g.u64())
		case 4:
			if len(prev) > 0 {
				other := prev[g.r.Intn(len(prev))]
				g.wf("\tacc = %s(acc)\n", other)
			} else {
				g.wf("\tacc ^= %s\n", g.u64())
			}
		case 5:
			g.wf("\tacc ^= %s[acc&%d] ^ %s[(acc>>16)&%d]\n", tbl, m, tbl2, m)
		case 6:
			g.wf("\tacc = acc*%s ^ (acc>>32)\n", g.ou64())
		case 7:
			g.wf("\tif %s[acc&%d]|%s[(acc>>16)&%d] != 0 { acc ^= %s }\n",
				tbl, m, tbl2, m, g.u64())
		}
	}
	g.w("\treturn acc\n}\n\n")
}

func (g *ggen) emitLightBallast(name string, tables []string, prev []string) {
	tbl := tables[g.r.Intn(len(tables))]
	k := g.ou64()
	r := g.rot()
	xsh := g.id("xsh")
	rotl := g.id("rotl")
	m := g.lookupMask

	g.wf("func %s(x uint64) uint64 {\n", name)
	g.wf("\tv := x*%s\n", k)
	for i := 0; i < 4+g.r.Intn(6); i++ {
		switch g.r.Intn(6) {
		case 0:
			g.wf("\tv ^= %s[v&%d]\n", tbl, m)
		case 1:
			g.wf("\tv = %s(v, %d)\n", rotl, r)
		case 2:
			g.wf("\tv = %s(v ^ %s)\n", xsh, g.u64())
		case 3:
			if len(prev) > 0 {
				g.wf("\tv = %s(v)\n", prev[g.r.Intn(len(prev))])
			} else {
				g.wf("\tv ^= %s\n", g.u64())
			}
		case 4:
			g.wf("\tv += %s[(v>>8)&%d]\n", tbl, m)
		case 5:
			g.wf("\tv = (v*%s) ^ (v>>32)\n", g.ou64())
		}
	}
	g.w("\treturn v\n}\n\n")
}

func (g *ggen) emitExtraParamBallast(tableNames []string, names []string) {
	m := g.lookupMask
	for _, name := range names {
		tbl := tableNames[g.r.Intn(len(tableNames))]
		p1, p2 := g.fresh(), g.fresh()
		k1, k2 := g.ou64(), g.ou64()
		r := g.rot()
		g.wf("func %s(x uint64, %s uint64, %s uint64) uint64 {\n", name, p1, p2)
		g.wf("\tacc := x*%s ^ %s\n", k1, g.u64())
		g.wf("\tacc ^= %s[(%s^%s)&%d]\n", tbl, p1, p2, m)
		g.wf("\tacc = acc*%s + %s[(acc^%s)&%d]\n", k2, tbl, g.u64(), m)
		g.wf("\tacc = (acc<<%d)|(acc>>%d)\n", r, 64-r)
		g.w("\treturn acc\n}\n\n")
	}
}

func (g *ggen) emitBallastChain(ballastNames []string) {
	chainName := g.id("chain")

	g.wf("var %s = [%d]func(uint64) uint64{\n", chainName, len(ballastNames))
	for _, fn := range ballastNames {
		g.wf("\t%s,\n", fn)
	}
	g.w("}\n\n")
}

var gsha256K = [64]uint32{
	0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5,
	0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
	0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3,
	0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
	0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc,
	0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
	0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7,
	0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
	0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13,
	0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
	0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3,
	0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
	0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5,
	0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
	0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208,
	0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
}

var gsha256H0 = [8]uint32{
	0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a,
	0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19,
}
