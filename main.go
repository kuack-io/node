package main

import (
	"context"
	"os/signal"
	"syscall"

	"kuack-node/pkg/app"
	"kuack-node/pkg/config"

	"k8s.io/klog/v2"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cfg := config.LoadConfig()

	err := app.Run(ctx, cfg)
	if err != nil {
		klog.Fatalf("%v", err)
	}
}
