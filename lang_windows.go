//go:build windows

package main

import "syscall"

// osJapanese は Windows の表示言語（UI 言語）が日本語か。
func osJapanese() bool {
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetUserDefaultUILanguage")
	if proc.Find() == nil {
		langID, _, _ := proc.Call()
		return langID&0x3ff == 0x11 // LANG_JAPANESE
	}
	isJa, _ := envJapanese()
	return isJa
}
