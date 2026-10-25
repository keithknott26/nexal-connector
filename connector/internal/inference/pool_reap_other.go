//go:build !unix

package inference

// ReapOrphanPoolServers does nothing where there is no process table to read.
func ReapOrphanPoolServers(string) []int { return nil }

// ReapOrphanServeServers does nothing where there is no process table to read.
func ReapOrphanServeServers(string) []int { return nil }
