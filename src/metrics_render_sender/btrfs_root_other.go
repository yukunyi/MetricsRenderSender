//go:build !linux

package main

type btrfsRootSource struct{}

func isBtrfsRootAvailable() bool {
	return false
}

func detectBtrfsRootSource() (btrfsRootSource, bool) {
	return btrfsRootSource{}, false
}

func readBtrfsRootSnapshot(source btrfsRootSource) (btrfsRootSnapshot, error) {
	return btrfsRootSnapshot{}, nil
}
