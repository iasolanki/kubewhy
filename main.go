package main

import "github.com/iasolanki/k8said/cmd"

var version = "dev"

func main() {
	cmd.Execute(version)
}
