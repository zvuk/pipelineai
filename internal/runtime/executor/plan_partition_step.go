package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	rtmatrix "github.com/zvuk/pipelineai/internal/runtime/matrix"
	"github.com/zvuk/pipelineai/pkg/dsl"
	"go.yaml.in/yaml/v3"
)

type partitionCandidate struct {
	FilePath string
	ItemHash string
	Weight   int
}

type partitionRuntimeConfig struct {
	SourcePath string
	Select     string

	ManifestJSONPath string
	ManifestYAMLPath string

	SwitchToBucketsAt int
	BucketMaxItems    int
	BucketMaxWeight   int
	PriorityWeight    int

	PriorityAnyGlob    []string
	NonPriorityAnyGlob []string
	PriorityAnyExt     []string
	NonPriorityAnyExt  []string

	LightweightAnyExt   []string
	PriorityPathMarkers []string

	UnitResourcesDir   string
	BasePromptPath     string
	BaseRulesDir       string
	OverrideConfigPath string
	OverrideProfile    string
}

type partitionProfileState struct {
	SelectedProfile string
	Profile         map[string]any
	OverrideBaseDir string
}

func (e *Executor) runPlanPartitionStep(step dsl.Step, stepID string, inputs map[string]ioValue, extra map[string]any, tctx map[string]any) (string, error) {
	cfg, err := renderPartitionRuntimeConfig(step.Plan.Partition, tctx)
	if err != nil {
		return "", fmt.Errorf("executor: failed to render plan.partition for step %s: %w", stepID, err)
	}

	rawSource, err := rtmatrix.ReadFile(cfg.SourcePath)
	if err != nil {
		return "", err
	}
	selected, err := rtmatrix.SelectItems(rawSource, cfg.Select)
	if err != nil {
		return "", err
	}

	candidates := make([]partitionCandidate, 0, len(selected))
	for _, item := range selected {
		filePath := strings.TrimSpace(asString(item["file_path"]))
		if filePath == "" {
			continue
		}
		candidates = append(candidates, partitionCandidate{
			FilePath: filePath,
			ItemHash: strings.TrimSpace(asString(item["item_hash"])),
			Weight:   asInt(item["item_weight"]),
		})
	}

	profileState, err := loadPartitionProfileState(cfg.OverrideConfigPath, cfg.OverrideProfile)
	if err != nil {
		return "", err
	}

	var basePromptText string
	if cfg.UnitResourcesDir != "" {
		if cfg.BasePromptPath == "" {
			return "", fmt.Errorf("executor: plan.partition.base_prompt_path is required when unit_resources_dir is set")
		}
		if cfg.BaseRulesDir == "" {
			return "", fmt.Errorf("executor: plan.partition.base_rules_dir is required when unit_resources_dir is set")
		}
		basePromptRaw, rerr := os.ReadFile(cfg.BasePromptPath)
		if rerr != nil {
			return "", fmt.Errorf("executor: failed to read base prompt file %s: %w", cfg.BasePromptPath, rerr)
		}
		basePromptText = string(basePromptRaw)
		if fi, serr := os.Stat(cfg.BaseRulesDir); serr != nil || !fi.IsDir() {
			return "", fmt.Errorf("executor: base rules dir not found: %s", cfg.BaseRulesDir)
		}
	}

	largeMode := len(candidates) >= cfg.SwitchToBucketsAt

	priorityItems := make([]partitionCandidate, 0, len(candidates))
	nonPriorityItems := make([]partitionCandidate, 0, len(candidates))
	for _, c := range candidates {
		if isPartitionPriority(c, cfg) {
			priorityItems = append(priorityItems, c)
		} else {
			nonPriorityItems = append(nonPriorityItems, c)
		}
	}

	units := make([][]partitionCandidate, 0, len(candidates))
	if !largeMode {
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].FilePath < candidates[j].FilePath })
		for _, c := range candidates {
			units = append(units, []partitionCandidate{c})
		}
	} else {
		sort.Slice(priorityItems, func(i, j int) bool { return priorityItems[i].FilePath < priorityItems[j].FilePath })
		for _, c := range priorityItems {
			units = append(units, []partitionCandidate{c})
		}
		units = append(units, chunkPartitionItems(nonPriorityItems, cfg.BucketMaxItems, cfg.BucketMaxWeight)...)
	}

	manifestItems := make([]map[string]any, 0, len(units))
	for _, unit := range units {
		item, err := buildPartitionManifestItem(unit, cfg, profileState, basePromptText)
		if err != nil {
			return "", err
		}
		manifestItems = append(manifestItems, item)
	}

	manifest := map[string]any{"items": manifestItems}
	if err := writeJSONFile(cfg.ManifestJSONPath, manifest); err != nil {
		return "", err
	}
	if err := writeYAMLFile(cfg.ManifestYAMLPath, manifest); err != nil {
		return "", err
	}

	summary := map[string]any{
		"profile":      profileState.SelectedProfile,
		"large_mode":   largeMode,
		"candidates":   len(candidates),
		"units":        len(manifestItems),
		"single_units": countPartitionUnitsByType(manifestItems, "file"),
		"group_units":  countPartitionUnitsByType(manifestItems, "group"),
	}
	summaryRaw, _ := json.Marshal(summary)
	summaryStr := string(summaryRaw)

	if err := e.processShellOutputs(step, summaryStr, "", inputs, extra); err != nil {
		return "", err
	}
	e.log.Debug("plan partition outputs",
		"step", stepID,
		"summary", crop(summaryStr, 200),
	)
	e.log.Info("plan partition done", "step", stepID)

	return summaryStr, nil
}

func renderPartitionRuntimeConfig(p *dsl.StepPlanPartition, tctx map[string]any) (partitionRuntimeConfig, error) {
	if p == nil {
		return partitionRuntimeConfig{}, fmt.Errorf("plan.partition is required")
	}

	renderRequired := func(name string, tpl dsl.TemplateString) (string, error) {
		raw, err := tpl.Execute(tctx)
		if err != nil {
			return "", fmt.Errorf("failed to render %s: %w", name, err)
		}
		val := strings.TrimSpace(raw)
		if val == "" {
			return "", fmt.Errorf("%s evaluated to empty", name)
		}
		return val, nil
	}
	renderOptional := func(name string, tpl dsl.TemplateString) (string, error) {
		if tpl.IsZero() {
			return "", nil
		}
		raw, err := tpl.Execute(tctx)
		if err != nil {
			return "", fmt.Errorf("failed to render %s: %w", name, err)
		}
		return strings.TrimSpace(raw), nil
	}
	renderInt := func(name string, tpl dsl.TemplateString, def int) (int, error) {
		if tpl.IsZero() {
			return def, nil
		}
		raw, err := tpl.Execute(tctx)
		if err != nil {
			return 0, fmt.Errorf("failed to render %s: %w", name, err)
		}
		val := strings.TrimSpace(raw)
		if val == "" {
			return def, nil
		}
		n, err := strconv.Atoi(val)
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer, got %q", name, val)
		}
		if n < 0 {
			return 0, fmt.Errorf("%s must be >= 0, got %d", name, n)
		}
		return n, nil
	}

	sourcePath, err := renderRequired("plan.partition.source_path", p.SourcePath)
	if err != nil {
		return partitionRuntimeConfig{}, err
	}
	manifestJSONPath, err := renderRequired("plan.partition.manifest_json_path", p.ManifestJSONPath)
	if err != nil {
		return partitionRuntimeConfig{}, err
	}
	manifestYAMLPath, err := renderRequired("plan.partition.manifest_yaml_path", p.ManifestYAMLPath)
	if err != nil {
		return partitionRuntimeConfig{}, err
	}

	unitResourcesDir, err := renderOptional(
		"plan.partition.unit_resources_dir",
		p.UnitResourcesDir,
	)
	if err != nil {
		return partitionRuntimeConfig{}, err
	}
	basePromptPath, err := renderOptional("plan.partition.base_prompt_path", p.BasePromptPath)
	if err != nil {
		return partitionRuntimeConfig{}, err
	}
	baseRulesDir, err := renderOptional("plan.partition.base_rules_dir", p.BaseRulesDir)
	if err != nil {
		return partitionRuntimeConfig{}, err
	}
	overrideConfigPath, err := renderOptional("plan.partition.override_config_path", p.OverrideConfigPath)
	if err != nil {
		return partitionRuntimeConfig{}, err
	}
	overrideProfile, err := renderOptional("plan.partition.override_profile", p.OverrideProfile)
	if err != nil {
		return partitionRuntimeConfig{}, err
	}
	switchToBucketsAt, err := renderInt(
		"plan.partition.switch_to_buckets_at",
		p.SwitchToBucketsAt,
		40,
	)
	if err != nil {
		return partitionRuntimeConfig{}, err
	}
	bucketMaxItems, err := renderInt(
		"plan.partition.bucket_max_items",
		p.BucketMaxItems,
		4,
	)
	if err != nil {
		return partitionRuntimeConfig{}, err
	}
	bucketMaxWeight, err := renderInt(
		"plan.partition.bucket_max_weight",
		p.BucketMaxWeight,
		700,
	)
	if err != nil {
		return partitionRuntimeConfig{}, err
	}
	priorityWeight, err := renderInt(
		"plan.partition.priority_weight",
		p.PriorityWeight,
		220,
	)
	if err != nil {
		return partitionRuntimeConfig{}, err
	}

	cfg := partitionRuntimeConfig{
		SourcePath: sourcePath,
		Select:     strings.TrimSpace(p.Select),

		ManifestJSONPath: manifestJSONPath,
		ManifestYAMLPath: manifestYAMLPath,

		SwitchToBucketsAt: switchToBucketsAt,
		BucketMaxItems:    bucketMaxItems,
		BucketMaxWeight:   bucketMaxWeight,
		PriorityWeight:    priorityWeight,

		PriorityAnyGlob:    normalizeStringSlice(p.PriorityAnyGlob),
		NonPriorityAnyGlob: normalizeStringSlice(p.NonPriorityAnyGlob),
		PriorityAnyExt:     normalizeExtList(p.PriorityAnyExt),
		NonPriorityAnyExt:  normalizeExtList(p.NonPriorityAnyExt),

		LightweightAnyExt: normalizeExtList(append(defaultLightweightExt(), p.LightweightAnyExt...)),
		PriorityPathMarkers: normalizeLowerSlice(
			append(defaultPriorityPathMarkers(), p.PriorityPathMarkers...),
		),

		UnitResourcesDir:   unitResourcesDir,
		BasePromptPath:     basePromptPath,
		BaseRulesDir:       baseRulesDir,
		OverrideConfigPath: overrideConfigPath,
		OverrideProfile:    defaultString(overrideProfile, "default"),
	}
	if cfg.Select == "" {
		cfg.Select = "items"
	}
	if cfg.SwitchToBucketsAt <= 0 {
		cfg.SwitchToBucketsAt = 1
	}
	if cfg.BucketMaxItems <= 0 {
		cfg.BucketMaxItems = 1
	}
	if cfg.BucketMaxWeight <= 0 {
		cfg.BucketMaxWeight = 1
	}
	if cfg.PriorityWeight <= 0 {
		cfg.PriorityWeight = 1
	}

	return cfg, nil
}

func loadPartitionProfileState(overrideConfigPath string, requestedProfile string) (partitionProfileState, error) {
	state := partitionProfileState{
		SelectedProfile: "default",
		Profile:         map[string]any{},
		OverrideBaseDir: "",
	}
	if strings.TrimSpace(overrideConfigPath) == "" {
		return state, nil
	}
	if _, err := os.Stat(overrideConfigPath); err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return state, fmt.Errorf("executor: failed to stat override config %s: %w", overrideConfigPath, err)
	}

	raw, err := rtmatrix.ReadFile(overrideConfigPath)
	if err != nil {
		return state, err
	}
	root, ok := raw.(map[string]any)
	if !ok {
		return state, fmt.Errorf("executor: override config root must be an object")
	}

	selected, profile := selectPartitionProfile(root, requestedProfile)
	state.SelectedProfile = selected
	state.Profile = profile
	state.OverrideBaseDir = filepath.Dir(overrideConfigPath)
	return state, nil
}

func selectPartitionProfile(root map[string]any, requested string) (string, map[string]any) {
	profiles, ok := root["profiles"].(map[string]any)
	if !ok || len(profiles) == 0 {
		return "default", map[string]any{}
	}

	want := strings.TrimSpace(requested)
	if want == "" {
		want = strings.TrimSpace(asString(root["default_profile"]))
	}
	if want == "" {
		want = "default"
	}
	if v, ok := profiles[want].(map[string]any); ok {
		return want, v
	}
	if v, ok := profiles["default"].(map[string]any); ok {
		return "default", v
	}
	keys := make([]string, 0, len(profiles))
	for k := range profiles {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if v, ok := profiles[k].(map[string]any); ok {
			return k, v
		}
	}
	return want, map[string]any{}
}

func isPartitionPriority(c partitionCandidate, cfg partitionRuntimeConfig) bool {
	if partitionAnyGlobMatch(c.FilePath, cfg.NonPriorityAnyGlob) {
		return false
	}
	if partitionAnyExtMatch(c.FilePath, cfg.NonPriorityAnyExt) {
		return false
	}
	if partitionAnyGlobMatch(c.FilePath, cfg.PriorityAnyGlob) {
		return true
	}
	if partitionAnyExtMatch(c.FilePath, cfg.PriorityAnyExt) {
		return true
	}

	ext := strings.ToLower(filepath.Ext(c.FilePath))
	pathLower := strings.ToLower(c.FilePath)
	if isInSlice(ext, defaultPriorityExt()) && c.Weight >= maxInt(80, cfg.PriorityWeight/2) {
		return true
	}
	for _, marker := range cfg.PriorityPathMarkers {
		if strings.Contains(pathLower, marker) {
			return true
		}
	}
	if c.Weight >= cfg.PriorityWeight {
		return true
	}
	if !isInSlice(ext, cfg.LightweightAnyExt) && c.Weight >= maxInt(120, cfg.PriorityWeight/2) {
		return true
	}
	return false
}

func chunkPartitionItems(items []partitionCandidate, maxItems int, maxWeight int) [][]partitionCandidate {
	grouped := map[string][]partitionCandidate{}
	for _, it := range items {
		key := partitionTopBucket(it.FilePath) + "|" + strings.ToLower(filepath.Ext(it.FilePath))
		grouped[key] = append(grouped[key], it)
	}

	keys := make([]string, 0, len(grouped))
	for k := range grouped {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	units := make([][]partitionCandidate, 0, len(items))
	for _, key := range keys {
		bucket := grouped[key]
		sort.Slice(bucket, func(i, j int) bool { return bucket[i].FilePath < bucket[j].FilePath })
		cur := make([]partitionCandidate, 0, len(bucket))
		curWeight := 0
		for _, it := range bucket {
			if len(cur) > 0 && (len(cur) >= maxItems || curWeight+it.Weight > maxWeight) {
				units = append(units, cur)
				cur = make([]partitionCandidate, 0, len(bucket))
				curWeight = 0
			}
			cur = append(cur, it)
			curWeight += it.Weight
		}
		if len(cur) > 0 {
			units = append(units, cur)
		}
	}
	return units
}

func buildPartitionManifestItem(unit []partitionCandidate, cfg partitionRuntimeConfig, profileState partitionProfileState, basePromptText string) (map[string]any, error) {
	if len(unit) == 0 {
		return nil, fmt.Errorf("executor: empty partition unit")
	}
	sort.Slice(unit, func(i, j int) bool { return unit[i].FilePath < unit[j].FilePath })

	unitID := partitionUnitID(unit)
	filePaths := make([]string, 0, len(unit))
	filesPayload := make([]map[string]any, 0, len(unit))
	for _, f := range unit {
		filePaths = append(filePaths, f.FilePath)
		filesPayload = append(filesPayload, map[string]any{
			"file_path":   f.FilePath,
			"item_hash":   f.ItemHash,
			"item_weight": f.Weight,
		})
	}

	item := map[string]any{
		"id":                unitID,
		"unit_type":         "group",
		"primary_file_path": filePaths[0],
		"file_paths_csv":    strings.Join(filePaths, ","),
		"file_count":        len(filePaths),
		"files":             filesPayload,
		"strategy_profile":  profileState.SelectedProfile,
	}
	if len(filePaths) == 1 {
		item["unit_type"] = "file"
	}

	if cfg.UnitResourcesDir == "" {
		if cfg.BasePromptPath != "" {
			item["prompt_file_path"] = cfg.BasePromptPath
		}
		if cfg.BaseRulesDir != "" {
			item["rules_dir"] = cfg.BaseRulesDir
		}
		return item, nil
	}

	unitDir := filepath.Join(cfg.UnitResourcesDir, unitID)
	unitRulesDir := filepath.Join(unitDir, "rules")
	unitPromptPath := filepath.Join(unitDir, "file-review.md")

	if err := os.RemoveAll(unitDir); err != nil {
		return nil, fmt.Errorf("executor: failed to cleanup unit dir %s: %w", unitDir, err)
	}
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return nil, fmt.Errorf("executor: failed to create unit dir %s: %w", unitDir, err)
	}
	if err := copyDir(cfg.BaseRulesDir, unitRulesDir); err != nil {
		return nil, err
	}

	promptText, err := partitionApplyPromptOverlays(basePromptText, filePaths, profileState.Profile, profileState.OverrideBaseDir)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(unitPromptPath, []byte(promptText), 0o644); err != nil {
		return nil, fmt.Errorf("executor: failed to write unit prompt file %s: %w", unitPromptPath, err)
	}
	if err := partitionApplyRuleOverlays(unitRulesDir, filePaths, profileState.Profile, profileState.OverrideBaseDir); err != nil {
		return nil, err
	}

	item["prompt_file_path"] = unitPromptPath
	item["rules_dir"] = unitRulesDir
	return item, nil
}

func partitionApplyPromptOverlays(basePrompt string, filePaths []string, profile map[string]any, overrideBaseDir string) (string, error) {
	promptOverlays, ok := profile["prompt_overlays"].(map[string]any)
	if !ok {
		return basePrompt, nil
	}
	rawEntries, ok := promptOverlays["file_review"].([]any)
	if !ok {
		return basePrompt, nil
	}

	out := basePrompt
	for _, rawEntry := range rawEntries {
		entry, ok := rawEntry.(map[string]any)
		if !ok {
			continue
		}
		relPath := strings.TrimSpace(asString(entry["path"]))
		if relPath == "" {
			continue
		}
		globs := partitionExtractWhenAnyGlob(entry["when"])
		if !partitionAnyFileMatchesGlobs(filePaths, globs) {
			continue
		}
		fullPath := partitionResolveRelPath(overrideBaseDir, relPath)
		if fullPath == "" {
			continue
		}
		data, err := os.ReadFile(fullPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("executor: failed to read prompt overlay file %s: %w", fullPath, err)
		}
		out = partitionMergeText(out, string(data), strings.TrimSpace(asString(entry["mode"])))
	}
	return out, nil
}

func partitionApplyRuleOverlays(unitRulesDir string, filePaths []string, profile map[string]any, overrideBaseDir string) error {
	rawEntries, ok := profile["rule_overlays"].([]any)
	if !ok {
		return nil
	}
	rootRules := filepath.Clean(unitRulesDir)
	for _, rawEntry := range rawEntries {
		entry, ok := rawEntry.(map[string]any)
		if !ok {
			continue
		}
		target := strings.TrimSpace(asString(entry["target"]))
		relPath := strings.TrimSpace(asString(entry["path"]))
		if target == "" || relPath == "" {
			continue
		}
		globs := partitionExtractWhenAnyGlob(entry["when"])
		if !partitionAnyFileMatchesGlobs(filePaths, globs) {
			continue
		}
		fullOverlayPath := partitionResolveRelPath(overrideBaseDir, relPath)
		if fullOverlayPath == "" {
			continue
		}
		extraRaw, err := os.ReadFile(fullOverlayPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("executor: failed to read rule overlay file %s: %w", fullOverlayPath, err)
		}

		targetPath := filepath.Clean(filepath.Join(rootRules, target))
		if !strings.HasPrefix(targetPath, rootRules+string(os.PathSeparator)) && targetPath != rootRules {
			return fmt.Errorf("executor: invalid rule overlay target path outside rules dir: %s", target)
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return fmt.Errorf("executor: failed to create dir for rule overlay target %s: %w", targetPath, err)
		}
		baseRaw, err := os.ReadFile(targetPath)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("executor: failed to read rule target file %s: %w", targetPath, err)
		}
		merged := partitionMergeText(string(baseRaw), string(extraRaw), strings.TrimSpace(asString(entry["mode"])))
		if err := os.WriteFile(targetPath, []byte(merged), 0o644); err != nil {
			return fmt.Errorf("executor: failed to write rule target file %s: %w", targetPath, err)
		}
	}
	return nil
}

func partitionMergeText(base string, extra string, mode string) string {
	m := strings.ToLower(strings.TrimSpace(mode))
	switch m {
	case "replace":
		return extra
	case "prepend":
		left := strings.TrimRight(extra, " \t\r\n")
		right := strings.TrimLeft(base, " \t\r\n")
		return strings.TrimSpace(left+"\n\n"+right) + "\n"
	default:
		left := strings.TrimRight(base, " \t\r\n")
		right := strings.TrimLeft(extra, " \t\r\n")
		return strings.TrimSpace(left+"\n\n"+right) + "\n"
	}
}

func partitionExtractWhenAnyGlob(raw any) []string {
	when, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	v, ok := when["any_glob"]
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return nil
		}
		return []string{s}
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			s := strings.TrimSpace(asString(item))
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func partitionAnyFileMatchesGlobs(filePaths []string, globs []string) bool {
	if len(globs) == 0 {
		return true
	}
	for _, fp := range filePaths {
		if partitionAnyGlobMatch(fp, globs) {
			return true
		}
	}
	return false
}

func partitionAnyGlobMatch(path string, globs []string) bool {
	if len(globs) == 0 {
		return false
	}
	for _, g := range globs {
		if partitionGlobMatch(g, path) {
			return true
		}
	}
	return false
}

func partitionGlobMatch(pattern string, value string) bool {
	p := strings.TrimSpace(pattern)
	if p == "" {
		return false
	}

	var b strings.Builder
	b.WriteString("^")
	for _, r := range p {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			if strings.ContainsRune(`.+()|[]{}^$\`, r) {
				b.WriteRune('\\')
			}
			b.WriteRune(r)
		}
	}
	b.WriteString("$")

	re, err := regexp.Compile(b.String())
	if err != nil {
		return false
	}
	return re.MatchString(value)
}

func partitionAnyExtMatch(path string, extList []string) bool {
	if len(extList) == 0 {
		return false
	}
	ext := strings.ToLower(filepath.Ext(path))
	return isInSlice(ext, extList)
}

func partitionResolveRelPath(baseDir string, maybeRel string) string {
	p := strings.TrimSpace(maybeRel)
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return p
	}
	if strings.TrimSpace(baseDir) == "" {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(baseDir, p))
}

func partitionUnitID(unit []partitionCandidate) string {
	parts := make([]string, 0, len(unit))
	for _, it := range unit {
		parts = append(parts, it.FilePath+"::"+it.ItemHash)
	}
	sort.Strings(parts)
	digest := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return "unit-" + hex.EncodeToString(digest[:8])
}

func partitionTopBucket(path string) string {
	p := strings.Trim(path, "/")
	parts := strings.Split(p, "/")
	if len(parts) >= 2 {
		return parts[0] + "/" + parts[1]
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return ""
}

func writeJSONFile(path string, payload any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("executor: failed to create output dir for %s: %w", path, err)
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("executor: failed to marshal json for %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("executor: failed to write output file %s: %w", path, err)
	}
	return nil
}

func writeYAMLFile(path string, payload any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("executor: failed to create output dir for %s: %w", path, err)
	}
	data, err := yaml.Marshal(payload)
	if err != nil {
		return fmt.Errorf("executor: failed to marshal yaml for %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("executor: failed to write output file %s: %w", path, err)
	}
	return nil
}

func countPartitionUnitsByType(items []map[string]any, t string) int {
	n := 0
	for _, it := range items {
		if strings.TrimSpace(asString(it["unit_type"])) == t {
			n++
		}
	}
	return n
}

func copyDir(src string, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("executor: failed to stat source dir %s: %w", src, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("executor: source path is not a directory: %s", src)
	}
	if err := os.MkdirAll(dst, info.Mode()); err != nil {
		return fmt.Errorf("executor: failed to create dir %s: %w", dst, err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("executor: failed to read source dir %s: %w", src, err)
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return fmt.Errorf("executor: failed to read source file %s: %w", srcPath, err)
		}
		mode := os.FileMode(0o644)
		if st, err := os.Stat(srcPath); err == nil {
			mode = st.Mode()
		}
		if err := os.WriteFile(dstPath, data, mode); err != nil {
			return fmt.Errorf("executor: failed to write target file %s: %w", dstPath, err)
		}
	}
	return nil
}

func normalizeStringSlice(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		s := strings.TrimSpace(v)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func normalizeLowerSlice(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, v := range in {
		s := strings.ToLower(strings.TrimSpace(v))
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func normalizeExtList(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, v := range in {
		s := strings.ToLower(strings.TrimSpace(v))
		if s == "" {
			continue
		}
		if !strings.HasPrefix(s, ".") {
			s = "." + s
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func defaultPriorityExt() []string {
	return []string{".go", ".py", ".js", ".jsx", ".ts", ".tsx", ".kt", ".kts", ".swift", ".java", ".sql", ".proto", ".graphql"}
}

func defaultLightweightExt() []string {
	return []string{".md", ".txt", ".rst", ".adoc", ".json", ".yaml", ".yml"}
}

func defaultPriorityPathMarkers() []string {
	return []string{"auth", "security", "permission", "acl", "migration", "db", "billing", "payment"}
}

func defaultString(v string, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func asInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int8:
		return int(t)
	case int16:
		return int(t)
	case int32:
		return int(t)
	case int64:
		return int(t)
	case uint:
		return int(t)
	case uint8:
		return int(t)
	case uint16:
		return int(t)
	case uint32:
		return int(t)
	case uint64:
		return int(t)
	case float32:
		return int(t)
	case float64:
		return int(t)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(t))
		return n
	default:
		return 0
	}
}

func isInSlice(v string, list []string) bool {
	for _, x := range list {
		if v == x {
			return true
		}
	}
	return false
}

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}
