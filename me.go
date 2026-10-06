package main

import (
	"encoding/json"
	"fmt"
	"net/http"
)

type meInfo struct {
	Email     string  `json:"email"`
	SSHPubkey *string `json:"ssh_pubkey"`
	Quota     int     `json:"quota"`
}

func getMe(a api) (*meInfo, error) {
	resp, data, err := a.do("GET", "/v1/me", nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, mapped(resp, data)
	}
	var me meInfo
	if err := json.Unmarshal(data, &me); err != nil {
		return nil, err
	}
	return &me, nil
}

func cmdMe([]string) error {
	_, a := requireLogin()
	me, err := getMe(a)
	if err != nil {
		return err
	}
	pub := "none"
	if me.SSHPubkey != nil && *me.SSHPubkey != "" {
		pub = sanitize(*me.SSHPubkey)
	}
	fmt.Printf("email:  %s\nquota:  %d\npubkey: %s\n", sanitize(me.Email), me.Quota, pub)
	return nil
}
