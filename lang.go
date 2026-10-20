package main

import (
	"os"
	"strings"
)

// ja が true ならレポートとメッセージを日本語で、false なら英語で出す。
var ja = true

// tr は表示言語に合わせて日本語か英語の文字列を返す。
func tr(jaText, enText string) string {
	if ja {
		return jaText
	}
	return enText
}

// envJapanese は LC_ALL / LC_MESSAGES / LANG が日本語か。判断できなければ ok = false。
func envJapanese() (isJa, ok bool) {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(k); v != "" && v != "C" && v != "POSIX" {
			return strings.HasPrefix(strings.ToLower(v), "ja"), true
		}
	}
	return false, false
}
