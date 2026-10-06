package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"text/tabwriter"
	"time"
)

type vmRow struct {
	SandboxID string  `json:"sandbox_id"`
	ExpiresAt float64 `json:"expires_at"`
	Live      bool    `json:"live"`
	Creating  bool    `json:"creating"`
}

func listVMs(a api) ([]vmRow, error) {
	resp, data, err := a.do("GET", "/v1/vm", nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, mapped(resp, data)
	}
	var rows []vmRow
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, err
	}
	for _, r := range rows {
		if !validSandboxID(r.SandboxID) {
			return nil, errors.New("malformed server response: invalid vm id")
		}
	}
	return rows, nil
}

func expiryLabel(r vmRow) string {
	switch {
	case r.Creating:
		return "creating"
	case !r.Live:
		return "expired"
	default:
		return time.Until(time.Unix(int64(r.ExpiresAt), 0)).Round(time.Second).String() + " left"
	}
}

func printVMs(rows []vmRow) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tLIVE\tEXPIRES\tCREATING")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%v\t%s\t%v\n", r.SandboxID, r.Live, expiryLabel(r), r.Creating)
	}
	_ = w.Flush()
}

func cmdLS([]string) error {
	_, a := requireLogin()
	rows, err := listVMs(a)
	if err != nil {
		return err
	}
	printVMs(rows)
	return nil
}
