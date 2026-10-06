package main

import (
	"reflect"
	"testing"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		input    string
		expected []int
	}{
		{"0.5.0", []int{0, 5, 0}},
		{"v0.5.0", []int{0, 5, 0}},
		{"1.0.0-rc1", []int{1, 0, 0}},
		{"1.2", []int{1, 2}},
		{"v2.10.4+build123", []int{2, 10, 4}},
	}

	for _, tc := range tests {
		got := parseVersion(tc.input)
		if !reflect.DeepEqual(got, tc.expected) {
			t.Errorf("parseVersion(%q) = %v; want %v", tc.input, got, tc.expected)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		v1       string
		v2       string
		expected int
	}{
		{"0.5.0", "0.5.0", 0},
		{"0.5.0", "0.5.1", -1},
		{"0.5.1", "0.5.0", 1},
		{"v0.5.0", "0.5.0", 0},
		{"0.4.9", "0.5.0", -1},
		{"1.0.0", "0.9.9", 1},
		{"0.5.0", "0.5.0.1", -1},
		{"dev", "0.5.0", -1},
		{"0.5.0", "dev", 1},
	}

	for _, tc := range tests {
		got := compareVersions(tc.v1, tc.v2)
		if got != tc.expected {
			t.Errorf("compareVersions(%q, %q) = %d; want %d", tc.v1, tc.v2, got, tc.expected)
		}
	}
}
