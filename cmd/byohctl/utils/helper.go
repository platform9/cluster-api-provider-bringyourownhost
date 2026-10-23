// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"bufio"
	"fmt"
	"os"
)

// AskBool function asks for the user input
// for a boolean input
func AskBool(msg string, args ...interface{}) (bool, error) {
	_, err := fmt.Fprintf(os.Stdout, fmt.Sprintf("%s: ", msg), args...)
	if err != nil {
		return false, fmt.Errorf("Unable to show options to user: %s", err.Error()) //nolint:staticcheck // user-facing message kept unchanged
	}

	r := bufio.NewReader(os.Stdin)
	byt, isPrefix, err := r.ReadLine()

	if isPrefix || err != nil {
		return false, fmt.Errorf("Unable to read i/p: %s", err.Error()) //nolint:staticcheck // user-facing message kept unchanged
	}

	resp := string(byt)
	if resp == "y" || resp == "Y" {
		return true, nil
	}

	if resp == "n" || resp == "N" {
		return false, nil
	}

	return false, fmt.Errorf("Please provide input as y or n, provided: %s", resp) //nolint:staticcheck // user-facing message kept unchanged
}
