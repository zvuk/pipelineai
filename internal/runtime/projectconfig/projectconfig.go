package projectconfig

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/zvuk/pipelineai/pkg/dsl"
	"go.yaml.in/yaml/v3"
)

var defaultTokenFallbackEnvs = []string{
	"PAI_CONFIG_REPO_TOKEN",
	"PAI_GIT_API_TOKEN",
	"CI_JOB_TOKEN",
	"GITHUB_TOKEN",
}

type overrideFile struct {
	Version           int                    `yaml:"version,omitempty"`
	ProjectConfig     *dsl.ProjectConfig     `yaml:"project_config,omitempty"`
	InstructionBlocks []dsl.InstructionBlock `yaml:"instruction_blocks,omitempty"`
	ResourceCopy      []dsl.ResourceCopy     `yaml:"resource_copy,omitempty"`
	Settings          map[string]string      `yaml:"settings,omitempty"`
}

// Prepare загружает repo-level override config и копирует объявленные ресурсы.
func Prepare(ctx context.Context, cfg *dsl.Config, log *slog.Logger) error {
	if cfg == nil {
		return nil
	}
	pc := cfg.ProjectConfig
	if !shouldPrepare(pc) {
		return nil
	}
	if pc == nil {
		pc = &dsl.ProjectConfig{Enabled: true}
		cfg.ProjectConfig = pc
	}

	if overlay, source, err := loadOverride(ctx, pc, cfg); err != nil {
		return err
	} else if overlay != nil {
		mergeProjectConfig(pc, overlay)
		if log != nil {
			log.Info("project config loaded", slog.String("source", source))
		}
	}

	resources, err := copyResources(ctx, pc, cfg, log)
	if err != nil {
		return err
	}
	if len(resources) > 0 {
		pc.Resources = resources
	}
	return nil
}

func shouldPrepare(pc *dsl.ProjectConfig) bool {
	if pc == nil {
		return strings.TrimSpace(os.Getenv("PAI_CONFIG_PATH")) != "" ||
			strings.TrimSpace(os.Getenv("PAI_CONFIG_REPO_URL")) != "" ||
			fileExists(".pai-config.yaml")
	}
	return pc.Enabled ||
		len(pc.InstructionBlocks) > 0 ||
		len(pc.ResourceCopy) > 0 ||
		len(pc.Settings) > 0 ||
		!pc.LocalPath.IsZero() ||
		pc.Remote != nil
}

func loadOverride(ctx context.Context, pc *dsl.ProjectConfig, cfg *dsl.Config) (*dsl.ProjectConfig, string, error) {
	if remoteURL := renderRemoteURL(pc, cfg); strings.TrimSpace(remoteURL) != "" {
		path, cleanup, err := fetchRemoteConfig(ctx, pc, cfg, remoteURL)
		if err != nil {
			return nil, "", err
		}
		defer cleanup()
		overlay, err := readProjectConfigFile(path)
		if err != nil {
			return nil, "", err
		}
		return overlay, path, nil
	}

	path, explicit, err := renderLocalPath(pc, cfg)
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(path) == "" {
		return nil, "", nil
	}
	if !filepath.IsAbs(path) {
		cwd, _ := os.Getwd()
		path = filepath.Join(cwd, path)
	}
	if !fileExists(path) {
		if explicit {
			return nil, "", fmt.Errorf("project config: local override file not found: %s", path)
		}
		return nil, "", nil
	}
	overlay, err := readProjectConfigFile(path)
	if err != nil {
		return nil, "", err
	}
	return overlay, path, nil
}

func renderRemoteURL(pc *dsl.ProjectConfig, cfg *dsl.Config) string {
	if envURL := strings.TrimSpace(os.Getenv("PAI_CONFIG_REPO_URL")); envURL != "" {
		return envURL
	}
	if pc == nil || pc.Remote == nil || pc.Remote.URL.IsZero() {
		return ""
	}
	value, err := pc.Remote.URL.Execute(baseTemplateContext(cfg))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func renderLocalPath(pc *dsl.ProjectConfig, cfg *dsl.Config) (string, bool, error) {
	if envPath := strings.TrimSpace(os.Getenv("PAI_CONFIG_PATH")); envPath != "" {
		return envPath, true, nil
	}
	if pc != nil && !pc.LocalPath.IsZero() {
		value, err := pc.LocalPath.Execute(baseTemplateContext(cfg))
		if err != nil {
			return "", false, fmt.Errorf("project config: failed to render local_path: %w", err)
		}
		value = strings.TrimSpace(value)
		return value, value != ".pai-config.yaml", nil
	}
	return ".pai-config.yaml", false, nil
}

func readProjectConfigFile(path string) (*dsl.ProjectConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("project config: failed to read %s: %w", path, err)
	}
	var raw overrideFile
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("project config: failed to parse %s: %w", path, err)
	}
	out := &dsl.ProjectConfig{Enabled: true}
	if raw.ProjectConfig != nil {
		out = raw.ProjectConfig
	}
	if len(raw.InstructionBlocks) > 0 {
		out.InstructionBlocks = mergeInstructionBlocks(out.InstructionBlocks, raw.InstructionBlocks)
	}
	if len(raw.ResourceCopy) > 0 {
		out.ResourceCopy = mergeResourceCopy(out.ResourceCopy, raw.ResourceCopy)
	}
	if len(raw.Settings) > 0 {
		out.Settings = mergeSettings(out.Settings, raw.Settings)
	}
	return out, nil
}

func fetchRemoteConfig(ctx context.Context, pc *dsl.ProjectConfig, cfg *dsl.Config, remoteURL string) (string, func(), error) {
	tmp, err := os.MkdirTemp("", "pipelineai-project-config-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("project config: failed to create temp dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }

	ref := firstNonEmpty(os.Getenv("PAI_CONFIG_REPO_REF"), renderRemoteTemplate(pc, cfg, "ref"), "main")
	configPath := firstNonEmpty(os.Getenv("PAI_CONFIG_REPO_FILE"), os.Getenv("PAI_CONFIG_REPO_PATH"), renderRemoteTemplate(pc, cfg, "path"), ".pai-config.yaml")
	tokenEnv := firstNonEmpty(os.Getenv("PAI_CONFIG_REPO_TOKEN_ENV"), renderRemoteTemplate(pc, cfg, "token_env"))
	authMode := firstNonEmpty(os.Getenv("PAI_CONFIG_REPO_AUTH_MODE"), remoteAuthMode(pc), "basic")
	username := firstNonEmpty(os.Getenv("PAI_CONFIG_REPO_USERNAME"), renderRemoteTemplate(pc, cfg, "username"), "oauth2")
	fallbacks := remoteTokenFallbacks(pc)

	token, tokenName := resolveToken(tokenEnv, fallbacks)
	cloneURL, extraEnv, err := prepareGitAuth(remoteURL, token, tokenName, authMode, username)
	if err != nil {
		cleanup()
		return "", cleanup, err
	}

	if err := gitRun(ctx, tmp, extraEnv, "clone", "--filter=blob:none", cloneURL, "repo"); err != nil {
		cleanup()
		return "", cleanup, fmt.Errorf("project config: failed to clone config repo: %w", err)
	}
	repoDir := filepath.Join(tmp, "repo")
	if strings.TrimSpace(ref) != "" {
		if err := gitRun(ctx, repoDir, extraEnv, "checkout", ref); err != nil {
			cleanup()
			return "", cleanup, fmt.Errorf("project config: failed to checkout config repo ref %s: %w", ref, err)
		}
	}
	path := filepath.Join(repoDir, filepath.Clean(configPath))
	if !fileExists(path) {
		cleanup()
		return "", cleanup, fmt.Errorf("project config: remote config file not found: %s", configPath)
	}
	return path, cleanup, nil
}

func renderRemoteTemplate(pc *dsl.ProjectConfig, cfg *dsl.Config, field string) string {
	if pc == nil || pc.Remote == nil {
		return ""
	}
	var tpl dsl.TemplateString
	switch field {
	case "ref":
		tpl = pc.Remote.Ref
	case "path":
		tpl = pc.Remote.Path
	case "token_env":
		tpl = pc.Remote.TokenEnv
	case "username":
		tpl = pc.Remote.Username
	}
	if tpl.IsZero() {
		return ""
	}
	value, err := tpl.Execute(baseTemplateContext(cfg))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func remoteAuthMode(pc *dsl.ProjectConfig) string {
	if pc == nil || pc.Remote == nil {
		return ""
	}
	return strings.TrimSpace(pc.Remote.AuthMode)
}

func remoteTokenFallbacks(pc *dsl.ProjectConfig) []string {
	if pc == nil || pc.Remote == nil || len(pc.Remote.TokenFallbackEnvs) == 0 {
		return defaultTokenFallbackEnvs
	}
	return pc.Remote.TokenFallbackEnvs
}

func mergeProjectConfig(base *dsl.ProjectConfig, overlay *dsl.ProjectConfig) {
	if base == nil || overlay == nil {
		return
	}
	base.InstructionBlocks = mergeInstructionBlocks(base.InstructionBlocks, overlay.InstructionBlocks)
	base.ResourceCopy = mergeResourceCopy(base.ResourceCopy, overlay.ResourceCopy)
	base.Settings = mergeSettings(base.Settings, overlay.Settings)
}

func mergeInstructionBlocks(base []dsl.InstructionBlock, overlay []dsl.InstructionBlock) []dsl.InstructionBlock {
	out := append([]dsl.InstructionBlock{}, base...)
	index := make(map[string]int, len(out))
	for i, item := range out {
		index[strings.TrimSpace(item.ID)] = i
	}
	for _, item := range overlay {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		if pos, ok := index[id]; ok {
			out[pos] = mergeInstructionBlock(out[pos], item)
			continue
		}
		index[id] = len(out)
		out = append(out, item)
	}
	return out
}

func mergeInstructionBlock(base dsl.InstructionBlock, overlay dsl.InstructionBlock) dsl.InstructionBlock {
	mode := strings.ToLower(strings.TrimSpace(overlay.Mode))
	if mode == "" {
		mode = "append"
	}
	switch mode {
	case "replace":
		return overlay
	case "prepend":
		base.Content = mergeTemplateStrings(overlay.Content, base.Content)
		base.Items = append(append([]dsl.InstructionItem{}, overlay.Items...), base.Items...)
	default:
		base.Content = mergeTemplateStrings(base.Content, overlay.Content)
		base.Items = append(base.Items, overlay.Items...)
	}
	if !overlay.Title.IsZero() {
		base.Title = overlay.Title
	}
	return base
}

func mergeResourceCopy(base []dsl.ResourceCopy, overlay []dsl.ResourceCopy) []dsl.ResourceCopy {
	out := append([]dsl.ResourceCopy{}, base...)
	index := make(map[string]int, len(out))
	for i, item := range out {
		index[strings.TrimSpace(item.ID)] = i
	}
	for _, item := range overlay {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		if pos, ok := index[id]; ok {
			out[pos] = item
			continue
		}
		index[id] = len(out)
		out = append(out, item)
	}
	return out
}

func mergeSettings(base map[string]string, overlay map[string]string) map[string]string {
	if len(base) == 0 && len(overlay) == 0 {
		return nil
	}
	out := make(map[string]string, len(base)+len(overlay))
	for k, v := range base {
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		out[key] = strings.TrimSpace(v)
	}
	for k, v := range overlay {
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		out[key] = strings.TrimSpace(v)
	}
	return out
}

func mergeTemplateStrings(left dsl.TemplateString, right dsl.TemplateString) dsl.TemplateString {
	leftRaw := strings.TrimSpace(left.String())
	rightRaw := strings.TrimSpace(right.String())
	switch {
	case leftRaw == "":
		return right
	case rightRaw == "":
		return left
	default:
		merged, err := dsl.NewTemplateString(leftRaw + "\n\n" + rightRaw)
		if err != nil {
			return left
		}
		return merged
	}
}

func copyResources(ctx context.Context, pc *dsl.ProjectConfig, cfg *dsl.Config, log *slog.Logger) (map[string]string, error) {
	if pc == nil || len(pc.ResourceCopy) == 0 {
		return nil, nil
	}
	resources := make(map[string]string, len(pc.ResourceCopy))
	for _, item := range pc.ResourceCopy {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		src, cleanup, err := resolveResourceSource(ctx, item.Source, cfg)
		if err != nil {
			return nil, fmt.Errorf("project config: resource_copy[%s]: %w", id, err)
		}
		defer cleanup()
		dst, err := renderDestination(item.Destination, cfg)
		if err != nil {
			return nil, fmt.Errorf("project config: resource_copy[%s]: %w", id, err)
		}
		if err := copyPath(src, dst); err != nil {
			return nil, fmt.Errorf("project config: resource_copy[%s]: %w", id, err)
		}
		resources[id] = dst
		if log != nil {
			log.Info("resource copied", slog.String("id", id), slog.String("destination", dst))
		}
	}
	return resources, nil
}

func resolveResourceSource(ctx context.Context, source dsl.ResourceSource, cfg *dsl.Config) (string, func(), error) {
	repo := strings.ToLower(strings.TrimSpace(source.Repo))
	if repo == "" {
		repo = "target"
	}
	switch repo {
	case "target":
		path, err := source.Path.Execute(baseTemplateContext(cfg))
		if err != nil {
			return "", func() {}, fmt.Errorf("failed to render source.path: %w", err)
		}
		path = strings.TrimSpace(path)
		if path == "" {
			return "", func() {}, fmt.Errorf("source.path evaluated to empty")
		}
		if !filepath.IsAbs(path) {
			cwd, _ := os.Getwd()
			path = filepath.Join(cwd, path)
		}
		if !pathExists(path) {
			return "", func() {}, fmt.Errorf("source path not found: %s", path)
		}
		return path, func() {}, nil
	case "git":
		return resolveGitResource(ctx, source, cfg)
	default:
		return "", func() {}, fmt.Errorf("unsupported source.repo: %s", source.Repo)
	}
}

func resolveGitResource(ctx context.Context, source dsl.ResourceSource, cfg *dsl.Config) (string, func(), error) {
	baseCtx := baseTemplateContext(cfg)
	rawURL, err := source.URL.Execute(baseCtx)
	if err != nil {
		return "", func() {}, fmt.Errorf("failed to render source.url: %w", err)
	}
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", func() {}, fmt.Errorf("source.url evaluated to empty")
	}
	ref := renderOptionalTemplate(source.Ref, baseCtx, "main")
	srcPath := renderOptionalTemplate(source.Path, baseCtx, "")
	if srcPath == "" {
		return "", func() {}, fmt.Errorf("source.path evaluated to empty")
	}
	tokenEnv := renderOptionalTemplate(source.TokenEnv, baseCtx, "")
	token, tokenName := resolveToken(tokenEnv, source.TokenFallbackEnvs)
	username := renderOptionalTemplate(source.Username, baseCtx, "oauth2")
	cloneURL, extraEnv, err := prepareGitAuth(rawURL, token, tokenName, source.AuthMode, username)
	if err != nil {
		return "", func() {}, err
	}

	tmp, err := os.MkdirTemp("", "pipelineai-resource-copy-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("failed to create temp dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	if err := gitRun(ctx, tmp, extraEnv, "clone", "--filter=blob:none", cloneURL, "repo"); err != nil {
		cleanup()
		return "", cleanup, fmt.Errorf("failed to clone resource repo: %w", err)
	}
	repoDir := filepath.Join(tmp, "repo")
	if strings.TrimSpace(ref) != "" {
		if err := gitRun(ctx, repoDir, extraEnv, "checkout", ref); err != nil {
			cleanup()
			return "", cleanup, fmt.Errorf("failed to checkout resource repo ref %s: %w", ref, err)
		}
	}
	fullPath := filepath.Join(repoDir, filepath.Clean(srcPath))
	if !pathExists(fullPath) {
		cleanup()
		return "", cleanup, fmt.Errorf("source path not found in resource repo: %s", srcPath)
	}
	return fullPath, cleanup, nil
}

func renderDestination(tpl dsl.TemplateString, cfg *dsl.Config) (string, error) {
	value, err := tpl.Execute(baseTemplateContext(cfg))
	if err != nil {
		return "", fmt.Errorf("failed to render destination: %w", err)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("destination evaluated to empty")
	}
	if !filepath.IsAbs(value) {
		cwd, _ := os.Getwd()
		value = filepath.Join(cwd, value)
	}
	return filepath.Clean(value), nil
}

func copyPath(src string, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("failed to stat source path %s: %w", src, err)
	}
	if info.IsDir() {
		if err := os.RemoveAll(dst); err != nil {
			return fmt.Errorf("failed to cleanup destination dir %s: %w", dst, err)
		}
		return copyDir(src, dst)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("failed to create destination dir for %s: %w", dst, err)
	}
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source file %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("failed to open destination file %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("failed to copy file %s to %s: %w", src, dst, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("failed to close destination file %s: %w", dst, err)
	}
	return nil
}

func copyDir(src string, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if _, err := d.Info(); err != nil {
			return err
		}
		return copyPath(path, target)
	})
}

func prepareGitAuth(rawURL, token, tokenName, mode, username string) (string, []string, error) {
	if strings.TrimSpace(token) == "" || strings.EqualFold(strings.TrimSpace(mode), "none") {
		return rawURL, nil, nil
	}
	authMode := strings.ToLower(strings.TrimSpace(mode))
	if authMode == "" {
		authMode = "basic"
	}
	switch authMode {
	case "bearer":
		return rawURL, []string{
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Bearer " + token,
		}, nil
	case "basic":
		u, err := url.Parse(rawURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return "", nil, fmt.Errorf("failed to parse git url for basic auth")
		}
		user := strings.TrimSpace(username)
		if user == "" {
			user = "oauth2"
		}
		if tokenName == "CI_JOB_TOKEN" {
			user = "gitlab-ci-token"
		}
		u.User = url.UserPassword(user, token)
		return u.String(), nil, nil
	default:
		return "", nil, fmt.Errorf("unsupported git auth_mode: %s", mode)
	}
}

func gitRun(ctx context.Context, dir string, extraEnv []string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w, output: %s", err, redactCredentials(strings.TrimSpace(string(out))))
	}
	return nil
}

func redactCredentials(s string) string {
	re := regexp.MustCompile(`https?://[^/\s:@]+:[^@\s/]+@`)
	return re.ReplaceAllStringFunc(s, func(match string) string {
		if strings.HasPrefix(match, "https://") {
			return "https://***:***@"
		}
		return "http://***:***@"
	})
}

func resolveToken(primary string, fallbacks []string) (string, string) {
	candidates := make([]string, 0, 1+len(fallbacks)+len(defaultTokenFallbackEnvs))
	if strings.TrimSpace(primary) != "" {
		candidates = append(candidates, strings.TrimSpace(primary))
	}
	candidates = append(candidates, fallbacks...)
	candidates = append(candidates, defaultTokenFallbackEnvs...)
	seen := map[string]struct{}{}
	for _, name := range candidates {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		if value := os.Getenv(name); strings.TrimSpace(value) != "" {
			return value, name
		}
	}
	return "", ""
}

func renderOptionalTemplate(tpl dsl.TemplateString, ctx map[string]any, fallback string) string {
	if tpl.IsZero() {
		return fallback
	}
	value, err := tpl.Execute(ctx)
	if err != nil {
		return fallback
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func baseTemplateContext(cfg *dsl.Config) map[string]any {
	agent := map[string]any{}
	if cfg != nil {
		agent["name"] = cfg.Agent.Name
		agent["model"] = cfg.Agent.Model
		agent["artifact_dir"] = cfg.Agent.ArtifactDir
	}
	return map[string]any{"agent": agent}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
