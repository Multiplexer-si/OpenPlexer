package main

import "syscall"

const (
	joinGetTermios = syscall.TIOCGETA
	joinSetTermios = syscall.TIOCSETA
)
