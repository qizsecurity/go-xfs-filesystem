package xfs_test

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/masahiro331/go-xfs-filesystem/xfs"
)

// Offsets in struct xfs_dsb, Linux fs/xfs/libxfs/xfs_format.h (v6.17):
// sb_features_incompat follows sb_features_compat and sb_features_ro_compat,
// sb_crc follows sb_features_log_incompat; XFS_SB_CRC_OFF = offsetof(sb_crc).
const (
	sbIncompatOff = 216
	sbCRCOff      = 224
)

// sbCRC32c is the superblock checksum as the Linux kernel computes it:
// xfs_update_cksum() in fs/xfs/libxfs/xfs_cksum.h is CRC-32C (Castagnoli) with
// seed ~0 over the whole sector (sb_sectsize bytes) with the 4 crc bytes taken
// as zero, final value inverted (xfs_end_cksum), stored little-endian.
// hash/crc32 with the Castagnoli table seeds with ~0 and inverts at the end.
func sbCRC32c(sector []byte) uint32 {
	b := append([]byte(nil), sector...)
	binary.LittleEndian.PutUint32(b[sbCRCOff:], 0)
	return crc32.Checksum(b, crc32.MakeTable(crc32.Castagnoli))
}

// setIncompat ORs bits into sb_features_incompat of the primary superblock of
// the partition at off in the raw disk and rewrites the superblock CRC.
// It first checks, on the unmodified sector, that the helper reads the field
// values xfs_db printed for this disk and that the CRC helper reproduces the
// stored CRC: so a wrong offset or a wrong CRC rule fails here, not silently.
func setIncompat(t *testing.T, raw string, off int64, wantIncompat, wantCRC uint32, bits uint32) {
	t.Helper()
	f, err := os.OpenFile(raw, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sector := make([]byte, 512)
	if _, err := f.ReadAt(sector, off); err != nil {
		t.Fatal(err)
	}
	if got := binary.BigEndian.Uint32(sector[sbIncompatOff:]); got != wantIncompat {
		t.Fatalf("sb_features_incompat at %d = 0x%x, xfs_db printed 0x%x", sbIncompatOff, got, wantIncompat)
	}
	if got := binary.LittleEndian.Uint32(sector[sbCRCOff:]); got != wantCRC {
		t.Fatalf("stored sb_crc = 0x%x, xfs_db printed (byte-swapped) 0x%x", got, wantCRC)
	}
	if got := sbCRC32c(sector); got != wantCRC {
		t.Fatalf("CRC-32C helper gives 0x%x on the unmodified superblock, stored 0x%x", got, wantCRC)
	}
	binary.BigEndian.PutUint32(sector[sbIncompatOff:], wantIncompat|bits)
	binary.LittleEndian.PutUint32(sector[sbCRCOff:], sbCRC32c(sector))
	if _, err := f.WriteAt(sector, off); err != nil {
		t.Fatal(err)
	}
}

// Values xfs_db printed on gpt-xfs-new (features.txt of the kernel oracle run):
// features_incompat = 0xeb and "crc = 0xcf27361e (correct)". xfs_db prints the
// four crc bytes cf 27 36 1e as a big-endian number; sb_crc is __le32 in
// xfs_format.h, so the value the kernel compares is 0x1e3627cf.
const (
	newIncompat = 0xeb
	newCRC      = 0x1e3627cf
)

// TestMeasuredDisksOpen: the incompat bits present on the two disks where the
// reader matched the kernel (0x2b on gpt-xfs-nrext64, 0xeb on gpt-xfs-new) are
// all accepted.
func TestMeasuredDisksOpen(t *testing.T) {
	dir := fixturesDir(t)
	for _, disk := range fixtureDisks {
		disk := disk
		t.Run(disk, func(t *testing.T) {
			openDisk(t, dir, disk) // fails the test on any error
		})
	}
}

// TestUnsupportedIncompatRefused: a copy of gpt-xfs-new with one more incompat
// bit set (superblock CRC recomputed) is refused with ErrUnsupportedFeature and
// the error names the bits.
func TestUnsupportedIncompatRefused(t *testing.T) {
	dir := fixturesDir(t)
	tests := []struct {
		name string
		bits uint32
		want []string // all must appear in the error text
	}{
		{"META_UUID", 1 << 2, []string{"META_UUID", "0x4"}},
		{"NEEDSREPAIR", 1 << 4, []string{"NEEDSREPAIR", "0x10"}},
		{"METADIR", 1 << 8, []string{"METADIR", "0x100"}},
		{"bit in no Linux definition", 1 << 20, []string{"0x100000", "unknown"}},
		{"two bits", 1<<2 | 1<<4, []string{"META_UUID", "NEEDSREPAIR", "0x14"}},
	}
	raw := extractDisk(t, dir, "gpt-xfs-new")
	off, size := partition(t, dir, "gpt-xfs-new")
	orig, err := os.ReadFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			cp := t.TempDir() + "/disk.raw"
			if err := os.WriteFile(cp, orig, 0o600); err != nil {
				t.Fatal(err)
			}
			setIncompat(t, cp, off, newIncompat, newCRC, tt.bits)
			_, err := openXFS(t, cp, off, size)
			if !errors.Is(err, xfs.ErrUnsupportedFeature) {
				t.Fatalf("NewFS error = %v, want ErrUnsupportedFeature", err)
			}
			t.Logf("error text: %v", err)
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
		})
	}
}

var _ io.Reader // keep io imported for helpers in this file's siblings
