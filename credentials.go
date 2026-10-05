package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Credentials struct {
	Host     string
	Email    string
	Token    string
	Insecure bool
}

func credentialsPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "zert", "credentials"), nil
}

func loadCredentials() (*Credentials, error) {
	path, err := credentialsPath()
	if err != nil {
		return nil, err
	}
	// #nosec G304 -- path comes from os.UserConfigDir, not user input
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c := &Credentials{}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "host":
			c.Host = strings.TrimSpace(v)
		case "email":
			c.Email = strings.TrimSpace(v)
		case "token":
			c.Token = strings.TrimSpace(v)
		case "insecure":
			c.Insecure = strings.TrimSpace(v) == "true"
		}
	}
	if c.Host == "" || c.Token == "" {
		return nil, fmt.Errorf("malformed credentials file %s", path)
	}
	return c, nil
}

func saveCredentials(c *Credentials) error {
	path, err := credentialsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	body := fmt.Sprintf("host = %s\nemail = %s\ntoken = %s\n", c.Host, c.Email, c.Token)
	if c.Insecure {
		body += "insecure = true\n"
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		return err
	}
	// WriteFile only sets the mode on create; chmod tightens a pre-existing
	// file that may have looser permissions.
	return os.Chmod(path, 0600)
}

func deleteCredentials() error {
	path, err := credentialsPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
