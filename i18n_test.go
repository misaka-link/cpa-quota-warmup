package main

import "testing"

func TestMessageCatalogsHaveTheSameKeys(t *testing.T) {
	for l, table := range messagesByLang {
		if l == langEN {
			continue
		}
		for key := range messagesEN {
			if _, ok := table[key]; !ok {
				t.Errorf("language %s is missing key %q (present in en)", l, key)
			}
		}
		for key := range table {
			if _, ok := messagesEN[key]; !ok {
				t.Errorf("language %s has key %q that en does not have", l, key)
			}
		}
	}
}

func TestTrFallsBackToEnglishForMissingLanguage(t *testing.T) {
	got := tr(lang("xx-unknown"), msgEngineNotRunning)
	want := messagesEN[msgEngineNotRunning]
	if got != want {
		t.Fatalf("tr(unknown language) = %q, want the English fallback %q", got, want)
	}
}

func TestTrFallsBackToBareKeyForUnknownKey(t *testing.T) {
	got := tr(langEN, msgKey("no_such_key"))
	if got != "no_such_key" {
		t.Fatalf("tr(unknown key) = %q, want the bare key string", got)
	}
}

func TestTrFormatsArguments(t *testing.T) {
	got := tr(langEN, msgNotCoveredAfterRounds, 3)
	want := "not covered by any usage record after 3 round(s)"
	if got != want {
		t.Fatalf("tr with args = %q, want %q", got, want)
	}
}

func TestNormalizeLangTag(t *testing.T) {
	cases := []struct {
		in   string
		want lang
		ok   bool
	}{
		{"zh-CN", langZhCN, true},
		{"zh-cn", langZhCN, true}, // case-insensitive
		{"zh-TW", langZhTW, true},
		{"en", langEN, true},
		{"ru", langRU, true},
		{"auto", "", false},
		{"", "", false},
		{"fr", "", false},
	}
	for _, c := range cases {
		got, ok := normalizeLangTag(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("normalizeLangTag(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestClassifyLanguageTag(t *testing.T) {
	cases := []struct {
		in   string
		want lang
	}{
		{"zh-CN", langZhCN},
		{"zh", langZhCN},
		{"zh-Hans", langZhCN},
		{"zh-TW", langZhTW},
		{"zh-HK", langZhTW},
		{"zh-MO", langZhTW},
		{"zh-Hant", langZhTW},
		{"ru-RU", langRU},
		{"en-US", langEN},
		{"fr-FR", langEN},
		{"*", langEN},
	}
	for _, c := range cases {
		if got := classifyLanguageTag(c.in); got != c.want {
			t.Errorf("classifyLanguageTag(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLanguageFromAcceptHeaderPicksHighestWeight(t *testing.T) {
	l, ok := languageFromAcceptHeader("fr;q=0.5, zh-CN;q=0.9, en;q=0.8")
	if !ok || l != langZhCN {
		t.Fatalf("languageFromAcceptHeader = (%q, %v), want (zh-CN, true)", l, ok)
	}
}

func TestLanguageFromAcceptHeaderDefaultQIsOne(t *testing.T) {
	// ru has no explicit q (defaults to 1.0), which must beat en;q=0.9.
	l, ok := languageFromAcceptHeader("en;q=0.9, ru")
	if !ok || l != langRU {
		t.Fatalf("languageFromAcceptHeader = (%q, %v), want (ru, true)", l, ok)
	}
}

func TestLanguageFromAcceptHeaderEmpty(t *testing.T) {
	if _, ok := languageFromAcceptHeader(""); ok {
		t.Fatalf("expected an empty Accept-Language to be unclassifiable")
	}
	if _, ok := languageFromAcceptHeader("   "); ok {
		t.Fatalf("expected a blank Accept-Language to be unclassifiable")
	}
}

func TestLanguageFromEnv(t *testing.T) {
	cases := []struct {
		in   string
		want lang
		ok   bool
	}{
		{"zh_CN.UTF-8", langZhCN, true},
		{"zh_TW.UTF-8", langZhTW, true},
		{"zh_HK", langZhTW, true},
		{"ru_RU.KOI8-R", langRU, true},
		{"en_US.UTF-8", langEN, true},
		{"fr_FR", langEN, true},
		{"C", "", false},
		{"c", "", false},
		{"POSIX", "", false},
		// "C.UTF-8" is the actual value this plugin's own dev/test machine
		// runs under (glibc's default when no locale is installed); it must
		// be treated the same as bare "C", not misread as English just
		// because stripping ".UTF-8" happened before the C/POSIX check.
		{"C.UTF-8", "", false},
		{"POSIX.UTF-8", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := languageFromEnv(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("languageFromEnv(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestRequestLanguagePrecedence(t *testing.T) {
	// Neutralize the real process environment for the whole test: this
	// plugin's own dev/test machine runs under LANG=C.UTF-8, which (now that
	// languageFromEnv correctly treats it as unset) would not by itself
	// break these assertions, but pinning both to "" here makes every
	// assertion below deterministic regardless of what runs the test suite.
	t.Setenv("LANG", "")
	t.Setenv("LC_ALL", "")

	cfg := defaultPluginConfig()
	cfg.Language = "auto"

	// Nothing at all: falls back to zh-CN.
	if got := requestLanguage(cfg, "", ""); got != langZhCN {
		t.Fatalf("with nothing set, requestLanguage = %q, want zh-CN", got)
	}

	if got := requestLanguage(cfg, "", "ru"); got != langRU {
		t.Fatalf("Accept-Language alone: requestLanguage = %q, want ru", got)
	}

	// A pinned (non-auto) config setting wins over Accept-Language.
	pinned := cfg
	pinned.Language = "ru"
	if got := requestLanguage(pinned, "", "en"); got != langRU {
		t.Fatalf("pinned config should beat Accept-Language: got %q, want ru", got)
	}

	// ?lang= wins even over a pinned config.
	if got := requestLanguage(pinned, "en", "ru"); got != langEN {
		t.Fatalf("?lang= should beat a pinned config: got %q, want en", got)
	}

	// An invalid ?lang= value is ignored, falling through to the next signal.
	if got := requestLanguage(cfg, "not-a-language", "ru"); got != langRU {
		t.Fatalf("invalid ?lang= should fall through to Accept-Language: got %q, want ru", got)
	}
}

func TestRequestLanguageFallsBackToEnv(t *testing.T) {
	cfg := defaultPluginConfig()
	cfg.Language = "auto"
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "ru_RU.UTF-8")
	if got := requestLanguage(cfg, "", ""); got != langRU {
		t.Fatalf("requestLanguage should fall back to LANG: got %q, want ru", got)
	}
}

func TestLogLanguagePrefersConfigThenEnvThenZhCN(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "")

	cfg := defaultPluginConfig()
	cfg.Language = "auto"

	if got := logLanguage(cfg); got != langZhCN {
		t.Fatalf("with nothing set, logLanguage = %q, want zh-CN", got)
	}

	t.Setenv("LANG", "en_US.UTF-8")
	if got := logLanguage(cfg); got != langEN {
		t.Fatalf("logLanguage should fall back to LANG: got %q, want en", got)
	}

	cfg.Language = "zh-TW"
	if got := logLanguage(cfg); got != langZhTW {
		t.Fatalf("a pinned config should win over LANG: got %q, want zh-TW", got)
	}
}

func TestEnvLocalePrefersLCAllOverLANG(t *testing.T) {
	t.Setenv("LC_ALL", "ru_RU.UTF-8")
	t.Setenv("LANG", "en_US.UTF-8")
	if got := envLocale(); got != "ru_RU.UTF-8" {
		t.Fatalf("envLocale() = %q, want LC_ALL to take precedence", got)
	}
}

func TestNormalizeLanguageSetting(t *testing.T) {
	cases := map[string]string{
		"":         "auto",
		"auto":     "auto",
		"AUTO":     "auto",
		"zh-CN":    "zh-CN",
		"zh-cn":    "zh-CN",
		"ru":       "ru",
		"nonsense": "auto",
	}
	for in, want := range cases {
		if got := normalizeLanguageSetting(in); got != want {
			t.Errorf("normalizeLanguageSetting(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestI18nCatalogJSONIncludesAllLanguages(t *testing.T) {
	raw, err := i18nCatalogJSON()
	if err != nil {
		t.Fatalf("i18nCatalogJSON: %v", err)
	}
	for _, l := range supportedLanguages {
		if !contains(raw, []byte(string(l))) {
			t.Errorf("catalog JSON does not mention language %q", l)
		}
	}
	if !contains(raw, []byte(string(msgUIPageTitle))) {
		t.Errorf("catalog JSON does not include a UI key (%s)", msgUIPageTitle)
	}
}

func contains(haystack, needle []byte) bool {
	return len(needle) == 0 || indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle []byte) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
