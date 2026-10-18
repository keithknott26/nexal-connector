//go:build !unix

package inference

// ReapOrphanPoolServers does nothing where there is no process table to read.
func ReapOrphanPoolServers(string) []int { return nil }
