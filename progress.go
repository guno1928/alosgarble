package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	progressLogFile = "progress.log"
	progressBarW    = 30
	progressInnerW  = 66
	progressPollMs  = 150
)

func isTerminalStderr() bool {
	fi, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

type progressBar struct {
	sharedDir string
	total     int
	start     time.Time
	stopCh    chan struct{}
	stopped   atomic.Bool
	drawn     int
}

func startProgressBar(sharedDir string, total int) *progressBar {
	if !isTerminalStderr() {
		return nil
	}
	if total <= 0 {
		total = 1
	}
	pb := &progressBar{
		sharedDir: sharedDir,
		total:     total,
		start:     time.Now(),
		stopCh:    make(chan struct{}),
	}
	pb.draw(0, "initializing…")
	go func() {
		t := time.NewTicker(progressPollMs * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-pb.stopCh:
				return
			case <-t.C:
				n, cur := pb.readLog()
				pb.draw(n, cur)
			}
		}
	}()
	return pb
}

func stopProgressBar(pb *progressBar) {
	if pb == nil {
		return
	}
	if pb.stopped.CompareAndSwap(false, true) {
		close(pb.stopCh)
		pb.draw(pb.total, "complete")
		fmt.Fprintln(os.Stderr)
	}
}

func (pb *progressBar) readLog() (done int, last string) {
	data, err := os.ReadFile(filepath.Join(pb.sharedDir, progressLogFile))
	if err != nil || len(data) == 0 {
		return 0, ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) == 0 {
		return 0, ""
	}
	return len(lines), lines[len(lines)-1]
}

func (pb *progressBar) draw(done int, current string) {
	total := pb.total
	if done > total {
		done = total
	}
	pct := float64(done) / float64(total)
	elapsed := time.Since(pb.start)

	etaStr := "--:--"
	if done > 0 && done < total {
		rem := time.Duration(float64(elapsed) / float64(done) * float64(total-done))
		etaStr = pbDurStr(rem)
	} else if done >= total {
		etaStr = "00:00"
	}

	filled := int(pct * float64(progressBarW))
	if filled > progressBarW {
		filled = progressBarW
	}
	barStr := strings.Repeat("█", filled) + strings.Repeat("░", progressBarW-filled)

	pctStr := fmt.Sprintf("%3.0f%%", pct*100)
	countStr := fmt.Sprintf("%d / %d pkgs", done, total)

	maxCur := progressInnerW - 12
	if utf8.RuneCountInString(current) > maxCur {
		runes := []rune(current)
		current = "…" + string(runes[len(runes)-(maxCur-1):])
	}

	iw := progressInnerW
	hbar := strings.Repeat("─", iw)

	titleStr := "A L O S G A R B L E   //   Binary Obfuscator"
	titlePad := (iw - utf8.RuneCountInString(titleStr)) / 2
	titleLine := strings.Repeat(" ", titlePad) + titleStr

	barLine := fmt.Sprintf(" %s %s  ·  %s", barStr, pctStr, countStr)

	timingLine := fmt.Sprintf(" Elapsed  %-8s  ETA  %s", pbDurStr(elapsed), etaStr)

	phaseLine := " Phase    Obfuscation  ·  Guard Inject  ·  Literal Encrypt"

	pkgLine := " Package  " + current

	lines := []string{
		"  ┌" + hbar + "┐",
		"  │" + pbPad(titleLine, iw) + "│",
		"  ├" + hbar + "┤",
		"  │" + pbPad(barLine, iw) + "│",
		"  │" + pbPad(timingLine, iw) + "│",
		"  │" + pbPad(pkgLine, iw) + "│",
		"  │" + pbPad(phaseLine, iw) + "│",
		"  └" + hbar + "┘",
	}

	if pb.drawn == 0 {
		fmt.Fprint(os.Stderr, "\x1b7")
	} else {
		fmt.Fprint(os.Stderr, "\x1b8")
	}
	for _, l := range lines {
		fmt.Fprintf(os.Stderr, "\r\033[K%s\n", l)
	}
	pb.drawn = len(lines)
}

func pbPad(s string, w int) string {
	n := utf8.RuneCountInString(s)
	if n >= w {
		runes := []rune(s)
		return string(runes[:w])
	}
	return s + strings.Repeat(" ", w-n)
}

func pbDurStr(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}
