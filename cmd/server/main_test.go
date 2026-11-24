package main

import "testing"

func TestReadinessDatabaseWithoutPool(t *testing.T) {
	if readinessDatabase(nil) != nil {
		t.Fatal("nil pool must remain a nil database interface")
	}
}
