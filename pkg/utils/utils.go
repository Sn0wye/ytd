package utils

import (
	"fmt"
	"os"
)

func ExitOnError(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
