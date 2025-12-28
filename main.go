package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"kuack-node/pkg/app"
	"kuack-node/pkg/config"

	"k8s.io/klog/v2"
)

// allow seam injection for testing.
//
//nolint:gochecknoglobals // top-level seams let tests stub signal/config/system exits without refactoring OS entrypoints
var (
	notifyContext  = signal.NotifyContext
	loadConfigFunc = config.LoadConfig
	runAppFunc     = app.Run
	exitOnError    = func(err error) {
		klog.Fatalf("%v", err)
	}
)

func main() {
	ctx, cancel := notifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cfg := loadConfigFunc(os.Getenv)

	err := runAppFunc(ctx, cfg, nil)
	if err != nil {
		exitOnError(err)
	}
}
