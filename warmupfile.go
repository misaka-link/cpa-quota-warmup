package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

// warmupFileName is the default file name generated/maintained alongside
// CPA's own config.yaml (same directory, i.e. the process cwd) when the
// plugin config does not set `config-file:` explicitly.
const warmupFileName = "quota-warmup.yaml"

// defaultWarmupModel is the model value a freshly generated account section
// gets: "auto" (candidate-list auto-selection, same as v0.3's model: auto).
const defaultWarmupModel = "auto"

// warmupFileVanishedMarker is appended to an account's key line-comment
// once host.auth.list stops reporting that auth file, so the operator can
// see it happened without the plugin ever deleting their own content.
const warmupFileVanishedMarker = "# 认证文件不存在"

// warmupFileHeaderComment is attached as the HeadComment of the first key
// ("defaults") in a freshly generated file, so it renders as a comment
// block at the very top of the document.
const warmupFileHeaderComment = "" +
	"# cpa-quota-warmup 预热配置：每个认证文件一段，改 enabled / time / model 即可，保存后自动生效（无需重启）\n" +
	"# time：写 \"05:30\"，多个写 \"05:30, 10:30\"，或 cron \"30 5,10,15,20 * * *\"\n" +
	"# model：auto = 自动选该 provider 最便宜的；也可写具体模型名（见 GET /v1/models）"

// warmupFileDefaults is the `defaults:` block: the schedule/model every
// account section inherits unless it sets its own.
type warmupFileDefaults struct {
	Time  flexStringList `yaml:"time"`
	Model string         `yaml:"model"`
}

// warmupFileAccount is one entry of the `accounts:` map. All fields are
// optional -- an absent Time/Model falls back to warmupFileDefaults, and an
// absent Enabled is false (a freshly generated section is always disabled
// until the operator opts it in).
type warmupFileAccount struct {
	Enabled   *bool          `yaml:"enabled"`
	Time      flexStringList `yaml:"time"`
	Model     string         `yaml:"model"`
	Message   string         `yaml:"message"`
	MaxTokens int            `yaml:"max_tokens"`
}

// warmupFileData is the typed, comment-stripped view of quota-warmup.yaml
// used by the scheduler/status routes. warmupFileManager keeps this in sync
// with the *yaml.Node tree it actually reads/writes (which is what
// preserves the operator's own comments and formatting).
type warmupFileData struct {
	Defaults warmupFileDefaults           `yaml:"defaults"`
	Accounts map[string]warmupFileAccount `yaml:"accounts"`
}

// warmupFieldUpdate is a partial update to one account section: a nil
// pointer/field means "leave this key alone". Used by the /set route.
type warmupFieldUpdate struct {
	Enabled *bool
	Time    *string
	Model   *string
}

// warmupFileManager owns the on-disk quota-warmup.yaml file: it generates
// the file the first time it is needed, re-parses it whenever its mtime
// changes, reconciles its accounts against the host's current auth list
// (appending new ones, annotating vanished ones) on every call, and applies
// the panel's /set edits at the *yaml.Node level so user comments and
// formatting choices survive every rewrite.
//
// A parse failure never discards the last successfully parsed data (see
// snapshot/reparseLocked): the scheduler keeps running against whatever it
// last understood, and the failure is only ever surfaced for logging/status
// -- a typo in this file must never stop the scheduler.
type warmupFileManager struct {
	mu         sync.Mutex
	path       string
	root       *yaml.Node // last successfully parsed document; nil until the first successful parse
	data       warmupFileData
	loadedAt   time.Time
	everLoaded bool
	parseErr   string
}

func newWarmupFileManager(path string) *warmupFileManager {
	return &warmupFileManager{path: path}
}

// ensureFresh makes sure the file exists (generating it from entries if
// not), re-parses it if its mtime advanced since the last read, and
// reconciles its accounts against entries (appending missing ones,
// (un)marking vanished ones), writing back only when something actually
// changed.
func (m *warmupFileManager) ensureFresh(entries []pluginapi.HostAuthFileEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	info, statErr := os.Stat(m.path)
	if statErr != nil {
		if !os.IsNotExist(statErr) {
			return fmt.Errorf("stat %s: %w", m.path, statErr)
		}
		if err := m.writeLocked(buildFreshWarmupDoc(entries)); err != nil {
			return err
		}
		return m.reparseAndReconcileLocked(entries)
	}

	if !m.everLoaded || info.ModTime().After(m.loadedAt) {
		if err := m.reparseLocked(); err != nil {
			// Keep whatever m.data/m.root already held (possibly the zero
			// value, if this is the very first load) -- never let a parse
			// error here propagate into "stop scheduling".
			m.parseErr = err.Error()
			return nil
		}
		m.parseErr = ""
	}
	if m.parseErr != "" {
		// The on-disk file is currently broken; do not attempt to mutate a
		// node tree we no longer trust reflects reality. The operator must
		// fix the syntax error before reconciliation resumes.
		return nil
	}
	changed, err := m.reconcileLocked(entries)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	if err := m.writeLocked(m.root); err != nil {
		return err
	}
	return m.reparseLocked()
}

func (m *warmupFileManager) reparseAndReconcileLocked(entries []pluginapi.HostAuthFileEntry) error {
	if err := m.reparseLocked(); err != nil {
		m.parseErr = err.Error()
		return nil
	}
	m.parseErr = ""
	changed, err := m.reconcileLocked(entries)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	if err := m.writeLocked(m.root); err != nil {
		return err
	}
	return m.reparseLocked()
}

// reparseLocked reads and decodes m.path into m.root/m.data. Callers must
// hold m.mu. On error, m.root/m.data/m.loadedAt are left untouched.
func (m *warmupFileManager) reparseLocked() error {
	raw, err := os.ReadFile(m.path)
	if err != nil {
		return fmt.Errorf("read %s: %w", m.path, err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return fmt.Errorf("parse %s: %w", m.path, err)
	}
	if len(root.Content) == 0 {
		return fmt.Errorf("parse %s: empty document", m.path)
	}
	var data warmupFileData
	if err := root.Content[0].Decode(&data); err != nil {
		return fmt.Errorf("parse %s: %w", m.path, err)
	}
	if data.Accounts == nil {
		data.Accounts = map[string]warmupFileAccount{}
	}
	if info, statErr := os.Stat(m.path); statErr == nil {
		m.loadedAt = info.ModTime()
	}
	m.root = &root
	m.data = data
	m.everLoaded = true
	return nil
}

// reconcileLocked appends a fresh (disabled) section for every non-runtime-
// only host auth missing from m.data.Accounts, and (un)marks the
// warmupFileVanishedMarker line-comment on every account key depending on
// whether entries still reports it. Callers must hold m.mu and must not
// call this while m.parseErr != "".
func (m *warmupFileManager) reconcileLocked(entries []pluginapi.HostAuthFileEntry) (changed bool, err error) {
	if m.root == nil || len(m.root.Content) == 0 {
		return false, fmt.Errorf("quota-warmup.yaml: no content to reconcile")
	}
	top := m.root.Content[0]
	_, accountsVal := findMapEntry(top, "accounts")
	if accountsVal == nil {
		accountsVal = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		top.Content = append(top.Content, plainStringNode("accounts"), accountsVal)
		changed = true
	}
	// An empty mapping ("accounts: {}") round-trips with Style=FlowStyle;
	// appending block-style children (with LineComments) to a flow-style
	// mapping renders corrupted YAML (yaml.v3 has no clean way to place a
	// line comment inside flow style), so always force block style back on
	// before adding anything to it.
	forceBlockStyle(accountsVal)

	defaultsTime := m.data.Defaults.Time
	if len(defaultsTime) == 0 {
		defaultsTime = flexStringList{defaultTime}
	}
	defaultsModel := strings.TrimSpace(m.data.Defaults.Model)
	if defaultsModel == "" {
		defaultsModel = defaultWarmupModel
	}

	known := make(map[string]bool, len(entries))
	for _, e := range entries {
		name := strings.TrimSpace(e.Name)
		if name == "" || e.RuntimeOnly {
			continue
		}
		known[name] = true
		if _, exists := m.data.Accounts[name]; exists {
			continue
		}
		keyNode := plainStringNode(name)
		keyNode.LineComment = providerLineComment(e.Provider)
		valNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		valNode.Content = append(valNode.Content,
			plainStringNode("enabled"), boolNode(false),
			plainStringNode("time"), quotedStringNode(strings.Join(defaultsTime, ", ")),
			plainStringNode("model"), plainStringNode(defaultsModel),
		)
		accountsVal.Content = append(accountsVal.Content, keyNode, valNode)
		if m.data.Accounts == nil {
			m.data.Accounts = map[string]warmupFileAccount{}
		}
		m.data.Accounts[name] = warmupFileAccount{
			Enabled: boolPtr(false),
			Time:    append(flexStringList(nil), defaultsTime...),
			Model:   defaultsModel,
		}
		changed = true
	}

	for i := 0; i+1 < len(accountsVal.Content); i += 2 {
		keyNode := accountsVal.Content[i]
		name := keyNode.Value
		hasMarker := strings.Contains(keyNode.LineComment, warmupFileVanishedMarker)
		switch {
		case known[name] && hasMarker:
			keyNode.LineComment = strings.TrimSpace(strings.Replace(keyNode.LineComment, warmupFileVanishedMarker, "", 1))
			changed = true
		case !known[name] && !hasMarker:
			if keyNode.LineComment == "" {
				keyNode.LineComment = warmupFileVanishedMarker
			} else {
				keyNode.LineComment += "  " + warmupFileVanishedMarker
			}
			changed = true
		}
	}
	return changed, nil
}

// snapshot returns a copy of the last successfully parsed data (safe to use
// even while parseErr is non-empty: it is the last known-good state, never
// the broken one) plus the current parse error string (empty if the file is
// currently valid).
func (m *warmupFileManager) snapshot() (warmupFileData, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := warmupFileData{Defaults: m.data.Defaults, Accounts: make(map[string]warmupFileAccount, len(m.data.Accounts))}
	for k, v := range m.data.Accounts {
		out.Accounts[k] = v
	}
	return out, m.parseErr
}

// setAccount applies update to one account section (creating the section,
// and the `accounts:` map itself, if either is missing), preserving every
// existing comment/field it does not touch, then writes the file back and
// re-parses it. providerHint is only used for the LineComment when a brand
// new section has to be created.
func (m *warmupFileManager) setAccount(name string, update warmupFieldUpdate, providerHint string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.root == nil || len(m.root.Content) == 0 {
		return fmt.Errorf("%s has not been loaded yet", m.path)
	}
	top := m.root.Content[0]
	_, accountsVal := findMapEntry(top, "accounts")
	if accountsVal == nil {
		accountsVal = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		top.Content = append(top.Content, plainStringNode("accounts"), accountsVal)
	}
	forceBlockStyle(accountsVal) // see reconcileLocked's comment on why
	_, acctVal := findMapEntry(accountsVal, name)
	if acctVal == nil {
		keyNode := plainStringNode(name)
		keyNode.LineComment = providerLineComment(providerHint)
		acctVal = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		accountsVal.Content = append(accountsVal.Content, keyNode, acctVal)
	}
	forceBlockStyle(acctVal)
	if update.Enabled != nil {
		v := *update.Enabled
		setOrAddMappingField(acctVal, "enabled", func(n *yaml.Node) { setBoolNodeValue(n, v) })
	}
	if update.Time != nil {
		v := *update.Time
		setOrAddMappingField(acctVal, "time", func(n *yaml.Node) { setQuotedStringNodeValue(n, v) })
	}
	if update.Model != nil {
		v := *update.Model
		setOrAddMappingField(acctVal, "model", func(n *yaml.Node) { setPlainStringNodeValue(n, v) })
	}
	if err := m.writeLocked(m.root); err != nil {
		return err
	}
	return m.reparseLocked()
}

// setDefaultsModel mutates defaults.model (used by the overrides.json
// migration to fold a global override into the new schema, which has no
// per-account-independent "every account" field other than defaults).
func (m *warmupFileManager) setDefaultsModel(model string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.root == nil || len(m.root.Content) == 0 {
		return fmt.Errorf("%s has not been loaded yet", m.path)
	}
	top := m.root.Content[0]
	_, defaultsVal := findMapEntry(top, "defaults")
	if defaultsVal == nil {
		defaultsVal = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		top.Content = append(top.Content, plainStringNode("defaults"), defaultsVal)
	}
	forceBlockStyle(defaultsVal)
	setOrAddMappingField(defaultsVal, "model", func(n *yaml.Node) { setPlainStringNodeValue(n, model) })
	if err := m.writeLocked(m.root); err != nil {
		return err
	}
	return m.reparseLocked()
}

// writeLocked atomically (temp file + rename) writes root to m.path with a
// 2-space indent. This is the plugin's own file, never watched by CPA's own
// config hot-reload inotify watcher, so a rename-based replace is safe.
func (m *warmupFileManager) writeLocked(root *yaml.Node) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		_ = enc.Close()
		return fmt.Errorf("encode %s: %w", m.path, err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("encode %s: %w", m.path, err)
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return fmt.Errorf("create dir for %s: %w", m.path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(m.path), ".quota-warmup-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", m.path, err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file for %s: %w", m.path, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp file for %s: %w", m.path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file for %s: %w", m.path, err)
	}
	if err := os.Rename(tmpPath, m.path); err != nil {
		return fmt.Errorf("rename temp file for %s: %w", m.path, err)
	}
	return nil
}

// buildFreshWarmupDoc builds a brand-new document from scratch: one
// disabled section per non-runtime-only host auth, sorted by name for
// deterministic output.
func buildFreshWarmupDoc(entries []pluginapi.HostAuthFileEntry) *yaml.Node {
	defaultsNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	defaultsNode.Content = append(defaultsNode.Content,
		plainStringNode("time"), quotedStringNode(defaultTime),
		plainStringNode("model"), plainStringNode(defaultWarmupModel),
	)

	accountsNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	byName := make(map[string]pluginapi.HostAuthFileEntry, len(entries))
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		name := strings.TrimSpace(e.Name)
		if name == "" || e.RuntimeOnly {
			continue
		}
		if _, dup := byName[name]; dup {
			continue
		}
		byName[name] = e
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		e := byName[name]
		keyNode := plainStringNode(name)
		keyNode.LineComment = providerLineComment(e.Provider)
		valNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		valNode.Content = append(valNode.Content,
			plainStringNode("enabled"), boolNode(false),
			plainStringNode("time"), quotedStringNode(defaultTime),
			plainStringNode("model"), plainStringNode(defaultWarmupModel),
		)
		accountsNode.Content = append(accountsNode.Content, keyNode, valNode)
	}

	defaultsKey := plainStringNode("defaults")
	defaultsKey.HeadComment = warmupFileHeaderComment

	top := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	top.Content = append(top.Content, defaultsKey, defaultsNode, plainStringNode("accounts"), accountsNode)
	return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{top}}
}

// resolveWarmupFilePath resolves the `config-file:` setting (relative paths
// are resolved against the process cwd, matching state.json/overrides.json)
// or the default <cwd>/quota-warmup.yaml.
func resolveWarmupFilePath(cfg pluginConfig) (string, error) {
	workDir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	p := strings.TrimSpace(cfg.ConfigFilePath)
	if p == "" {
		return filepath.Join(workDir, warmupFileName), nil
	}
	if filepath.IsAbs(p) {
		return p, nil
	}
	return filepath.Join(workDir, p), nil
}

// --- small yaml.Node helpers -------------------------------------------------

// forceBlockStyle clears any flow-style marker a mapping/sequence node
// picked up (most commonly: it round-tripped through disk while empty, e.g.
// "accounts: {}", which yaml.v3 always re-parses with Style=FlowStyle).
// Appending block-style children -- especially ones with a LineComment --
// to a still-flow-styled parent produces corrupted YAML on the next
// encode (yaml.v3 cannot cleanly place a line comment inside flow style),
// so every mutator that might add children to a possibly-empty-and-now-
// flow-styled node calls this first.
func forceBlockStyle(n *yaml.Node) {
	if n != nil {
		n.Style &^= yaml.FlowStyle
	}
}

func findMapEntry(mapNode *yaml.Node, key string) (idx int, val *yaml.Node) {
	if mapNode == nil {
		return -1, nil
	}
	for i := 0; i+1 < len(mapNode.Content); i += 2 {
		if mapNode.Content[i].Value == key {
			return i, mapNode.Content[i+1]
		}
	}
	return -1, nil
}

// setOrAddMappingField mutates the existing value node for key in place
// (preserving whatever comments are attached to it) if key is already
// present, or appends a brand new key/value pair otherwise.
func setOrAddMappingField(mapNode *yaml.Node, key string, apply func(*yaml.Node)) {
	if _, v := findMapEntry(mapNode, key); v != nil {
		apply(v)
		return
	}
	v := &yaml.Node{Kind: yaml.ScalarNode}
	apply(v)
	mapNode.Content = append(mapNode.Content, plainStringNode(key), v)
}

func providerLineComment(provider string) string {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return ""
	}
	return "# provider: " + provider
}

func plainStringNode(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
}

func quotedStringNode(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s, Style: yaml.DoubleQuotedStyle}
}

func boolNode(b bool) *yaml.Node {
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool"}
	setBoolNodeValue(n, b)
	return n
}

func setBoolNodeValue(n *yaml.Node, b bool) {
	n.Kind = yaml.ScalarNode
	n.Tag = "!!bool"
	n.Style = 0
	if b {
		n.Value = "true"
	} else {
		n.Value = "false"
	}
}

func setPlainStringNodeValue(n *yaml.Node, s string) {
	n.Kind = yaml.ScalarNode
	n.Tag = "!!str"
	n.Style = 0
	n.Value = s
}

func setQuotedStringNodeValue(n *yaml.Node, s string) {
	n.Kind = yaml.ScalarNode
	n.Tag = "!!str"
	n.Style = yaml.DoubleQuotedStyle
	n.Value = s
}

func boolPtr(b bool) *bool { return &b }

// resolveFileAuth is the v0.4 file-mode schedule/model resolution for one
// auth entry, sourced from the maintained quota-warmup.yaml. Unlike
// resolveNewAuth's v3InlineMode panel/account/global/provider/auto chain,
// file mode has a single source of truth -- the file itself, which the
// panel's /set route edits directly -- so there is no separate override
// layer to consult.
func resolveFileAuth(data warmupFileData, name, provider string) newAuthResolution {
	acct, ok := data.Accounts[name]
	if !ok || acct.Enabled == nil || !*acct.Enabled {
		return newAuthResolution{Selected: false}
	}

	timeRaw := []string(acct.Time)
	if len(timeRaw) == 0 {
		timeRaw = []string(data.Defaults.Time)
	}
	if len(timeRaw) == 0 {
		timeRaw = []string{defaultTime}
	}

	model := strings.TrimSpace(acct.Model)
	if model == "" {
		model = strings.TrimSpace(data.Defaults.Model)
	}
	modelSpec, modelSource := "", modelSourceAuto
	if model != "" && !strings.EqualFold(model, "auto") {
		modelSpec, modelSource = model, modelSourceAccount
	}

	reasoning := ""
	if strings.EqualFold(strings.TrimSpace(provider), "codex") {
		reasoning = codexReasoningEffort
	}

	return newAuthResolution{
		Selected:        true,
		TimeRaw:         timeRaw,
		ModelSpec:       modelSpec,
		ModelSource:     modelSource,
		ReasoningEffort: reasoning,
	}
}
