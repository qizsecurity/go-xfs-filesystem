package xfs_test

// Fixture-disk tests (qizsecurity fork).
//
// The tests read real XFS disks written by the Linux kernel's mkfs tools and
// compare what this library reports with what the Linux kernel reported for
// the same disk (the "oracle"). They run only when XFS_QIZ_FIXTURES names a
// folder; they are skipped when it is unset. The folder holds, per disk <d>:
//
//	<d>.raw.zst             zstd-compressed GPT disk with one XFS partition
//	<d>.raw.zst.sha256      sha256sum line of the .zst file
//	<d>.raw.sha256          sha256sum line of the decompressed disk
//	<d>.sfdisk.json         `sfdisk --json` of the disk (partition start, sector size)
//	<d>.oracle.tsv          kernel mount, then `find -printf '%y %p %s %m %T@ %l\n'`
//	                        and sha256sum of each regular file; columns:
//	                        type path size perm mtime sha256 link
//	<d>.hardlinks.txt       `find -printf '%i %n %P\n'` for files with link count > 1

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/masahiro331/go-xfs-filesystem/xfs"
)

const fixturesEnv = "XFS_QIZ_FIXTURES"

func fixturesDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv(fixturesEnv)
	if dir == "" {
		t.Skipf("%s is not set", fixturesEnv)
	}
	return dir
}

// sha256Line reads the digest of a `sha256sum` file.
func sha256Line(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(string(b))
	if len(f) != 2 || len(f[0]) != 64 {
		t.Fatalf("%s: not a sha256sum line: %q", file, b)
	}
	return f[0]
}

func fileSHA256(t *testing.T, file string) string {
	t.Helper()
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// extractDisk decompresses <disk>.raw.zst into t.TempDir(), checks the digest
// of the compressed file and of the decompressed disk against the recorded
// ones, and returns the path of the raw disk.
func extractDisk(t *testing.T, dir, disk string) string {
	t.Helper()
	zst := filepath.Join(dir, disk+".raw.zst")
	if got, want := fileSHA256(t, zst), sha256Line(t, zst+".sha256"); got != want {
		t.Fatalf("%s: sha256 %s, recorded %s", zst, got, want)
	}
	in, err := os.Open(zst)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	dec, err := zstd.NewReader(in)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	raw := filepath.Join(t.TempDir(), disk+".raw")
	out, err := os.Create(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, dec); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	if got, want := fileSHA256(t, raw), sha256Line(t, filepath.Join(dir, disk+".raw.sha256")); got != want {
		t.Fatalf("%s: decompressed sha256 %s, recorded %s", disk, got, want)
	}
	return raw
}

// partition returns the byte offset and size of partition 1 from sfdisk.json.
func partition(t *testing.T, dir, disk string) (off, size int64) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, disk+".sfdisk.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		PartitionTable struct {
			SectorSize int64 `json:"sectorsize"`
			Partitions []struct {
				Start int64 `json:"start"`
				Size  int64 `json:"size"`
			} `json:"partitions"`
		} `json:"partitiontable"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	pt := v.PartitionTable
	if len(pt.Partitions) != 1 || pt.SectorSize == 0 {
		t.Fatalf("%s: expected one partition and a sector size: %s", disk, b)
	}
	return pt.Partitions[0].Start * pt.SectorSize, pt.Partitions[0].Size * pt.SectorSize
}

// openXFS opens the XFS partition of a raw disk file.
func openXFS(t *testing.T, raw string, off, size int64) (*xfs.FileSystem, error) {
	t.Helper()
	f, err := os.Open(raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return xfs.NewFS(*io.NewSectionReader(f, off, size), nil)
}

// openDisk extracts a fixture disk and opens its XFS partition; it fails the
// test if the file system cannot be opened.
func openDisk(t *testing.T, dir, disk string) *xfs.FileSystem {
	t.Helper()
	raw := extractDisk(t, dir, disk)
	off, size := partition(t, dir, disk)
	fsys, err := openXFS(t, raw, off, size)
	if err != nil {
		t.Fatal(err)
	}
	return fsys
}

// row is one line of the oracle: the columns of `find -printf` and sha256sum.
type row struct {
	Type  string
	Path  string
	Size  int64
	Perm  string // octal, as `find -printf %m`
	MTime string // seconds.nanoseconds, 9 fraction digits
	SHA   string // regular files only
	Link  string // symlinks only
}

func (r row) String() string {
	return fmt.Sprintf("type=%s size=%d perm=%s mtime=%s sha256=%s link=%q", r.Type, r.Size, r.Perm, r.MTime, r.SHA, r.Link)
}

func dash(s string) string {
	if s == "-" {
		return ""
	}
	return s
}

// loadOracle reads <disk>.oracle.tsv. find prints %T@ with 10 fraction digits;
// the file system holds nanoseconds, so the tenth digit must be 0 or the row is
// rejected rather than rounded.
func loadOracle(t *testing.T, dir, disk string) map[string]row {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, disk+".oracle.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows := map[string]row{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		c := strings.Split(sc.Text(), "\t")
		if len(c) != 7 {
			t.Fatalf("oracle line has %d columns, want 7: %q", len(c), sc.Text())
		}
		size, err := strconv.ParseInt(c[2], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		sec, frac, ok := strings.Cut(c[4], ".")
		if !ok || len(frac) != 10 || frac[9] != '0' {
			t.Fatalf("oracle mtime %q is not seconds.nanoseconds plus one zero digit", c[4])
		}
		r := row{Type: c[0], Path: c[1], Size: size, Perm: c[3], MTime: sec + "." + frac[:9], SHA: dash(c[5]), Link: dash(c[6])}
		if _, dup := rows[r.Path]; dup {
			t.Fatalf("oracle path twice: %s", r.Path)
		}
		rows[r.Path] = r
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatalf("%s: oracle is empty", disk)
	}
	return rows
}

func typeLetter(m fs.FileMode) string {
	switch {
	case m&fs.ModeDir != 0:
		return "d"
	case m&fs.ModeSymlink != 0:
		return "l"
	case m&fs.ModeNamedPipe != 0:
		return "p"
	case m&fs.ModeSocket != 0:
		return "s"
	case m&fs.ModeCharDevice != 0:
		return "c"
	case m&fs.ModeDevice != 0:
		return "b"
	case m&fs.ModeIrregular != 0:
		return "?"
	}
	return "f"
}

func permOctal(m fs.FileMode) string {
	p := uint32(m.Perm())
	if m&fs.ModeSetuid != 0 {
		p |= 0o4000
	}
	if m&fs.ModeSetgid != 0 {
		p |= 0o2000
	}
	if m&fs.ModeSticky != 0 {
		p |= 0o1000
	}
	return strconv.FormatUint(uint64(p), 8)
}

// walkRows lists every path of the file system the way the oracle does.
func walkRows(t *testing.T, fsys *xfs.FileSystem) map[string]row {
	t.Helper()
	rows := map[string]row{}
	var walk func(dir string)
	walk = func(dir string) {
		entries, err := fsys.ReadDir(dir)
		if err != nil {
			t.Fatalf("ReadDir(%q): %v", dir, err)
		}
		for _, e := range entries {
			p := path.Join(dir, e.Name())
			if dir == "." {
				p = e.Name()
			}
			info, err := e.Info()
			if err != nil {
				t.Fatalf("Info(%q): %v", p, err)
			}
			mt := info.ModTime()
			r := row{
				Type:  typeLetter(info.Mode()),
				Path:  p,
				Size:  info.Size(),
				Perm:  permOctal(info.Mode()),
				MTime: fmt.Sprintf("%d.%09d", mt.Unix(), mt.Nanosecond()),
			}
			switch r.Type {
			case "f":
				f, err := fsys.Open(p)
				if err != nil {
					t.Fatalf("Open(%q): %v", p, err)
				}
				h := sha256.New()
				if _, err := io.Copy(h, f); err != nil {
					t.Fatalf("read %q: %v", p, err)
				}
				r.SHA = hex.EncodeToString(h.Sum(nil))
			case "l":
				target, err := fsys.ReadLink(p)
				if err != nil {
					r.Link = "ERR: " + err.Error()
				} else {
					r.Link = target
				}
			}
			rows[p] = r
			if r.Type == "d" {
				walk(p)
			}
		}
	}
	walk(".")
	return rows
}

// diffRows returns one line per path that differs, is missing or is extra.
func diffRows(want, got map[string]row) []string {
	var out []string
	for p, w := range want {
		g, ok := got[p]
		switch {
		case !ok:
			out = append(out, fmt.Sprintf("%s: missing; oracle: %s", p, w))
		case g != w:
			out = append(out, fmt.Sprintf("%s:\n    oracle: %s\n    got:    %s", p, w, g))
		}
	}
	for p, g := range got {
		if _, ok := want[p]; !ok {
			out = append(out, fmt.Sprintf("%s: extra; got: %s", p, g))
		}
	}
	sort.Strings(out)
	return out
}

var fixtureDisks = []string{"gpt-xfs-nrext64", "gpt-xfs-new"}
