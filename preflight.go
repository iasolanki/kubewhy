package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	flag "github.com/spf13/pflag"
)

type violation struct {
	kind      string
	name      string
	namespace string
	field     string
	reason    string
}

// matches: error patching: StatefulSet.apps "my-sts" is invalid: spec.volumeClaimTemplates: Forbidden: ...
var rePatching = regexp.MustCompile(`error patching: (\S+) "([^"]+)" is invalid: ([^:\n]+): (Forbidden[^\n]*)`)

// matches: The Deployment "my-deploy" is invalid: spec.selector: Invalid value: ...: field is immutable
var reInvalid = regexp.MustCompile(`The (\w+) "([^"]+)" is invalid: ([^:\n]+):.*field is immutable`)

func parseViolations(output, ns string) []violation {
	if !strings.Contains(output, "immutable") &&
		!strings.Contains(output, "Forbidden: updates to") &&
		!strings.Contains(output, "Forbidden: pod updates") {
		return nil
	}

	seen := map[string]bool{}
	var out []violation

	for _, m := range rePatching.FindAllStringSubmatch(output, -1) {
		kind := strings.Split(m[1], ".")[0]
		name, field, reason := m[2], strings.TrimSpace(m[3]), strings.TrimSpace(m[4])
		if k := kind + "/" + name; !seen[k] {
			seen[k] = true
			out = append(out, violation{kind: kind, name: name, namespace: ns, field: field, reason: reason})
		}
	}

	for _, m := range reInvalid.FindAllStringSubmatch(output, -1) {
		kind, name, field := m[1], m[2], strings.TrimSpace(m[3])
		if k := kind + "/" + name; !seen[k] {
			seen[k] = true
			out = append(out, violation{kind: kind, name: name, namespace: ns, field: field, reason: "field is immutable"})
		}
	}

	return out
}

func runPreflight(args []string) {
	fs := flag.NewFlagSet("preflight", flag.ExitOnError)
	ns := fs.StringP("namespace", "n", "", "Kubernetes namespace")
	file := fs.StringP("file", "f", "", `Manifest file to check (use - for stdin)`)
	fix := fs.Bool("fix", false, "Delete conflicting resources and re-apply after confirmation")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage:
  kubewhy preflight -f <manifest> [-n <namespace>] [--fix]
  helm template my-release ./chart | kubewhy preflight -f -

Flags:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	if *file == "" {
		fmt.Fprintln(os.Stderr, "error: -f <manifest> is required")
		fs.Usage()
		os.Exit(1)
	}

	// If reading from stdin, buffer to a temp file so we can re-apply later
	manifestPath := *file
	if *file == "-" {
		tmp, err := os.CreateTemp("", "kubewhy-preflight-*.yaml")
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		defer os.Remove(tmp.Name())

		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			fmt.Fprintln(tmp, scanner.Text())
		}
		tmp.Close()
		manifestPath = tmp.Name()
	}

	// ── dry-run ───────────────────────────────────────────────────────────────
	dryArgs := []string{"apply", "--dry-run=server", "-f", manifestPath}
	if *ns != "" {
		dryArgs = append(dryArgs, "-n", *ns)
	}

	fmt.Printf("Running: kubectl %s\n\n", strings.Join(dryArgs, " "))

	cmd := exec.Command("kubectl", dryArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	combined := stdout.String() + "\n" + stderr.String()

	if err == nil {
		fmt.Println(stdout.String())
		fmt.Println("No immutable field conflicts detected.")
		return
	}

	// ── parse violations ──────────────────────────────────────────────────────
	violations := parseViolations(combined, *ns)

	if len(violations) == 0 {
		// Some other error — just surface it
		fmt.Fprintln(os.Stderr, strings.TrimSpace(stderr.String()))
		os.Exit(1)
	}

	fmt.Printf("%s\n", strings.Repeat("─", 60))
	fmt.Printf("Found %d immutable field conflict(s):\n\n", len(violations))

	for _, v := range violations {
		nsLabel := v.namespace
		if nsLabel == "" {
			nsLabel = "(current context namespace)"
		}
		fmt.Printf("  %s/%s  [namespace: %s]\n", v.kind, v.name, nsLabel)
		fmt.Printf("  Field:  %s\n", v.field)
		fmt.Printf("  Reason: %s\n\n", v.reason)
	}

	fmt.Println("These resources must be deleted and recreated. Delete commands:")
	fmt.Println()
	for _, v := range violations {
		deleteCmd := fmt.Sprintf("  kubectl delete %s %s", strings.ToLower(v.kind), v.name)
		if v.namespace != "" {
			deleteCmd += " -n " + v.namespace
		}
		fmt.Println(deleteCmd)
	}
	fmt.Println()

	if !*fix {
		fmt.Println("Re-run with --fix to delete conflicting resources and re-apply automatically.")
		os.Exit(1)
	}

	// ── auto-fix ──────────────────────────────────────────────────────────────
	fmt.Print("Proceed with deletion and re-apply? [y/N] ")
	var answer string
	fmt.Scanln(&answer)
	if strings.ToLower(strings.TrimSpace(answer)) != "y" {
		fmt.Println("Aborted.")
		os.Exit(1)
	}

	for _, v := range violations {
		delArgs := []string{"delete", strings.ToLower(v.kind), v.name}
		if v.namespace != "" {
			delArgs = append(delArgs, "-n", v.namespace)
		}
		fmt.Printf("\n  kubectl %s\n", strings.Join(delArgs, " "))
		out, err := exec.Command("kubectl", delArgs...).CombinedOutput()
		fmt.Print(string(out))
		if err != nil {
			fmt.Fprintf(os.Stderr, "  error: %v\n", err)
			os.Exit(1)
		}
	}

	// re-apply
	applyArgs := []string{"apply", "-f", manifestPath}
	if *ns != "" {
		applyArgs = append(applyArgs, "-n", *ns)
	}
	fmt.Printf("\n  kubectl %s\n", strings.Join(applyArgs, " "))
	out, err := exec.Command("kubectl", applyArgs...).CombinedOutput()
	fmt.Print(string(out))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: re-apply failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n%s\n", strings.Repeat("─", 60))
	fmt.Println("Done. Resources recreated successfully.")
}
