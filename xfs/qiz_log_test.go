package xfs_test

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/masahiro331/go-xfs-filesystem/log"
)

const (
	helperEnvDisk = "XFS_QIZ_HELPER_DISK"
	helperEnvOff  = "XFS_QIZ_HELPER_OFF"
	helperEnvSize = "XFS_QIZ_HELPER_SIZE"
)

// corruptDir: the disk copy in which the inode of the directory "etc" is
// damaged. Returns the raw disk path, partition offset and size, and the 512
// bytes of the inode as written on the damaged disk.
func corruptDir(t *testing.T, dir, disk string, damage func(inode []byte)) (raw string, off, size int64, inode []byte) {
	t.Helper()
	raw = extractDisk(t, dir, disk)
	off, size = partition(t, dir, disk)
	clean, err := openXFS(t, raw, off, size)
	if err != nil {
		t.Fatal(err)
	}
	info, err := clean.Stat("etc")
	if err != nil {
		t.Fatal(err)
	}
	ino := info.Sys().(uint64)
	inodeOff := off + int64(clean.PrimaryAG.SuperBlock.InodeAbsOffset(ino))
	isize := int(clean.PrimaryAG.SuperBlock.Inodesize)

	f, err := os.OpenFile(raw, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	inode = make([]byte, isize)
	if _, err := f.ReadAt(inode, inodeOff); err != nil {
		t.Fatal(err)
	}
	damage(inode)
	if _, err := f.WriteAt(inode, inodeOff); err != nil {
		t.Fatal(err)
	}
	return raw, off, size, inode
}

// damageForkCount sets the number of entries of the short-form directory held
// in the inode (first byte of the data fork, after the 176-byte v3 inode core)
// to 0x40, so the entry parser runs off the end of the inode.
func damageForkCount(inode []byte) { inode[176] = 0x40 }

// damageNameLength makes the second entry of the short-form directory ask for a
// 255-byte name when only 64 bytes of the inode are left. The inode after the
// first entry's name is filled with letters so that raw bytes are recognisable.
// Offsets: 176 = start of the data fork of a v3 inode; count (1), i8count (1),
// parent (4); entry = namelen (1), offset (2), name, ftype (1), inumber (4).
func damageNameLength(inode []byte) {
	for i := 185; i < len(inode); i++ {
		inode[i] = 'A' + byte(i%26)
	}
	inode[176] = 2    // two entries
	inode[182] = 0xff // first entry: 255-byte name, 185..439
	inode[445] = 0xff // second entry: namelen after ftype (440) and inumber (441..444)
}

// leaks reports every piece of raw inode data found in text: the hex of any
// 4-byte window of the inode (continuous or as hex.Dump prints it), the raw
// window itself, or a run of 8 spaced hex bytes. Windows made of one repeated
// byte (zero padding) are not informative and are skipped.
func leaks(text string, inode []byte) []string {
	var out []string
	spaced := regexp.MustCompile(`(?i)(?:\b[0-9a-f]{2} ){8}`)
	if m := spaced.FindString(text); m != "" {
		out = append(out, fmt.Sprintf("run of spaced hex bytes %q", m))
	}
	for i := 0; i+4 <= len(inode); i++ {
		w := inode[i : i+4]
		if bytes.Count(w, w[:1]) == 4 {
			continue
		}
		forms := []string{
			hex.EncodeToString(w),
			fmt.Sprintf("% x", w),
			string(w),
		}
		for _, form := range forms {
			if strings.Contains(strings.ToLower(text), strings.ToLower(form)) {
				out = append(out, fmt.Sprintf("inode bytes [%d:%d] as %q", i, i+4, form))
				break
			}
		}
	}
	return out
}

// TestCorruptInodeNoRawBytes: a damaged inode makes the library return an error
// and, with a logger that keeps every level, write log lines. Neither the error
// text nor any log line holds the bytes of the inode.
func TestCorruptInodeNoRawBytes(t *testing.T) {
	dir := fixturesDir(t)
	cases := map[string]func([]byte){
		"entry count too large":  damageForkCount,
		"name longer than inode": damageNameLength,
	}
	for name, damage := range cases {
		damage := damage
		t.Run(name, func(t *testing.T) {
			raw, off, size, inode := corruptDir(t, dir, "gpt-xfs-new", damage)

			core, logs := observer.New(zapcore.DebugLevel)
			prev := log.Logger
			log.SetLogger(zap.New(core).Sugar())
			t.Cleanup(func() { log.SetLogger(prev) })

			fsys, err := openXFS(t, raw, off, size)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			_, err = fsys.ReadDir("etc")
			if err == nil {
				t.Fatal("ReadDir of the damaged directory returned no error; the damage did not reach the parser")
			}
			var text strings.Builder
			text.WriteString(err.Error())
			for _, e := range logs.All() {
				text.WriteString("\n")
				text.WriteString(e.Message)
				for _, f := range e.Context {
					text.WriteString(" " + f.String)
				}
			}
			if l := leaks(text.String(), inode); len(l) > 0 {
				t.Fatalf("raw inode data in error or log (%d finds: %v):\n%s", len(l), l, text.String())
			}
			t.Logf("error text: %v", err)
		})
	}
}

// TestSilentByDefault: a program that never sets a logger gets nothing on
// stdout or stderr from the library, not even while it reads a damaged inode.
func TestSilentByDefault(t *testing.T) {
	dir := fixturesDir(t)
	raw, off, size, _ := corruptDir(t, dir, "gpt-xfs-new", damageForkCount)

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperCorruptWalk$")
	cmd.Env = append(os.Environ(),
		helperEnvDisk+"="+raw, fmt.Sprintf("%s=%d", helperEnvOff, off), fmt.Sprintf("%s=%d", helperEnvSize, size))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child failed: %v\n%s", err, out)
	}
	if len(out) != 0 {
		t.Fatalf("the library wrote %d bytes on its own:\n%s", len(out), out)
	}
}

// TestHelperCorruptWalk is the child of TestSilentByDefault. It does nothing
// when run as a normal test.
func TestHelperCorruptWalk(t *testing.T) {
	raw := os.Getenv(helperEnvDisk)
	if raw == "" {
		t.Skip("child of TestSilentByDefault")
	}
	var off, size int64
	fmt.Sscan(os.Getenv(helperEnvOff), &off)
	fmt.Sscan(os.Getenv(helperEnvSize), &size)
	fsys, err := openXFS(t, raw, off, size)
	if err != nil {
		os.Exit(3)
	}
	if _, err := fsys.ReadDir("etc"); err == nil {
		os.Exit(4) // the damage must reach the parser, as in TestCorruptInodeNoRawBytes
	}
	os.Exit(0) // exit here so the test binary prints no PASS line
}

// TestDefaultLoggerIsOff needs no fixture: the logger a program gets without
// calling SetLogger is disabled at every level.
func TestDefaultLoggerIsOff(t *testing.T) {
	for _, lvl := range []zapcore.Level{zapcore.DebugLevel, zapcore.InfoLevel, zapcore.WarnLevel, zapcore.ErrorLevel} {
		if log.Logger.Desugar().Core().Enabled(lvl) {
			t.Errorf("default logger is enabled at %s", lvl)
		}
	}
}
