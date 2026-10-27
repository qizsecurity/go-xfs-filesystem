package xfs

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

// The inputs below are CONSTRUCTED from the Linux on-disk definitions, not
// measured on a kernel-written disk:
//   - fs/xfs/libxfs/xfs_format.h: struct xfs_dsymlink_hdr (56 bytes: sl_magic
//     XFS_SYMLINK_MAGIC "XSLM", sl_offset, sl_bytes, sl_crc, sl_uuid, sl_owner,
//     sl_blkno, sl_lsn), XFS_SYMLINK_MAXLEN 1024.
//   - fs/xfs/libxfs/xfs_symlink_remote.c: xfs_symlink_remote_read() and
//     xfs_symlink_hdr_ok(): every extent block of a v5 file system starts with
//     the header; chunk i holds min(pathlen left, blocksize - 56) bytes at
//     sl_offset = bytes before it; sl_owner is the inode number. A v4 file
//     system has no header.

const testSymlinkHdrSize = 56

// packRec packs an extent record (xfs_bmbt_rec: startoff 54 bits, startblock
// 52 bits, blockcount 21 bits, flag 1 bit), the inverse of BmbtRec.Unpack.
func packRec(startOff, startBlock, count uint64, unwritten bool) BmbtRec {
	var flag uint64
	if unwritten {
		flag = 1
	}
	return BmbtRec{
		L0: flag<<63 | startOff<<9 | startBlock>>43,
		L1: (startBlock&(1<<43-1))<<21 | count,
	}
}

type symlinkChunk struct {
	block  int64 // file system block the chunk lives in
	offset uint32
	bytes  uint32
	owner  uint64
	magic  uint32
	data   string
}

// newSymlinkTestFS returns a file system over 16 blocks of 512 bytes with the
// chunks written, and the Inode that points at the given extent records.
func newSymlinkTestFS(t *testing.T, v5 bool, size uint64, recs []BmbtRec, chunks []symlinkChunk) (*FileSystem, *Inode) {
	t.Helper()
	data := make([]byte, 16*512)
	for _, c := range chunks {
		blk := data[c.block*512:]
		p := blk
		if v5 {
			binary.BigEndian.PutUint32(blk[0:], c.magic)
			binary.BigEndian.PutUint32(blk[4:], c.offset)
			binary.BigEndian.PutUint32(blk[8:], c.bytes)
			binary.BigEndian.PutUint64(blk[32:], c.owner)
			p = blk[testSymlinkHdrSize:]
		}
		copy(p, c.data)
	}
	fsys := newReadTestFileSystem(bytes.NewReader(data))
	if v5 {
		fsys.PrimaryAG.SuperBlock.Versionnum = 5
	} else {
		fsys.PrimaryAG.SuperBlock.Versionnum = 4
	}
	inode := &Inode{
		inodeCore:      InodeCore{InodeCoreBase: InodeCoreBase{Mode: 0xA1FF, Format: XFS_DINODE_FMT_EXTENTS, Size: size}},
		symlinkExtents: &SymlinkExtents{bmbtRecs: recs},
	}
	inode.inodeCore.Ino = 77
	return fsys, inode
}

func linkText(n int) string {
	var b strings.Builder
	for b.Len() < n {
		b.WriteString("/d0123456789")
	}
	return b.String()[:n]
}

func TestSymlinkTargetLocal(t *testing.T) {
	fsys := newReadTestFileSystem(bytes.NewReader(nil))
	got, err := fsys.symlinkTarget(&Inode{
		inodeCore:     InodeCore{InodeCoreBase: InodeCoreBase{Mode: 0xA1FF, Format: XFS_DINODE_FMT_LOCAL, Size: 8}},
		symlinkString: &SymlinkString{Name: "tiny.txt"},
	})
	if err != nil || got != "tiny.txt" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestSymlinkTargetRemoteV5TwoExtents(t *testing.T) {
	// 900 bytes: 456 in block 3 (512-56), 444 in block 9.
	want := linkText(900)
	fsys, inode := newSymlinkTestFS(t, true, 900,
		[]BmbtRec{packRec(0, 3, 1, false), packRec(1, 9, 1, false)},
		[]symlinkChunk{
			{block: 3, offset: 0, bytes: 456, owner: 77, magic: XFS_SYMLINK_MAGIC, data: want[:456]},
			{block: 9, offset: 456, bytes: 444, owner: 77, magic: XFS_SYMLINK_MAGIC, data: want[456:]},
		})
	got, err := fsys.symlinkTarget(inode)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %d bytes %q, want %d bytes %q", len(got), got, len(want), want)
	}
}

func TestSymlinkTargetRemoteV5HeaderOncePerExtent(t *testing.T) {
	// 1000 bytes: one header for the 2-block extent at block 3 (1024-56 = 968
	// bytes, crossing the block boundary), then 32 bytes in block 9.
	want := linkText(1000)
	fsys, inode := newSymlinkTestFS(t, true, 1000,
		[]BmbtRec{packRec(0, 3, 2, false), packRec(2, 9, 1, false)},
		[]symlinkChunk{
			{block: 3, offset: 0, bytes: 968, owner: 77, magic: XFS_SYMLINK_MAGIC, data: want[:968]},
			{block: 9, offset: 968, bytes: 32, owner: 77, magic: XFS_SYMLINK_MAGIC, data: want[968:]},
		})
	got, err := fsys.symlinkTarget(inode)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %d bytes %q, want %d bytes %q", len(got), got, len(want), want)
	}
}

func TestSymlinkTargetRemoteV4NoHeader(t *testing.T) {
	// 700 bytes on a v4 file system: 512 in block 2, 188 in block 5, no header.
	want := linkText(700)
	fsys, inode := newSymlinkTestFS(t, false, 700,
		[]BmbtRec{packRec(0, 2, 1, false), packRec(1, 5, 1, false)},
		[]symlinkChunk{
			{block: 2, data: want[:512]},
			{block: 5, data: want[512:]},
		})
	got, err := fsys.symlinkTarget(inode)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSymlinkTargetRemoteRefusals(t *testing.T) {
	ok := symlinkChunk{block: 3, offset: 0, bytes: 100, owner: 77, magic: XFS_SYMLINK_MAGIC, data: linkText(100)}
	rec := []BmbtRec{packRec(0, 3, 1, false)}
	tests := []struct {
		name   string
		size   uint64
		recs   []BmbtRec
		chunk  symlinkChunk
		errHas string
	}{
		{"wrong magic", 100, rec, func() symlinkChunk { c := ok; c.magic = 0; return c }(), "magic"},
		{"wrong owner", 100, rec, func() symlinkChunk { c := ok; c.owner = 78; return c }(), "owner"},
		{"wrong offset", 100, rec, func() symlinkChunk { c := ok; c.offset = 1; return c }(), "offset"},
		{"wrong byte count", 100, rec, func() symlinkChunk { c := ok; c.bytes = 99; return c }(), "bytes"},
		{"size zero", 0, rec, ok, "size"},
		{"size over XFS_SYMLINK_MAXLEN", 1025, rec, ok, "size"},
		{"hole before first extent", 100, []BmbtRec{packRec(1, 3, 1, false)}, ok, "hole"},
		{"unwritten extent", 100, []BmbtRec{packRec(0, 3, 1, true)}, ok, "unwritten"},
		{"extents end before the size", 600, rec, symlinkChunk{block: 3, offset: 0, bytes: 456, owner: 77, magic: XFS_SYMLINK_MAGIC, data: linkText(456)}, "short"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys, inode := newSymlinkTestFS(t, true, tt.size, tt.recs, []symlinkChunk{tt.chunk})
			got, err := fsys.symlinkTarget(inode)
			if err == nil {
				t.Fatalf("got %q, want an error with %q", got, tt.errHas)
			}
			if got != "" {
				t.Fatalf("got a target %q together with an error", got)
			}
			if !strings.Contains(err.Error(), tt.errHas) {
				t.Fatalf("error %q lacks %q", err, tt.errHas)
			}
		})
	}
}

// An EXTENTS-format symlink inode keeps its extent records (they were dropped
// with a "not support" warning before).
func TestInodeFormatExtentsKeepsSymlinkRecords(t *testing.T) {
	fsys := newReadTestFileSystem(bytes.NewReader(nil))
	in := Inode{inodeCore: InodeCore{InodeCoreBase: InodeCoreBase{Mode: 0xA1FF, Format: XFS_DINODE_FMT_EXTENTS}}}
	in.inodeCore.Nextents = 1
	rec := packRec(0, 3, 1, false)
	b := make([]byte, 16)
	binary.BigEndian.PutUint64(b[0:], rec.L0)
	binary.BigEndian.PutUint64(b[8:], rec.L1)
	got, err := fsys.inodeFormatExtents(bytes.NewReader(b), in)
	if err != nil {
		t.Fatal(err)
	}
	if got.symlinkExtents == nil || len(got.symlinkExtents.bmbtRecs) != 1 || got.symlinkExtents.bmbtRecs[0] != rec {
		t.Fatalf("symlink extent records not kept: %+v", got.symlinkExtents)
	}
}
