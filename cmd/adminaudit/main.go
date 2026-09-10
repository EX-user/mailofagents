// Command adminaudit is a READ-ONLY audit tool for admin-row diagnostics.
// It opens the bbolt database read-only and prints the account rows that
// matter for the admin-panel regression triage (alice 01M23VT3 thread):
// address, is_admin, disabled, created_at — so the IsAdmin-bit hypothesis
// can be confirmed/refuted on the host WITHOUT the live admin password
// (Sam's seed secret went stale, 401 since 09-05; probes via HTTP cannot
// read the flag without valid admin credentials).
//
// Zero mutation: bolt.Options{ReadOnly: true}, no Put anywhere.
//
// Run against a COPY of the db if the server must stay up (bbolt takes an
// exclusive file lock; a second process cannot open the live file). Stop -
// copy - audit - restart also works (sub-second audit).
//
// Usage: adminaudit <agentmail.db> [address-filter]
//
//	adminaudit agentmail.db                 → every row, one per line
//	adminaudit agentmail.db admin@          → rows whose address contains "admin@"
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

type row struct {
	UUID      string `json:"uuid"`
	Address   string `json:"address"`
	IsAdmin   bool   `json:"is_admin"`
	Disabled  bool   `json:"disabled"`
	CreatedAt int64  `json:"created_at"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: adminaudit <agentmail.db> [address-filter]")
		fmt.Fprintln(os.Stderr, "  read-only: prints address/is_admin/disabled/created_at per row")
		os.Exit(2)
	}
	filter := ""
	if len(os.Args) > 2 {
		filter = strings.ToLower(os.Args[2])
	}
	db, err := bolt.Open(os.Args[1], 0o600, &bolt.Options{ReadOnly: true})
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		fmt.Fprintln(os.Stderr, "(if the file is locked, the live server holds the bolt lock — audit a copy)")
		os.Exit(1)
	}
	defer db.Close()

	fmt.Printf("%-40s %-9s %-9s %s\n", "ADDRESS", "IS_ADMIN", "DISABLED", "CREATED_AT")
	admins := 0
	total := 0
	err = db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("accounts")).ForEach(func(k, v []byte) error {
			var r row
			if err := json.Unmarshal(v, &r); err != nil {
				return nil // skip undecodable rows silently; audit never dies on one bad record
			}
			total++
			if filter != "" && !strings.Contains(strings.ToLower(r.Address), filter) {
				return nil
			}
			if r.IsAdmin {
				admins++
			}
			fmt.Printf("%-40s %-9v %-9v %s\n",
				r.Address, r.IsAdmin, r.Disabled,
				time.Unix(r.CreatedAt, 0).Format("2006-01-02 15:04:05"))
			return nil
		})
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "scan:", err)
		os.Exit(1)
	}
	fmt.Printf("-- %d rows scanned, %d with is_admin=true\n", total, admins)
}
