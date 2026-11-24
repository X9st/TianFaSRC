package system

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

type Database interface {
	Ping(context.Context) error
}

type Check struct {
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type Readiness struct {
	Database Database
	DataDir  string
}

func (r Readiness) Checks(ctx context.Context) map[string]Check {
	checks := make(map[string]Check, 3)
	if r.Database == nil {
		checks["database"] = Check{Status: "down", Detail: "database is not configured"}
	} else if err := r.Database.Ping(ctx); err != nil {
		checks["database"] = Check{Status: "down", Detail: "database is unavailable"}
	} else {
		checks["database"] = Check{Status: "up"}
	}

	checks["blob_store"] = writableDirectory(filepath.Join(r.DataDir, "blobs"))
	checks["workspace"] = writableDirectory(filepath.Join(r.DataDir, "workspaces"))
	return checks
}

func AllUp(checks map[string]Check) bool {
	for _, check := range checks {
		if check.Status != "up" {
			return false
		}
	}
	return true
}

func writableDirectory(path string) Check {
	if err := os.MkdirAll(path, 0o750); err != nil {
		return Check{Status: "down", Detail: fmt.Sprintf("create directory: %v", err)}
	}
	file, err := os.CreateTemp(path, ".ready-*")
	if err != nil {
		return Check{Status: "down", Detail: fmt.Sprintf("write directory: %v", err)}
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		return Check{Status: "down", Detail: fmt.Sprintf("close probe: %v", err)}
	}
	if err := os.Remove(name); err != nil {
		return Check{Status: "down", Detail: fmt.Sprintf("remove probe: %v", err)}
	}
	return Check{Status: "up"}
}
