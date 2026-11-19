// fakeghost is a test-only gh-ost double (test builds only; compiled by osc/runner_test.go).
// Behavior is encoded via the --alter text (since Runner minimizes env and strips mode hints):
//
//	MODE_OK    print copy progress -> cutover -> cleanup -> exit 0
//	MODE_FAIL  print progress -> exit 1
//	MODE_HANG  print progress -> sleep indefinitely (for cancellation / process-loss injection)
//	MODE_DUMP  write argv/env dump (fakeghost-dump) in working directory then exit 0
//
// --ask-pass semantics: read one password line from stdin (never echoed; dump records only an ask-pass marker).
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"
)

func main() {
	alter := ""
	for _, a := range os.Args[1:] {
		if strings.HasPrefix(a, "--alter=") {
			alter = strings.TrimPrefix(a, "--alter=")
		}
	}
	// ask-pass: read password line from stdin (simulating gh-ost prompt read without emitting any output).
	stat, err := os.Stdin.Stat()
	if err == nil && (stat.Mode()&os.ModeCharDevice) == 0 {
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	}
	if strings.Contains(alter, "MODE_DUMP") {
		if f, err := os.Create("fakeghost-dump"); err == nil {
			for _, a := range os.Args {
				fmt.Fprintf(f, "ARG %s\n", a)
			}
			for _, e := range os.Environ() {
				fmt.Fprintf(f, "ENV %s\n", e)
			}
			_ = f.Close()
		}
	}
	switch {
	case strings.Contains(alter, "MODE_PTCOPY_OK"):
		fmt.Println("Copying `shop`.`accounts`/`shop`.`_accounts_new`: 40%")
		time.Sleep(1200 * time.Millisecond)
		fmt.Println("Copying `shop`.`accounts`/`shop`.`_accounts_new`: 100%")
		fmt.Println("Successfully altered `shop`.`accounts`.")
	case strings.Contains(alter, "MODE_FAIL"):
		fmt.Println("Copy: 50/100 rows")
		fmt.Fprintln(os.Stderr, "gh-ost: fatal error")
		os.Exit(1)
	case strings.Contains(alter, "MODE_HANG"):
		fmt.Println("Copy: 10/1000 rows")
		time.Sleep(120 * time.Second)
	default:
		fmt.Println("Copy: 25/100 rows")
		time.Sleep(1200 * time.Millisecond) // Cross Runner's 1s heartbeat throttle window deterministically
		fmt.Println("Copy: 100/100 rows")
		fmt.Println("Waiting for postpone cut-over")
		fmt.Println("CutOver: done")
		fmt.Println("Cleanup: done")
	}
}
