// Command caseaudit is a READ-ONLY audit tool for the account-case cleanup
// (superior's mixed-case report, alice 01M13YN3 task 2). It opens the bbolt
// database read-only, sweeps the accounts bucket, and reports any keys that
// are not all-lowercase — count + samples — so running
// POST /admin/normalize-account-case can be verified before/after.
//
// Usage: caseaudit <path-to-agentmail.db> [sample-limit]
package main

import (
	"fmt"
	"os"

	bolt "go.etcd.io/bbolt"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: caseaudit <agentmail.db> [sample-limit]")
		os.Exit(2)
	}
	limit := 20
	if len(os.Args) > 2 {
		fmt.Sscanf(os.Args[2], "%d", &limit)
	}
	db, err := bolt.Open(os.Args[1], 0o600, &bolt.Options{ReadOnly: true})
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()

	total, mixed := 0, 0
	err = db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("accounts")).ForEach(func(k, v []byte) error {
			total++
			s := string(k)
			for _, r := range s {
				if r >= 'A' && r <= 'Z' {
					mixed++
					if mixed <= limit {
						fmt.Printf("MIXED-CASE KEY: %s\n", s)
					}
					break
				}
			}
			return nil
		})
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "sweep:", err)
		os.Exit(1)
	}
	fmt.Printf("total=%d mixed_case=%d (showing up to %d)\n", total, mixed, limit)
	if mixed > 0 {
		os.Exit(1) // nonzero exit = cleanup still pending
	}
}
