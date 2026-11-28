package main

import "syscall"

const (
	joinGetTermios = syscall.TCGETS
	joinSetTermios = syscall.TCSETS
)
