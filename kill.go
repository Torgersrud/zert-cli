package main

import (
	"errors"
	"fmt"
	"net/http"
)

func cmdKill(args []string) error {
	if len(args) != 1 || args[0] == "" {
		return errors.New("usage: zert kill <id>")
	}
	if !validSandboxID(args[0]) {
		return errors.New("invalid vm id")
	}
	_, a := requireLogin()
	resp, data, err := a.do("DELETE", vmPath(args[0]), nil)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return mapped(resp, data)
	}
	fmt.Printf("killed %s\n", args[0])
	return nil
}
