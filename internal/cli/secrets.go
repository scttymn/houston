package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

func runSecretsList(file, projectFlag string, stdout, stderr io.Writer) int {
	client, name, code := remote(file, projectFlag, true, stderr)
	if code != 0 {
		return code
	}
	secrets, err := client.Secrets(context.Background(), name)
	if err != nil {
		return remoteFailed(err, stderr)
	}
	for _, s := range secrets {
		state := "not set"
		if s.Set {
			state = "set"
		}
		need := "optional"
		if s.Required {
			need = "required"
		}
		fmt.Fprintf(stdout, "%-30s %-8s %s\n", s.Name, need, state)
	}
	return 0
}

// runSecretsSet reads the value from stdin (a hidden prompt on a terminal,
// all of a pipe), never from arguments, and never prints it.
func runSecretsSet(file, projectFlag, key string, stdin io.Reader, stdout, stderr io.Writer) int {
	client, name, code := remote(file, projectFlag, true, stderr)
	if code != 0 {
		return code
	}
	var value string
	if stdinIsTerminal() {
		fmt.Fprintf(stderr, "Value for %s (hidden): ", key)
		secret, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(stderr)
		if err != nil {
			return remoteFailed(err, stderr)
		}
		value = string(secret)
	} else {
		data, err := io.ReadAll(io.LimitReader(stdin, 64<<10+1))
		if err != nil {
			return remoteFailed(err, stderr)
		}
		value = strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	}
	if err := client.SetSecret(context.Background(), name, key, value); err != nil {
		return remoteFailed(err, stderr)
	}
	fmt.Fprintf(stdout, "%s is set for %s.\n", key, name)
	return 0
}

func runSecretsUnset(file, projectFlag, key string, stdout, stderr io.Writer) int {
	client, name, code := remote(file, projectFlag, true, stderr)
	if code != 0 {
		return code
	}
	if err := client.UnsetSecret(context.Background(), name, key); err != nil {
		return remoteFailed(err, stderr)
	}
	fmt.Fprintf(stdout, "%s is unset for %s.\n", key, name)
	return 0
}

func runSecretsGenerate(file, projectFlag, key string, stdout, stderr io.Writer) int {
	client, name, code := remote(file, projectFlag, true, stderr)
	if code != 0 {
		return code
	}
	if err := client.GenerateSecret(context.Background(), name, key); err != nil {
		return remoteFailed(err, stderr)
	}
	fmt.Fprintf(stdout, "%s is set for %s to a generated value nobody sees.\n", key, name)
	return 0
}
