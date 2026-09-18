package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"kovar-gateway/internal/platform/database"
	"kovar-gateway/migrations"
)

func main() {
	if len(os.Args) != 2 || (os.Args[1] != "up" && os.Args[1] != "down") {
		fmt.Fprintln(os.Stderr, "usage: migrate up|down")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := database.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "database connection failed (details withheld to protect connection credentials)")
		os.Exit(1)
	}
	defer db.Close()
	if err = migrations.Apply(ctx, db, os.Args[1] == "down"); err != nil {
		fmt.Fprintln(os.Stderr, "migration failed:", err)
		os.Exit(1)
	}
	fmt.Println("migration", os.Args[1], "complete")
}
