package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/thellmwhisperer/la-roca-vector/internal/model"
)

func main() {
	flags := flag.NewFlagSet("model-release", flag.ExitOnError)
	tag := flags.String("tag", "", "release tag to validate")
	output := flags.String("out", "", "directory for verified release assets")
	flags.Parse(os.Args[1:])
	if err := run(context.Background(), *tag, *output); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, tag, output string) error {
	if err := model.ValidateReleaseTag(tag); err != nil {
		return err
	}
	if output == "" {
		return fmt.Errorf("--out is required")
	}
	artifacts, err := model.StageRelease(ctx, output, model.DefaultReleaseSpec(), nil)
	if err != nil {
		return err
	}
	fmt.Printf("model release: %s\nasset: %s\nlicense: %s\nchecksums: %s\n",
		model.ReleaseTag, artifacts.AssetName, artifacts.LicenseName, artifacts.ChecksumsName)
	return nil
}
