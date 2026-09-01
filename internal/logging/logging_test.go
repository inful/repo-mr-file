package logging

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestNew_TextFormat(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, "text", false)
	log.Info("hello", "key", "value")

	out := buf.String()
	if !strings.Contains(out, "level=INFO") {
		t.Errorf("expected text format with level=INFO, got %q", out)
	}
	if !strings.Contains(out, "msg=hello") {
		t.Errorf("expected msg=hello, got %q", out)
	}
	if !strings.Contains(out, "key=value") {
		t.Errorf("expected key=value attribute, got %q", out)
	}
}

func TestNew_JSONFormat(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, "json", false)
	log.Info("hello", "key", "value")

	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("expected valid JSON, got %q: %v", buf.String(), err)
	}
	if out["msg"] != "hello" {
		t.Errorf("msg = %v, want hello", out["msg"])
	}
	if out["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", out["level"])
	}
	if out["key"] != "value" {
		t.Errorf("key = %v, want value", out["key"])
	}
}

func TestNew_InfoLevelByDefault(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, "text", false)
	log.Debug("debug-message")

	if strings.Contains(buf.String(), "debug-message") {
		t.Errorf("expected debug message filtered at info level, got %q", buf.String())
	}
}

func TestNew_DebugLevelWhenVerbose(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, "text", true)
	log.Debug("debug-message")
	log.Info("info-message")

	out := buf.String()
	if !strings.Contains(out, "debug-message") {
		t.Errorf("expected debug message in verbose mode, got %q", out)
	}
	if !strings.Contains(out, "info-message") {
		t.Errorf("expected info message, got %q", out)
	}
}

func TestNew_JSONFormatDebugLevel(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, "json", true)
	log.Debug("hidden")

	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("expected valid JSON, got %q: %v", buf.String(), err)
	}
	if out["level"] != "DEBUG" {
		t.Errorf("level = %v, want DEBUG", out["level"])
	}
	if out["msg"] != "hidden" {
		t.Errorf("msg = %v, want hidden", out["msg"])
	}
}

func TestNew_UnknownFormatFallsBackToText(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, "xml", false)
	log.Info("hello")

	if !strings.Contains(buf.String(), "level=INFO") {
		t.Errorf("expected text fallback for unknown format, got %q", buf.String())
	}
	if strings.HasPrefix(strings.TrimSpace(buf.String()), "{") {
		t.Errorf("output looks like JSON, expected text fallback, got %q", buf.String())
	}
}

func TestMsgConstants_MirrorBashScript(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"GettingProjectInfo", MsgGettingProjectInfo, "Getting project info for %s..."},
		{"FoundProjectID", MsgFoundProjectID, "Found project ID: %d"},
		{"UsingTargetBranch", MsgUsingTargetBranch, "Using target branch: %s"},
		{"CheckingBranch", MsgCheckingBranch, "Checking if branch %s exists..."},
		{"BranchDoesNotExist", MsgBranchDoesNotExist, "Branch does not exist, will create from %s..."},
		{"BranchExists", MsgBranchExists, "Branch exists, will update existing branch"},
		{"BundleMatches", MsgBundleMatches, "%s already matches the source bundle"},
		{"UpdatingFile", MsgUpdatingFile, "Updating %s in %s..."},
		{"CreatingFile", MsgCreatingFile, "Creating %s in %s..."},
		{"FileUpdated", MsgFileUpdated, "✓ File %s completed in branch %s"},
		{"CreatingMR", MsgCreatingMR, "Creating merge request..."},
		{"MRCreated", MsgMRCreated, "✓ Merge request created: %s"},
		{"ExistingMR", MsgExistingMR, "✓ Existing MR: %s"},
		{"NoUpdateNeeded", MsgNoUpdateNeeded, "✓ No update or merge request is needed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("got %q, want %q", tc.got, tc.want)
			}
		})
	}
}

func TestMsgConstants_FormatWithArgs(t *testing.T) {
	got := fmt.Sprintf(MsgMRCreated, "https://gitlab.example.com/foo/-/merge_requests/42")
	want := "✓ Merge request created: https://gitlab.example.com/foo/-/merge_requests/42"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
