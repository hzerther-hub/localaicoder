//go:build !linux

// 非 Linux：无需外部工具链（库内捕获）。
package main

func captureLinux(path string) (bool, bool) { return false, false }
