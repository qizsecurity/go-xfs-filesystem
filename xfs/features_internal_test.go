package xfs

import (
	"errors"
	"strings"
	"testing"
)

// Bit names and values: Linux fs/xfs/libxfs/xfs_format.h (v6.17),
// XFS_SB_FEAT_INCOMPAT_*. Allowed: the bits present on the disks where this
// reader matched the Linux kernel (gpt-xfs-nrext64 0x2b, gpt-xfs-new 0xeb).
func TestCheckIncompat(t *testing.T) {
	bits := []struct {
		name    string
		value   uint32
		allowed bool
	}{
		{"FTYPE", 0x1, true},
		{"SPINODES", 0x2, true},
		{"META_UUID", 0x4, false},
		{"BIGTIME", 0x8, true},
		{"NEEDSREPAIR", 0x10, false},
		{"NREXT64", 0x20, true},
		{"EXCHRANGE", 0x40, true},
		{"PARENT", 0x80, true},
		{"METADIR", 0x100, false},
		{"ZONED", 0x200, false},
		{"ZONE_GAPS", 0x400, false},
	}
	for _, b := range bits {
		t.Run(b.name, func(t *testing.T) {
			err := checkIncompat(SuperBlock{Versionnum: 5, FeaturesIncompat: b.value})
			if b.allowed {
				if err != nil {
					t.Fatalf("allowed bit refused: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrUnsupportedFeature) {
				t.Fatalf("err = %v, want ErrUnsupportedFeature", err)
			}
			if !strings.Contains(err.Error(), b.name) {
				t.Fatalf("error %q does not name %s", err, b.name)
			}
		})
	}

	t.Run("all allowed bits together", func(t *testing.T) {
		if err := checkIncompat(SuperBlock{Versionnum: 5, FeaturesIncompat: 0xeb}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("unknown bit is named by value", func(t *testing.T) {
		err := checkIncompat(SuperBlock{Versionnum: 5, FeaturesIncompat: 0xeb | 1<<31})
		if !errors.Is(err, ErrUnsupportedFeature) || !strings.Contains(err.Error(), "0x80000000") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("version 4 superblock has no incompat word", func(t *testing.T) {
		// xfs_sb_has_incompat_feature() looks at the word only on a v5 superblock.
		if err := checkIncompat(SuperBlock{Versionnum: 4, FeaturesIncompat: 0xffffffff}); err != nil {
			t.Fatalf("v4 superblock refused for a field it does not have: %v", err)
		}
	})
}
