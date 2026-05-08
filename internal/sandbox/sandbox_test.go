package sandbox

import "testing"

func TestPrepareEnvVars_NilMap(t *testing.T) {
	req := ExecRequest{CloneURL: "https://github.com/org/repo.git", Branch: "main"}
	PrepareEnvVars(&req)

	if req.EnvVars == nil {
		t.Fatal("EnvVars should be initialized")
	}
	if req.EnvVars["CLONE_URL"] != "https://github.com/org/repo.git" {
		t.Errorf("CLONE_URL = %q, want %q", req.EnvVars["CLONE_URL"], "https://github.com/org/repo.git")
	}
	if req.EnvVars["CLONE_BRANCH"] != "main" {
		t.Errorf("CLONE_BRANCH = %q, want %q", req.EnvVars["CLONE_BRANCH"], "main")
	}
}

func TestPrepareEnvVars_ExistingMap(t *testing.T) {
	req := ExecRequest{
		CloneURL: "https://github.com/org/repo.git",
		Branch:   "feat/x",
		EnvVars:  map[string]string{"API_KEY": "secret"},
	}
	PrepareEnvVars(&req)

	if req.EnvVars["API_KEY"] != "secret" {
		t.Error("existing env vars should be preserved")
	}
	if req.EnvVars["CLONE_URL"] != "https://github.com/org/repo.git" {
		t.Errorf("CLONE_URL = %q, want injected", req.EnvVars["CLONE_URL"])
	}
}

func TestPrepareEnvVars_NoCloneURL(t *testing.T) {
	req := ExecRequest{Branch: "main"}
	PrepareEnvVars(&req)

	if _, ok := req.EnvVars["CLONE_URL"]; ok {
		t.Error("CLONE_URL should not be set when CloneURL is empty")
	}
	if _, ok := req.EnvVars["CLONE_BRANCH"]; ok {
		t.Error("CLONE_BRANCH should not be set when CloneURL is empty")
	}
}

func TestPrepareEnvVars_CloneURLNoBranch(t *testing.T) {
	req := ExecRequest{CloneURL: "https://github.com/org/repo.git"}
	PrepareEnvVars(&req)

	if req.EnvVars["CLONE_URL"] != "https://github.com/org/repo.git" {
		t.Errorf("CLONE_URL = %q, want set", req.EnvVars["CLONE_URL"])
	}
	if _, ok := req.EnvVars["CLONE_BRANCH"]; ok {
		t.Error("CLONE_BRANCH should not be set when Branch is empty")
	}
}
