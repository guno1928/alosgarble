# alosgarble

> **Built on top of [garble](https://github.com/burrowers/garble) by the Garble Authors.**
> We do not take full credit for this project. The original garble is an incredible tool and the foundation of everything here.
> Our work adds new obfuscation layers, a code guard system, a live build progress display, and performance improvements on top of that base.

---

`alosgarble` is a Go binary obfuscator that wraps the Go toolchain — just like the original `garble` — but with additional protection layers designed for high-security production binaries. You use it the same way you use `go build`, just prefix it with `alosgarble`.

---

## Installation

### One-liner (Linux / macOS)

```sh
curl -sL https://raw.githubusercontent.com/guno1928/alosgarble/main/install.sh | bash
```

This will install `alosgarble` via `go install`, auto-detect your shell (bash/zsh/fish), add `GOPATH/bin` to your PATH, and verify the command works — all automatically.

### Manual

```sh
go install github.com/guno1928/alosgarble@latest
```

If `alosgarble` says "command not found" after install, add Go's bin directory to your PATH:

```sh
echo 'export PATH="$(go env GOPATH)/bin:$PATH"' >> ~/.bashrc && source ~/.bashrc
```

### Windows

```
go install github.com/guno1928/alosgarble@latest
```

Go automatically adds `%GOPATH%\bin` to PATH on Windows.

**Requirements:**
- Go 1.26.2 or later (required by the patched linker)
- Windows, Linux, or macOS

---

## Quick Start

```sh
# Build your project (replaces: go build)
alosgarble build .

# Build with a fixed seed (reproducible output)
alosgarble -seed=o9WDTZ4CN4w build .

# Build with a random seed each time
alosgarble -seed=random build .

# Cross-compile
GOOS=linux GOARCH=amd64 alosgarble build -o myapp-linux .

# Build a specific package path
alosgarble build ./cmd/myapp
```

---

## What It Does

`alosgarble` applies multiple independent obfuscation passes to your compiled binary. Each pass is always-on by default — there are no flags to forget.

### 1. Symbol Renaming

All exported and unexported symbols (function names, type names, variable names, package paths) are replaced with random-looking identifiers derived from a hash of the build seed. The binary contains no readable symbol table.

```
main.processRequest  →  fv3kXqZ9
crypto/tls.(*Conn).Read  →  bR7mNw2p
```

### 2. Literal Obfuscation (Always On)

String literals, integer constants, byte slices, and other constant values in your source are never stored plaintext in the binary. They are encrypted at compile time and decrypted at runtime using the **WideXOR** cipher.

**WideXOR** is our custom literal obfuscator. Each literal gets:

- A randomly sized keystream split into **K independent byte-slice fragments** (K varies per literal)
- **Four independent permutations** controlling which fragment is declared where, which alias it gets, and what order the `copy()` calls appear in
- **Per-fragment alias variables** — each real fragment gets a one-hop alias so an analyst must trace a three-way mapping (offset → alias → fragment → bytes) before reconstructing the key
- **Two decoy fragments** with identical random content that participate in a self-cancelling checksum XOR — they look real to static analysis but contribute nothing to decryption
- **`len()`-based offset arithmetic** in `copy()` calls instead of integer constants, forcing the analyst to trace alias→fragment lengths before computing copy offsets
- A **rolling XOR checksum** over decrypted bytes with tamper detection — if the check fails, the data is corrupted in-place so the program silently misbehaves rather than crashing cleanly

```go
// Source
var token = "prod-secret-key-XKZETA2847"

// Binary sees: fragmented encrypted bytes + 4 permutations + 2 decoys
// No string scanner will find "prod-secret-key-XKZETA2847" anywhere
```

### 3. Control Flow Obfuscation (Always On)

Functions annotated with `//garble:controlflow` have their control flow graph destroyed and rebuilt using **control flow flattening**. The original structure (loops, branches, returns) is replaced by a dispatcher switch that routes execution via an opaque state integer.

You can tune the flattening per function:

```go
//garble:controlflow block_splits=2 junk_jumps=4 flatten_passes=2 trash_blocks=8
func sensitiveFunction() {
    // ...
}
```

| Parameter | Default | Max | Description |
|---|---|---|---|
| `block_splits` | 0 | unlimited | Split basic blocks into more pieces |
| `junk_jumps` | 0 | 256 | Insert dead branches that never execute |
| `flatten_passes` | 1 | 4 | How many times to apply flattening |
| `trash_blocks` | 0 | 1024 | Insert unreachable junk basic blocks |

Dispatcher hardening options:

```go
//garble:controlflow flatten_passes=2 hardening=xor
//garble:controlflow flatten_passes=2 hardening=delegate_table
//garble:controlflow flatten_passes=2 hardening=xor,delegate_table
```

### 4. Guard Code Injection

This is the major addition over upstream garble. Every package in your build gets a **guard file** injected at compile time. The guard file serves two purposes:

**a) Binary integrity check** — the guard reads your own executable path via `os.Executable()`, hashes its content, and verifies it against a magic value baked in at build time. If the binary has been patched or tampered with, the guard sets `_gsecActive = false`, which makes all literal decryption silently produce garbage. The program keeps running but outputs corrupted data — no obvious crash for a reverse engineer to trigger.

**b) Anti-analysis ballast** — the guard generates **90–110 large lookup tables** (each ~20 KB) and **300–500 chained computation functions** of varying complexity. These are real, live code paths that execute at `init()` time. They:
- Dramatically increase the binary's apparent complexity
- Fill decompiler views with thousands of functions that look like they do something
- Create genuine GC and analysis pressure that slows automated tooling
- Are seeded per-build so every binary looks different even from the same source

Packages that cannot import `"os"` (e.g. pure utility packages with no OS dependency) get a **ballast-only guard** — same lookup tables and function chains, but no file-integrity check, so the import graph stays clean.

The `_gsecActive` boolean is the bridge between the guard and the obfuscated literals. String decryption is gated on it:

```
guard.init() runs → file hash verified → _gsecActive = true → literals decrypt correctly
binary tampered → hash mismatch → _gsecActive stays false → literals corrupt silently
```

### 5. Linker-Level Symbol Stripping

The Go linker is patched to remove unexported function names from the binary's function name table. Combined with symbol renaming this means there is no readable name anywhere in the binary for unexported code — not in the symbol table, not in stack traces, not in pclntab.

### 6. Decoy Literals

Every obfuscated package gets 2–4 package-level `var` declarations injected that look like real secrets — API keys, JWTs, connection strings, bearer tokens. They are encrypted identically to real string literals using WideXOR, so an analyst who decrypts them cannot tell which strings are real and which are bait. Every decrypted value must be individually validated.

```go
// injected automatically — analyst sees this after decryption:
var _dc3a7f = "postgresql://svc_user:Kp8!nR3zQw@db-prod-07.internal:5432/coredb"
var _dc91be = "ghp_16C7e42F292c6912E169D1a7dB4e59b1D2c5"
// these are bait — they decrypt fine but mean nothing
```

### 7. Live Build Progress Display

While `alosgarble` builds your project, a live animated box is drawn to the terminal showing:

- Package count and percentage complete
- Elapsed time and ETA
- Current package being compiled
- Active pipeline phases

```
  ┌──────────────────────────────────────────────────────────────────┐
  │           A L O S G A R B L E   //   Binary Obfuscator           │
  ├──────────────────────────────────────────────────────────────────┤
  │ ██████████████████████████████ 100%  ·  105 / 105 pkgs           │
  │ Elapsed  4:46      ETA  00:00                                    │
  │ Package  complete                                                │
  │ Phase    Obfuscation  ·  Guard Inject  ·  Literal Encrypt        │
  └──────────────────────────────────────────────────────────────────┘
```

The display only activates when stderr is a real terminal (not redirected or piped), so CI logs stay clean.

---

## Examples

### Basic Application

```sh
cd myapp/
alosgarble build -o myapp.exe .
./myapp.exe
```

### With a Reproducible Seed

Using the same seed produces the same obfuscation every time. Useful for CI.

```sh
alosgarble -seed=o9WDTZ4CN4w build -o myapp.exe .
```

### Force Full Rebuild

```sh
alosgarble build -a -o myapp.exe .
```

The `-a` flag forces every package to be rebuilt and re-obfuscated rather than reusing cached packages from a previous build.

### Control Flow on a Sensitive Function

```go
package main

import "fmt"

//garble:controlflow flatten_passes=2 junk_jumps=8 trash_blocks=16 hardening=xor,delegate_table
func verifyLicense(key string) bool {
    // complex logic here
    return key == "valid"
}

func main() {
    fmt.Println(verifyLicense("valid"))
}
```

```sh
alosgarble build -o app.exe .
```

### Anti-Debug Integration

`alosgarble` works with [`github.com/guno1928/antidebug`](https://github.com/guno1928/antidebug) for runtime debugger detection:

```go
package main

import (
    "fmt"
    "os"
    antidebug "github.com/guno1928/antidebug/core"
)

func main() {
    cfg := antidebug.DefaultConfig()
    cfg.OnDetect = func(reason string) {
        os.Exit(1)
    }
    antidebug.Start(cfg)

    fmt.Println("Running securely.")
}
```

```sh
cd myapp/
alosgarble build -a -o secure.exe .
```

The obfuscated binary will:
1. Run the anti-debug checks at startup
2. Verify binary integrity via the guard
3. Decrypt all strings only if both checks pass

### Multi-Package Project

Guard injection works across every package in your module automatically. No changes to your code are needed.

```sh
# All packages in the module get guard code injected
alosgarble build -a ./...
```

---

## Flags

| Flag | Default | Description |
|---|---|---|
| `-seed` | random per build | Base64 seed for obfuscation. Use `-seed=random` for a fresh random seed. |
| `-debug` | off | Inject a debug runtime into the built binary. On startup the binary prints a banner to stderr and writes all debug events to a `.garbledebug_<pid>.log` file next to the executable. |
| `-debugpassword <pass>` | off | Requires `-debug`. Encrypt all debug log messages with ChaCha20 using `<pass>` as the key. No output appears on stderr — everything goes to the encrypted log file only. The password constant is itself obfuscated in the binary by WideXOR. |
| `-debugdir` | off | Write pre/post obfuscation source trees to a directory |

`-literals` and `-tiny` are always forced on and cannot be disabled.

---

## Debug Mode

### Plain debug (logs to stderr + file)

```sh
alosgarble -debug build -o myapp.exe .
./myapp.exe
# stderr shows: [GARBLE-DEBUG] ====== PROCESS STARTED ======
# also written to: myapp.exe.garbledebug_<pid>.log
```

The injected debug runtime captures:
- Process startup (PID, executable path, Go version, OS/arch, GOMAXPROCS)
- Unhandled panics in `main()` with full goroutine stack dump
- Panics in any `init()` function
- `os.Exit()` calls with exit code and call stack

### Encrypted debug (log file only, no terminal output)

```sh
alosgarble -debug -debugpassword mySecretPass build -o myapp.exe .
./myapp.exe
# nothing on stderr
# encrypted log written to: myapp.exe.garbledebug_<pid>.log
```

Decrypt the log later:

```sh
alosgarble decrypt -password mySecretPass myapp.exe.garbledebug_12345.log
```

Output:

```
[GARBLE-DEBUG] ====== PROCESS STARTED ======
[GARBLE-DEBUG] PID        : 12345
[GARBLE-DEBUG] Executable : C:\path\to\myapp.exe
[GARBLE-DEBUG] Go version : go1.26.2
[GARBLE-DEBUG] OS/Arch    : windows/amd64
[GARBLE-DEBUG] GOMAXPROCS : 8
[GARBLE-DEBUG] Log file   : myapp.exe.garbledebug_12345.log (encrypted)
[GARBLE-DEBUG] Mode       : password-encrypted — no terminal output
...
```

If you supply the wrong password, decryption fails with a clear error:

```
decryption failed at message 0 — wrong password or corrupted log file
```

**Encrypted log format:**

| Offset | Size | Content |
|---|---|---|
| 0 | 8 bytes | Magic: `ALOSDBG\x01` |
| 8 | 16 bytes | PID-derived salt |
| 24+ | repeated | `msgLen[4LE]` + `nonce[12]` + ChaCha20 ciphertext |

Each plaintext block has `ALOS` prepended as a validation token so wrong-password attempts are detected immediately.

The ChaCha20 implementation and 100,000-round key derivation function are fully inlined into the built binary with no extra imports — there is no `golang.org/x/crypto` dependency in your final binary.

---

## Debugging a Build (Source Inspection)

If you want to inspect the generated source before and after obfuscation:

```sh
alosgarble -debugdir=./debug_out build .
```

This writes the original and transformed Go AST for every package into `./debug_out/`.

---

## How It Differs from Upstream Garble

| Feature | Upstream garble | alosgarble |
|---|---|---|
| Symbol renaming | ✅ | ✅ |
| Literal obfuscation | ✅ (multiple strategies) | ✅ WideXOR (custom, always on) |
| Control flow flattening | ✅ | ✅ |
| Linker symbol stripping | ✅ | ✅ (patched) |
| Binary integrity guard | ❌ | ✅ |
| Anti-analysis ballast | ❌ | ✅ (~20 MB of chained fake complexity per package) |
| Decoy literals | ❌ | ✅ (2–4 fake secrets per package, WideXOR encrypted) |
| Per-fragment alias permutations | ❌ | ✅ (4 independent permutations) |
| Silent corruption on tamper | ❌ | ✅ |
| Live build progress display | ❌ | ✅ (animated terminal UI with ETA) |
| ASLR-aware guard | ❌ | ✅ (table base address mixed into verification) |
| Cross-validation chain | ❌ | ✅ (adjacent lookup tables check each other) |
| Multi-sentinel guard activation | ❌ | ✅ (3 independent booleans + monotonic counter) |
| Debug runtime injection | ❌ | ✅ (panic/exit capture, startup banner, log file) |
| Encrypted debug logs | ❌ | ✅ (ChaCha20, inline KDF, no extra imports) |
| `decrypt` subcommand | ❌ | ✅ (`alosgarble decrypt -password <pass> <logfile>`) |

---

## Credits

This project is a fork of **[garble](https://github.com/burrowers/garble)** by the Garble Authors, licensed under the BSD 3-Clause License. The core obfuscation engine, toolchain wrapping, AST transformation, and SSA-based control flow work are all from the original project. We do not take credit for that work.

Our additions:
- **Guard code injection system** — binary integrity check + multi-sentinel activation + ASLR-aware verification + cross-validation chain between lookup tables
- **WideXOR literal cipher** — per-fragment aliases, 4 independent permutations, rolling checksum, decoy fragments, and silent tamper corruption
- **Anti-analysis ballast generator** — 90–110 lookup tables, 300–500 chained computation functions, content-dependent sizing per build
- **Decoy literal injection** — 2–4 fake secrets per package, obfuscated identically to real strings
- **Live build progress display** — animated terminal UI with ETA, package count, and current package name
- **Performance optimizations** — lookup table encoding switched from composite literals to string ballast, reducing go/types type-check work by ~10–19% on large builds
- **Debug runtime injection** (`-debug`) — captures panics, `os.Exit` calls, and `init()` failures; writes a startup banner to stderr and a persistent log file next to the executable
- **Encrypted debug logs** (`-debugpassword`) — ChaCha20-encrypted log-file-only mode; no terminal output; password constant is itself obfuscated by WideXOR in the binary; fully inline crypto with no extra binary imports
- **`decrypt` subcommand** — `alosgarble decrypt -password <pass> <logfile>` streams all decrypted debug messages; wrong-password detection via per-block validation token

---

## License

BSD 3-Clause — same as upstream garble. See [LICENSE](LICENSE).
