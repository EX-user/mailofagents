// Command caseaudit is a READ-ONLY audit tool for the account-case cleanup
// (superior's mixed-case report, alice 01M13YN3 task 2). It opens the bbolt
// database read-only, sweeps the accounts bucket, and reports any keys that
// are not all-lowercase — count + samples — so running
// POST /admin/normalize-account-case can be verified before/after.
//
// Usage: caseaudit <path-to-agentmail.db> [sample-limit]
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// rowIsAdmin decodes the is_admin flag of an account row (false on any
// decode problem — audit output must not crash on legacy shapes).
func rowIsAdmin(v []byte) bool {
	var acc struct {
		IsAdmin bool `json:"is_admin"`
	}
	return json.Unmarshal(v, &acc) == nil && acc.IsAdmin
}

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

	total, mixed, admins := 0, 0, 0
	err = db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("accounts")).ForEach(func(k, v []byte) error {
			total++
			s := string(k)
			admin := rowIsAdmin(v)
			if admin {
				admins++
				if admins <= limit {
					fmt.Printf("ADMIN ROW: %s\n", s)
				}
			}
			for _, r := range s {
				if r >= 'A' && r <= 'Z' {
					mixed++
					if mixed <= limit {
						twin := "(no lowercase twin)"
						if tv := tx.Bucket([]byte("accounts")).Get([]byte(strings.ToLower(s))); tv != nil {
							twin = fmt.Sprintf("twin is_admin=%v", rowIsAdmin(tv))
						}
						fmt.Printf("MIXED-CASE KEY: %s (is_admin=%v, %s)\n", s, admin, twin)
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
	fmt.Printf("total=%d mixed_case=%d admin_rows=%d (showing up to %d)\n", total, mixed, admins, limit)
	if mixed > 0 {
		os.Exit(1) // nonzero exit = cleanup still pending
	}
}
