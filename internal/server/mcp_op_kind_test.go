package server

import "testing"

func TestMCPOpKind(t *testing.T) {
	kinds := map[string]string{}
	for _, op := range mcpAppCatalog {
		kinds[op.Name] = op.Kind
	}
	for name, want := range map[string]string{
		"pocket_projects_create_issue": "add", "pocket_projects_link_pr": "add", "pocket_projects_update_issue": "change",
		"pocket_projects_issues": "read", "pocket_files_save_text": "change", "pocket_tasks_complete": "change", "pocket_notes_append": "add",
	} {
		if kinds[name] != want {
			t.Errorf("%s: kind %q, want %q", name, kinds[name], want)
		}
	}
	for name := range mcpChangePosts {
		if _, ok := kinds["pocket_"+name]; !ok {
			t.Errorf("mcpChangePosts names a missing operation %s", name)
		}
	}
}
