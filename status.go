package main

import (
	"fmt"
)

// cmdStatus answers the Customer's one question — is my machine on? — in
// plain words. Which row is the machine is the door's question, not this
// command's: it renders the answer the door gave — the machine's row,
// none, or the several-row anomaly admitted as unusual — and the state
// is read from the row's own flags (its live / asleep / creating words),
// never guessed from the clock. The machine's raw id never appears here:
// ids live in `zert ls` and nowhere else on the customer-facing surface.
func cmdStatus([]string) error {
	_, a := requireLogin()
	which, row, err := askWhichRow(a)
	if err != nil {
		return err
	}
	switch which {
	case doorRowNone:
		fmt.Println("no machine yet — run `zert vm` to make one")
		return nil
	case doorRowSeveral, doorRowDead:
		// the Customer has one machine, so the door naming no row is a
		// server-side anomaly this command must not paper over —
		// whether several machines answer or no row does, the admission
		// points at the technical listing instead of naming whatever
		// state an arbitrary row happens to carry
		fmt.Println(errUnusualListing.Error())
		return nil
	}
	r := *row // the Customer's one machine, whatever state word its row carries
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
