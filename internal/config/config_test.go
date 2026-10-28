package config

import (
	"os"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDefaultsAreSafeForDev(t *testing.T) {
	c, err := Load(env(map[string]string{"CLASSYCASH_ENV": "dev"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Mail.Driver != "log" || c.Bank.Enabled() || c.Location.String() != "Europe/Warsaw" || c.Demo {
		t.Fatalf("%+v", c)
	}
}

func TestProductionRequiresSessionSecretAndHTTPSRelay(t *testing.T) {
	if _, err := Load(env(map[string]string{})); err == nil || !strings.Contains(err.Error(), "SESSION_SECRET") {
		t.Fatalf("prod without secret must fail: %v", err)
	}
	base := map[string]string{"CLASSYCASH_SESSION_SECRET": strings.Repeat("s", 32)}
	if c, err := Load(env(base)); err != nil || c.Mail.Driver != "capture" {
		t.Fatalf("prod without relay captures mail instead of pretending to send: %v %+v", err, c)
	}
	base["CLASSYCASH_MAIL_RELAY_URL"] = "http://relay.example/send"
	base["CLASSYCASH_MAIL_RELAY_KEY"] = "k"
	if _, err := Load(env(base)); err == nil {
		t.Fatal("plain-http relay must be refused in prod")
	}
	base["CLASSYCASH_MAIL_RELAY_URL"] = "https://relay.example/send"
	if c, err := Load(env(base)); err != nil || c.Mail.Driver != "relay" {
		t.Fatalf("relay: %v", err)
	}
	delete(base, "CLASSYCASH_MAIL_RELAY_KEY")
	if _, err := Load(env(base)); err == nil {
		t.Fatal("relay without key must fail")
	}
}

func TestConfigVersionGuard(t *testing.T) {
	_, err := Load(env(map[string]string{"CLASSYCASH_ENV": "dev", "CLASSYCASH_CONFIG_VERSION": "99"}))
	if err == nil || !strings.Contains(err.Error(), "CONFIG_VERSION") {
		t.Fatalf("incompatible config version must be refused: %v", err)
	}
	if _, err := Load(env(map[string]string{"CLASSYCASH_ENV": "dev", "CLASSYCASH_CONFIG_VERSION": "1"})); err != nil {
		t.Fatal(err)
	}
}

func TestBankSecretMinimumLength(t *testing.T) {
	_, err := Load(env(map[string]string{"CLASSYCASH_ENV": "dev", "CLASSYCASH_BANK_MAIL_SECRET": "too-short"}))
	if err == nil {
		t.Fatal("short webhook secret must be refused")
	}
	c, err := Load(env(map[string]string{"CLASSYCASH_ENV": "dev", "CLASSYCASH_BANK_MAIL_SECRET": strings.Repeat("k", 32), "CLASSYCASH_BANK_MAIL_ADDRESS": "BANK-X@classycash.dpdns.org"}))
	if err != nil || !c.Bank.Enabled() || c.Bank.Address != "bank-x@classycash.dpdns.org" {
		t.Fatalf("%v %+v", err, c.Bank)
	}
}

func TestDemoModeOverridesIntegrations(t *testing.T) {
	c, err := Load(env(map[string]string{
		"CLASSYCASH_ENV": "dev", "CLASSYCASH_DEMO_MODE": "true", "CLASSYCASH_MAIL_DRIVER": "relay",
		"CLASSYCASH_MAIL_RELAY_URL": "https://relay.example", "CLASSYCASH_MAIL_RELAY_KEY": "k",
		"CLASSYCASH_BANK_MAIL_SECRET": strings.Repeat("k", 32), "CLASSYCASH_BANK_MAIL_ADDRESS": "bank@x.pl",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Demo || c.Mail.Driver != "capture" || c.Mail.RelayURL != "" || c.Mail.RelayKey != "" || c.Bank.Enabled() || c.Bank.Address != "" {
		t.Fatalf("demo mode must make real mail and the bank webhook impossible: %+v %+v", c.Mail, c.Bank)
	}
}

func TestRejectsBadValues(t *testing.T) {
	for k, v := range map[string]string{"CLASSYCASH_ENV": "staging", "CLASSYCASH_TIMEZONE": "Mars/Base", "CLASSYCASH_MEMORY_LIMIT_MB": "lots", "CLASSYCASH_MAIL_DRIVER": "smtp"} {
		m := map[string]string{"CLASSYCASH_ENV": "dev"}
		m[k] = v
		if _, err := Load(env(m)); err == nil {
			t.Errorf("%s=%s must be rejected", k, v)
		}
	}
}

func TestSupportURLDefaultsToTheAuthorAndCanBeChangedOrSwitchedOff(t *testing.T) {
	load := func(v string, demo bool) (*Config, error) {
		m := map[string]string{"CLASSYCASH_ENV": "dev"}
		if v != "" {
			m["CLASSYCASH_SUPPORT_URL"] = v
		}
		if demo {
			m["CLASSYCASH_DEMO_MODE"] = "1"
		}
		return Load(env(m))
	}
	for _, demo := range []bool{false, true} { // on by default in every instance, demo included
		if c, err := load("", demo); err != nil || c.SupportURL != "https://suppi.pl/mleczakm" {
			t.Fatalf("demo=%v default: %q %v", demo, c.SupportURL, err)
		}
	}
	if c, err := load("https://example.org/kawa", false); err != nil || c.SupportURL != "https://example.org/kawa" {
		t.Fatalf("custom: %v", err)
	}
	for _, off := range []string{"off", "OFF"} {
		if c, err := load(off, false); err != nil || c.SupportURL != "" {
			t.Fatalf("%s must hide the link: %q %v", off, c.SupportURL, err)
		}
	}
	for _, bad := range []string{"http://suppi.pl/mleczakm", "suppi.pl/mleczakm", "javascript:alert(1)", "https://", "https://user:pw@suppi.pl/x", "https://suppi.pl/a b", `https://suppi.pl/"><script>`} {
		if _, err := load(bad, false); err == nil || !strings.Contains(err.Error(), "CLASSYCASH_SUPPORT_URL") {
			t.Errorf("%q must be rejected, got %v", bad, err)
		}
	}
}

// GitHub's Sponsor button must point at the same page as the in-app default.
func TestFundingFileMatchesTheDefaultSupportURL(t *testing.T) {
	data, err := os.ReadFile("../../.github/FUNDING.yml")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != "custom: "+DefaultSupportURL {
		t.Fatalf("FUNDING.yml = %q", got)
	}
}
