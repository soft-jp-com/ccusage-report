//go:build !windows

package main

// osJapanese は LANG などのロケール設定が日本語か。
func osJapanese() bool {
	isJa, _ := envJapanese()
	return isJa
}
