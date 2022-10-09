package shared

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/AlecAivazis/survey/v2"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/config"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/cli/cli/v2/pkg/surveyext"
	"github.com/spf13/cobra"
)

type InputType int

const (
	InputTypeEditor InputType = iota
	InputTypeInline
	InputTypeWeb
)

type Commentable interface {
	Link() string
	Identifier() string
	LastComment(username string) (*api.Comment, error)
}

type CommentableOptions struct {
	IO                    *iostreams.IOStreams
	HttpClient            func() (*http.Client, error)
	RetrieveCommentable   func() (Commentable, ghrepo.Interface, error)
	EditSurvey            func(string) (string, error)
	InteractiveEditSurvey func(string) (string, error)
	ConfirmSubmitSurvey   func() (bool, error)
	OpenInBrowser         func(string) error
	Interactive           bool
	InputType             InputType
	Body                  string
	EditLast              bool
	Quiet                 bool
}

func CommentablePreRun(cmd *cobra.Command, opts *CommentableOptions) error {
	inputFlags := 0
	if cmd.Flags().Changed("body") {
		opts.InputType = InputTypeInline
		inputFlags++
	}
	if cmd.Flags().Changed("body-file") {
		opts.InputType = InputTypeInline
		inputFlags++
	}
	if web, _ := cmd.Flags().GetBool("web"); web {
		opts.InputType = InputTypeWeb
		inputFlags++
	}
	if editor, _ := cmd.Flags().GetBool("editor"); editor {
		opts.InputType = InputTypeEditor
		inputFlags++
	}

	if inputFlags == 0 {
		if !opts.IO.CanPrompt() {
			return cmdutil.FlagErrorf("flags required when not running interactively")
		}
		opts.Interactive = true
	} else if inputFlags > 1 {
		return cmdutil.FlagErrorf("specify only one of `--body`, `--body-file`, `--editor`, or `--web`")
	}

	return nil
}

func CommentableRun(opts *CommentableOptions) error {
	commentable, repo, err := opts.RetrieveCommentable()
	if err != nil {
		return err
	}

	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}
	apiClient := api.NewClientFromHTTP(httpClient)

	var lastComment *api.Comment
	if opts.EditLast {
		username, err := api.CurrentLoginName(apiClient, repo.RepoHost())
		if err != nil {
			return err
		}
		lastComment, _ = commentable.LastComment(username)
	}

	switch opts.InputType {
	case InputTypeWeb:
		openURL := ""
		if lastComment == nil {
			openURL = commentable.Link() + "#issuecomment-new"
		} else {
			openURL = lastComment.Link()
		}
		if opts.IO.IsStdoutTTY() && !opts.Quiet {
			fmt.Fprintf(opts.IO.ErrOut, "Opening %s in your browser.\n", text.DisplayURL(openURL))
		}
		return opts.OpenInBrowser(openURL)
	case InputTypeEditor:
		var body string
		initialValue := ""
		if lastComment != nil {
			initialValue = lastComment.Content()
		}
		if opts.Interactive {
			body, err = opts.InteractiveEditSurvey(initialValue)
		} else {
			body, err = opts.EditSurvey(initialValue)
		}
		if err != nil {
			return err
		}
		opts.Body = body
	}

	if opts.Interactive {
		cont, err := opts.ConfirmSubmitSurvey()
		if err != nil {
			return err
		}
		if !cont {
			return errors.New("Discarding...")
		}
	}

	url := ""
	if lastComment == nil {
		params := api.CommentCreateInput{Body: opts.Body, SubjectId: commentable.Identifier()}
		url, err = api.CommentCreate(apiClient, repo.RepoHost(), params)
		if err != nil {
			return err
		}
	} else {
		params := api.CommentUpdateInput{Body: opts.Body, CommentId: lastComment.Identifier()}
		url, err = api.CommentUpdate(apiClient, repo.RepoHost(), params)
		if err != nil {
			return err
		}
	}
	if !opts.Quiet {
		fmt.Fprintln(opts.IO.Out, url)
	}
	return nil
}

func CommentableConfirmSubmitSurvey() (bool, error) {
	var confirm bool
	submit := &survey.Confirm{
		Message: "Submit?",
		Default: true,
	}
	err := survey.AskOne(submit, &confirm)
	return confirm, err
}

func CommentableInteractiveEditSurvey(cf func() (config.Config, error), io *iostreams.IOStreams) func(string) (string, error) {
	return func(initialValue string) (string, error) {
		editorCommand, err := cmdutil.DetermineEditor(cf)
		if err != nil {
			return "", err
		}
		cs := io.ColorScheme()
		fmt.Fprintf(io.Out, "- %s to draft your comment in %s... ", cs.Bold("Press Enter"), cs.Bold(surveyext.EditorName(editorCommand)))
		_ = waitForEnter(io.In)
		return surveyext.Edit(editorCommand, "*.md", initialValue, io.In, io.Out, io.ErrOut)
	}
}

func CommentableEditSurvey(cf func() (config.Config, error), io *iostreams.IOStreams) func(string) (string, error) {
	return func(initialValue string) (string, error) {
		editorCommand, err := cmdutil.DetermineEditor(cf)
		if err != nil {
			return "", err
		}
		return surveyext.Edit(editorCommand, "*.md", initialValue, io.In, io.Out, io.ErrOut)
	}
}

func waitForEnter(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	scanner.Scan()
	return scanner.Err()
}
