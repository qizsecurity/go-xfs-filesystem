package xfs_test

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"strings"
	"testing"
)

// Offsets in struct xfs_dinode, Linux fs/xfs/libxfs/xfs_format.h (v6.17):
// di_size (__be64) follows di_core's timestamps at byte 56; di_crc (__le32) is
// XFS_DINODE_CRC_OFF, after di_next_unlinked of the 96-byte core, byte 100.
// The test checks both offsets against the unmodified inode before writing.
const (
	dinodeSizeOff = 56
	dinodeCRCOff  = 100
)

// inodeCRC32c: xfs_dinode_calc_crc() is xfs_update_cksum(dip, inode size,
// XFS_DINODE_CRC_OFF): the same CRC-32C rule as the superblock, over the whole
// on-disk inode.
func inodeCRC32c(inode []byte) uint32 {
	b := append([]byte(nil), inode...)
	binary.LittleEndian.PutUint32(b[dinodeCRCOff:], 0)
	return crc32.Checksum(b, crc32.MakeTable(crc32.Castagnoli))
}

// TestSymlinkSizeAboveMaxRefused: the symlink "rel-link" (8 bytes, local) has
// its di_size raised past XFS_SYMLINK_MAXLEN ("#define XFS_SYMLINK_MAXLEN 1024",
// xfs_format.h v6.17 line 1515); the inode CRC is recomputed. The library must
// return an error that names the size and the inode number, and must not
// allocate a buffer of that size.
func TestSymlinkSizeAboveMaxRefused(t *testing.T) {
	dir := fixturesDir(t)
	sizes := []uint64{1025, 1 << 62} // 1<<62 is a makeslice panic / out of memory on an unbounded make()
	for _, size := range sizes {
		size := size
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			raw := extractDisk(t, dir, "gpt-xfs-new")
			off, fsSize := partition(t, dir, "gpt-xfs-new")
			clean, err := openXFS(t, raw, off, fsSize)
			if err != nil {
				t.Fatal(err)
			}
			info, err := clean.Stat("rel-link")
			if err != nil {
				t.Fatal(err)
			}
			ino := info.Sys().(uint64)
			inodeOff := off + int64(clean.PrimaryAG.SuperBlock.InodeAbsOffset(ino))

			f, err := os.OpenFile(raw, os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			inode := make([]byte, clean.PrimaryAG.SuperBlock.Inodesize)
			if _, err := f.ReadAt(inode, inodeOff); err != nil {
				t.Fatal(err)
			}
			if got := binary.BigEndian.Uint64(inode[dinodeSizeOff:]); got != 8 {
				t.Fatalf("di_size at %d = %d, the oracle says rel-link is 8 bytes", dinodeSizeOff, got)
			}
			if got, want := binary.LittleEndian.Uint32(inode[dinodeCRCOff:]), inodeCRC32c(inode); got != want {
				t.Fatalf("stored di_crc 0x%x, helper computes 0x%x on the unmodified inode", got, want)
			}
			binary.BigEndian.PutUint64(inode[dinodeSizeOff:], size)
			binary.LittleEndian.PutUint32(inode[dinodeCRCOff:], inodeCRC32c(inode))
			if _, err := f.WriteAt(inode, inodeOff); err != nil {
				t.Fatal(err)
			}
			f.Close()

			fsys, err := openXFS(t, raw, off, fsSize)
			if err != nil {
				t.Fatal(err)
			}
			_, err = fsys.ReadDir(".")
			if err == nil {
				t.Fatal("ReadDir returned no error for a symlink inode with di_size above XFS_SYMLINK_MAXLEN")
			}
			t.Logf("error text: %v", err)
			for _, w := range []string{fmt.Sprint(size), fmt.Sprintf("inode %d", ino), "XFS_SYMLINK_MAXLEN"} {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
		})
	}
}
