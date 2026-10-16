package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/cmfrunner"
	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/format"
	"github.com/ollama/ollama/parser"
	"github.com/ollama/ollama/progress"
	"github.com/ollama/ollama/types/model"
	"github.com/ollama/ollama/version"
)

// defaultCMFAuthor is the Hugging Face account that publishes CMF models.
const defaultCMFAuthor = "infosave"

func cmfCommand() *cobra.Command {
	cmfCmd := &cobra.Command{
		Use:   "cmf",
		Short: "Manage CMF models and the Cortiq runtime",
		Long: `Work with CMF models.

  ollamacmf cmf status              show the runtime, the server and installed CMF models
  ollamacmf cmf setup               download the Cortiq runtime now instead of on first use
  ollamacmf cmf import model.cmf    register a local .cmf file as a model
  ollamacmf cmf search [query]      find CMF models on Hugging Face

Models from Hugging Face are pulled like any other model:

  ollamacmf pull hf.co/infosave/LFM2.5-cmf:lfm2.5-230m-q4tp
  ollamacmf run  hf.co/infosave/LFM2.5-cmf:lfm2.5-230m-q4tp`,
	}

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show the Cortiq runtime, server and CMF models",
		Args:  cobra.ExactArgs(0),
		RunE:  cmfStatusHandler,
	}

	setupCmd := &cobra.Command{
		Use:   "setup",
		Short: "Download and install the Cortiq runtime",
		Args:  cobra.ExactArgs(0),
		RunE:  cmfSetupHandler,
	}

	importCmd := &cobra.Command{
		Use:   "import FILE.cmf",
		Short: "Register a local .cmf file as a model",
		Args:  cobra.ExactArgs(1),
		PreRunE: func(cmd *cobra.Command, args []string) error {
			return checkServerHeartbeat(cmd, args)
		},
		RunE: cmfImportHandler,
	}
	importCmd.Flags().String("name", "", "Model name (default: the file name without .cmf)")
	importCmd.Flags().String("system", "", "System prompt to store with the model")

	searchCmd := &cobra.Command{
		Use:   "search [QUERY]",
		Short: "Find CMF models on Hugging Face",
		Args:  cobra.MaximumNArgs(1),
		RunE:  cmfSearchHandler,
	}
	searchCmd.Flags().String("author", defaultCMFAuthor, "Hugging Face account to search")
	searchCmd.Flags().Int("limit", 50, "Maximum number of repositories to list")

	cmfCmd.AddCommand(statusCmd, setupCmd, importCmd, searchCmd)
	return cmfCmd
}

func cmfStatusHandler(cmd *cobra.Command, _ []string) error {
	client, err := api.ClientFromEnvironment()
	if err != nil {
		return err
	}
	host := envconfig.Host()

	serverVersion := ""
	var runtime api.CMFRuntimeStatus
	serverUp := false
	if status, err := client.CMFStatus(cmd.Context()); err == nil {
		serverUp = true
		serverVersion = status.Version
		runtime = status.Runtime
	} else {
		local := cmfrunner.Status(cmd.Context())
		runtime = api.CMFRuntimeStatus{
			Installed:         local.Installed,
			Path:              local.Path,
			Source:            string(local.Source),
			Version:           local.Version,
			PinnedVersion:     local.PinnedVersion,
			Platform:          local.Platform,
			PlatformSupported: local.PlatformSupported,
			InstallDir:        local.InstallDir,
			AutoInstall:       local.AutoInstall,
			Error:             local.Error,
		}
	}

	if serverUp {
		fmt.Printf("Server:    running at %s (ollamacmf %s)\n", host, serverVersion)
	} else {
		fmt.Printf("Server:    not running at %s (start it with `ollamacmf serve`)\n", host)
	}
	fmt.Printf("Platform:  %s\n", runtime.Platform)
	switch {
	case runtime.Installed:
		detail := runtime.Source
		if runtime.Version != "" {
			detail = runtime.Version + ", " + runtime.Source
		}
		fmt.Printf("Runtime:   Cortiq installed (%s)\n           %s\n", detail, runtime.Path)
	case !runtime.PlatformSupported:
		fmt.Printf("Runtime:   not available for %s\n", runtime.Platform)
	case runtime.AutoInstall:
		fmt.Printf("Runtime:   Cortiq %s will be downloaded on first use (or run `ollamacmf cmf setup`)\n", runtime.PinnedVersion)
	default:
		fmt.Printf("Runtime:   not installed; run `ollamacmf cmf setup`\n")
	}
	fmt.Printf("Expected:  Cortiq %s in %s\n", runtime.PinnedVersion, runtime.InstallDir)
	if runtime.Error != "" && (!runtime.Installed || !runtime.PlatformSupported || strings.Contains(runtime.Error, "expects")) {
		fmt.Printf("Note:      %s\n", runtime.Error)
	}

	if !serverUp {
		return nil
	}
	models, err := client.List(cmd.Context())
	if err != nil {
		return err
	}
	var rows [][]string
	for _, m := range models.Models {
		if !strings.EqualFold(m.Details.Format, "cmf") {
			continue
		}
		rows = append(rows, []string{m.Name, m.Details.Family, m.Details.ParameterSize, m.Details.QuantizationLevel, format.HumanBytes(m.Size)})
	}
	fmt.Println()
	if len(rows) == 0 {
		fmt.Println("No CMF models yet. Try: ollamacmf run hf.co/infosave/LFM2.5-cmf:lfm2.5-230m-q4tp")
		return nil
	}
	fmt.Println("CMF models:")
	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"NAME", "ARCH", "PARAMS", "QUANT", "SIZE"})
	table.SetHeaderAlignment(tablewriter.ALIGN_LEFT)
	table.SetAlignment(tablewriter.ALIGN_LEFT)
	table.SetHeaderLine(false)
	table.SetBorder(false)
	table.SetNoWhiteSpace(true)
	table.SetTablePadding("    ")
	table.AppendBulk(rows)
	table.Render()
	return nil
}

func cmfSetupHandler(cmd *cobra.Command, _ []string) error {
	p := progress.NewProgress(os.Stderr)
	defer p.Stop()
	fn := newProgressRenderer(p)

	client, err := api.ClientFromEnvironment()
	if err != nil {
		return err
	}
	// Prefer installing through the server: it is the process that runs
	// Cortiq and may use a different user or models directory.
	if _, err := client.CMFStatus(cmd.Context()); err == nil {
		if err := client.CMFInstallRuntime(cmd.Context(), fn); err != nil {
			return err
		}
	} else if _, err := cmfrunner.Install(cmd.Context(), func(r api.ProgressResponse) { _ = fn(r) }); err != nil {
		return err
	}
	p.StopAndClear()

	status := cmfrunner.Status(cmd.Context())
	if resp, err := client.CMFStatus(cmd.Context()); err == nil {
		fmt.Printf("Cortiq %s is ready: %s\n", resp.Runtime.PinnedVersion, resp.Runtime.Path)
		return nil
	}
	fmt.Printf("Cortiq %s is ready: %s\n", status.PinnedVersion, status.Path)
	return nil
}

func cmfImportHandler(cmd *cobra.Command, args []string) error {
	name, _ := cmd.Flags().GetString("name")
	system, _ := cmd.Flags().GetString("system")
	modelName, err := importCMFFile(cmd, args[0], name, system)
	if err != nil {
		return err
	}
	fmt.Printf("Imported %s. Run it with: ollamacmf run %s\n", modelName, modelName)
	return nil
}

// isCMFFileArgument reports whether a model argument names a .cmf file on
// disk, so `ollamacmf run ./model.cmf` can import it on the fly.
func isCMFFileArgument(arg string) bool {
	if !strings.EqualFold(filepath.Ext(arg), ".cmf") {
		return false
	}
	info, err := os.Stat(arg)
	return err == nil && info.Mode().IsRegular()
}

// importCMFFile registers filePath as modelName (derived from the file name
// when empty) and returns the resulting model name.
func importCMFFile(cmd *cobra.Command, filePath, modelName, system string) (string, error) {
	abs, err := filepath.Abs(filePath)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a file", filePath)
	}
	if _, err := cmfrunner.ReadInfoFile(abs); err != nil {
		return "", err
	}

	if modelName == "" {
		modelName = modelNameFromFile(abs)
	}
	if !model.ParseName(modelName).IsValid() {
		return "", fmt.Errorf("invalid model name: %s", modelName)
	}

	var modelfile strings.Builder
	fmt.Fprintf(&modelfile, "FROM %s\n", abs)
	if system != "" {
		fmt.Fprintf(&modelfile, "SYSTEM %q\n", system)
	}
	parsed, err := parser.ParseFile(strings.NewReader(modelfile.String()))
	if err != nil {
		return "", err
	}

	p := progress.NewProgress(os.Stderr)
	defer p.Stop()
	if err := createFromModelfile(cmd, modelName, parsed, filepath.Dir(abs), "", p); err != nil {
		return "", err
	}
	p.StopAndClear()
	return modelName, nil
}

var modelNameCleaner = regexp.MustCompile(`[^a-z0-9._-]+`)

// modelNameFromFile turns "/models/Granite-4.2-3B_q4tp.cmf" into
// "granite-4.2-3b_q4tp".
func modelNameFromFile(filePath string) string {
	name := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	name = modelNameCleaner.ReplaceAllString(strings.ToLower(name), "-")
	name = strings.Trim(name, "-._")
	if name == "" {
		name = "cmf-model"
	}
	return name
}

// newProgressRenderer draws pull-style progress: a bar per digest and a
// spinner per status message.
func newProgressRenderer(p *progress.Progress) func(api.ProgressResponse) error {
	bars := make(map[string]*progress.Bar)
	var status string
	var spinner *progress.Spinner
	return func(resp api.ProgressResponse) error {
		if resp.Total > 0 {
			key := resp.Digest
			if key == "" {
				key = resp.Status
			}
			bar, ok := bars[key]
			if !ok {
				if spinner != nil {
					spinner.Stop()
				}
				msg := resp.Status
				if msg == "" && len(resp.Digest) >= 19 {
					msg = fmt.Sprintf("pulling %s...", resp.Digest[7:19])
				}
				bar = progress.NewBar(msg, resp.Total, resp.Completed)
				bars[key] = bar
				p.Add(key, bar)
			}
			bar.Set(resp.Completed)
			return nil
		}
		if resp.Status != "" && resp.Status != status {
			if spinner != nil {
				spinner.Stop()
			}
			status = resp.Status
			spinner = progress.NewSpinner(status)
			p.Add(status, spinner)
		}
		return nil
	}
}

func cmfSearchHandler(cmd *cobra.Command, args []string) error {
	author, _ := cmd.Flags().GetString("author")
	limit, _ := cmd.Flags().GetInt("limit")
	query := ""
	if len(args) == 1 {
		query = args[0]
	}
	repos, err := searchHuggingFaceCMF(cmd.Context(), author, query, limit)
	if err != nil {
		return err
	}
	if len(repos) == 0 {
		fmt.Println("No CMF repositories found.")
		return nil
	}

	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"MODEL", "TASK", "PULL"})
	table.SetHeaderAlignment(tablewriter.ALIGN_LEFT)
	table.SetAlignment(tablewriter.ALIGN_LEFT)
	table.SetHeaderLine(false)
	table.SetBorder(false)
	table.SetNoWhiteSpace(true)
	table.SetTablePadding("    ")
	table.SetAutoWrapText(false)
	for _, repo := range repos {
		for i, file := range repo.Files {
			name, task := "", ""
			if i == 0 {
				name, task = repo.ID, repo.Task
			}
			table.Append([]string{name, task, fmt.Sprintf("ollamacmf pull hf.co/%s:%s", repo.ID, file)})
		}
	}
	table.Render()
	return nil
}

type huggingFaceSearchResult struct {
	ID    string
	Task  string
	Files []string
}

// searchHuggingFaceCMF lists repositories of author that contain .cmf files.
func searchHuggingFaceCMF(ctx context.Context, author, query string, limit int) ([]huggingFaceSearchResult, error) {
	values := url.Values{}
	values.Set("author", author)
	values.Set("limit", fmt.Sprint(max(limit, 1)))
	values.Set("sort", "downloads")
	values.Set("direction", "-1")
	values.Add("expand[]", "siblings")
	values.Add("expand[]", "pipeline_tag")
	if query != "" {
		values.Set("search", query)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://huggingface.co/api/models?"+values.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ollamacmf/"+version.Version)
	if token := strings.TrimSpace(os.Getenv("HF_TOKEN")); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query Hugging Face: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("query Hugging Face: %s", resp.Status)
	}
	var payload []struct {
		ID          string `json:"id"`
		PipelineTag string `json:"pipeline_tag"`
		Siblings    []struct {
			Filename string `json:"rfilename"`
		} `json:"siblings"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode Hugging Face response: %w", err)
	}

	var results []huggingFaceSearchResult
	for _, repo := range payload {
		result := huggingFaceSearchResult{ID: repo.ID, Task: repo.PipelineTag}
		for _, sibling := range repo.Siblings {
			name := sibling.Filename
			lower := strings.ToLower(name)
			if !strings.HasSuffix(lower, ".cmf") || strings.HasSuffix(lower, ".skill.cmf") || strings.HasSuffix(lower, ".mtp.cmf") || strings.HasSuffix(lower, ".mm.cmf") {
				continue
			}
			result.Files = append(result.Files, strings.TrimSuffix(path.Base(name), path.Ext(name)))
		}
		if len(result.Files) > 0 {
			results = append(results, result)
		}
	}
	return results, nil
}
