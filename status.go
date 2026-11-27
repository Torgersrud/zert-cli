package main

import (
	"fmt"
)

// cmdStatus answers the Customer's one question — is my machine on? — in
// plain words. The state is read from the door's listing row (its live /
// asleep / creating flags), never guessed from the clock, and the machine's
// raw id never appears here: ids live in `zert ls` and nowhere else on the
// customer-facing surface.
func cmdStatus([]string) error {
	_, a := requireLogin()
	rows, err := listVMs(a)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Println("no machine yet — run `zert vm` to make one")
		return nil
	}
	r := rows[0] // the Customer's one machine: one live-or-asleep machine per customer
	switch {
	case r.Creating:
		fmt.Println("your machine is being created — give it a moment, then ask again")
	case r.Live:
		fmt.Printf("your machine is running — %s left before it goes to sleep\n", durationLeft(r))
	case r.Asleep:
		fmt.Println("your machine is asleep — connect to it and it wakes up, just as you left it")
	default:
		// a state word this build has never seen: no crash, no guess —
		// the machine exists, and `zert ls` shows the server's raw answer
		fmt.Println("your machine is in a state this version of zert does not know — `zert ls` shows what the server says")
	}
	return nil
}
