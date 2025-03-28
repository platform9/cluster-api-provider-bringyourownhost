package utils

import (
	"bufio"
	"fmt"
	"os"
)

// AskBool function asks for the user input
// for a boolean input
func AskBool(msg string, args ...interface{}) (bool, error) {
	_, err := fmt.Fprintf(os.Stdout, fmt.Sprintf("%s (y/n): ", msg), args...)
	if err != nil {
		return false, fmt.Errorf("Unable to show options to user: %s", err.Error())
	}

	r := bufio.NewReader(os.Stdin)
	byt, isPrefix, err := r.ReadLine()

	if isPrefix || err != nil {
		return false, fmt.Errorf("Unable to read i/p: %s", err.Error())
	}

	resp := string(byt)
	if resp == "y" || resp == "Y" {
		return true, nil
	}

	if resp == "n" || resp == "N" {
		return false, nil
	}

	return false, fmt.Errorf("Please provide input as y or n, provided: %s", resp)
}
