//go:build !darwin && !linux

package runtimebridge

import "os/exec"

func checkLocalDirectory(string) error                      { return ErrPlatform }
func readLocalFile(string, int, bool, bool) ([]byte, error) { return nil, ErrPlatform }
func prepareProcess(*exec.Cmd) error                        { return ErrPlatform }
func killProcessGroup(*exec.Cmd) error                      { return ErrPlatform }
func acquireConfigLock(string) (func(), error)              { return nil, ErrPlatform }
