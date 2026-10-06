package main

import "testing"

func TestIsInternalRmteFile(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"rmte-1234.meta", true},
		{"rmte-abcd.pid", true},
		{"rmte-sess99.log", true},
		{"/path/to/rmte-test.meta", true},
		{"C:\\workspace\\rmte-test.pid", true},
		{"main.go", false},
		{"rmte.exe", false},
		{"rmte_notes.txt", false},
		{"rmte-other.txt", false},
		{"notes-rmte.meta", false},
	}

	for _, tc := range tests {
		got := isInternalRmteFile(tc.input)
		if got != tc.expected {
			t.Errorf("isInternalRmteFile(%q) = %v; want %v", tc.input, got, tc.expected)
		}
	}
}
