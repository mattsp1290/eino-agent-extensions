package toolresultredactor

import (
	"context"
	"strings"
	"testing"
)

func policyForTest(t testing.TB, options Options) *compiledPolicy {
	t.Helper()
	_, policy, err := compilePolicy(options)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func TestBuiltinCatalog(t *testing.T) {
	policy := policyForTest(t, Options{Limits: testLimits()})
	positives := map[string]string{
		"private key":       "before\n-----BEGIN PRIVATE KEY-----\nQUJD\n-----END PRIVATE KEY-----\nafter",
		"encrypted crlf":    "before\r\n-----BEGIN ENCRYPTED PRIVATE KEY-----\r\nQUJD\r\n-----END ENCRYPTED PRIVATE KEY-----\r\nafter",
		"bearer shortest":   "before Authorization: Bearer A after",
		"bearer classes":    "authorization\t: bearer aZ09-._~+/==",
		"ghp":               "before ghp_ABCDEFGHIJKLMNOP after",
		"github pat":        "before github_pat_ABCDEFGHIJKLMNOP after",
		"gho":               "before gho_ABCDEFGHIJKLMNOP after",
		"ghu":               "before ghu_ABCDEFGHIJKLMNOP after",
		"ghs":               "before ghs_ABCDEFGHIJKLMNOP after",
		"ghr":               "before ghr_ABCDEFGHIJKLMNOP after",
		"ghs app jwt shape": "before ghs_1234_ABCDEFGHIJKLMNO after",
	}
	for name, input := range positives {
		t.Run(name, func(t *testing.T) {
			output, state := policy.scanScalar(context.Background(), input)
			if state != scanChanged || !strings.Contains(output, Placeholder) {
				t.Fatalf("builtin did not redact: state=%d", state)
			}
		})
	}
	nearMisses := map[string]string{
		"public key":      "-----BEGIN PUBLIC KEY-----\nQUJD\n-----END PUBLIC KEY-----",
		"missing end":     "-----BEGIN PRIVATE KEY-----\nQUJD",
		"embedded begin":  "prose -----BEGIN PRIVATE KEY-----\nQUJD\n-----END PRIVATE KEY-----",
		"bearer prose":    "Bearer ABCD",
		"empty bearer":    "Authorization: Bearer ",
		"other scheme":    "Authorization: Basic ABCD",
		"embedded header": "XAuthorization: Bearer ABCD",
		"proxy header":    "Proxy-Authorization: Bearer ABCD",
		"short github":    "ghp_ABCDEFGHIJKLMNO",
		"embedded github": "xghp_ABCDEFGHIJKLMNOP",
		"unknown github":  "ghx_ABCDEFGHIJKLMNOP",
	}
	for name, input := range nearMisses {
		t.Run(name, func(t *testing.T) {
			output, state := policy.scanScalar(context.Background(), input)
			if state != scanUnchanged || output != input {
				t.Fatalf("near miss changed: state=%d", state)
			}
		})
	}
}

func TestBuiltinCatalogRedactsCompleteStatelessInstallationToken(t *testing.T) {
	policy := policyForTest(t, Options{Limits: testLimits()})
	input := "ghs_1234_eyJhbGciOiJSUzI1NiJ9.eyJpc3MiOiIxLTIifQ.c2lnbmF0dXJl_X-Y"
	output, state := policy.scanScalar(context.Background(), input)
	if state != scanChanged || output != Placeholder {
		t.Fatalf("stateless installation token was not fully redacted: state=%d output=%q", state, output)
	}
}

func TestReplacementHandlesUnicodeAndAdjacentMatches(t *testing.T) {
	options := testOptions()
	options.AdditionalPatterns = []Pattern{{ID: "adjacent", Expression: `SYN_[A-Z]{4}`}}
	policy := policyForTest(t, options)
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "multibyte prefix", input: "🙂SYN_ABCD", want: "🙂" + Placeholder},
		{name: "adjacent ranges", input: "SYN_ABCDSYN_EFGH", want: Placeholder},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			output, state := policy.scanScalar(context.Background(), test.input)
			if state != scanChanged || output != test.want {
				t.Fatalf("replacement mismatch: state=%d output=%q want=%q", state, output, test.want)
			}
		})
	}
}

func TestMergedAndAggregateMatches(t *testing.T) {
	options := testOptions()
	options.AdditionalPatterns = []Pattern{{ID: "wide", Expression: `SYN_[A-Z]+`}, {ID: "narrow", Expression: `[A-Z]{4}`}}
	policy := policyForTest(t, options)
	output, state := policy.scanScalar(context.Background(), "x SYN_ABCD y")
	if state != scanChanged || output != "x "+Placeholder+" y" {
		t.Fatalf("overlap merge failed: state=%d", state)
	}

	options.Limits.MaxMatchesPerField = 1
	policy = policyForTest(t, options)
	output, state = policy.scanScalar(context.Background(), "SYN_ABCD")
	if state != scanUnsafe || output != Placeholder {
		t.Fatalf("aggregate match budget did not fallback: state=%d", state)
	}
}
