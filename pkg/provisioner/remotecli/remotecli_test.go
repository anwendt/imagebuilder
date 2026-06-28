package remotecli

import "testing"

func TestCommandWithArgsQuotesShellArguments(t *testing.T) {
	got := CommandWithArgs("sudo chef-client", []string{"--why-run", "role[web app]", "name='value'"})
	want := `sudo chef-client '--why-run' 'role[web app]' 'name='"'"'value'"'"''`
	if got != want {
		t.Fatalf("CommandWithArgs() = %q, want %q", got, want)
	}
}

func TestCommandWithArgsWithoutBaseQuotesArguments(t *testing.T) {
	got := CommandWithArgs("", []string{"printf", "hello world", ""})
	want := `'printf' 'hello world' ''`
	if got != want {
		t.Fatalf("CommandWithArgs() = %q, want %q", got, want)
	}
}
