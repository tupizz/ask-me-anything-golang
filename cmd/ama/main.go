package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/tupizz/ask-me-anything-server/internal/api"
	"github.com/tupizz/ask-me-anything-server/internal/store/pgstore"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
)

func main() {
	if err := godotenv.Load(); err != nil {
		panic(".env file not found")
	}

	ctx := context.Background()
	connStr := fmt.Sprintf(
		"user=%s password=%s host=%s port=%s dbname=%s",
		os.Getenv("AMA_DB_USER"),
		os.Getenv("AMA_DB_PASSWORD"),
		os.Getenv("AMA_DB_HOST"),
		os.Getenv("AMA_DB_PORT"),
		os.Getenv("AMA_DB_NAME"),
	)

	pool, err := pgxpool.New(
		ctx,
		connStr,
	)

	if err != nil {
		panic(err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		panic(err)
	}

	handler := api.NewAPIHandler(pgstore.New(pool))

	/**
	>_ Non-Blocking Listening:
	This allows the server to run concurrently, without blocking the rest
	of the program's execution. If this was not done, the http.ListenAndServe
	function would block the main goroutine, preventing the code that handles
	graceful shutdown from running.
	*/
	go func() {
		slog.Info("Listening on port 8080")
		if err := http.ListenAndServe(":8080", handler); err != nil {
			if !errors.Is(err, http.ErrServerClosed) {
				panic(err)
			}
		}
	}()

	/**
	>_ Graceful Shutdown:
	The combination of listening for the interrupt signal and blocking until
	it's received allows the program to perform any necessary cleanup or shutdown
	procedures (not shown in this snippet) before exiting. This is crucial for
	avoiding abrupt termination, which could result in lost data or
	incomplete operations.
	*/

	// The make(chan os.Signal, 1) initializes a buffered channel with
	// a capacity of 1, meaning it can hold one signal at a time.
	quit := make(chan os.Signal, 1) // receive operating system signals
	//  This line tells the program to listen for a specific signal, os.Interrupt,
	//  which corresponds to the interrupt signal (e.g., when you press Ctrl+C
	// in the terminal)
	signal.Notify(quit, os.Interrupt)
	// This is a blocking operation that waits until a value is received on
	// the quit channel. The program will pause execution at this line until
	// the os.Interrupt signal is sent to the quit channel.
	<-quit

}
