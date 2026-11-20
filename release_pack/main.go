package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	r, e := decode(os.Stdin)
	if e == nil {
		var b []byte
		b, e = pack(ctx, r)
		if e == nil {
			_, e = os.Stdout.Write(b)
		}
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, "release.pack:", e)
		os.Exit(1)
	}
}
