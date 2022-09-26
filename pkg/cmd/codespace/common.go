package codespace

// This file defines functions common to the entire codespace command set.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sort"

	"github.com/AlecAivazis/survey/v2"
	"github.com/AlecAivazis/survey/v2/terminal"
	"github.com/cli/cli/v2/internal/browser"
	"github.com/cli/cli/v2/internal/codespaces"
	"github.com/cli/cli/v2/internal/codespaces/api"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type executable interface {
	Executable() string
}

type App struct {
	io         *iostreams.IOStreams
	apiClient  apiClient
	grpcClient grpcClient
	errLogger  *log.Logger
	executable executable
	browser    browser.Browser
}

type apiClient interface {
	codespaces.ApiClient
}

type grpcClient interface {
	codespaces.GrpcClient
}

type liveshareSession interface {
	codespaces.LiveshareSession
}

func NewApp(io *iostreams.IOStreams, exe executable, apiClient apiClient, grpcClient grpcClient, browser browser.Browser) *App {
	errLogger := log.New(io.ErrOut, "", 0)

	return &App{
		io:         io,
		apiClient:  apiClient,
		grpcClient: grpcClient,
		errLogger:  errLogger,
		executable: exe,
		browser:    browser,
	}
}

// StartProgressIndicatorWithLabel starts a progress indicator with a message.
func (a *App) StartProgressIndicatorWithLabel(s string) {
	a.io.StartProgressIndicatorWithLabel(s)
}

// StopProgressIndicator stops the progress indicator.
func (a *App) StopProgressIndicator() {
	a.io.StopProgressIndicator()
}

// Connects to a codespace using Live Share and returns that session
func startLiveShareSession(ctx context.Context, codespace *api.Codespace, a *App, debug bool, debugFile string) (session liveshareSession, err error) {
	liveshareLogger := noopLogger()
	if debug {
		debugLogger, err := newFileLogger(debugFile)
		if err != nil {
			return nil, fmt.Errorf("couldn't create file logger: %w", err)
		}
		defer safeClose(debugLogger, &err)

		liveshareLogger = debugLogger.Logger
		a.errLogger.Printf("Debug file located at: %s", debugLogger.Name())
	}

	session, err = codespaces.ConnectToLiveshare(ctx, a, liveshareLogger, a.apiClient, codespace)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Live Share: %w", err)
	}

	return session, nil
}

var errNoCodespaces = errors.New("you have no codespaces")

func chooseCodespace(ctx context.Context, apiClient apiClient) (*api.Codespace, error) {
	codespaces, err := apiClient.ListCodespaces(ctx, api.ListCodespacesOptions{})
	if err != nil {
		return nil, fmt.Errorf("error getting codespaces: %w", err)
	}
	return chooseCodespaceFromList(ctx, codespaces, false)
}

// chooseCodespaceFromList returns the codespace that the user has interactively selected from the list, or
// an error if there are no codespaces.
func chooseCodespaceFromList(ctx context.Context, codespaces []*api.Codespace, includeOwner bool) (*api.Codespace, error) {
	if len(codespaces) == 0 {
		return nil, errNoCodespaces
	}

	sortedCodespaces := codespaces
	sort.Slice(sortedCodespaces, func(i, j int) bool {
		return sortedCodespaces[i].CreatedAt > sortedCodespaces[j].CreatedAt
	})

	csSurvey := []*survey.Question{
		{
			Name: "codespace",
			Prompt: &survey.Select{
				Message: "Choose codespace:",
				Options: formatCodespacesForSelect(sortedCodespaces, includeOwner),
			},
			Validate: survey.Required,
		},
	}

	var answers struct {
		Codespace int
	}
	if err := ask(csSurvey, &answers); err != nil {
		return nil, fmt.Errorf("error getting answers: %w", err)
	}

	return sortedCodespaces[answers.Codespace], nil
}

func formatCodespacesForSelect(codespaces []*api.Codespace, includeOwner bool) []string {
	names := make([]string, len(codespaces))

	for i, apiCodespace := range codespaces {
		cs := codespace{apiCodespace}
		names[i] = cs.displayName(includeOwner)
	}

	return names
}

// getOrChooseCodespace prompts the user to choose a codespace if the codespaceName is empty.
// It then fetches the codespace record with full connection details.
// TODO(josebalius): accept a progress indicator or *App and show progress when fetching.
func getOrChooseCodespace(ctx context.Context, apiClient apiClient, codespaceName string) (codespace *api.Codespace, err error) {
	if codespaceName == "" {
		codespace, err = chooseCodespace(ctx, apiClient)
		if err != nil {
			if err == errNoCodespaces {
				return nil, err
			}
			return nil, fmt.Errorf("choosing codespace: %w", err)
		}
	} else {
		codespace, err = apiClient.GetCodespace(ctx, codespaceName, true)
		if err != nil {
			return nil, fmt.Errorf("getting full codespace details: %w", err)
		}
	}

	if codespace.PendingOperation {
		return nil, fmt.Errorf(
			"codespace is disabled while it has a pending operation: %s",
			codespace.PendingOperationDisabledReason,
		)
	}

	return codespace, nil
}

func safeClose(closer io.Closer, err *error) {
	if closeErr := closer.Close(); *err == nil {
		*err = closeErr
	}
}

// hasTTY indicates whether the process connected to a terminal.
// It is not portable to assume stdin/stdout are fds 0 and 1.
var hasTTY = term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))

// ask asks survey questions on the terminal, using standard options.
// It fails unless hasTTY, but ideally callers should avoid calling it in that case.
func ask(qs []*survey.Question, response interface{}) error {
	if !hasTTY {
		return fmt.Errorf("no terminal")
	}
	err := survey.Ask(qs, response, survey.WithShowCursor(true))
	// The survey package temporarily clears the terminal's ISIG mode bit
	// (see tcsetattr(3)) so the QUIT button (Ctrl-C) is reported as
	// ASCII \x03 (ETX) instead of delivering SIGINT to the application.
	// So we have to serve ourselves the SIGINT.
	//
	// https://github.com/AlecAivazis/survey/#why-isnt-ctrl-c-working
	if err == terminal.InterruptErr {
		self, _ := os.FindProcess(os.Getpid())
		_ = self.Signal(os.Interrupt) // assumes POSIX

		// Suspend the goroutine, to avoid a race between
		// return from main and async delivery of INT signal.
		select {}
	}
	return err
}

var ErrTooManyArgs = errors.New("the command accepts no arguments")

func noArgsConstraint(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return ErrTooManyArgs
	}
	return nil
}

func noopLogger() *log.Logger {
	return log.New(io.Discard, "", 0)
}

type codespace struct {
	*api.Codespace
}

// displayName formats the codespace name for the interactive selector prompt.
func (c codespace) displayName(includeOwner bool) string {
	branch := c.branchWithGitStatus()
	displayName := c.DisplayName

	if displayName == "" {
		displayName = c.Name
	}

	description := fmt.Sprintf("%s (%s): %s", c.Repository.FullName, branch, displayName)

	if includeOwner {
		description = fmt.Sprintf("%-15s %s", c.Owner.Login, description)
	}

	return description
}

// gitStatusDirty represents an unsaved changes status.
const gitStatusDirty = "*"

// branchWithGitStatus returns the branch with a star
// if the branch is currently being worked on.
func (c codespace) branchWithGitStatus() string {
	if c.hasUnsavedChanges() {
		return c.GitStatus.Ref + gitStatusDirty
	}

	return c.GitStatus.Ref
}

// hasUnsavedChanges returns whether the environment has
// unsaved changes.
func (c codespace) hasUnsavedChanges() bool {
	return c.GitStatus.HasUncommitedChanges || c.GitStatus.HasUnpushedChanges
}

// running returns whether the codespace environment is running.
func (c codespace) running() bool {
	return c.State == api.CodespaceStateAvailable
}
