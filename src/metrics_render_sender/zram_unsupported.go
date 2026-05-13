//go:build !linux

package main

func isZramAvailable() bool {
	return false
}

func detectZramSource() (zramSource, bool) {
	return zramSource{}, false
}

func readZramSnapshot(source zramSource) (zramSnapshot, error) {
	return zramSnapshot{}, nil
}
