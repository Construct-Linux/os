package update

// The partition types of a slot, x86-64's in the Discoverable Partitions Specification: what the
// build's repart.d gives them (Type=root, Type=root-verity), and what systemd-sysupdate's
// transfers match a slot's partitions by.
const (
	TypeRoot       = "4f68bce3-e8cd-4db1-96e7-fbcaf984b709"
	TypeRootVerity = "2c7357ed-ebd2-46d9-aec1-23d437ec2bf5"
)

// Empty is systemd-sysupdate's label for a partition it may write: slot B's until the first
// update, and slot A's while an install writes it.
const Empty = "_empty"
