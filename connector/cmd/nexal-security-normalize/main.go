// nexal-security-normalize is an offline EVE adapter, not a live sensor.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"nexal/connector/internal/cybersecurity"
	"os"
	"time"
)

func main() {
	source := flag.String("source-id", "", "Persistent opaque capture ID (not a filename)")
	rules := flag.String("rules-version", "", "Installed public ruleset version, using letters/digits/_/-")
	flag.Parse()
	if *source == "" || *rules == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "source-id and rules-version required; read EVE JSONL from stdin")
		os.Exit(2)
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), cybersecurity.MaxEVEBytes+1)
	out := json.NewEncoder(os.Stdout)
	var offset uint64
	for scanner.Scan() {
		offset++
		event, err := cybersecurity.NormalizeEVE(scanner.Bytes(), *source, offset, *rules, time.Now())
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid EVE event at line %d\n", offset)
			os.Exit(1)
		}
		if event != nil {
			if err := out.Encode(event); err != nil {
				fmt.Fprintln(os.Stderr, "output unavailable")
				os.Exit(1)
			}
		}
	}
	if scanner.Err() != nil {
		fmt.Fprintln(os.Stderr, "input unavailable or line limit exceeded")
		os.Exit(1)
	}
}
