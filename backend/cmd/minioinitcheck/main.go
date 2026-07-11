package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/emfont/emfont/backend/internal/platform/minioinitcheck"
)

func main() {
	if len(os.Args) < 2 {
		failUsage()
	}
	var err error
	switch os.Args[1] {
	case "policy":
		flags := flag.NewFlagSet("policy", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		policy := flags.String("policy", "", "expected policy name")
		bucket := flags.String("bucket", "", "expected bucket name")
		role := flags.String("role", "controller", "expected principal role")
		if flags.Parse(os.Args[2:]) != nil || flags.NArg() != 0 {
			failUsage()
		}
		switch *role {
		case "controller":
			err = minioinitcheck.Policy(os.Stdin, *policy, *bucket)
		case "cleanup":
			err = minioinitcheck.CleanupPolicy(os.Stdin, *policy, *bucket)
		default:
			failUsage()
		}
	case "user":
		flags := flag.NewFlagSet("user", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		policy := flags.String("policy", "", "expected direct policy")
		role := flags.String("role", "controller", "expected principal role")
		if flags.Parse(os.Args[2:]) != nil || flags.NArg() != 0 {
			failUsage()
		}
		var accessKey string
		switch *role {
		case "controller":
			accessKey = os.Getenv("EMFONT_MINIO_ACCESS_KEY")
		case "cleanup":
			accessKey = os.Getenv("EMFONT_MINIO_CLEANUP_ACCESS_KEY")
		default:
			failUsage()
		}
		err = minioinitcheck.User(os.Stdin, accessKey, *policy)
	case "identity":
		flags := flag.NewFlagSet("identity", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		subsystem := flags.String("subsystem", "", "expected identity-provider subsystem")
		if flags.Parse(os.Args[2:]) != nil || flags.NArg() != 0 {
			failUsage()
		}
		err = minioinitcheck.IdentityProviders(os.Stdin, *subsystem)
	case "anonymous":
		flags := flag.NewFlagSet("anonymous", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		target := flags.String("target", "", "expected alias and bucket")
		if flags.Parse(os.Args[2:]) != nil || flags.NArg() != 0 {
			failUsage()
		}
		err = minioinitcheck.Anonymous(os.Stdin, *target)
	case "lifecycle":
		flags := flag.NewFlagSet("lifecycle", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		target := flags.String("target", "", "expected alias and bucket")
		prefix := flags.String("prefix", "", "expected object prefix")
		days := flags.Int("noncurrent-days", 0, "expected noncurrent expiry")
		if flags.Parse(os.Args[2:]) != nil || flags.NArg() != 0 {
			failUsage()
		}
		err = minioinitcheck.Lifecycle(os.Stdin, *target, *prefix, *days)
	default:
		failUsage()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "minio-init-check: verification failed: %v\n", err)
		os.Exit(1)
	}
}

func failUsage() {
	fmt.Fprintln(os.Stderr, "minio-init-check: expected policy, user, identity, anonymous, or lifecycle verification command")
	os.Exit(2)
}
