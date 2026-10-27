package xfs

import (
	"fmt"
	"strings"
)

type SuperBlock struct {
	Magicnum   uint32
	BlockSize  uint32
	Dblocks    uint64
	Rblocks    uint64
	Rextens    uint64
	UUID       [16]byte
	Logstart   uint64
	Rootino    uint64
	Rbmino     uint64
	Rsmino     uint64
	Rextsize   uint32
	Agblocks   uint32
	Agcount    uint32
	Rbblocks   uint32
	Logblocks  uint32
	Versionnum uint16
	Sectsize   uint16
	Inodesize  uint16
	Inopblock  uint16
	Fname      [12]byte
	Blocklog   uint8
	Sectlog    uint8
	Inodelog   uint8
	Inopblog   uint8
	Agblklog   uint8
	Rextslog   uint8
	Inprogress uint8
	ImaxPct    uint8

	Icount    uint64
	Ifree     uint64
	Fdblocks  uint64
	Frextents uint64

	Uqunotino   uint64
	Gquotino    uint64
	Qflags      uint16
	Flags       uint8
	SharedVn    uint8
	Inoalignmt  uint32
	Unit        uint32
	Width       uint32
	Dirblklog   uint8
	Logsectlog  uint8
	Logsectsize uint16
	Logsunit    uint32
	Features2   uint32

	BadFeatures2        uint32
	FeaturesCompat      uint32
	FeaturesRoCompat    uint32
	FeaturesIncompat    uint32
	FeaturesLogIncompat uint32
	CRC                 uint32
	SpinoAlign          uint32
	Pquotino            uint64
	Lsn                 int64
	MetaUUID            [16]byte
}

// return (AG number), (Inode Block), (Inode Offset)
func (sb SuperBlock) InodeOffset(inodeNumber uint64) (int, uint64, uint64) {
	offsetAddress := sb.Inopblog + sb.Agblklog
	lowMask := 1<<(offsetAddress) - 1
	AGNumber := inodeNumber >> uint32(offsetAddress)

	relativeInodeNumber := inodeNumber & uint64(lowMask)
	InodeBlock := relativeInodeNumber / uint64(sb.Inopblock)
	InodeOffset := relativeInodeNumber % uint64(sb.Inopblock)

	return int(AGNumber), InodeBlock, InodeOffset
}

// return Offset
func (sb SuperBlock) InodeAbsOffset(inodeNumber uint64) uint64 {
	agNumber, blockCount, inodeOffset := sb.InodeOffset(inodeNumber)

	offset := (uint64(agNumber) * uint64(sb.Agblocks) * uint64(sb.BlockSize)) +
		(blockCount * uint64(sb.BlockSize)) +
		inodeOffset*uint64(sb.Inodesize)

	return offset
}

func (sb SuperBlock) BlockToAgNumber(n uint64) uint64 {
	return n >> uint64(sb.Agblklog)
}

func (sb SuperBlock) BlockToAgBlockNumber(n uint64) uint64 {
	return n & Mask64Lo(int64(sb.Agblklog))
}

func (sb SuperBlock) BlockToPhysicalOffset(n uint64) int64 {
	return int64(sb.BlockToAgNumber(n)*uint64(sb.Agblocks) + sb.BlockToAgBlockNumber(n))
}

// HasFtype returns true if directory entries contain a file type field.
// V5 (CRC) filesystems use FeaturesIncompat, V4 uses Features2.
func (sb SuperBlock) HasFtype() bool {
	return sb.FeaturesIncompat&XFS_SB_FEAT_INCOMPAT_FTYPE != 0 ||
		sb.Features2&XFS_SB_VERSION2_FTYPE != 0
}

// HasBigtime returns true if the filesystem has the bigtime feature enabled.
// Bigtime extends the timestamp range from 1901-2038 to 1901-2486.
func (sb SuperBlock) HasBigtime() bool {
	return sb.FeaturesIncompat&XFS_SB_FEAT_INCOMPAT_BIGTIME != 0
}

// HasSparseInodes returns true if the filesystem has the sparse inode feature enabled.
func (sb SuperBlock) HasSparseInodes() bool {
	return sb.FeaturesIncompat&XFS_SB_FEAT_INCOMPAT_SPINODES != 0
}

// IsV5 returns true for a version 5 superblock (the "crc" file system), like
// xfs_sb_is_v5() in Linux fs/xfs/libxfs/xfs_format.h:
// sb_versionnum & XFS_SB_VERSION_NUMBITS == XFS_SB_VERSION_5.
// The feature words after Features2 exist only in a version 5 superblock.
func (sb SuperBlock) IsV5() bool {
	return sb.Versionnum&XFS_SB_VERSION_NUMBITS == XFS_SB_VERSION_5
}

// incompatNames are the XFS_SB_FEAT_INCOMPAT_* bits of Linux
// fs/xfs/libxfs/xfs_format.h (v6.17), in bit order.
var incompatNames = []struct {
	bit  uint32
	name string
}{
	{XFS_SB_FEAT_INCOMPAT_FTYPE, "FTYPE"},
	{XFS_SB_FEAT_INCOMPAT_SPINODES, "SPINODES"},
	{XFS_SB_FEAT_INCOMPAT_META_UUID, "META_UUID"},
	{XFS_SB_FEAT_INCOMPAT_BIGTIME, "BIGTIME"},
	{XFS_SB_FEAT_INCOMPAT_NEEDSREPAIR, "NEEDSREPAIR"},
	{XFS_SB_FEAT_INCOMPAT_NREXT64, "NREXT64"},
	{XFS_SB_FEAT_INCOMPAT_EXCHRANGE, "EXCHRANGE"},
	{XFS_SB_FEAT_INCOMPAT_PARENT, "PARENT"},
	{XFS_SB_FEAT_INCOMPAT_METADIR, "METADIR"},
	{XFS_SB_FEAT_INCOMPAT_ZONED, "ZONED"},
	{XFS_SB_FEAT_INCOMPAT_ZONE_GAPS, "ZONE_GAPS"},
}

// supportedIncompat are the incompat bits this reader accepts: the ones present
// on the disks where its output matched the Linux kernel's (gpt-xfs-nrext64
// 0x2b = FTYPE|SPINODES|BIGTIME|NREXT64; gpt-xfs-new 0xeb = those plus
// EXCHRANGE|PARENT). NEEDSREPAIR is never accepted.
const supportedIncompat = XFS_SB_FEAT_INCOMPAT_FTYPE |
	XFS_SB_FEAT_INCOMPAT_SPINODES |
	XFS_SB_FEAT_INCOMPAT_BIGTIME |
	XFS_SB_FEAT_INCOMPAT_NREXT64 |
	XFS_SB_FEAT_INCOMPAT_EXCHRANGE |
	XFS_SB_FEAT_INCOMPAT_PARENT

// checkIncompat returns ErrUnsupportedFeature, naming the bits, if the
// superblock sets an incompat bit outside supportedIncompat. Only a version 5
// superblock has the feature words (xfs_sb_has_incompat_feature in Linux).
func checkIncompat(sb SuperBlock) error {
	if !sb.IsV5() {
		return nil
	}
	bad := sb.FeaturesIncompat &^ supportedIncompat
	if bad == 0 {
		return nil
	}
	var names []string
	known := uint32(0)
	for _, n := range incompatNames {
		known |= n.bit
		if bad&n.bit != 0 {
			names = append(names, n.name)
		}
	}
	if bad&^known != 0 {
		names = append(names, fmt.Sprintf("unknown bits 0x%x", bad&^known))
	}
	return fmt.Errorf("%w: incompat bits 0x%x (%s)", ErrUnsupportedFeature, bad, strings.Join(names, ", "))
}
