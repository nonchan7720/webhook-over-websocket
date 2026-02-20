package cmd

import "fmt"

func Execute() {
	cmd := rootCommand()
	if err := cmd.Execute(); err != nil {
		fmt.Println(err.Error())
	}
}
