package xfs_test

import (
	"strings"
	"testing"
)

// TestOracleEveryRow compares every row of the kernel oracle (type, size,
// perm, mtime with nanoseconds, sha256 of regular files, symlink target) with
// what the library reports for the same disk, and the reverse (no extra paths).
func TestOracleEveryRow(t *testing.T) {
	dir := fixturesDir(t)
	for _, disk := range fixtureDisks {
		disk := disk
		t.Run(disk, func(t *testing.T) {
			fsys := openDisk(t, dir, disk)
			want := loadOracle(t, dir, disk)
			got := walkRows(t, fsys)
			if d := diffRows(want, got); len(d) > 0 {
				t.Fatalf("%d of %d oracle rows differ:\n  %s", len(d), len(want), strings.Join(d, "\n  "))
			}
		})
	}
}
