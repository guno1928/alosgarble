package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"go/token"
	"go/types"
	"io"
	mathrand "math/rand"
	"os/exec"
	"strconv"
	"strings"
)

const buildIDSeparator = "/"

func splitActionID(buildID string) string {
	return buildID[:strings.Index(buildID, buildIDSeparator)]
}

func splitContentID(buildID string) string {
	return buildID[strings.LastIndex(buildID, buildIDSeparator)+1:]
}

const buildIDHashLength = 15

func decodeBuildIDHash(str string) []byte {
	h, err := base64.RawURLEncoding.DecodeString(str)
	if err != nil {
		panic(fmt.Sprintf("invalid hash %q: %v", str, err))
	}
	if len(h) != buildIDHashLength {
		panic(fmt.Sprintf("decodeBuildIDHash expects to result in a hash of length %d, got %d", buildIDHashLength, len(h)))
	}
	return h
}

func encodeBuildIDHash(h [sha256.Size]byte) string {
	return base64.RawURLEncoding.EncodeToString(h[:buildIDHashLength])
}

func alterToolVersion(tool string, args []string) error {
	cmd := exec.Command(args[0], args[1:]...)
	out, err := cmd.Output()
	if err != nil {
		if err, _ := err.(*exec.ExitError); err != nil {
			return fmt.Errorf("%v: %s", err, err.Stderr)
		}
		return err
	}
	line := string(bytes.TrimSpace(out))
	f := strings.Fields(line)
	if len(f) < 3 || f[0] != tool || f[1] != "version" || f[2] == "devel" && !strings.HasPrefix(f[len(f)-1], "buildID=") {
		return fmt.Errorf("%s -V=full: unexpected output:\n\t%s", args[0], line)
	}
	var toolID []byte
	if f[2] == "devel" {

		toolID = decodeBuildIDHash(splitContentID(f[len(f)-1]))
	} else {

		toolID = []byte(line)
	}

	contentID := addGarbleToHash(toolID)

	fmt.Printf("%s +garble buildID=_/_/_/%s\n", line, encodeBuildIDHash(contentID))
	return nil
}

var (
	hasher    = sha256.New()
	sumBuffer [sha256.Size]byte
)

func addGarbleToHash(inputHash []byte) [sha256.Size]byte {

	hasher.Reset()
	hasher.Write(inputHash)
	if len(sharedCache.BinaryContentID) == 0 {
		panic("missing binary content ID")
	}
	hasher.Write(sharedCache.BinaryContentID)

	fmt.Fprintf(hasher, " GOGARBLE=%s", sharedCache.GOGARBLE)
	appendFlags(hasher, true)

	var sumBuffer [sha256.Size]byte
	hasher.Sum(sumBuffer[:0])
	return sumBuffer
}

func appendFlags(w io.Writer, forBuildHash bool) {
	if flagLiterals {
		io.WriteString(w, " -literals")
	}
	if flagTiny {
		io.WriteString(w, " -tiny")
	}
	if flagDebug && !forBuildHash {

		io.WriteString(w, " -debug")
	}
	if flagDebugDir != "" && !forBuildHash {

		io.WriteString(w, " -debugdir=")
		io.WriteString(w, flagDebugDir)
	}
	if flagDebugPassword != "" && !forBuildHash {
		io.WriteString(w, " -debugpassword=")
		io.WriteString(w, flagDebugPassword)
	}
	if flagSeed.present() {
		io.WriteString(w, " -seed=")
		io.WriteString(w, flagSeed.String())
	}
	if flagControlFlow && forBuildHash {
		io.WriteString(w, " -ctrlflow")
	}
}

func buildidOf(path string) (string, error) {
	cmd := exec.Command("go", "tool", "buildid", path)
	out, err := cmd.Output()
	if err != nil {
		if err, _ := err.(*exec.ExitError); err != nil {
			return "", fmt.Errorf("%v: %s", err, err.Stderr)
		}
		return "", err
	}
	return string(out), nil
}

var (
	nameBase64 = base64.URLEncoding.WithPadding(base64.NoPadding)

	b64NameBuffer [12]byte
)

func isDigit(b byte) bool { return '0' <= b && b <= '9' }
func isLower(b byte) bool { return 'a' <= b && b <= 'z' }
func isUpper(b byte) bool { return 'A' <= b && b <= 'Z' }
func toLower(b byte) byte { return b + ('a' - 'A') }
func toUpper(b byte) byte { return b - ('a' - 'A') }

func runtimeHashWithCustomSalt(salt []byte) uint32 {
	hasher.Reset()
	if !flagSeed.present() {
		hasher.Write(sharedCache.ListedPackages["runtime"].GarbleActionID[:])
	} else {
		hasher.Write(flagSeed.bytes)
	}
	hasher.Write(salt)
	sum := hasher.Sum(sumBuffer[:0])
	return binary.LittleEndian.Uint32(sum)
}

func magicValue() uint32 {
	return runtimeHashWithCustomSalt([]byte("magic"))
}

func entryOffKey() uint32 {
	return runtimeHashWithCustomSalt([]byte("entryOffKey"))
}

func hashWithPackage(pkg *listedPackage, name string) string {

	if !flagSeed.present() {
		return hashWithCustomSalt(pkg.GarbleActionID[:], name)
	}

	return hashWithCustomSalt([]byte(pkg.ImportPath+"|"), name)
}

func hashWithStruct(strct *types.Struct, field *types.Var) string {

	salt := strconv.AppendUint(nil, uint64(typeutil_hash(strct)), 32)

	if !flagSeed.present() {
		withGarbleHash := addGarbleToHash(salt)
		salt = withGarbleHash[:]
	}
	return hashWithCustomSalt(salt, field.Name())
}

const (
	minHashLength = 6
	maxHashLength = 12

	neededSumBytes = 9
)

func randomName(rand *mathrand.Rand, baseName string) string {
	salt := make([]byte, buildIDHashLength)
	if _, err := rand.Read(salt); err != nil {
		panic(err)
	}
	return hashWithCustomSalt(salt, baseName)
}

func hashWithCustomSalt(salt []byte, name string) string {
	if len(salt) == 0 {
		panic("hashWithCustomSalt: empty salt")
	}
	if name == "" {
		panic("hashWithCustomSalt: empty name")
	}

	hasher.Reset()
	hasher.Write(salt)
	hasher.Write(flagSeed.bytes)
	io.WriteString(hasher, name)
	sum := hasher.Sum(sumBuffer[:0])

	hashLengthRandomness := sum[neededSumBytes] % ((maxHashLength - minHashLength) + 1)
	hashLength := minHashLength + hashLengthRandomness

	nameBase64.Encode(b64NameBuffer[:], sum[:neededSumBytes])
	b64Name := b64NameBuffer[:hashLength]

	if isDigit(b64Name[0]) {

		b64Name[0] += 'A' - '0'
	}
	for i, b := range b64Name {
		if b == '-' {
			b64Name[i] = 'a'
		}
	}

	if token.IsIdentifier(name) {
		if token.IsExported(name) {
			if b64Name[0] == '_' {

				b64Name[0] = 'Z'
			} else if isLower(b64Name[0]) {

				b64Name[0] = toUpper(b64Name[0])
			}
		} else if isUpper(b64Name[0]) {

			b64Name[0] = toLower(b64Name[0])
		}
	}
	return string(b64Name)
}
