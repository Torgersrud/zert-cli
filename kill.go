package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
)

// killAll reports whether args mean "every vm" (all, or a quoted *).
func killAll(args []string) bool {
	return len(args) == 1 && (args[0] == "all" || args[0] == "*")
}

func killIDs(a api, stderr io.Writer, ids []string) error {
	var failed int
	for _, id := range ids {
		resp, data, err := a.do("DELETE", vmPath(id), nil)
		if err != nil {
			fmt.Fprintf(stderr, "zert: kill %s failed: %v\n", id, err)
			failed++
			continue
		}
		if resp.StatusCode != http.StatusOK {
			fmt.Fprintf(stderr, "zert: kill %s failed: %v\n", id, mapped(resp, data))
			failed++
			continue
		}
		fmt.Printf("killed %s\n", id)
	}
	if failed > 0 {
		return &exitError{1, fmt.Errorf("%d of %d vms not killed", failed, len(ids))}
	}
	return nil
}

func cmdKill(args []string) error {
	if len(args) == 0 || args[0] == "" {
		return errors.New("usage: zert kill <id>|all")
	}
	if killAll(args) {
		_, a := requireLogin()
		rows, err := listVMs(a)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			fmt.Println("no vms to kill")
			return nil
		}
		ids := make([]string, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.SandboxID)
		}
		return killIDs(a, os.Stderr, ids)
	}
	for _, id := range args {
		if !validSandboxID(id) {
			return errors.New("invalid vm id")
		}
	}
	_, a := requireLogin()
	return killIDs(a, os.Stderr, args)
}
