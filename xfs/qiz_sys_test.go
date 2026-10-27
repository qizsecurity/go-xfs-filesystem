package xfs_test

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestSysIsInodeNumber: FileInfo.Sys() is the inode number (uint64), which is
// the identity of a hard link. Oracle: the inode numbers `find -printf %i` read
// from the mounted disk (<disk>.hardlinks.txt: inode, link count, path).
func TestSysIsInodeNumber(t *testing.T) {
	dir := fixturesDir(t)
	for _, disk := range fixtureDisks {
		disk := disk
		t.Run(disk, func(t *testing.T) {
			fsys := openDisk(t, dir, disk)

			f, err := os.Open(filepath.Join(dir, disk+".hardlinks.txt"))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			kernel := map[string]uint64{}
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				c := strings.Fields(sc.Text())
				if len(c) != 3 {
					t.Fatalf("hardlinks line: %q", sc.Text())
				}
				ino, err := strconv.ParseUint(c[0], 10, 64)
				if err != nil {
					t.Fatal(err)
				}
				kernel[c[2]] = ino
			}
			if len(kernel) != 2 {
				t.Fatalf("expected the 2 names of one hard-linked file, got %v", kernel)
			}

			ino := func(p string) uint64 {
				info, err := fsys.Stat(p)
				if err != nil {
					t.Fatalf("Stat(%q): %v", p, err)
				}
				v, ok := info.Sys().(uint64)
				if !ok {
					t.Fatalf("Sys() of %q is %T(%v), want uint64", p, info.Sys(), info.Sys())
				}
				return v
			}
			for p, want := range kernel {
				if got := ino(p); got != want {
					t.Errorf("%s: Sys() = %d, kernel inode = %d", p, got, want)
				}
			}
			if a, b := ino("var/data/data.bin"), ino("var/data/data-hardlink.bin"); a != b {
				t.Errorf("hard links have different inode numbers: %d and %d", a, b)
			}
			if a, b := ino("var/data/data.bin"), ino("tiny.txt"); a == b {
				t.Errorf("data.bin and tiny.txt have the same inode number %d", a)
			}
		})
	}
}
