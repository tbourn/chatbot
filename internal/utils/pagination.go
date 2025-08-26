// Package utils provides small, generic helper functions used across
// different layers of the application. These utilities are independent
// of domain or business logic.
package utils

import "strconv"

// AtoiDefault converts a string to an int using strconv.Atoi.
// If the string is empty or cannot be parsed as an integer,
// it returns the provided default value instead.
//
// Example:
//
//	n := utils.AtoiDefault("42", 0) // returns 42
//	n = utils.AtoiDefault("", 10)   // returns 10
//	n = utils.AtoiDefault("x", 5)   // returns 5
func AtoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}
