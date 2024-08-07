package main

import (
	"github.com/joho/godotenv"
	"os/exec"
)

func main() {
	if err := godotenv.Load(); err != nil {
		panic(err)
	}

	cmd := exec.Command("tern", "migrate", "--migrations", "./internal/store/pgstore/migrations", "--config", "./internal/store/pgstore/migrations/tern.conf")
	if err := cmd.Run(); err != nil {
		panic(err)
	}

	println("migrations complete")
}
