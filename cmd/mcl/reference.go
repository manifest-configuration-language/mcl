package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/manifest-configuration-language/mcl/internal/engine"
	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
)

func newReferenceCommand() *cobra.Command {
	var outputDirectory string
	var version string
	command := &cobra.Command{
		Use:    "reference",
		Short:  "Generate versioned reference documentation",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if outputDirectory == "" || version == "" {
				return errors.New("reference requires --output and --version")
			}
			if strings.ContainsAny(version, "/\\\n\r") {
				return fmt.Errorf("invalid reference version %q", version)
			}

			referenceDirectory := filepath.Join(outputDirectory, "reference")
			if err := os.RemoveAll(referenceDirectory); err != nil {
				return fmt.Errorf("clear reference directory: %w", err)
			}
			cliDirectory := filepath.Join(referenceDirectory, "cli")
			if err := os.MkdirAll(cliDirectory, 0o755); err != nil {
				return fmt.Errorf("create CLI reference directory: %w", err)
			}
			root := newRootCommand(nil)
			root.DisableAutoGenTag = true
			if err := doc.GenMarkdownTree(root, cliDirectory); err != nil {
				return fmt.Errorf("generate CLI reference: %w", err)
			}

			stdlibPath := filepath.Join(outputDirectory, "reference", "stdlib-functions.md")
			if err := os.MkdirAll(filepath.Dir(stdlibPath), 0o755); err != nil {
				return fmt.Errorf("create standard-library reference directory: %w", err)
			}
			if err := os.WriteFile(stdlibPath, []byte(standardLibraryReference(version)), 0o600); err != nil {
				return fmt.Errorf("write standard-library reference: %w", err)
			}

			return nil
		},
	}
	command.Flags().StringVar(&outputDirectory, "output", "", "Output directory")
	command.Flags().StringVar(&version, "version", "", "MCL package version")

	return command
}

func standardLibraryReference(version string) string {
	var output strings.Builder
	fmt.Fprintf(&output, "# Standard library function reference\n\nMCL version: `%s`\n\n", version)
	output.WriteString("Function names, modules, and parameter names are generated from the standard-library registry. Function behavior and type semantics are documented in the hand-written language reference.\n\n")
	output.WriteString("| Module | Function | Parameters |\n| --- | --- | --- |\n")
	for _, function := range engine.StandardLibraryFunctions() {
		parameters := make([]string, len(function.Parameters))
		for index, parameter := range function.Parameters {
			parameters[index] = "`" + parameter + "`"
		}
		fmt.Fprintf(&output, "| `%s` | `%s` | %s |\n", function.Module, function.Name, strings.Join(parameters, ", "))
	}

	return output.String()
}
