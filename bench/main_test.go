package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestSelectScenariosAll(t *testing.T) {
	got, err := selectScenarios("all")
	if err != nil {
		t.Fatalf("selectScenarios(all): %v", err)
	}
	if !reflect.DeepEqual(got, canonicalScenarios) {
		t.Errorf("selectScenarios(all) = %v, want %v", got, canonicalScenarios)
	}
}

func TestSelectScenariosPreservesCanonicalOrder(t *testing.T) {
	// Requested out of order; the harness always runs in canonical order
	// (churn last) regardless of how -run listed them.
	got, err := selectScenarios("churn,cold-boot,exec")
	if err != nil {
		t.Fatalf("selectScenarios: %v", err)
	}
	want := []string{"cold-boot", "exec", "churn"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("selectScenarios(churn,cold-boot,exec) = %v, want %v", got, want)
	}
}

func TestSelectScenariosUnknownName(t *testing.T) {
	_, err := selectScenarios("cold-boot,bogus")
	if err == nil {
		t.Fatal("selectScenarios(cold-boot,bogus): want error, got nil")
	}
	if !containsAll(err.Error(), "bogus", "cold-boot", "ttfr", "churn") {
		t.Errorf("error %q should name the bad scenario and list valid names", err.Error())
	}
}

func TestSelectScenariosEmpty(t *testing.T) {
	if _, err := selectScenarios(""); err == nil {
		t.Fatal("selectScenarios(\"\"): want error, got nil")
	}
}

func TestParseConcLevels(t *testing.T) {
	got, err := parseConcLevels("1,2,4,8,16")
	if err != nil {
		t.Fatalf("parseConcLevels: %v", err)
	}
	want := []int{1, 2, 4, 8, 16}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseConcLevels() = %v, want %v", got, want)
	}
}

func TestParseConcLevelsInvalid(t *testing.T) {
	if _, err := parseConcLevels("1,two,4"); err == nil {
		t.Fatal("parseConcLevels(1,two,4): want error, got nil")
	}
}

func TestParseConcLevelsEmpty(t *testing.T) {
	if _, err := parseConcLevels(""); err == nil {
		t.Fatal("parseConcLevels(\"\"): want error, got nil")
	}
}

func TestValidatePacks(t *testing.T) {
	if err := validatePacks([]string{"python"}); err != nil {
		t.Errorf("validatePacks([python]): unexpected error %v", err)
	}
}

func TestValidatePacksEmpty(t *testing.T) {
	// -packs "" and -packs "," both yield an empty slice via splitCSV; the
	// guard must reject them and name the flag rather than letting Packs[0]
	// panic deep inside a scenario.
	for _, in := range []string{"", ","} {
		if err := validatePacks(splitCSV(in)); err == nil {
			t.Errorf("validatePacks(splitCSV(%q)): want error, got nil", in)
		} else if !strings.Contains(err.Error(), "-packs") {
			t.Errorf("validatePacks error %q should name the -packs flag", err.Error())
		}
	}
}

func TestSplitCSV(t *testing.T) {
	got := splitCSV(" a, b ,,c")
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("splitCSV() = %v, want %v", got, want)
	}
}

func TestRegisterScenarioUnknownPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("registerScenario(bogus, ...): want panic, got none")
		}
	}()
	registerScenario("bogus", func(rc *runContext) (ScenarioResult, error) {
		return ScenarioResult{}, nil
	})
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
