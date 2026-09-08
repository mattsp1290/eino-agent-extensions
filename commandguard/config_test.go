package commandguard

import (
	"reflect"
	"strings"
	"testing"
)

func testOptions() Options {
	return Options{Rules: []Rule{{ID: "blocked", Executable: "blocked"}, {ID: "git-push", Executable: "git", ArgPrefix: []string{"push"}}}, Limits: Limits{
		MaxBindings: 8, MaxRules: 32, MaxRuleBytes: 2048, MaxPrefixArgs: 16, MaxRawInputBytes: 16384, MaxJSONDepth: 16, MaxJSONNodes: 256, MaxCommandBytes: 4096, MaxAnalysisBytes: 8192, MaxASTNodes: 2048, MaxASTDepth: 64, MaxWords: 512, MaxWordBytes: 4096, MaxWrapperDepth: 8, MaxInFlight: 4,
	}}
}
func TestConfigCanonicalAndFrozen(t *testing.T) {
	o := testOptions()
	o.Bindings = DefaultBindings()
	p, err := canonicalize(o)
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := ConfigHash(o)
	o.Rules[0], o.Rules[1] = o.Rules[1], o.Rules[0]
	o.Bindings[0], o.Bindings[1] = o.Bindings[1], o.Bindings[0]
	o.Bindings[0].Dialect = ""
	if h, _ := ConfigHash(o); h != hash {
		t.Fatal("reorder/default changed hash")
	}
	o.Rules[0].ArgPrefix[0] = "status"
	o.Bindings[0].CommandField = "changed"
	if p.rules[1].ArgPrefix[0] != "push" || p.bindings[0].CommandField != "command" {
		t.Fatal("caller aliases retained")
	}
	if h, _ := ConfigHash(o); h == hash {
		t.Fatal("behavior change retained hash")
	}
	defaults := DefaultBindings()
	defaults[0].ToolName = "changed"
	if DefaultBindings()[0].ToolName != "shell" {
		t.Fatal("shared defaults")
	}
	nilPrefix := testOptions()
	emptyPrefix := testOptions()
	emptyPrefix.Rules[0].ArgPrefix = []string{}
	h, _ := ConfigHash(nilPrefix)
	h2, _ := ConfigHash(emptyPrefix)
	if h != h2 {
		t.Fatal("empty prefix not canonical")
	}
}
func TestConfigEveryLimitIdentityAndValidation(t *testing.T) {
	original := testOptions()
	base, _ := ConfigHash(original)
	fields := reflect.TypeOf(original.Limits)
	for i := 0; i < fields.NumField(); i++ {
		t.Run(fields.Field(i).Name, func(t *testing.T) {
			o := testOptions()
			v := reflect.ValueOf(&o.Limits).Elem().Field(i)
			v.SetInt(v.Int() + 1)
			if i == 7 {
				o.Limits.MaxAnalysisBytes++
			}
			h, err := ConfigHash(o)
			if err != nil || h == base {
				t.Fatalf("limit identity: %v", err)
			}
			for _, bad := range []int64{0, -1} {
				v.SetInt(bad)
				if _, err := ConfigHash(o); err == nil {
					t.Fatal("invalid limit accepted")
				}
			}
		})
	}
}
func TestValidation(t *testing.T) {
	cases := map[string]func(*Options){
		"empty bindings":     func(o *Options) { o.Bindings = []Binding{} },
		"duplicate bindings": func(o *Options) { o.Bindings = []Binding{{"shell", "cmd", ""}, {"shell", "other", ""}} },
		"bad tool":           func(o *Options) { o.Bindings = []Binding{{"secret bad", "cmd", ""}} },
		"bad field":          func(o *Options) { o.Bindings = []Binding{{"custom", "\x00secret", ""}} },
		"blank field":        func(o *Options) { o.Bindings = []Binding{{"custom", "  ", ""}} },
		"dialect":            func(o *Options) { o.Bindings = []Binding{{"custom", "cmd", "secret"}} },
		"no rules":           func(o *Options) { o.Rules = nil },
		"duplicate rules":    func(o *Options) { o.Rules[1].ID = o.Rules[0].ID },
		"bad id":             func(o *Options) { o.Rules[0].ID = "secret bad" },
		"path":               func(o *Options) { o.Rules[0].Executable = "/secret" },
		"backslash":          func(o *Options) { o.Rules[0].Executable = `secret\file` },
		"dot":                func(o *Options) { o.Rules[0].Executable = "." },
		"dotdot":             func(o *Options) { o.Rules[0].Executable = ".." },
		"empty exe":          func(o *Options) { o.Rules[0].Executable = "" },
		"nul":                func(o *Options) { o.Rules[0].Executable = "secret\x00" },
		"utf8":               func(o *Options) { o.Rules[0].ArgPrefix = []string{"secret\xff"} },
		"rule count":         func(o *Options) { o.Limits.MaxRules = 1 },
		"binding count":      func(o *Options) { o.Limits.MaxBindings = 1 },
		"rule bytes":         func(o *Options) { o.Limits.MaxRuleBytes = 3 },
		"prefix count":       func(o *Options) { o.Limits.MaxPrefixArgs = 1; o.Rules[0].ArgPrefix = []string{"a", "b"} },
		"word bytes":         func(o *Options) { o.Limits.MaxWordBytes = 3 },
		"analysis bytes":     func(o *Options) { o.Limits.MaxAnalysisBytes = o.Limits.MaxCommandBytes - 1 },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			o := testOptions()
			change(&o)
			_, err := ConfigHash(o)
			if err == nil {
				t.Fatal("invalid config accepted")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("configuration leaked")
			}
		})
	}
	o := testOptions()
	o.Rules[0].ArgPrefix = []string{"", " λ ", "*"}
	if _, err := ConfigHash(o); err != nil {
		t.Fatal(err)
	}
}

func TestConfigDefaultDialectUsesEffectiveBindingBounds(t *testing.T) {
	o := testOptions()
	o.Bindings = []Binding{{ToolName: "a", CommandField: "c"}}
	o.Rules = []Rule{{ID: "r", Executable: "x"}}
	o.Limits.MaxWordBytes = 1
	if _, err := ConfigHash(o); err == nil {
		t.Fatal("default dialect bypassed effective binding bound")
	}
	o.Bindings[0].Dialect = DialectPOSIX
	if _, err := ConfigHash(o); err == nil {
		t.Fatal("explicit dialect bypassed binding bound")
	}
	o.Limits.MaxWordBytes = 5
	explicit, err := ConfigHash(o)
	if err != nil {
		t.Fatal(err)
	}
	o.Bindings[0].Dialect = ""
	implicit, err := ConfigHash(o)
	if err != nil || implicit != explicit {
		t.Fatal("default dialect not canonical", err)
	}
}
